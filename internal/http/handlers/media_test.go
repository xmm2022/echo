package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	authmw "github.com/xmm2022/echo/internal/http/middleware"
	"github.com/xmm2022/echo/internal/media"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestUserMediaRoutesRequireDiscoveryScope(t *testing.T) {
	deps := APIDeps{Media: &media.Service{}}
	user := auth.UserContext{UserID: "u1", Role: "user", Scopes: []string{"read"}}
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/me/discovery/search?q=matrix&type=movie", ""},
		{http.MethodGet, "/api/me/discovery/catalog", ""},
		{http.MethodPost, "/api/me/discovery/requests", `{"tmdb_id":"603","media_type":"movie","target_id":1}`},
		{http.MethodGet, "/api/me/discovery/requests", ""},
		{http.MethodGet, "/api/me/discovery/requests/1", ""},
		{http.MethodPost, "/api/me/discovery/requests/1/cancel", ""},
		{http.MethodGet, "/api/me/discovery/subscriptions", ""},
		{http.MethodPost, "/api/me/discovery/subscriptions/1/pause", ""},
		{http.MethodPost, "/api/me/discovery/subscriptions/1/resume", ""},
	}

	for _, tc := range cases {
		rec := doReqAs(t, tc.method, tc.path, tc.body, user, func(r chi.Router) {
			deps.MountMedia(r)
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s, want 403", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
	}

	rec := doReqAs(t, http.MethodGet, "/api/me/discovery/catalog", "", mediaHTTPUser("u1"), func(r chi.Router) {
		APIDeps{}.MountMedia(r)
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing media status=%d body=%s, want 503", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "media service not configured") {
		t.Fatalf("missing media body=%s, want safe service error", rec.Body.String())
	}
	assertNoStore(t, rec)
}

func TestUserMediaRoutesRejectDisabledOrRevokedTokenThroughAuthMiddleware(t *testing.T) {
	st := newAPIStore(t)
	seedMediaHTTPUserStatus(t, st, "disabled-user", "user", "disabled")
	seedMediaHTTPUserStatus(t, st, "revoked-user", "user", "active")

	disabledToken := "disabled-token"
	revokedToken := "revoked-token"
	seedMediaHTTPAPIToken(t, st, "tok-disabled", "disabled-user", disabledToken, []string{"discovery"})
	seedMediaHTTPAPIToken(t, st, "tok-revoked", "revoked-user", revokedToken, []string{"discovery"})
	if err := st.RevokeAPIToken(context.Background(), queries.RevokeAPITokenParams{
		RevokedAt: sql.NullInt64{Int64: 1000, Valid: true},
		ID:        "tok-revoked",
		UserID:    "revoked-user",
	}); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	checker := (&auth.Authenticator{Store: st, Now: apiClock()}).Authenticate
	deps := APIDeps{Media: &media.Service{Store: st, Now: apiClock()}}
	r := chi.NewRouter()
	r.Use(authmw.AuthFunc(checker))
	deps.Mount(r)

	for _, token := range []string{disabledToken, revokedToken} {
		req := httptest.NewRequest(http.MethodGet, "/api/me/discovery/catalog", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status=%d body=%s, want 401", token, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("token %q missing WWW-Authenticate header", token)
		}
	}
}

func TestUserMediaSearchRequiresPolicyAndReturnsSafeSummaries(t *testing.T) {
	st := newAPIStore(t)
	createUserRow(t, st, "u1", "user")
	fake := newFakeMediaMetadataClient()
	fake.searches["movie:Matrix"] = []tmdb.Media{{
		TMDBID:      "603",
		MediaType:   "movie",
		Title:       "The Matrix",
		ReleaseYear: 1999,
		PosterPath:  "/poster.jpg",
		RawJSON:     `{"secret":"raw metadata"}`,
	}}
	deps := newMediaHTTPDeps(st, fake)

	rec := doReqAs(t, http.MethodGet, "/api/me/discovery/search?q=Matrix&type=movie", "", mediaHTTPUser("u1"), func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("without policy status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request-disabled") {
		t.Fatalf("without policy body=%s, want request-disabled", rec.Body.String())
	}

	seedMediaHTTPPolicy(t, st, "search policy", "u1", "approval_required", 1)
	rec = doReqAs(t, http.MethodGet, "/api/me/discovery/search?q=Matrix&type=movie&language=zh-CN", "", mediaHTTPUser("u1"), func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	assertNoStore(t, rec)
	var got []media.TMDBSummaryDTO
	decodeBody(t, rec, &got)
	if len(got) != 1 || got[0].TMDBID != "603" || got[0].Title != "The Matrix" || got[0].Availability != "unknown" {
		t.Fatalf("search summaries=%+v, want safe Matrix summary", got)
	}
	assertBodyOmits(t, rec.Body.String(), "raw metadata", "raw_json", "secret", "default_args_json")
}

func TestUserMediaCreateRequestRejectsForbiddenFields(t *testing.T) {
	forbidden := []string{
		"owner_id",
		"library_id",
		"producer_profile_id",
		"rule_profile_id",
		"source_id",
		"account_id",
		"target_account",
		"target_library_id",
		"pipeline_owner_id",
		"next_check_at",
		"share_url",
		"share_code",
		"receive_code",
		"default_args_json",
		"raw_text_ref",
		"job_payload",
		"status",
		"reviewed_by",
		"args",
	}
	deps := APIDeps{Media: &media.Service{}}

	for _, field := range forbidden {
		body := `{"tmdb_id":"603","media_type":"movie","target_id":1,"` + field + `":"forbidden"}`
		rec := doReqAs(t, http.MethodPost, "/api/me/discovery/requests", body, mediaHTTPUser("u1"), func(r chi.Router) {
			deps.MountMedia(r)
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("field %s status=%d body=%s, want 400", field, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "forbidden request field") {
			t.Fatalf("field %s body=%s, want forbidden request field", field, rec.Body.String())
		}
		assertNoStore(t, rec)
	}
}

func TestUserMediaCreateRequestReturnsExistingDuplicate(t *testing.T) {
	st, deps, fake, target := newMediaHTTPCreateFixture(t, "u1", "approval_required")
	fake.movies["603"] = tmdb.Media{
		TMDBID:      "603",
		MediaType:   "movie",
		Title:       "The Matrix",
		ReleaseYear: 1999,
		RawJSON:     `{"id":603}`,
	}
	body := `{"tmdb_id":"603","media_type":"movie","tmdb_language":"zh-CN","target_id":` + itoa(target.ID) + `,"user_note":"please"}`
	user := mediaHTTPUser("u1")

	first := postMediaRequest(t, deps, user, body)
	second := postMediaRequestExpectStatus(t, deps, user, body, http.StatusOK)
	if second.ID != first.ID {
		t.Fatalf("duplicate id=%d, want existing id %d", second.ID, first.ID)
	}
	if got := countMediaHTTPRequestsForUser(t, st, "u1"); got != 1 {
		t.Fatalf("request count=%d, want 1", got)
	}
	if fake.detailCalls() != 1 {
		t.Fatalf("metadata detail calls=%d, want 1 duplicate-safe fetch", fake.detailCalls())
	}
}

func TestUserMediaCreateDuplicateBypassesRequestCreateRateLimit(t *testing.T) {
	st, deps, fake, target := newMediaHTTPCreateFixture(t, "u1", "approval_required")
	fake.movies["609"] = tmdb.Media{
		TMDBID:    "609",
		MediaType: "movie",
		Title:     "Duplicate Movie",
		RawJSON:   `{"id":609}`,
	}
	limiter := newFakeMediaLimiter()
	deps.Media.Limiter = limiter
	body := `{"tmdb_id":"609","media_type":"movie","tmdb_language":"zh-CN","target_id":` + itoa(target.ID) + `}`
	user := mediaHTTPUser("u1")

	first := postMediaRequest(t, deps, user, body)
	limiter.deny["request-create:user:u1"] = true
	second := postMediaRequestExpectStatus(t, deps, user, body, http.StatusOK)
	if second.ID != first.ID {
		t.Fatalf("duplicate id=%d, want existing id %d", second.ID, first.ID)
	}
	if got := countMediaHTTPRequestsForUser(t, st, "u1"); got != 1 {
		t.Fatalf("request count=%d, want 1", got)
	}
	if fake.detailCalls() != 1 {
		t.Fatalf("metadata detail calls=%d, want no duplicate fetch after limiter deny", fake.detailCalls())
	}
}

func TestUserMediaCreateConcurrentDuplicateReturnsOneCreated(t *testing.T) {
	st, deps, fake, target := newMediaHTTPCreateFixture(t, "u1", "approval_required")
	fake.movies["610"] = tmdb.Media{
		TMDBID:    "610",
		MediaType: "movie",
		Title:     "Concurrent Duplicate",
		RawJSON:   `{"id":610}`,
	}
	releaseDetails := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseDetails)
		})
	}
	defer release()
	fake.detailRelease = releaseDetails
	body := `{"tmdb_id":"610","media_type":"movie","tmdb_language":"zh-CN","target_id":` + itoa(target.ID) + `}`
	user := mediaHTTPUser("u1")

	type result struct {
		status int
		body   string
		dto    media.RequestDTO
	}
	const callers = 8
	start := make(chan struct{})
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := doReqAs(t, http.MethodPost, "/api/me/discovery/requests", body, user, func(r chi.Router) {
				deps.MountMedia(r)
			})
			got := result{status: rec.Code, body: rec.Body.String()}
			if rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
				decodeBody(t, rec, &got.dto)
			}
			results <- got
		}()
	}
	close(start)
	waitForMediaHTTPDetailCalls(t, fake, 1)
	duplicateFetch := waitForMediaHTTPDetailCallsWithin(fake, 2, 100*time.Millisecond)
	release()
	wg.Wait()
	close(results)

	created := 0
	ok := 0
	var requestID int64
	for got := range results {
		switch got.status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			ok++
		default:
			t.Fatalf("concurrent create status=%d body=%s, want 201 or 200", got.status, got.body)
		}
		if got.dto.ID == 0 {
			t.Fatalf("concurrent create returned zero id with status %d body=%s", got.status, got.body)
		}
		if requestID == 0 {
			requestID = got.dto.ID
		}
		if got.dto.ID != requestID {
			t.Fatalf("concurrent create id=%d, want shared id %d", got.dto.ID, requestID)
		}
	}
	if created != 1 || ok != callers-1 {
		t.Fatalf("status counts created=%d ok=%d, want 1/%d", created, ok, callers-1)
	}
	if duplicateFetch {
		t.Fatalf("metadata detail calls reached %d before first create completed, want one in-flight fetch for duplicate request", fake.detailCalls())
	}
	if got := countMediaHTTPRequestsForUser(t, st, "u1"); got != 1 {
		t.Fatalf("request count=%d, want 1", got)
	}
	if fake.detailCalls() != 1 {
		t.Fatalf("metadata detail calls=%d, want one duplicate-safe fetch", fake.detailCalls())
	}
}

func TestUserMediaOwnerScopeBlocksOtherUsersRequest(t *testing.T) {
	_, deps, fake, target := newMediaHTTPCreateFixture(t, "u1", "approval_required")
	createUserRow(t, deps.Store, "u2", "user")
	fake.movies["604"] = tmdb.Media{TMDBID: "604", MediaType: "movie", Title: "Owner Movie", RawJSON: `{}`}
	req := createMediaRequestThroughService(t, deps, "u1", target.ID, "604", "movie")
	other := mediaHTTPUser("u2")

	rec := doReqAs(t, http.MethodGet, "/api/me/discovery/requests/"+itoa(req.ID), "", other, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get other user request status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}

	rec = doReqAs(t, http.MethodPost, "/api/me/discovery/requests/"+itoa(req.ID)+"/cancel", "", other, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cancel other user request status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}

	rec = doReqAs(t, http.MethodGet, "/api/me/discovery/requests", "", other, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("list as other user status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	assertBodyOmits(t, rec.Body.String(), "Owner Movie", `"id":`+itoa(req.ID))
}

func TestUserMediaOwnerScopeBlocksOtherUsersListCancelPauseResume(t *testing.T) {
	_, deps, fake, target := newMediaHTTPCreateFixture(t, "u1", "auto_approve")
	createUserRow(t, deps.Store, "u2", "user")
	fake.movies["605"] = tmdb.Media{TMDBID: "605", MediaType: "movie", Title: "Subscribed Movie", RawJSON: `{}`}
	req := createMediaRequestThroughService(t, deps, "u1", target.ID, "605", "movie")
	if req.SubscriptionID == 0 {
		t.Fatalf("subscription_id=0, want auto-approved subscription")
	}
	other := mediaHTTPUser("u2")

	rec := doReqAs(t, http.MethodGet, "/api/me/discovery/requests", "", other, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("list other requests status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	assertBodyOmits(t, rec.Body.String(), "Subscribed Movie", `"id":`+itoa(req.ID))

	rec = doReqAs(t, http.MethodPost, "/api/me/discovery/requests/"+itoa(req.ID)+"/cancel", "", other, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cancel other request status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}

	rec = doReqAs(t, http.MethodGet, "/api/me/discovery/subscriptions", "", other, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("list other subscriptions status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	assertBodyOmits(t, rec.Body.String(), "Subscribed Movie", `"id":`+itoa(req.SubscriptionID))

	for _, action := range []string{"pause", "resume"} {
		rec = doReqAs(t, http.MethodPost, "/api/me/discovery/subscriptions/"+itoa(req.SubscriptionID)+"/"+action, "", other, func(r chi.Router) {
			deps.MountMedia(r)
		})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s other subscription status=%d body=%s, want 404", action, rec.Code, rec.Body.String())
		}
	}
}

func TestUserMediaRoutesDoNotExposeAdminDiscoveryData(t *testing.T) {
	st, deps, fake, target := newMediaHTTPCreateFixture(t, "u1", "auto_approve")
	fake.movies["606"] = tmdb.Media{
		TMDBID:    "606",
		MediaType: "movie",
		Title:     "Redaction Movie",
		RawJSON:   `{"share_code":"raw-share-secret","receive_code":"raw-receive-secret"}`,
	}
	req := createMediaRequestThroughService(t, deps, "u1", target.ID, "606", "movie")
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE discovery_subscription_requests
SET admin_note = 'admin note secret',
    last_error_kind = 'metadata_unavailable',
    last_error_message = 'raw stack secret',
    reviewed_by = 'admin'
WHERE id = ?`, req.ID); err != nil {
		t.Fatalf("mark request sensitive: %v", err)
	}

	user := mediaHTTPUser("u1")
	paths := []string{
		"/api/me/discovery/catalog",
		"/api/me/discovery/requests",
		"/api/me/discovery/requests/" + itoa(req.ID),
		"/api/me/discovery/subscriptions",
	}
	for _, path := range paths {
		rec := doReqAs(t, http.MethodGet, path, "", user, func(r chi.Router) {
			deps.MountMedia(r)
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s, want 200", path, rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
		assertBodyOmits(t, rec.Body.String(), mediaHTTPForbiddenResponseFragments()...)
	}

	rec := doReqAs(t, http.MethodGet, "/api/discovery/producer-profiles", "", user, func(r chi.Router) {
		deps.Mount(r)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin v0.3 discovery status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	assertBodyOmits(t, rec.Body.String(), "acc-115", "default_args_json", "target_account")
}

func TestUserMediaRateLimitReturnsSafe429(t *testing.T) {
	limiter := newFakeMediaLimiter()
	limiter.deny["search:user:u1"] = true
	fake := newFakeMediaMetadataClient()
	deps := APIDeps{
		Media:  &media.Service{TMDB: fake, Limiter: limiter, Now: apiClock()},
		Logger: apiLogger(),
		Now:    apiClock(),
	}

	rec := doReqAs(t, http.MethodGet, "/api/me/discovery/search?q=Matrix&type=movie", "", mediaHTTPUser("u1"), func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited search status=%d body=%s, want 429", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request-limit-reached") {
		t.Fatalf("rate limited search body=%s, want safe rate-limit code", rec.Body.String())
	}
	assertNoStore(t, rec)
	assertBodyOmits(t, rec.Body.String(), mediaHTTPForbiddenResponseFragments()...)
	if fake.searchCalls() != 0 {
		t.Fatalf("search calls=%d, want 0 when HTTP limiter denies", fake.searchCalls())
	}
}

func TestUserMediaSearchCapsQueryAndPageSize(t *testing.T) {
	st, deps, fake, target := newMediaHTTPCreateFixture(t, "u1", "approval_required")
	fake.searches["movie:Matrix"] = []tmdb.Media{{TMDBID: "603", MediaType: "movie", Title: "The Matrix", RawJSON: `{}`}}
	user := mediaHTTPUser("u1")

	longQuery := strings.Repeat("x", 121)
	rec := doReqAs(t, http.MethodGet, "/api/me/discovery/search?q="+longQuery+"&type=movie", "", user, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long search status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.searchCalls() != 0 {
		t.Fatalf("search calls=%d, want 0 for overlong query", fake.searchCalls())
	}

	for i := 0; i < 101; i++ {
		seedMediaHTTPRequestRow(t, st, "u1", target, mediaHTTPTargetDeps{
			libraryID:         target.LibraryID,
			producerProfileID: target.ProducerProfileID,
			ruleProfileID:     target.RuleProfileID,
		}, int64(i))
	}
	rec = doReqAs(t, http.MethodGet, "/api/me/discovery/requests?limit=9999", "", user, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("request list status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var requests []media.RequestDTO
	decodeBody(t, rec, &requests)
	if len(requests) != 100 {
		t.Fatalf("request list length=%d, want capped 100", len(requests))
	}

	seedMediaHTTPSubscriptions(t, st, "u1", mediaHTTPTargetDeps{
		libraryID:         target.LibraryID,
		producerProfileID: target.ProducerProfileID,
		ruleProfileID:     target.RuleProfileID,
	}, 101)
	rec = doReqAs(t, http.MethodGet, "/api/me/discovery/subscriptions?limit=9999", "", user, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("subscription list status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var subscriptions []media.SubscriptionDTO
	decodeBody(t, rec, &subscriptions)
	if len(subscriptions) != 100 {
		t.Fatalf("subscription list length=%d, want capped 100", len(subscriptions))
	}
}

func TestUserMediaSearchHonorsGlobalTMDBLimit(t *testing.T) {
	st := newAPIStore(t)
	createUserRow(t, st, "u1", "user")
	seedMediaHTTPPolicy(t, st, "search policy", "u1", "approval_required", 1)
	fake := newFakeMediaMetadataClient()
	limiter := newFakeMediaLimiter()
	limiter.deny["tmdb:global:search"] = true
	deps := newMediaHTTPDeps(st, fake)
	deps.Media.Limiter = limiter

	rec := doReqAs(t, http.MethodGet, "/api/me/discovery/search?q=Matrix&type=movie", "", mediaHTTPUser("u1"), func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("global search limit status=%d body=%s, want 429", rec.Code, rec.Body.String())
	}
	if fake.searchCalls() != 0 {
		t.Fatalf("search calls=%d, want 0 when global TMDB limiter denies", fake.searchCalls())
	}
}

func TestUserMediaRateLimitUsesRemoteAddrNotForwardedFor(t *testing.T) {
	limiter := newFakeMediaLimiter()
	fake := newFakeMediaMetadataClient()
	fake.searches["movie:Matrix"] = []tmdb.Media{{TMDBID: "603", MediaType: "movie", Title: "The Matrix", RawJSON: `{}`}}
	deps := APIDeps{
		Media:  &media.Service{TMDB: fake, Limiter: limiter, Now: apiClock()},
		Logger: apiLogger(),
		Now:    apiClock(),
	}
	r := chi.NewRouter()
	deps.MountMedia(r)
	req := httptest.NewRequest(http.MethodGet, "/api/me/discovery/search?q=Matrix&type=movie", nil)
	req.RemoteAddr = "203.0.113.10:54321"
	req.Header.Set("X-Forwarded-For", "198.51.100.99")
	req = req.WithContext(auth.NewContext(req.Context(), mediaHTTPUser("u1")))
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("search status=%d body=%s, want policy 403 after rate checks", rec.Code, rec.Body.String())
	}
	limiter.assertCalled(t, "search:ip:203.0.113.10")
	limiter.assertNotCalled(t, "search:ip:198.51.100.99")
}

func TestUserMediaPolicyAndRateLimitDenyCreateSafeAuditEvents(t *testing.T) {
	st := newAPIStore(t)
	createUserRow(t, st, "u1", "user")
	deps := newMediaHTTPDeps(st, newFakeMediaMetadataClient())
	rec := doReqAs(t, http.MethodPost, "/api/me/discovery/requests", `{"tmdb_id":"603","media_type":"movie","target_id":1}`, mediaHTTPUser("u1"), func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("policy deny create status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	action, reason := latestMediaHTTPAudit(t, st, "u1")
	if action != "request_create_deny" || reason != "request-disabled" {
		t.Fatalf("policy deny audit action=%q reason=%q, want request_create_deny/request-disabled", action, reason)
	}

	st, deps, fake, target := newMediaHTTPCreateFixture(t, "limited-user", "approval_required")
	fake.movies["603"] = tmdb.Media{TMDBID: "603", MediaType: "movie", Title: "The Matrix", RawJSON: `{}`}
	limiter := newFakeMediaLimiter()
	limiter.deny["request-create:user:limited-user"] = true
	deps.Media.Limiter = limiter
	body := `{"tmdb_id":"603","media_type":"movie","target_id":` + itoa(target.ID) + `}`
	rec = doReqAs(t, http.MethodPost, "/api/me/discovery/requests", body, mediaHTTPUser("limited-user"), func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate deny create status=%d body=%s, want 429", rec.Code, rec.Body.String())
	}
	if fake.detailCalls() != 0 {
		t.Fatalf("detail calls=%d, want 0 when create limiter denies", fake.detailCalls())
	}
	action, reason = latestMediaHTTPAudit(t, st, "limited-user")
	if action != "rate_limit_deny" || reason != "rate-limit-reached" {
		t.Fatalf("rate deny audit action=%q reason=%q, want rate_limit_deny/rate-limit-reached", action, reason)
	}
}

func TestUserMediaDTOJSONDoesNotContainSensitiveKeys(t *testing.T) {
	payload, err := json.Marshal(struct {
		Search        media.TMDBSummaryDTO  `json:"search"`
		Request       media.RequestDTO      `json:"request"`
		Subscription  media.SubscriptionDTO `json:"subscription"`
		Target        media.TargetDTO       `json:"target"`
		PosterPathRaw string                `json:"poster_path_raw"`
	}{
		Search: media.TMDBSummaryDTO{
			TMDBID:       "603",
			MediaType:    "movie",
			Title:        "The Matrix",
			PosterPath:   "/poster.jpg?share_code=not-a-json-key",
			Availability: "unknown",
		},
		Request:       media.RequestDTO{ID: 1, TMDBID: "603", MediaType: "movie", Title: "The Matrix", TargetLabel: "Default", Status: "pending_review"},
		Subscription:  media.SubscriptionDTO{ID: 2, TMDBID: "603", MediaType: "movie", Title: "The Matrix", UserStatus: "active", PipelineStatus: "active", LatestState: "pending"},
		Target:        media.TargetDTO{ID: 3, Label: "Default", Default: true, RequestMode: "approval_required", CanSearch: true},
		PosterPathRaw: "/still/inert/share_code/text",
	})
	if err != nil {
		t.Fatalf("marshal DTOs: %v", err)
	}
	body := string(payload)
	for _, key := range []string{
		`"share_code"`,
		`"receive_code"`,
		`"raw_text_ref"`,
		`"target_account"`,
		`"storage_mount"`,
		`"default_args_json"`,
		`"queued_job_id"`,
	} {
		if strings.Contains(body, key) {
			t.Fatalf("DTO JSON leaked sensitive key %s: %s", key, body)
		}
	}
}

func TestHXRequestDoesNotBypassDiscoveryScope(t *testing.T) {
	st := newAPIStore(t)
	createUserRow(t, st, "discovery-user", "user")
	createUserRow(t, st, "read-user", "user")
	discoveryToken := "discovery-token"
	readToken := "read-token"
	seedMediaHTTPAPIToken(t, st, "tok-discovery", "discovery-user", discoveryToken, []string{"discovery"})
	seedMediaHTTPAPIToken(t, st, "tok-read", "read-user", readToken, []string{"read"})

	checker := (&auth.Authenticator{Store: st, Now: apiClock()}).Authenticate
	deps := APIDeps{Media: &media.Service{Store: st, Now: apiClock()}, Logger: apiLogger(), Now: apiClock()}
	router := chi.NewRouter()
	router.Use(authmw.AuthFunc(checker))
	deps.MountMedia(router)

	do := func(method, target, token string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		req.RemoteAddr = "203.0.113.10:12345"
		req.Header.Set("HX-Request", "true")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if mutate != nil {
			mutate(req)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodGet, "/api/me/discovery/catalog", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("HX request without bearer status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("HX request without bearer missing WWW-Authenticate")
	}

	rec = do(http.MethodGet, "/api/me/discovery/catalog", readToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bearer without discovery scope status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	authBypassCases := []struct {
		name   string
		target string
		mutate func(*http.Request)
	}{
		{
			name: "cookie",
			mutate: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: "Authorization", Value: "Bearer " + discoveryToken})
			},
		},
		{name: "query token", target: "?access_token=" + discoveryToken},
		{
			name: "form token",
			mutate: func(req *http.Request) {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Body = io.NopCloser(strings.NewReader("access_token=" + discoveryToken))
			},
		},
	}
	for _, tc := range authBypassCases {
		target := "/api/me/discovery/catalog" + tc.target
		rec = do(http.MethodGet, target, "", tc.mutate)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s auth bypass status=%d body=%s, want 401", tc.name, rec.Code, rec.Body.String())
		}
	}

	methodOverrideCases := []struct {
		name   string
		target string
		mutate func(*http.Request)
	}{
		{name: "_method query", target: "?_method=POST"},
		{
			name: "X-HTTP-Method-Override",
			mutate: func(req *http.Request) {
				req.Header.Set("X-HTTP-Method-Override", "POST")
			},
		},
	}
	for _, tc := range methodOverrideCases {
		target := "/api/me/discovery/requests/1/cancel" + tc.target
		rec = do(http.MethodGet, target, discoveryToken, tc.mutate)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s override status=%d body=%s, want 405 method not accepted", tc.name, rec.Code, rec.Body.String())
		}
	}

	rec = do(http.MethodGet, "/api/me/discovery/requests?_method=POST", discoveryToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create route GET with _method status=%d body=%s, want list route 200", rec.Code, rec.Body.String())
	}
	if got := countMediaHTTPRequestsForUser(t, st, "discovery-user"); got != 0 {
		t.Fatalf("GET _method=POST created %d requests, want 0", got)
	}

	for _, path := range []string{
		"/api/me/discovery/requests/1/cancel",
		"/api/me/discovery/subscriptions/1/pause",
		"/api/me/discovery/subscriptions/1/resume",
	} {
		rec = do(http.MethodGet, path, discoveryToken, nil)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("state-changing route %s GET status=%d body=%s, want 405", path, rec.Code, rec.Body.String())
		}
	}
}

type fakeMediaMetadataClient struct {
	mu            sync.Mutex
	searches      map[string][]tmdb.Media
	movies        map[string]tmdb.Media
	tv            map[string]tmdb.Media
	search        int
	details       int
	detailRelease <-chan struct{}
}

func newFakeMediaMetadataClient() *fakeMediaMetadataClient {
	return &fakeMediaMetadataClient{
		searches: make(map[string][]tmdb.Media),
		movies:   make(map[string]tmdb.Media),
		tv:       make(map[string]tmdb.Media),
	}
}

func (f *fakeMediaMetadataClient) Search(_ context.Context, query, mediaType string) ([]tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.search++
	return append([]tmdb.Media(nil), f.searches[mediaType+":"+query]...), nil
}

func (f *fakeMediaMetadataClient) MovieDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	f.mu.Lock()
	f.details++
	release := f.detailRelease
	item, ok := f.movies[tmdbID]
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	if !ok {
		return tmdb.Media{}, media.ErrMetadataUnavailable
	}
	return item, nil
}

func (f *fakeMediaMetadataClient) TVDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	f.mu.Lock()
	f.details++
	release := f.detailRelease
	item, ok := f.tv[tmdbID]
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	if !ok {
		return tmdb.Media{}, media.ErrMetadataUnavailable
	}
	return item, nil
}

func (f *fakeMediaMetadataClient) detailCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.details
}

func (f *fakeMediaMetadataClient) searchCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.search
}

func waitForMediaHTTPDetailCalls(t *testing.T, fake *fakeMediaMetadataClient, min int) {
	t.Helper()
	if waitForMediaHTTPDetailCallsWithin(fake, min, 2*time.Second) {
		return
	}
	t.Fatalf("metadata detail calls=%d, want at least %d", fake.detailCalls(), min)
}

func waitForMediaHTTPDetailCallsWithin(fake *fakeMediaMetadataClient, min int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fake.detailCalls() >= min {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return fake.detailCalls() >= min
}

func newMediaHTTPDeps(st *store.Store, tmdbClient media.MetadataClient) APIDeps {
	return APIDeps{
		Store:  st,
		Media:  &media.Service{Store: st, TMDB: tmdbClient, Now: apiClock()},
		Logger: apiLogger(),
		Now:    apiClock(),
	}
}

func newMediaHTTPCreateFixture(t *testing.T, userID, requestMode string) (*store.Store, APIDeps, *fakeMediaMetadataClient, queries.DiscoveryPolicyTarget) {
	t.Helper()
	st := newAPIStore(t)
	createUserRow(t, st, userID, "user")
	policy := seedMediaHTTPPolicy(t, st, "policy-"+userID+"-"+requestMode, userID, requestMode, 1)
	deps := seedMediaHTTPTargetDeps(t, st)
	target := seedMediaHTTPTarget(t, st, deps, policy.ID, "Default Target", "", 1)
	fake := newFakeMediaMetadataClient()
	return st, newMediaHTTPDeps(st, fake), fake, target
}

type mediaHTTPTargetDeps struct {
	libraryID         int64
	producerProfileID int64
	ruleProfileID     int64
}

func seedMediaHTTPUserStatus(t *testing.T, st *store.Store, id, role, status string) {
	t.Helper()
	if err := st.CreateUser(context.Background(), queries.CreateUserParams{
		ID:            id,
		Username:      id,
		Role:          role,
		Status:        status,
		QuotaPolicyID: 1,
		CreatedAt:     1000,
		UpdatedAt:     1000,
	}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
}

func seedMediaHTTPAPIToken(t *testing.T, st *store.Store, id, userID, token string, scopes []string) {
	t.Helper()
	rawScopes, err := json.Marshal(scopes)
	if err != nil {
		t.Fatalf("marshal scopes: %v", err)
	}
	if err := st.CreateAPIToken(context.Background(), queries.CreateAPITokenParams{
		ID:        id,
		UserID:    userID,
		Name:      id,
		TokenHash: auth.HashToken(token),
		Scopes:    string(rawScopes),
		CreatedAt: 1000,
	}); err != nil {
		t.Fatalf("create api token %s: %v", id, err)
	}
}

func seedMediaHTTPPolicy(t *testing.T, st *store.Store, name, userID, requestMode string, canSearch int64) queries.DiscoveryAccessPolicy {
	t.Helper()
	policy, err := st.CreateDiscoveryAccessPolicy(context.Background(), queries.CreateDiscoveryAccessPolicyParams{
		Name:          name,
		Enabled:       1,
		Priority:      100,
		SubjectUserID: mediaHTTPNullString(userID),
		RequestMode:   requestMode,
		CanSearch:     canSearch,
		CreatedBy:     mediaHTTPNullString("admin"),
		CreatedAt:     1000,
		UpdatedAt:     1000,
	})
	if err != nil {
		t.Fatalf("create media policy %s: %v", name, err)
	}
	return policy
}

func seedMediaHTTPTargetDeps(t *testing.T, st *store.Store) mediaHTTPTargetDeps {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           "acc-115",
		Provider:     "115",
		SidecarID:    "sidecar-1",
		StorageMount: "/115",
		Status:       "active",
		OwnerID:      "admin",
		CreatedAt:    1000,
		UpdatedAt:    1000,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	library, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "Media",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/echo-media-http-test",
		OwnerID:        "admin",
		CreatedAt:      1000,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	profile, err := st.CreateDiscoveryProducerProfile(ctx, queries.CreateDiscoveryProducerProfileParams{
		Name:                   "115 default",
		Provider:               "115",
		Tool:                   "115share2cas",
		TargetAccount:          "acc-115",
		TargetSubdirTemplate:   "{{.Title}}",
		LibraryRelPathTemplate: "{{.Title}}",
		DefaultArgsJson:        `{"secret":"producer args"}`,
		Enabled:                1,
		CreatedAt:              1000,
		UpdatedAt:              1000,
	})
	if err != nil {
		t.Fatalf("create producer profile: %v", err)
	}
	ruleProfile, err := st.CreateRuleProfile(ctx, queries.CreateRuleProfileParams{
		Name:      "default rules",
		Version:   1,
		RulesJson: `{"weights":["resolutions"],"secret":"rule secret"}`,
		Enabled:   1,
		CreatedAt: 1000,
		UpdatedAt: 1000,
	})
	if err != nil {
		t.Fatalf("create rule profile: %v", err)
	}
	return mediaHTTPTargetDeps{
		libraryID:         library.ID,
		producerProfileID: profile.ID,
		ruleProfileID:     ruleProfile.ID,
	}
}

func seedMediaHTTPTarget(t *testing.T, st *store.Store, deps mediaHTTPTargetDeps, policyID int64, label, mediaType string, enabled int64) queries.DiscoveryPolicyTarget {
	t.Helper()
	target, err := st.CreateDiscoveryPolicyTarget(context.Background(), queries.CreateDiscoveryPolicyTargetParams{
		PolicyID:                policyID,
		Label:                   label,
		LibraryID:               deps.libraryID,
		ProducerProfileID:       deps.producerProfileID,
		RuleProfileID:           deps.ruleProfileID,
		PipelineOwnerID:         "admin",
		MediaType:               mediaHTTPNullString(mediaType),
		MatchMode:               "admin_review",
		GrantPlaybackOnApproval: 1,
		Enabled:                 enabled,
		DefaultTarget:           1,
		CreatedAt:               1000,
		UpdatedAt:               1000,
	})
	if err != nil {
		t.Fatalf("create target %s: %v", label, err)
	}
	return target
}

func postMediaRequest(t *testing.T, deps APIDeps, user auth.UserContext, body string) media.RequestDTO {
	t.Helper()
	return postMediaRequestExpectStatus(t, deps, user, body, http.StatusCreated)
}

func postMediaRequestExpectStatus(t *testing.T, deps APIDeps, user auth.UserContext, body string, wantStatus int) media.RequestDTO {
	t.Helper()
	rec := doReqAs(t, http.MethodPost, "/api/me/discovery/requests", body, user, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != wantStatus {
		t.Fatalf("create request status=%d body=%s, want %d", rec.Code, rec.Body.String(), wantStatus)
	}
	assertNoStore(t, rec)
	var got media.RequestDTO
	decodeBody(t, rec, &got)
	if got.ID == 0 {
		t.Fatalf("request DTO has zero id: %+v", got)
	}
	return got
}

func createMediaRequestThroughService(t *testing.T, deps APIDeps, userID string, targetID int64, tmdbID, mediaType string) media.RequestDTO {
	t.Helper()
	got, err := deps.Media.CreateRequest(context.Background(), media.Actor{
		User: mediaHTTPUser(userID),
		IP:   "127.0.0.1",
	}, media.CreateRequestInput{
		TMDBID:       tmdbID,
		MediaType:    mediaType,
		TMDBLanguage: "zh-CN",
		TargetID:     targetID,
	})
	if err != nil {
		t.Fatalf("create request through service: %v", err)
	}
	return got
}

func seedMediaHTTPRequestRow(t *testing.T, st *store.Store, userID string, target queries.DiscoveryPolicyTarget, deps mediaHTTPTargetDeps, n int64) queries.DiscoverySubscriptionRequest {
	t.Helper()
	row, err := st.CreateDiscoverySubscriptionRequest(context.Background(), queries.CreateDiscoverySubscriptionRequestParams{
		RequesterUserID:             userID,
		Status:                      "pending_review",
		TmdbID:                      "req-" + itoa(n),
		MediaType:                   "movie",
		TmdbLanguage:                "zh-CN",
		TitleSnapshot:               "Request " + itoa(n),
		OriginalTitleSnapshot:       sql.NullString{},
		ReleaseYearSnapshot:         sql.NullInt64{},
		PosterPathSnapshot:          sql.NullString{},
		SeasonFilterJson:            sql.NullString{},
		PolicyIDSnapshot:            sql.NullInt64{Int64: target.PolicyID, Valid: true},
		PolicyTargetIDSnapshot:      sql.NullInt64{Int64: target.ID, Valid: true},
		TargetLabelSnapshot:         target.Label,
		TargetLibraryID:             deps.libraryID,
		TargetLibraryNameSnapshot:   "Media",
		ProducerProfileIDSnapshot:   deps.producerProfileID,
		ProducerProfileNameSnapshot: "115 default",
		RuleProfileIDSnapshot:       deps.ruleProfileID,
		RuleProfileVersionSnapshot:  1,
		UserNote:                    sql.NullString{},
		AdminNote:                   sql.NullString{},
		ReviewedBy:                  sql.NullString{},
		ReviewedAt:                  sql.NullInt64{},
		SubscriptionID:              sql.NullInt64{},
		IdempotencyKey:              "http-request-" + userID + "-" + itoa(n),
		LastErrorKind:               sql.NullString{},
		LastErrorMessage:            sql.NullString{},
		CreatedAt:                   1000 + n,
		UpdatedAt:                   1000 + n,
	})
	if err != nil {
		t.Fatalf("seed request row %d: %v", n, err)
	}
	return row
}

func seedMediaHTTPSubscriptions(t *testing.T, st *store.Store, userID string, deps mediaHTTPTargetDeps, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		n := int64(i)
		subscription, err := st.CreateDiscoverySubscription(context.Background(), queries.CreateDiscoverySubscriptionParams{
			OwnerID:           userID,
			TmdbID:            "sub-" + itoa(n),
			MediaType:         "movie",
			TmdbLanguage:      "zh-CN",
			TitleSnapshot:     "Subscription " + itoa(n),
			LibraryID:         deps.libraryID,
			ProducerProfileID: deps.producerProfileID,
			RuleProfileID:     deps.ruleProfileID,
			Status:            "active",
			SeasonFilterJson:  sql.NullString{},
			NextCheckAt:       sql.NullInt64{},
			CreatedAt:         2000 + n,
			UpdatedAt:         2000 + n,
		})
		if err != nil {
			t.Fatalf("seed discovery subscription %d: %v", i, err)
		}
		if _, err := st.UpsertUserMediaSubscription(context.Background(), queries.UpsertUserMediaSubscriptionParams{
			EchoUserID:              userID,
			RequestID:               sql.NullInt64{},
			DiscoverySubscriptionID: subscription.ID,
			TmdbID:                  subscription.TmdbID,
			MediaType:               subscription.MediaType,
			SeasonFilterJson:        sql.NullString{},
			SeasonFilterKey:         "",
			Status:                  "active",
			CreatedAt:               2000 + n,
			UpdatedAt:               2000 + n,
		}); err != nil {
			t.Fatalf("seed user media subscription %d: %v", i, err)
		}
	}
}

func countMediaHTTPRequestsForUser(t *testing.T, st *store.Store, userID string) int64 {
	t.Helper()
	var count int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM discovery_subscription_requests
WHERE requester_user_id = ?`, userID).Scan(&count); err != nil {
		t.Fatalf("count requests for %s: %v", userID, err)
	}
	return count
}

func latestMediaHTTPAudit(t *testing.T, st *store.Store, userID string) (string, string) {
	t.Helper()
	var action, reason string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT action, safe_reason
FROM discovery_user_audit_events
WHERE actor_user_id = ?
ORDER BY id DESC
LIMIT 1`, userID).Scan(&action, &reason); err != nil {
		t.Fatalf("latest audit for %s: %v", userID, err)
	}
	return action, reason
}

func mediaHTTPUser(userID string, scopes ...string) auth.UserContext {
	if len(scopes) == 0 {
		scopes = []string{"discovery"}
	}
	return auth.UserContext{
		UserID: userID,
		Role:   "user",
		Scopes: scopes,
		Now:    time.Unix(1000, 0),
	}
}

func mediaHTTPNullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func assertNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store; body=%s", got, rec.Body.String())
	}
}

func assertBodyOmits(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(body, fragment) {
			t.Fatalf("response leaked %q: %s", fragment, body)
		}
	}
}

func mediaHTTPForbiddenResponseFragments() []string {
	return []string{
		"owner_id",
		"library_id",
		"producer_profile_id",
		"rule_profile_id",
		"source_id",
		"account_id",
		"target_account",
		"target_library_id",
		"pipeline_owner_id",
		"next_check_at",
		"share_url",
		"share_code",
		"receive_code",
		"default_args_json",
		"raw_text_ref",
		"job_payload",
		"admin_note",
		"user_note",
		"reviewed_by",
		"raw-share-secret",
		"raw-receive-secret",
		"admin note secret",
		"raw stack secret",
		"producer args",
		"rule secret",
		"acc-115",
	}
}

type fakeMediaLimiter struct {
	mu    sync.Mutex
	deny  map[string]bool
	calls []string
}

func newFakeMediaLimiter() *fakeMediaLimiter {
	return &fakeMediaLimiter{deny: make(map[string]bool)}
}

func (l *fakeMediaLimiter) Allow(key string, limit int, window time.Duration) bool {
	return l.AllowAll([]media.RateLimitCheck{{Key: key, Limit: limit, Window: window}})
}

func (l *fakeMediaLimiter) AllowAll(checks []media.RateLimitCheck) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	allowed := true
	for _, check := range checks {
		l.calls = append(l.calls, check.Key)
		if l.deny[check.Key] {
			allowed = false
		}
	}
	return allowed
}

func (l *fakeMediaLimiter) assertCalled(t *testing.T, key string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, call := range l.calls {
		if call == key {
			return
		}
	}
	t.Fatalf("limiter calls=%v, want call %q", l.calls, key)
}

func (l *fakeMediaLimiter) assertNotCalled(t *testing.T, key string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, call := range l.calls {
		if call == key {
			t.Fatalf("limiter calls=%v, did not want call %q", l.calls, key)
		}
	}
}
