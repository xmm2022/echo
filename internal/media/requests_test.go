package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

type fakeMetadataClient struct {
	mu          sync.Mutex
	searches    map[string][]tmdb.Media
	details     map[string]tmdb.Media
	searchCalls int
	movieCalls  int
	tvCalls     int
}

func newFakeMetadataClient() *fakeMetadataClient {
	return &fakeMetadataClient{
		searches: make(map[string][]tmdb.Media),
		details:  make(map[string]tmdb.Media),
	}
}

func (f *fakeMetadataClient) Search(_ context.Context, query, mediaType string) ([]tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchCalls++
	return append([]tmdb.Media(nil), f.searches[mediaType+":"+query]...), nil
}

func (f *fakeMetadataClient) MovieDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.movieCalls++
	media, ok := f.details["movie:"+tmdbID]
	if !ok {
		return tmdb.Media{}, errors.New("movie missing")
	}
	return media, nil
}

func (f *fakeMetadataClient) TVDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tvCalls++
	media, ok := f.details["tv:"+tmdbID]
	if !ok {
		return tmdb.Media{}, errors.New("tv missing")
	}
	return media, nil
}

func (f *fakeMetadataClient) detailCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.movieCalls + f.tvCalls
}

func TestSearchReturnsSafeSummaries(t *testing.T) {
	ctx := context.Background()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	createMediaPolicy(t, st, "search policy", "u1", 1, 100, "approval_required", 1)
	fake := newFakeMetadataClient()
	fake.searches["movie:Matrix"] = []tmdb.Media{{
		TMDBID:      "603",
		MediaType:   "movie",
		Title:       "The Matrix",
		ReleaseYear: 1999,
		PosterPath:  "/poster.jpg",
		RawJSON:     `{"secret":"raw"}`,
	}}
	svc := Service{Store: st, TMDB: fake, Now: mediaNow}

	got, err := svc.Search(ctx, mediaActor("u1"), SearchInput{
		Query:     "  Matrix  ",
		MediaType: "movie",
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].TMDBID != "603" || got[0].MediaType != "movie" || got[0].Title != "The Matrix" {
		t.Fatalf("summary = %+v, want safe TMDB summary", got[0])
	}
	if got[0].Availability != "unknown" {
		t.Fatalf("availability = %q, want unknown", got[0].Availability)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal search dto: %v", err)
	}
	if string(data) == "" || containsAny(string(data), []string{"raw_json", "secret"}) {
		t.Fatalf("search DTO JSON leaked raw metadata: %s", string(data))
	}
}

func TestCatalogListsEnabledPolicyTargetsWithoutRawIDs(t *testing.T) {
	ctx := context.Background()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	policy := createMediaPolicy(t, st, "catalog policy", "u1", 1, 100, "approval_required", 1)
	deps := seedMediaTargetDeps(t, st)
	target := createMediaTarget(t, st, deps, policy.ID, "TV Default", "tv", 1)
	createMediaTarget(t, st, deps, policy.ID, "Disabled", "movie", 0)
	svc := Service{Store: st, Now: mediaNow}

	got, err := svc.Catalog(ctx, mediaActor("u1"))
	if err != nil {
		t.Fatalf("Catalog returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].ID != target.ID || got[0].Label != "TV Default" || got[0].MediaType != "tv" {
		t.Fatalf("target dto = %+v, want enabled tv target", got[0])
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal target dto: %v", err)
	}
	if containsAny(string(data), []string{"library_id", "producer_profile_id", "rule_profile_id", "target_account", "default_args_json"}) {
		t.Fatalf("catalog DTO JSON leaked raw target fields: %s", string(data))
	}
}

func TestCatalogAllowsCanSearchFalse(t *testing.T) {
	ctx := context.Background()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	policy := createMediaPolicy(t, st, "catalog request only policy", "u1", 1, 100, "approval_required", 0)
	deps := seedMediaTargetDeps(t, st)
	target := createMediaTarget(t, st, deps, policy.ID, "Request Only", "movie", 1)
	svc := Service{Store: st, Now: mediaNow}

	got, err := svc.Catalog(ctx, mediaActor("u1"))
	if err != nil {
		t.Fatalf("Catalog returned error: %v", err)
	}
	if len(got) != 1 || got[0].ID != target.ID {
		t.Fatalf("Catalog = %+v, want request-only target", got)
	}
	if got[0].CanSearch {
		t.Fatalf("CanSearch = true, want false in catalog DTO")
	}
}

func TestCreateRequestApprovalRequiredCreatesPendingWithSnapshots(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["tv:1399"] = tmdb.Media{
		TMDBID:        "1399",
		MediaType:     "tv",
		Title:         "权力的游戏",
		OriginalTitle: "Game of Thrones",
		ReleaseYear:   2011,
		PosterPath:    "/poster.jpg",
		RawJSON:       `{"id":1399}`,
	}

	got, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:           " 1399 ",
		MediaType:        " tv ",
		TMDBLanguage:     "",
		SeasonFilterJSON: `[2,1,2]`,
		TargetID:         fixture.target.ID,
		UserNote:         " please find this ",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if got.ID == 0 || got.Status != "pending_review" || got.Title != "权力的游戏" || got.TargetLabel != fixture.target.Label {
		t.Fatalf("request dto = %+v, want pending snapshot DTO", got)
	}

	row, err := fixture.st.GetDiscoverySubscriptionRequestForUser(ctx, queries.GetDiscoverySubscriptionRequestForUserParams{
		ID:              got.ID,
		RequesterUserID: "u1",
	})
	if err != nil {
		t.Fatalf("get created request: %v", err)
	}
	if row.TmdbID != "1399" || row.MediaType != "tv" || row.TmdbLanguage != "zh-CN" {
		t.Fatalf("request identity = (%q,%q,%q), want 1399/tv/zh-CN", row.TmdbID, row.MediaType, row.TmdbLanguage)
	}
	if row.TitleSnapshot != "权力的游戏" || !row.OriginalTitleSnapshot.Valid || row.OriginalTitleSnapshot.String != "Game of Thrones" {
		t.Fatalf("title snapshots = (%q,%+v), want TMDB details", row.TitleSnapshot, row.OriginalTitleSnapshot)
	}
	if !row.ReleaseYearSnapshot.Valid || row.ReleaseYearSnapshot.Int64 != 2011 {
		t.Fatalf("release year = %+v, want 2011", row.ReleaseYearSnapshot)
	}
	if !row.PosterPathSnapshot.Valid || row.PosterPathSnapshot.String != "/poster.jpg" {
		t.Fatalf("poster path = %+v, want /poster.jpg", row.PosterPathSnapshot)
	}
	if !row.SeasonFilterJson.Valid || row.SeasonFilterJson.String != `[1,2]` {
		t.Fatalf("season filter = %+v, want normalized [1,2]", row.SeasonFilterJson)
	}
	if !row.PolicyIDSnapshot.Valid || row.PolicyIDSnapshot.Int64 != fixture.policy.ID {
		t.Fatalf("policy snapshot = %+v, want %d", row.PolicyIDSnapshot, fixture.policy.ID)
	}
	if !row.PolicyTargetIDSnapshot.Valid || row.PolicyTargetIDSnapshot.Int64 != fixture.target.ID {
		t.Fatalf("target snapshot = %+v, want %d", row.PolicyTargetIDSnapshot, fixture.target.ID)
	}
	if row.TargetLibraryID != fixture.deps.LibraryID || row.ProducerProfileIDSnapshot != fixture.deps.ProducerProfileID || row.RuleProfileIDSnapshot != fixture.deps.RuleProfileID {
		t.Fatalf("target dependency snapshots = %+v, want fixture deps %+v", row, fixture.deps)
	}
	if !row.UserNote.Valid || row.UserNote.String != "please find this" {
		t.Fatalf("user note = %+v, want trimmed note", row.UserNote)
	}
	if len(row.IdempotencyKey) != 64 {
		t.Fatalf("idempotency key length = %d, want sha256 hex", len(row.IdempotencyKey))
	}
	if row.SubscriptionID.Valid {
		t.Fatalf("subscription_id = %+v, want NULL for pending review", row.SubscriptionID)
	}

	cached, err := fixture.st.GetTMDBMedia(ctx, queries.GetTMDBMediaParams{
		TmdbID:    "1399",
		MediaType: "tv",
		Language:  "zh-CN",
	})
	if err != nil {
		t.Fatalf("get cached tmdb media: %v", err)
	}
	if cached.Title != "权力的游戏" || cached.RawJson != `{"id":1399}` {
		t.Fatalf("cached tmdb = %+v, want fetched details", cached)
	}

	events, err := fixture.st.ListDiscoverySubscriptionRequestEvents(ctx, queries.ListDiscoverySubscriptionRequestEventsParams{
		RequestID: got.ID,
		Limit:     10,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("list request events: %v", err)
	}
	if len(events) != 1 || events[0].Action != "created" || !events[0].ToStatus.Valid || events[0].ToStatus.String != "pending_review" {
		t.Fatalf("events = %+v, want one created pending event", events)
	}
	if countJobs(t, fixture.st) != 0 {
		t.Fatalf("producer jobs were enqueued, want none")
	}
	if fixture.tmdb.tvCalls != 1 || fixture.tmdb.movieCalls != 0 {
		t.Fatalf("tmdb calls movie=%d tv=%d, want one tv detail call", fixture.tmdb.movieCalls, fixture.tmdb.tvCalls)
	}
}

func TestCreateRequestAllowsCanSearchFalse(t *testing.T) {
	ctx := context.Background()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	policy := createMediaPolicy(t, st, "request only policy", "u1", 1, 100, "approval_required", 0)
	deps := seedMediaTargetDeps(t, st)
	target := createMediaTarget(t, st, deps, policy.ID, "Request Only", "movie", 1)
	fake := newFakeMetadataClient()
	fake.details["movie:604"] = tmdb.Media{
		TMDBID:    "604",
		MediaType: "movie",
		Title:     "Request Only Movie",
		RawJSON:   `{}`,
	}
	svc := Service{Store: st, TMDB: fake, Now: mediaNow}

	got, err := svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "604",
		MediaType: "movie",
		TargetID:  target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if got.Status != "pending_review" || got.Title != "Request Only Movie" {
		t.Fatalf("CreateRequest = %+v, want pending request despite can_search=0", got)
	}
	if countRequestsForUser(t, st, "u1") != 1 {
		t.Fatalf("request rows = %d, want 1", countRequestsForUser(t, st, "u1"))
	}
}

func TestCreateRequestAutoApproveReturnsInvalidTransitionWithoutSideEffects(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "auto_approve", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:605"] = tmdb.Media{
		TMDBID:    "605",
		MediaType: "movie",
		Title:     "Auto",
		RawJSON:   `{}`,
	}

	_, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "605",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("CreateRequest error = %v, want %v", err, ErrInvalidTransition)
	}
	if got := countRequestsForUser(t, fixture.st, "u1"); got != 0 {
		t.Fatalf("request rows = %d, want 0", got)
	}
	if got := countDiscoverySubscriptions(t, fixture.st); got != 0 {
		t.Fatalf("canonical subscriptions = %d, want 0", got)
	}
	if got := countUserMediaSubscriptions(t, fixture.st); got != 0 {
		t.Fatalf("user subscriptions = %d, want 0", got)
	}
	if got := countJobs(t, fixture.st); got != 0 {
		t.Fatalf("producer jobs = %d, want 0", got)
	}
	if calls := fixture.tmdb.detailCalls(); calls != 0 {
		t.Fatalf("tmdb detail calls = %d, want 0 for auto_approve guard", calls)
	}
}

func TestCreateRequestTMDBDetailsErrorReturnsMetadataUnavailable(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})

	_, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "606",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
	})
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Fatalf("CreateRequest error = %v, want %v", err, ErrMetadataUnavailable)
	}
	if got := countRequestsForUser(t, fixture.st, "u1"); got != 0 {
		t.Fatalf("request rows = %d, want 0 on metadata error", got)
	}
}

func TestCreateRequestDuplicateReturnsExistingRequest(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:603"] = tmdb.Media{
		TMDBID:      "603",
		MediaType:   "movie",
		Title:       "The Matrix",
		ReleaseYear: 1999,
		RawJSON:     `{}`,
	}

	first, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:       "603",
		MediaType:    "movie",
		TargetID:     fixture.target.ID,
		UserNote:     "first note",
		TMDBLanguage: "zh-CN",
	})
	if err != nil {
		t.Fatalf("first CreateRequest returned error: %v", err)
	}
	second, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:       "603",
		MediaType:    "movie",
		TargetID:     fixture.target.ID,
		UserNote:     "second note must not create a new request",
		TMDBLanguage: "zh-CN",
	})
	if err != nil {
		t.Fatalf("second CreateRequest returned error: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate request ID = %d, want existing %d", second.ID, first.ID)
	}
	if countRequestsForUser(t, fixture.st, "u1") != 1 {
		t.Fatalf("request count = %d, want 1", countRequestsForUser(t, fixture.st, "u1"))
	}
	rows, err := fixture.svc.ListRequests(ctx, mediaActor("u1"), 10, 0)
	if err != nil {
		t.Fatalf("ListRequests returned error: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != first.ID {
		t.Fatalf("listed requests = %+v, want existing request only", rows)
	}
	got, err := fixture.svc.GetRequest(ctx, mediaActor("u1"), first.ID)
	if err != nil {
		t.Fatalf("GetRequest returned error: %v", err)
	}
	if got.ID != first.ID || got.Title != "The Matrix" {
		t.Fatalf("GetRequest = %+v, want original request", got)
	}
}

func TestCreateRequestDuplicateConcurrentCallsReturnOneRequest(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["tv:456"] = tmdb.Media{
		TMDBID:    "456",
		MediaType: "tv",
		Title:     "Concurrent Show",
		RawJSON:   `{}`,
	}

	const callers = 12
	var wg sync.WaitGroup
	results := make(chan RequestDTO, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
				TMDBID:           "456",
				MediaType:        "tv",
				TMDBLanguage:     "zh-CN",
				SeasonFilterJSON: `[3,1,3]`,
				TargetID:         fixture.target.ID,
			})
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
		t.Fatalf("concurrent CreateRequest returned error: %v", err)
	}
	var wantID int64
	seen := 0
	for got := range results {
		seen++
		if wantID == 0 {
			wantID = got.ID
		}
		if got.ID != wantID {
			t.Fatalf("concurrent request ID = %d, want %d", got.ID, wantID)
		}
	}
	if seen != callers {
		t.Fatalf("results = %d, want %d", seen, callers)
	}
	if countRequestsForUser(t, fixture.st, "u1") != 1 {
		t.Fatalf("request count = %d, want 1", countRequestsForUser(t, fixture.st, "u1"))
	}
}

func TestCreateRequestRejectsForbiddenLimits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		requestMode   string
		maxPending    sql.NullInt64
		maxActive     sql.NullInt64
		cooldown      sql.NullInt64
		seedActive    bool
		seedRecent    bool
		wantErr       error
		wantSafeAudit string
		wantRows      int64
	}{
		{
			name:          "request disabled",
			requestMode:   "disabled",
			wantErr:       ErrPolicyDenied,
			wantSafeAudit: "request-disabled",
			wantRows:      0,
		},
		{
			name:          "pending limit",
			requestMode:   "approval_required",
			maxPending:    nullInt64(0),
			wantErr:       ErrLimitReached,
			wantSafeAudit: "request-limit-reached",
			wantRows:      0,
		},
		{
			name:          "active subscription limit",
			requestMode:   "approval_required",
			maxActive:     nullInt64(0),
			wantErr:       ErrLimitReached,
			wantSafeAudit: "request-limit-reached",
			wantRows:      0,
		},
		{
			name:          "cooldown",
			requestMode:   "approval_required",
			cooldown:      nullInt64(3600),
			seedRecent:    true,
			wantErr:       ErrLimitReached,
			wantSafeAudit: "rate-limit-reached",
			wantRows:      1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newRequestFixture(t, "u1", tc.requestMode, tc.maxPending, tc.maxActive, tc.cooldown)
			fixture.tmdb.details["movie:100"] = tmdb.Media{TMDBID: "100", MediaType: "movie", Title: "Existing", RawJSON: `{}`}
			fixture.tmdb.details["movie:101"] = tmdb.Media{TMDBID: "101", MediaType: "movie", Title: "Denied", RawJSON: `{}`}
			if tc.seedActive {
				seedMediaUserSubscription(t, fixture.st, "u1", 0, fixture.deps, "Active", "999", "movie", "active")
			}
			if tc.seedRecent {
				_, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
					TMDBID:       "100",
					MediaType:    "movie",
					TMDBLanguage: "zh-CN",
					TargetID:     fixture.target.ID,
				})
				if err != nil {
					t.Fatalf("seed recent request: %v", err)
				}
			}

			_, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
				TMDBID:       "101",
				MediaType:    "movie",
				TMDBLanguage: "zh-CN",
				TargetID:     fixture.target.ID,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateRequest error = %v, want %v", err, tc.wantErr)
			}
			if got := countRequestsForUser(t, fixture.st, "u1"); got != tc.wantRows {
				t.Fatalf("request rows = %d, want %d", got, tc.wantRows)
			}
			audit := latestAuditSafeReason(t, fixture.st, "u1")
			if audit != tc.wantSafeAudit {
				t.Fatalf("latest audit reason = %q, want %q", audit, tc.wantSafeAudit)
			}
		})
	}
}

func TestCancelPendingRequestOwnerOnly(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	seedMediaUser(t, fixture.st, "u2")
	fixture.tmdb.details["movie:603"] = tmdb.Media{
		TMDBID:    "603",
		MediaType: "movie",
		Title:     "The Matrix",
		RawJSON:   `{}`,
	}
	req, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "603",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}

	if _, err := fixture.svc.CancelRequest(ctx, mediaActor("u2"), req.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CancelRequest as non-owner error = %v, want %v", err, ErrNotFound)
	}
	canceled, err := fixture.svc.CancelRequest(ctx, mediaActor("u1"), req.ID)
	if err != nil {
		t.Fatalf("CancelRequest as owner returned error: %v", err)
	}
	if canceled.Status != "canceled" {
		t.Fatalf("canceled status = %q, want canceled", canceled.Status)
	}
	row, err := fixture.st.GetDiscoverySubscriptionRequestForUser(ctx, queries.GetDiscoverySubscriptionRequestForUserParams{
		ID:              req.ID,
		RequesterUserID: "u1",
	})
	if err != nil {
		t.Fatalf("get canceled request: %v", err)
	}
	if row.Status != "canceled" {
		t.Fatalf("stored status = %q, want canceled", row.Status)
	}
	events, err := fixture.st.ListDiscoverySubscriptionRequestEvents(ctx, queries.ListDiscoverySubscriptionRequestEventsParams{
		RequestID: req.ID,
		Limit:     10,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("list request events: %v", err)
	}
	if len(events) != 2 || events[0].Action != "canceled" || events[1].Action != "created" {
		t.Fatalf("events = %+v, want canceled then created", events)
	}
}

type mediaRequestFixture struct {
	st     *store.Store
	svc    Service
	tmdb   *fakeMetadataClient
	policy queries.DiscoveryAccessPolicy
	target queries.DiscoveryPolicyTarget
	deps   mediaTargetDeps
}

func newRequestFixture(
	t *testing.T,
	userID,
	requestMode string,
	maxPendingRequests,
	maxActiveSubscriptions,
	requestCooldownSeconds sql.NullInt64,
) mediaRequestFixture {
	t.Helper()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, userID)
	policy := createMediaPolicyWithLimits(
		t,
		st,
		"policy-"+userID+"-"+requestMode,
		userID,
		1,
		100,
		requestMode,
		1,
		maxPendingRequests,
		maxActiveSubscriptions,
		requestCooldownSeconds,
	)
	deps := seedMediaTargetDeps(t, st)
	target := createMediaTarget(t, st, deps, policy.ID, "Default Target", "", 1)
	fake := newFakeMetadataClient()
	return mediaRequestFixture{
		st:     st,
		svc:    Service{Store: st, TMDB: fake, Now: mediaNow},
		tmdb:   fake,
		policy: policy,
		target: target,
		deps:   deps,
	}
}

func countRequestsForUser(t *testing.T, st *store.Store, userID string) int64 {
	t.Helper()
	var count int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM discovery_subscription_requests
WHERE requester_user_id = ?`, userID).Scan(&count); err != nil {
		t.Fatalf("count requests for user: %v", err)
	}
	return count
}

func countJobs(t *testing.T, st *store.Store) int64 {
	t.Helper()
	var count int64
	if err := st.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM jobs`).Scan(&count); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return count
}

func countDiscoverySubscriptions(t *testing.T, st *store.Store) int64 {
	t.Helper()
	var count int64
	if err := st.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM discovery_subscriptions`).Scan(&count); err != nil {
		t.Fatalf("count discovery subscriptions: %v", err)
	}
	return count
}

func countUserMediaSubscriptions(t *testing.T, st *store.Store) int64 {
	t.Helper()
	var count int64
	if err := st.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM user_media_subscriptions`).Scan(&count); err != nil {
		t.Fatalf("count user media subscriptions: %v", err)
	}
	return count
}

func latestAuditSafeReason(t *testing.T, st *store.Store, userID string) string {
	t.Helper()
	var reason string
	err := st.DB.QueryRowContext(context.Background(), `
SELECT safe_reason
FROM discovery_user_audit_events
WHERE actor_user_id = ?
ORDER BY id DESC
LIMIT 1`, userID).Scan(&reason)
	if err != nil {
		t.Fatalf("latest audit safe reason: %v", err)
	}
	return reason
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
