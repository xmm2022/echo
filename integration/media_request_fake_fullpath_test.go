package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/media"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestMediaRequestFakeFullPathReusesOneDiscoverySubscription(t *testing.T) {
	ctx := context.Background()
	h := newDiscoveryHarness(t)
	seedMediaRequestUser(t, h.store, "u1", "user")
	seedMediaRequestUser(t, h.store, "u2", "user")
	seedMediaRequestUser(t, h.store, "admin", "admin")
	target := seedMediaRequestTarget(t, h)
	h.SeedTMDB("123", "movie", "Known Movie")

	svc := &media.Service{
		Store: h.store,
		TMDB: &fakeMediaRequestTMDB{
			movies: map[string]tmdb.Media{
				"123": {
					TMDBID:      "123",
					MediaType:   "movie",
					Title:       "Known Movie",
					ReleaseYear: 2024,
					RawJSON:     `{}`,
				},
			},
		},
		Now: func() time.Time { return h.now },
	}

	req1, err := svc.CreateRequest(ctx, mediaRequestActor("u1", h.now), media.CreateRequestInput{
		TMDBID:    "123",
		MediaType: "movie",
		TargetID:  target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u1 returned error: %v", err)
	}
	req2, err := svc.CreateRequest(ctx, mediaRequestActor("u2", h.now), media.CreateRequestInput{
		TMDBID:    "123",
		MediaType: "movie",
		TargetID:  target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u2 returned error: %v", err)
	}
	if got := h.CountProducerJobs(t); got != 0 {
		t.Fatalf("producer jobs after user request creation = %d, want 0", got)
	}

	admin := mediaRequestAdminActor(h.now)
	approvedReq1 := approveMediaRequestConcurrently(t, svc, admin, req1.ID, 2)
	approvedReq2, err := svc.ApproveRequest(ctx, admin, req2.ID, "")
	if err != nil {
		t.Fatalf("ApproveRequest u2 returned error: %v", err)
	}
	subscriptionID := approvedReq1[0].SubscriptionID
	if subscriptionID == 0 {
		t.Fatalf("u1 approved subscription id = 0")
	}
	for _, got := range append(approvedReq1, approvedReq2) {
		if got.Status != "approved" || got.SubscriptionID != subscriptionID {
			t.Fatalf("approved request = %+v, want approved with subscription %d", got, subscriptionID)
		}
	}

	h.subscriptionID = requireOneDiscoverySubscriptionForMedia(t, h.store, "123", "movie")
	if h.subscriptionID != subscriptionID {
		t.Fatalf("stored subscription id = %d, approved DTO subscription id = %d", h.subscriptionID, subscriptionID)
	}
	requireUserMediaSubscriptionCount(t, h.store, 2)

	h.SeedTelegramMessage("channel", 10, "Known Movie 2024 2160p https://115.com/s/abc?password=pass")
	if err := h.RunSourceCrawl("channel"); err != nil {
		t.Fatal(err)
	}
	if err := h.RunSubscriptionCheck("123", "movie"); err != nil {
		t.Fatal(err)
	}
	matchID := h.RequireOneReviewMatch(t)
	h.AcceptMatchConcurrently(t, matchID, 2)
	if got := h.CountProducerJobs(t); got != 1 {
		t.Fatalf("producer jobs = %d, want 1", got)
	}
	h.RequireProducerPayloadRedacted(t)

	subscriptionsU1, err := svc.ListSubscriptions(ctx, mediaRequestActor("u1", h.now), 10, 0)
	if err != nil {
		t.Fatalf("ListSubscriptions u1 returned error: %v", err)
	}
	subscriptionsU2, err := svc.ListSubscriptions(ctx, mediaRequestActor("u2", h.now), 10, 0)
	if err != nil {
		t.Fatalf("ListSubscriptions u2 returned error: %v", err)
	}
	if len(subscriptionsU1) != 1 || len(subscriptionsU2) != 1 {
		t.Fatalf("projected subscription counts = %d/%d, want 1/1", len(subscriptionsU1), len(subscriptionsU2))
	}
	assertMediaRequestProjectionSafeJSON(t, map[string][]media.SubscriptionDTO{
		"u1": subscriptionsU1,
		"u2": subscriptionsU2,
	})
}

type fakeMediaRequestTMDB struct {
	movies map[string]tmdb.Media
	tv     map[string]tmdb.Media
}

func (f *fakeMediaRequestTMDB) Search(_ context.Context, _ string, _ string) ([]tmdb.Media, error) {
	return nil, nil
}

func (f *fakeMediaRequestTMDB) MovieDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	if media, ok := f.movies[tmdbID]; ok {
		return media, nil
	}
	return tmdb.Media{}, fmt.Errorf("movie %s not found", tmdbID)
}

func (f *fakeMediaRequestTMDB) TVDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	if media, ok := f.tv[tmdbID]; ok {
		return media, nil
	}
	return tmdb.Media{}, fmt.Errorf("tv %s not found", tmdbID)
}

func seedMediaRequestUser(t *testing.T, st *store.Store, id, role string) {
	t.Helper()
	if role == "" {
		role = "user"
	}
	if id == "admin" {
		existing, err := st.GetUser(context.Background(), queries.GetUserParams{ID: id})
		if err == nil {
			if existing.Role != "admin" || existing.Status != "active" {
				t.Fatalf("existing admin = role %q status %q, want admin/active", existing.Role, existing.Status)
			}
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("get admin user: %v", err)
		}
	}
	if err := st.CreateUser(context.Background(), queries.CreateUserParams{
		ID:            id,
		Username:      id,
		Role:          role,
		Status:        "active",
		QuotaPolicyID: 1,
		CreatedAt:     100,
		UpdatedAt:     100,
	}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
}

func seedMediaRequestTarget(t *testing.T, h *discoveryHarness) queries.DiscoveryPolicyTarget {
	t.Helper()
	ctx := context.Background()
	now := h.now.Unix()

	if err := h.store.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           "acc-115",
		Provider:     "115",
		SidecarID:    "sidecar-1",
		StorageMount: "/115",
		Status:       "active",
		OwnerID:      "admin",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	library, err := h.store.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "fake media request gate",
		EchoOutputKind: "local",
		EchoOutputPath: h.t.TempDir(),
		OwnerID:        "admin",
		CreatedAt:      now,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	producerProfile, err := h.store.CreateDiscoveryProducerProfile(ctx, queries.CreateDiscoveryProducerProfileParams{
		Name:                   "fake 115 media request",
		Provider:               "115",
		Tool:                   "115share2cas",
		TargetAccount:          "acc-115",
		TargetSubdirTemplate:   "{{.Title}}",
		LibraryRelPathTemplate: "{{.Title}}",
		DefaultArgsJson:        `{"cookie_file":"ref:fake/cookie.txt","recycle_password_file":"ref:fake/recycle.txt","mode":"transfer-batch"}`,
		Enabled:                1,
		CreatedAt:              now,
		UpdatedAt:              now,
	})
	if err != nil {
		t.Fatalf("create producer profile: %v", err)
	}
	ruleProfile, err := h.store.CreateRuleProfile(ctx, queries.CreateRuleProfileParams{
		Name:      "fake media request rules",
		Version:   1,
		RulesJson: `{"weights":["colors"]}`,
		Enabled:   1,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create rule profile: %v", err)
	}
	policy, err := h.store.CreateDiscoveryAccessPolicy(ctx, queries.CreateDiscoveryAccessPolicyParams{
		Name:        "fake media request approval",
		Enabled:     1,
		Priority:    100,
		RequestMode: "approval_required",
		CanSearch:   1,
		CreatedBy:   sql.NullString{String: "admin", Valid: true},
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	target, err := h.store.CreateDiscoveryPolicyTarget(ctx, queries.CreateDiscoveryPolicyTargetParams{
		PolicyID:                policy.ID,
		Label:                   "Fake Movie Target",
		LibraryID:               library.ID,
		ProducerProfileID:       producerProfile.ID,
		RuleProfileID:           ruleProfile.ID,
		PipelineOwnerID:         "admin",
		MediaType:               sql.NullString{String: "movie", Valid: true},
		MatchMode:               "admin_review",
		GrantPlaybackOnApproval: 1,
		Enabled:                 1,
		DefaultTarget:           1,
		CreatedAt:               now,
		UpdatedAt:               now,
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	return target
}

func approveMediaRequestConcurrently(t *testing.T, svc *media.Service, actor media.Actor, requestID int64, n int) []media.RequestDTO {
	t.Helper()
	var wg sync.WaitGroup
	results := make(chan media.RequestDTO, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := svc.ApproveRequest(context.Background(), actor, requestID, "")
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent ApproveRequest returned error: %v", err)
	}
	out := make([]media.RequestDTO, 0, n)
	for got := range results {
		out = append(out, got)
	}
	if len(out) != n {
		t.Fatalf("concurrent approvals = %d, want %d", len(out), n)
	}
	return out
}

func requireOneDiscoverySubscriptionForMedia(t *testing.T, st *store.Store, tmdbID, mediaType string) int64 {
	t.Helper()
	var count int
	var id int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*), COALESCE(MAX(id), 0)
FROM discovery_subscriptions
WHERE tmdb_id = ? AND media_type = ?`, tmdbID, mediaType).Scan(&count, &id); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("discovery subscriptions for %s/%s = %d, want 1", mediaType, tmdbID, count)
	}
	return id
}

func requireUserMediaSubscriptionCount(t *testing.T, st *store.Store, want int) {
	t.Helper()
	var count int
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM user_media_subscriptions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("user media subscriptions = %d, want %d", count, want)
	}
}

func assertMediaRequestProjectionSafeJSON(t *testing.T, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal projected subscriptions: %v", err)
	}
	body := string(data)
	for _, key := range []string{
		"share_code",
		"receive_code",
		"raw_text_ref",
		"target_account",
		"storage_mount",
		"default_args_json",
		"queued_job_id",
	} {
		if strings.Contains(body, `"`+key+`"`) {
			t.Fatalf("projected subscription JSON leaked sensitive key %q: %s", key, body)
		}
	}
	for _, value := range []string{
		"https://115.com/s/abc?password=pass",
		"password=pass",
	} {
		if strings.Contains(body, value) {
			t.Fatalf("projected subscription JSON leaked sensitive value %q: %s", value, body)
		}
	}
}

func mediaRequestActor(userID string, now time.Time) media.Actor {
	return media.Actor{
		User: auth.UserContext{
			UserID: userID,
			Role:   "user",
			Scopes: []string{"discovery"},
			Now:    now,
		},
		IP: "127.0.0.1",
	}
}

func mediaRequestAdminActor(now time.Time) media.Actor {
	return media.Actor{
		User: auth.UserContext{
			UserID: "admin",
			Role:   "admin",
			Scopes: []string{"admin"},
			Now:    now,
		},
		IP: "127.0.0.1",
	}
}
