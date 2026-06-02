package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/discovery"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestDiscoveryRoutesRequireAdmin(t *testing.T) {
	deps := APIDeps{}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.NewContext(req.Context(), auth.UserContext{UserID: "u1", Scopes: []string{"read"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	deps.MountDiscovery(r)
	req := httptest.NewRequest(http.MethodGet, "/api/discovery/subscriptions", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

type discoveryAPITestDeps struct {
	Router *chi.Mux
	Jobs   *fakeDiscoveryJobs
}

type fakeDiscoveryJobs struct {
	enqueued []struct {
		kind    string
		payload any
	}
}

func (f *fakeDiscoveryJobs) Enqueue(ctx context.Context, kind string, payload any) (int64, error) {
	f.enqueued = append(f.enqueued, struct {
		kind    string
		payload any
	}{kind: kind, payload: payload})
	return int64(len(f.enqueued)), nil
}

func (f *fakeDiscoveryJobs) Cancel(jobID int64) bool {
	return false
}

func newDiscoveryAPITestDeps(t *testing.T, admin bool) discoveryAPITestDeps {
	t.Helper()
	jobs := &fakeDiscoveryJobs{}
	deps := APIDeps{Jobs: jobs}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			scopes := []string{"read"}
			if admin {
				scopes = append(scopes, "admin")
			}
			ctx := auth.NewContext(req.Context(), auth.UserContext{UserID: "admin", Scopes: scopes})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	deps.MountDiscovery(r)
	return discoveryAPITestDeps{Router: r, Jobs: jobs}
}

func TestDiscoveryRunSourceReturnsAccepted(t *testing.T) {
	deps := newDiscoveryAPITestDeps(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/discovery/run/source/1", nil)
	req = req.WithContext(auth.NewContext(req.Context(), auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}))
	rr := httptest.NewRecorder()
	deps.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	if len(deps.Jobs.enqueued) != 1 || deps.Jobs.enqueued[0].kind != discovery.KindSourceCrawl {
		t.Fatalf("enqueued = %#v", deps.Jobs.enqueued)
	}
}

func TestDiscoveryRawDebugDisabledByDefault(t *testing.T) {
	deps := newDiscoveryAPITestDeps(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/discovery/debug/resources/1/raw", nil)
	rr := httptest.NewRecorder()
	deps.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when raw debug is disabled", rr.Code)
	}
}

func TestDiscoveryAdminEndpointsRejectNonAdmin(t *testing.T) {
	deps := newDiscoveryAPITestDeps(t, false)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/discovery/subscriptions"},
		{http.MethodPost, "/api/discovery/subscriptions"},
		{http.MethodPatch, "/api/discovery/subscriptions/1"},
		{http.MethodGet, "/api/discovery/tmdb/search?q=known&type=movie"},
		{http.MethodGet, "/api/discovery/sources"},
		{http.MethodPost, "/api/discovery/sources"},
		{http.MethodPatch, "/api/discovery/sources/1"},
		{http.MethodGet, "/api/discovery/producer-profiles"},
		{http.MethodPost, "/api/discovery/producer-profiles"},
		{http.MethodPatch, "/api/discovery/producer-profiles/1"},
		{http.MethodGet, "/api/discovery/rule-profiles"},
		{http.MethodPost, "/api/discovery/rule-profiles"},
		{http.MethodPatch, "/api/discovery/rule-profiles/1"},
		{http.MethodPost, "/api/discovery/rule-profiles/1/test"},
		{http.MethodGet, "/api/discovery/candidates"},
		{http.MethodGet, "/api/discovery/matches"},
		{http.MethodPost, "/api/discovery/matches/1/accept"},
		{http.MethodPost, "/api/discovery/matches/1/reject"},
		{http.MethodPost, "/api/discovery/matches/1/retry"},
		{http.MethodPost, "/api/discovery/run/source/1"},
		{http.MethodPost, "/api/discovery/run/subscription/1"},
		{http.MethodGet, "/api/discovery/debug/resources/1/raw"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()
		deps.Router.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403", tc.method, tc.path, rr.Code)
		}
	}
}

type fakeDiscoveryTMDB struct {
	searches []struct {
		query     string
		mediaType string
	}
	movie tmdb.Media
	tv    tmdb.Media
}

func (f *fakeDiscoveryTMDB) Search(ctx context.Context, query, mediaType string) ([]tmdb.Media, error) {
	f.searches = append(f.searches, struct {
		query     string
		mediaType string
	}{query: query, mediaType: mediaType})
	return []tmdb.Media{f.movie}, nil
}

func (f *fakeDiscoveryTMDB) MovieDetails(ctx context.Context, tmdbID string) (tmdb.Media, error) {
	media := f.movie
	media.TMDBID = tmdbID
	media.MediaType = "movie"
	return media, nil
}

func (f *fakeDiscoveryTMDB) TVDetails(ctx context.Context, tmdbID string) (tmdb.Media, error) {
	media := f.tv
	media.TMDBID = tmdbID
	media.MediaType = "tv"
	return media, nil
}

func TestDiscoveryRawDebugRedactsStringValuesCapsAndAudits(t *testing.T) {
	st := newAPIStore(t)
	rawRoot := t.TempDir()
	raw := discovery.NewRawStore(discovery.RawStoreConfig{Root: rawRoot, MaxBytes: 4096})
	ref, redactedPreview, err := raw.Put(context.Background(), "source", "raw:1", []byte(`{"note":"receive_code=abcd","padding":"zzzz"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redactedPreview, "abcd") {
		t.Fatalf("rawstore preview leaked secret before handler test: %s", redactedPreview)
	}
	resourceID := seedDiscoveryRawResource(t, st, ref)
	deps := discoveryStoreDeps(st)
	deps.Config.DiscoveryRawDebug = DiscoveryRawDebugAPIConfig{Enabled: true, MaxBytes: 48, StorageRoot: rawRoot}

	rec := doReqAs(t, http.MethodGet, "/api/discovery/debug/resources/"+itoa(resourceID)+"/raw", "", auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var got struct {
		RawTextRedacted string `json:"raw_text_redacted"`
	}
	decodeBody(t, rec, &got)
	if strings.Contains(got.RawTextRedacted, "abcd") || strings.Contains(got.RawTextRedacted, "receive_code=abcd") {
		t.Fatalf("raw debug leaked sensitive string value: %s", got.RawTextRedacted)
	}
	if len([]byte(got.RawTextRedacted)) > deps.Config.DiscoveryRawDebug.MaxBytes {
		t.Fatalf("redacted body length=%d, want <= %d", len([]byte(got.RawTextRedacted)), deps.Config.DiscoveryRawDebug.MaxBytes)
	}
	var actor string
	var events int
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*), COALESCE(MAX(actor_user_id), '')
FROM discovery_raw_access_events
WHERE resource_id = ?`, resourceID).Scan(&events, &actor); err != nil {
		t.Fatal(err)
	}
	if events != 1 || actor != "admin" {
		t.Fatalf("audit events=%d actor=%q, want one admin event", events, actor)
	}
}

func TestDiscoveryCreateSubscriptionFetchesTMDBAndUpsertsSnapshot(t *testing.T) {
	st := newAPIStore(t)
	fixture := seedDiscoveryAPIFixture(t, st)
	tmdbFake := &fakeDiscoveryTMDB{movie: tmdb.Media{
		Title:         "Known Movie",
		OriginalTitle: "Known Original",
		ReleaseYear:   2024,
		PosterPath:    "/poster.jpg",
		RawJSON:       `{"id":321,"title":"Known Movie"}`,
	}}
	deps := discoveryStoreDeps(st)
	deps.DiscoveryTMDB = tmdbFake
	body := `{"tmdb_id":"321","media_type":"movie","tmdb_language":"zh-CN","library_id":` + itoa(fixture.libraryID) + `,"producer_profile_id":` + itoa(fixture.producerProfileID) + `,"rule_profile_id":` + itoa(fixture.ruleProfileID) + `}`
	rec := doReqAs(t, http.MethodPost, "/api/discovery/subscriptions", body, auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}
	var snapshot string
	if err := st.DB.QueryRowContext(context.Background(), `SELECT title_snapshot FROM discovery_subscriptions WHERE tmdb_id = '321'`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != "Known Movie (2024)" {
		t.Fatalf("title_snapshot=%q, want fetched title/year", snapshot)
	}
	var rawJSON string
	if err := st.DB.QueryRowContext(context.Background(), `SELECT raw_json FROM tmdb_media WHERE tmdb_id = '321' AND media_type = 'movie'`).Scan(&rawJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawJSON, "Known Movie") {
		t.Fatalf("tmdb raw_json=%q, want upserted details", rawJSON)
	}
}

func TestDiscoveryCandidatesListOmitsSecrets(t *testing.T) {
	st := newAPIStore(t)
	seedDiscoveryAPIFixture(t, st)
	deps := discoveryStoreDeps(st)
	rec := doReqAs(t, http.MethodGet, "/api/discovery/candidates", "", auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"share_code", "receive_code", "raw_text_ref", "share-secret", "receive-secret", "raw:"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("candidate list leaked %q in %s", forbidden, body)
		}
	}
}

func TestDiscoveryAcceptMissingMatchReturnsNotFoundAndDoesNotEnqueue(t *testing.T) {
	st := newAPIStore(t)
	jobs := &fakeDiscoveryJobs{}
	deps := discoveryStoreDeps(st)
	deps.Jobs = jobs
	rec := doReqAs(t, http.MethodPost, "/api/discovery/matches/9999/accept", "", auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if len(jobs.enqueued) != 0 {
		t.Fatalf("enqueued=%#v, want none", jobs.enqueued)
	}
}

func TestDiscoveryAcceptRejectsInFlightMatchWithoutResettingState(t *testing.T) {
	st := newAPIStore(t)
	fixture := seedDiscoveryAPIFixture(t, st)
	matchID := seedDiscoveryAPIMatch(t, st, fixture, "accept", "running")
	jobs := &fakeDiscoveryJobs{}
	deps := discoveryStoreDeps(st)
	deps.Jobs = jobs
	rec := doReqAs(t, http.MethodPost, "/api/discovery/matches/"+itoa(matchID)+"/accept", "", auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	assertDiscoveryMatchState(t, st, matchID, "accept", "running")
	if len(jobs.enqueued) != 0 {
		t.Fatalf("enqueued=%#v, want none", jobs.enqueued)
	}
}

func TestDiscoveryAcceptValidMatchEnqueuesDispatch(t *testing.T) {
	st := newAPIStore(t)
	fixture := seedDiscoveryAPIFixture(t, st)
	matchID := seedDiscoveryAPIMatch(t, st, fixture, "review", "none")
	jobs := &fakeDiscoveryJobs{}
	deps := discoveryStoreDeps(st)
	deps.Jobs = jobs
	rec := doReqAs(t, http.MethodPost, "/api/discovery/matches/"+itoa(matchID)+"/accept", "", auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202", rec.Code, rec.Body.String())
	}
	assertDiscoveryMatchState(t, st, matchID, "accept", "none")
	if len(jobs.enqueued) != 1 || jobs.enqueued[0].kind != discovery.KindDispatch {
		t.Fatalf("enqueued=%#v, want one dispatch", jobs.enqueued)
	}
}

func TestDiscoveryRejectAndRetryTransitions(t *testing.T) {
	st := newAPIStore(t)
	fixture := seedDiscoveryAPIFixture(t, st)
	rejectID := seedDiscoveryAPIMatch(t, st, fixture, "review", "none")
	deps := discoveryStoreDeps(st)
	deps.Jobs = &fakeDiscoveryJobs{}
	rec := doReqAs(t, http.MethodPost, "/api/discovery/matches/"+itoa(rejectID)+"/reject", "", auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reject status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	assertDiscoveryMatchState(t, st, rejectID, "reject", "none")
	assertDiscoveredResourceStatus(t, st, fixture.resourceID, "rejected")

	failedID := seedDiscoveryAPIMatch(t, st, fixture, "failed", "failed")
	rec = doReqAs(t, http.MethodPost, "/api/discovery/matches/"+itoa(failedID)+"/retry", "", auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry failed status=%d body=%s, want 202", rec.Code, rec.Body.String())
	}
	assertDiscoveryMatchState(t, st, failedID, "queue", "none")

	runningID := seedDiscoveryAPIMatch(t, st, fixture, "queue", "running")
	rec = doReqAs(t, http.MethodPost, "/api/discovery/matches/"+itoa(runningID)+"/retry", "", auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}, func(r chi.Router) {
		deps.MountDiscovery(r)
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry running status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	assertDiscoveryMatchState(t, st, runningID, "queue", "running")
}

func TestDiscoverySourceValidationRejectsInvalidKindAndJSON(t *testing.T) {
	st := newAPIStore(t)
	deps := discoveryStoreDeps(st)
	admin := auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}
	cases := []struct {
		name string
		body string
	}{
		{name: "bad kind", body: `{"kind":"bad","name":"Bad","enabled":true,"config_json":"{}"}`},
		{name: "bad config", body: `{"kind":"poster_http","name":"Bad JSON","enabled":true,"config_json":"{"}`},
	}
	for _, tc := range cases {
		rec := doReqAs(t, http.MethodPost, "/api/discovery/sources", tc.body, admin, func(r chi.Router) {
			deps.MountDiscovery(r)
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s, want 400", tc.name, rec.Code, rec.Body.String())
		}
	}
}

type discoveryAPIFixture struct {
	sourceID          int64
	libraryID         int64
	producerProfileID int64
	ruleProfileID     int64
	subscriptionID    int64
	resourceID        int64
}

func discoveryStoreDeps(st *store.Store) APIDeps {
	return APIDeps{Store: st, Logger: apiLogger(), Now: apiClock()}
}

func seedDiscoveryAPIFixture(t *testing.T, st *store.Store) discoveryAPIFixture {
	t.Helper()
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO accounts (
  id, provider, sidecar_id, storage_mount, status, owner_id, created_at, updated_at
) VALUES ('acc-115', '115', 'sidecar-1', '/115', 'active', 'admin', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	var libraryID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO libraries (name, echo_output_kind, echo_output_path, owner_id, created_at)
VALUES ('Discovery Movies', 'local', '/tmp/echo-discovery-test', 'admin', 1)
RETURNING id`).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	var sourceID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_sources (
  kind, name, enabled, config_json, scheduler_state, created_at, updated_at
) VALUES ('poster_http', 'Poster', 1, '{}', 'healthy', 1, 1)
RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	var producerProfileID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_producer_profiles (
  name, provider, tool, target_account, target_subdir_template,
  library_rel_path_template, default_args_json, enabled, created_at, updated_at
) VALUES ('115 profile ' || ?, '115', '115share2cas', 'acc-115', '{{.Title}}', '{{.Title}}', '{}', 1, 1, 1)
RETURNING id`, libraryID).Scan(&producerProfileID); err != nil {
		t.Fatal(err)
	}
	var ruleProfileID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO rule_profiles (name, version, rules_json, enabled, created_at, updated_at)
VALUES ('rule ' || ?, 1, '{"weights":["resolutions"]}', 1, 1, 1)
RETURNING id`, libraryID).Scan(&ruleProfileID); err != nil {
		t.Fatal(err)
	}
	var subscriptionID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_subscriptions (
  owner_id, tmdb_id, media_type, tmdb_language, title_snapshot, library_id,
  producer_profile_id, rule_profile_id, status, created_at, updated_at
) VALUES ('admin', '123', 'movie', 'zh-CN', 'Known Movie', ?, ?, ?, 'active', 1, 1)
RETURNING id`, libraryID, producerProfileID, ruleProfileID).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	var resourceID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovered_resources (
  source_id, provider, link_kind, external_key, tmdb_id, media_type, title,
  share_code, receive_code, share_url_redacted, raw_text_redacted, raw_text_ref,
  parsed_json, feature_json, status, first_seen_at, last_seen_at
) VALUES (?, '115', '115_share', 'poster:1', '123', 'movie', 'Known Movie',
  'share-secret', 'receive-secret', 'https://115.com/s/abc?password=[REDACTED]', 'Known Movie', 'raw:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  '{}', '{}', 'candidate', 1, 1)
RETURNING id`, sourceID).Scan(&resourceID); err != nil {
		t.Fatal(err)
	}
	return discoveryAPIFixture{
		sourceID:          sourceID,
		libraryID:         libraryID,
		producerProfileID: producerProfileID,
		ruleProfileID:     ruleProfileID,
		subscriptionID:    subscriptionID,
		resourceID:        resourceID,
	}
}

func seedDiscoveryRawResource(t *testing.T, st *store.Store, rawRef string) int64 {
	t.Helper()
	fixture := seedDiscoveryAPIFixture(t, st)
	var id int64
	if err := st.DB.QueryRowContext(context.Background(), `
INSERT INTO discovered_resources (
  source_id, provider, link_kind, external_key, raw_text_redacted, raw_text_ref,
  parsed_json, feature_json, status, first_seen_at, last_seen_at
) VALUES (?, '115', '115_share', 'raw-debug', 'preview', ?, '{}', '{}', 'candidate', 1, 1)
RETURNING id`, fixture.sourceID, rawRef).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedDiscoveryAPIMatch(t *testing.T, st *store.Store, fixture discoveryAPIFixture, decision, dispatchState string) int64 {
	t.Helper()
	var queuedJob sql.NullInt64
	if dispatchState == "queued" || dispatchState == "running" || dispatchState == "succeeded" || dispatchState == "failed" {
		job, err := st.CreateJob(context.Background(), queries.CreateJobParams{
			Kind: "ingest_producer", Status: dispatchState, Payload: "{}", OwnerID: "discovery", CreatedAt: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		queuedJob = sql.NullInt64{Int64: job.ID, Valid: true}
	}
	var id int64
	if err := st.DB.QueryRowContext(context.Background(), `
INSERT INTO subscription_matches (
  subscription_id, resource_id, rule_profile_id, rule_profile_version,
  score_json, decision, reason, dispatch_state, idempotency_key, queued_job_id,
  created_at, updated_at
) VALUES (?, ?, ?, 1, '{"tuple":[1]}', ?, 'test', ?, ?, ?, 1, 1)
RETURNING id`,
		fixture.subscriptionID,
		fixture.resourceID,
		fixture.ruleProfileID,
		decision,
		dispatchState,
		decision+":"+dispatchState+":"+itoa(fixture.resourceID)+":"+itoa(fixture.ruleProfileID)+":"+itoa(fixture.subscriptionID)+":"+itoa(queuedJob.Int64),
		queuedJob,
	).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertDiscoveryMatchState(t *testing.T, st *store.Store, matchID int64, decision, dispatchState string) {
	t.Helper()
	var gotDecision, gotDispatchState string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT decision, dispatch_state FROM subscription_matches WHERE id = ?`, matchID).Scan(&gotDecision, &gotDispatchState); err != nil {
		t.Fatal(err)
	}
	if gotDecision != decision || gotDispatchState != dispatchState {
		t.Fatalf("match %d state=(%s,%s), want (%s,%s)", matchID, gotDecision, gotDispatchState, decision, dispatchState)
	}
}

func assertDiscoveredResourceStatus(t *testing.T, st *store.Store, resourceID int64, status string) {
	t.Helper()
	var got string
	if err := st.DB.QueryRowContext(context.Background(), `SELECT status FROM discovered_resources WHERE id = ?`, resourceID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != status {
		t.Fatalf("resource %d status=%q, want %q", resourceID, got, status)
	}
}
