package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
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
	second := postMediaRequest(t, deps, user, body)
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

type fakeMediaMetadataClient struct {
	mu       sync.Mutex
	searches map[string][]tmdb.Media
	movies   map[string]tmdb.Media
	tv       map[string]tmdb.Media
	details  int
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
	return append([]tmdb.Media(nil), f.searches[mediaType+":"+query]...), nil
}

func (f *fakeMediaMetadataClient) MovieDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.details++
	item, ok := f.movies[tmdbID]
	if !ok {
		return tmdb.Media{}, media.ErrMetadataUnavailable
	}
	return item, nil
}

func (f *fakeMediaMetadataClient) TVDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.details++
	item, ok := f.tv[tmdbID]
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
	rec := doReqAs(t, http.MethodPost, "/api/me/discovery/requests", body, user, func(r chi.Router) {
		deps.MountMedia(r)
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create request status=%d body=%s, want 201", rec.Code, rec.Body.String())
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
