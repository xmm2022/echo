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

func TestCreateRequestAutoApproveCreatesPendingThenApprovesInOneTransaction(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "auto_approve", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:605"] = tmdb.Media{
		TMDBID:    "605",
		MediaType: "movie",
		Title:     "Auto",
		RawJSON:   `{}`,
	}

	got, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "605",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if got.Status != "approved" || got.SubscriptionID == 0 || got.ReviewedAt != mediaTestNow || got.Title != "Auto" {
		t.Fatalf("CreateRequest = %+v, want approved DTO with subscription", got)
	}
	if countRequestsForUser(t, fixture.st, "u1") != 1 {
		t.Fatalf("request rows = %d, want 1", countRequestsForUser(t, fixture.st, "u1"))
	}
	if countDiscoverySubscriptions(t, fixture.st) != 1 {
		t.Fatalf("canonical subscriptions = %d, want 1", countDiscoverySubscriptions(t, fixture.st))
	}
	if countUserMediaSubscriptions(t, fixture.st) != 1 {
		t.Fatalf("user subscriptions = %d, want 1", countUserMediaSubscriptions(t, fixture.st))
	}
	row := getRequestByID(t, fixture.st, got.ID)
	if row.Status != "approved" || !row.SubscriptionID.Valid || row.SubscriptionID.Int64 != got.SubscriptionID {
		t.Fatalf("stored request = %+v, want approved linked request", row)
	}
	events, err := fixture.st.ListDiscoverySubscriptionRequestEvents(ctx, queries.ListDiscoverySubscriptionRequestEventsParams{
		RequestID: got.ID,
		Limit:     10,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("list request events: %v", err)
	}
	if len(events) != 2 || events[0].Action != "approved" || events[1].Action != "created" {
		t.Fatalf("events = %+v, want approved then created", events)
	}
	if got := countJobs(t, fixture.st); got != 0 {
		t.Fatalf("producer jobs = %d, want 0", got)
	}
	if calls := fixture.tmdb.detailCalls(); calls != 1 {
		t.Fatalf("tmdb detail calls = %d, want one details fetch", calls)
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

func TestCreatePendingRequestDuplicateReturnsExistingBeforeLimits(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", nullInt64(1), sql.NullInt64{}, sql.NullInt64{})
	target, err := fixture.svc.ResolveTarget(ctx, fixture.policy, fixture.target.ID, "movie")
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	media := tmdb.Media{TMDBID: "607", MediaType: "movie", Title: "Duplicate", RawJSON: `{}`}
	key, err := requestIdempotencyKey("u1", media.TMDBID, media.MediaType, "zh-CN", target.TargetID, "")
	if err != nil {
		t.Fatalf("idempotency key: %v", err)
	}

	first, created, err := fixture.svc.createPendingRequest(ctx, mediaActor("u1"), fixture.policy, target, media, "zh-CN", "", "", key)
	if err != nil {
		t.Fatalf("first createPendingRequest returned error: %v", err)
	}
	if !created {
		t.Fatalf("first createPendingRequest created=%v, want true", created)
	}
	second, created, err := fixture.svc.createPendingRequest(ctx, mediaActor("u1"), fixture.policy, target, media, "zh-CN", "", "", key)
	if err != nil {
		t.Fatalf("duplicate createPendingRequest returned error: %v", err)
	}
	if created {
		t.Fatalf("duplicate createPendingRequest created=%v, want false", created)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate ID = %d, want existing %d", second.ID, first.ID)
	}
	if got := countRequestsForUser(t, fixture.st, "u1"); got != 1 {
		t.Fatalf("request rows = %d, want 1", got)
	}
}

func TestCreatePendingRequestConcurrentDifferentKeysRespectPendingLimit(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", nullInt64(1), sql.NullInt64{}, sql.NullInt64{})
	target, err := fixture.svc.ResolveTarget(ctx, fixture.policy, fixture.target.ID, "movie")
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}

	type result struct {
		row     queries.DiscoverySubscriptionRequest
		created bool
		err     error
	}
	results := make(chan result, 2)
	for _, tmdbID := range []string{"608", "609"} {
		tmdbID := tmdbID
		go func() {
			media := tmdb.Media{TMDBID: tmdbID, MediaType: "movie", Title: "Movie " + tmdbID, RawJSON: `{}`}
			key, keyErr := requestIdempotencyKey("u1", media.TMDBID, media.MediaType, "zh-CN", target.TargetID, "")
			if keyErr != nil {
				results <- result{err: keyErr}
				return
			}
			row, created, createErr := fixture.svc.createPendingRequest(ctx, mediaActor("u1"), fixture.policy, target, media, "zh-CN", "", "", key)
			results <- result{row: row, created: created, err: createErr}
		}()
	}

	successes := 0
	limitErrors := 0
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err == nil && got.row.ID != 0 {
			if !got.created {
				t.Fatalf("createPendingRequest success created=%v, want true", got.created)
			}
			successes++
			continue
		}
		if errors.Is(got.err, ErrLimitReached) {
			limitErrors++
			continue
		}
		t.Fatalf("createPendingRequest result = row %+v err %v, want one success and one limit error", got.row, got.err)
	}
	if successes != 1 || limitErrors != 1 {
		t.Fatalf("successes=%d limitErrors=%d, want 1/1", successes, limitErrors)
	}
	if got := countRequestsForUser(t, fixture.st, "u1"); got != 1 {
		t.Fatalf("request rows = %d, want 1", got)
	}
	if reason := latestAuditSafeReason(t, fixture.st, "u1"); reason != "request-limit-reached" {
		t.Fatalf("audit reason = %q, want request-limit-reached", reason)
	}
}

func TestListRequestsRedactsUnsafeFailureFields(t *testing.T) {
	ctx := context.Background()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	policy := createMediaPolicy(t, st, "redaction policy", "u1", 1, 100, "approval_required", 1)
	deps := seedMediaTargetDeps(t, st)
	target := createMediaTarget(t, st, deps, policy.ID, "Redaction Target", "movie", 1)
	request := seedUnsafeFailedRequest(t, st, "u1", policy, target, deps)
	svc := Service{Store: st, Now: mediaNow}

	listed, err := svc.ListRequests(ctx, mediaActor("u1"), 10, 0)
	if err != nil {
		t.Fatalf("ListRequests returned error: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != request.ID {
		t.Fatalf("listed requests = %+v, want seeded failed request", listed)
	}
	if listed[0].SafeReason != "request_failed" {
		t.Fatalf("SafeReason = %q, want request_failed", listed[0].SafeReason)
	}
	got, err := svc.GetRequest(ctx, mediaActor("u1"), request.ID)
	if err != nil {
		t.Fatalf("GetRequest returned error: %v", err)
	}
	if got.SafeReason != "request_failed" {
		t.Fatalf("GetRequest SafeReason = %q, want request_failed", got.SafeReason)
	}
	data, err := json.Marshal([]RequestDTO{got})
	if err != nil {
		t.Fatalf("marshal request DTO: %v", err)
	}
	jsonText := string(data)
	for _, forbidden := range []string{
		"unsafe_kind",
		"last_error_message",
		"last error secret",
		"raw user note secret",
		"raw admin note secret",
		"raw_text_ref",
	} {
		if strings.Contains(jsonText, forbidden) {
			t.Fatalf("request DTO JSON leaked %q: %s", forbidden, jsonText)
		}
	}
}

func TestRecordUserAuditEventTruncatesTargetID(t *testing.T) {
	ctx := context.Background()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	svc := Service{Store: st, Now: mediaNow}
	longTargetID := strings.Repeat("a", 160)

	if err := svc.RecordUserAuditEvent(ctx, mediaActor("u1"), "request_create_deny", "request", longTargetID, "request-disabled"); err != nil {
		t.Fatalf("RecordUserAuditEvent returned error: %v", err)
	}
	var stored string
	if err := st.DB.QueryRowContext(ctx, `
SELECT target_id
FROM discovery_user_audit_events
WHERE actor_user_id = 'u1'
ORDER BY id DESC
LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("read audit target id: %v", err)
	}
	if len(stored) > 120 {
		t.Fatalf("stored target_id length = %d, want <= 120", len(stored))
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
			detailCallsBeforeDeny := fixture.tmdb.detailCalls()

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
			if errors.Is(tc.wantErr, ErrLimitReached) {
				if calls := fixture.tmdb.detailCalls(); calls != detailCallsBeforeDeny {
					t.Fatalf("tmdb detail calls after denied request = %d, want unchanged %d", calls, detailCallsBeforeDeny)
				}
				if tmdbMediaExists(t, fixture.st, "101", "movie", "zh-CN") {
					t.Fatalf("denied request tmdb media was cached, want no cache write")
				}
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

func TestApproveRequestCreatesCanonicalSubscriptionAndUserLink(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:700"] = tmdb.Media{
		TMDBID:      "700",
		MediaType:   "movie",
		Title:       "Approved Movie",
		ReleaseYear: 2026,
		RawJSON:     `{}`,
	}
	req, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "700",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
		UserNote:  "raw approve user secret",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}

	got, err := fixture.svc.ApproveRequest(ctx, adminMediaActor(), req.ID, " raw approve admin secret ")
	if err != nil {
		t.Fatalf("ApproveRequest returned error: %v", err)
	}
	if got.ID != req.ID || got.Status != "approved" || got.SubscriptionID == 0 || got.ReviewedAt != mediaTestNow {
		t.Fatalf("approved DTO = %+v, want approved request with subscription", got)
	}

	row := getRequestByID(t, fixture.st, req.ID)
	if row.Status != "approved" || !row.SubscriptionID.Valid || row.SubscriptionID.Int64 != got.SubscriptionID {
		t.Fatalf("stored request = %+v, want approved linked request", row)
	}
	if !row.AdminNote.Valid || row.AdminNote.String != "raw approve admin secret" {
		t.Fatalf("admin note = %+v, want trimmed note", row.AdminNote)
	}
	if !row.ReviewedBy.Valid || row.ReviewedBy.String != "admin" {
		t.Fatalf("reviewed_by = %+v, want admin", row.ReviewedBy)
	}

	sub := getDiscoverySubscriptionByID(t, fixture.st, got.SubscriptionID)
	if sub.OwnerID != "admin" || sub.TmdbID != "700" || sub.MediaType != "movie" || sub.LibraryID != fixture.deps.LibraryID {
		t.Fatalf("canonical subscription = %+v, want target-owned canonical row", sub)
	}
	if sub.ProducerProfileID != fixture.deps.ProducerProfileID || sub.RuleProfileID != fixture.deps.RuleProfileID {
		t.Fatalf("canonical profiles = (%d,%d), want fixture deps %+v", sub.ProducerProfileID, sub.RuleProfileID, fixture.deps)
	}
	if sub.SeasonFilterJson.Valid {
		t.Fatalf("canonical season filter = %+v, want series/movie-level NULL", sub.SeasonFilterJson)
	}

	userSub := getOnlyUserMediaSubscription(t, fixture.st, "u1")
	if userSub.RequestID.Int64 != req.ID || userSub.DiscoverySubscriptionID != got.SubscriptionID || userSub.Status != "active" {
		t.Fatalf("user subscription = %+v, want active request link", userSub)
	}
	if userSub.SeasonFilterJson.Valid || userSub.SeasonFilterKey != "" {
		t.Fatalf("user season filter/key = %+v/%q, want all seasons empty key", userSub.SeasonFilterJson, userSub.SeasonFilterKey)
	}
	if countJobs(t, fixture.st) != 0 {
		t.Fatalf("producer jobs = %d, want 0", countJobs(t, fixture.st))
	}
	if actions := requestEventActions(t, fixture.st, req.ID); strings.Join(actions, ",") != "approved,created" {
		t.Fatalf("event actions = %v, want approved,created", actions)
	}
	assertRequestEventNotesRedacted(t, fixture.st, req.ID, "raw approve admin secret", "raw approve user secret")
}

func TestApproveRequestReusesCanonicalSubscriptionAcrossUsers(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	seedMediaUser(t, fixture.st, "u2")
	policy2 := createMediaPolicy(t, fixture.st, "policy-u2-approval", "u2", 1, 100, "approval_required", 1)
	target2 := createMediaTarget(t, fixture.st, fixture.deps, policy2.ID, "Default Target u2", "tv", 1)
	fixture.tmdb.details["tv:701"] = tmdb.Media{
		TMDBID:    "701",
		MediaType: "tv",
		Title:     "Shared Show",
		RawJSON:   `{}`,
	}
	req1, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:           "701",
		MediaType:        "tv",
		SeasonFilterJSON: `[2,1,2]`,
		TargetID:         fixture.target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u1 returned error: %v", err)
	}
	req2, err := fixture.svc.CreateRequest(ctx, mediaActor("u2"), CreateRequestInput{
		TMDBID:           "701",
		MediaType:        "tv",
		SeasonFilterJSON: `[3]`,
		TargetID:         target2.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u2 returned error: %v", err)
	}

	approved1, err := fixture.svc.ApproveRequest(ctx, adminMediaActor(), req1.ID, "")
	if err != nil {
		t.Fatalf("ApproveRequest u1 returned error: %v", err)
	}
	approved2, err := fixture.svc.ApproveRequest(ctx, adminMediaActor(), req2.ID, "")
	if err != nil {
		t.Fatalf("ApproveRequest u2 returned error: %v", err)
	}
	if approved1.SubscriptionID == 0 || approved1.SubscriptionID != approved2.SubscriptionID {
		t.Fatalf("subscription ids = %d/%d, want same canonical subscription", approved1.SubscriptionID, approved2.SubscriptionID)
	}
	if countDiscoverySubscriptions(t, fixture.st) != 1 {
		t.Fatalf("canonical subscriptions = %d, want 1", countDiscoverySubscriptions(t, fixture.st))
	}
	if countUserMediaSubscriptions(t, fixture.st) != 2 {
		t.Fatalf("user subscriptions = %d, want 2", countUserMediaSubscriptions(t, fixture.st))
	}
	canonical := getDiscoverySubscriptionByID(t, fixture.st, approved1.SubscriptionID)
	if canonical.SeasonFilterJson.Valid {
		t.Fatalf("canonical season filter = %+v, want series-level canonical row", canonical.SeasonFilterJson)
	}
	user1 := getOnlyUserMediaSubscription(t, fixture.st, "u1")
	if !user1.SeasonFilterJson.Valid || user1.SeasonFilterJson.String != `[1,2]` || user1.SeasonFilterKey != testSeasonFilterKey(`[1,2]`) {
		t.Fatalf("u1 season filter/key = %+v/%q, want normalized [1,2]", user1.SeasonFilterJson, user1.SeasonFilterKey)
	}
	user2 := getOnlyUserMediaSubscription(t, fixture.st, "u2")
	if !user2.SeasonFilterJson.Valid || user2.SeasonFilterJson.String != `[3]` || user2.SeasonFilterKey != testSeasonFilterKey(`[3]`) {
		t.Fatalf("u2 season filter/key = %+v/%q, want normalized [3]", user2.SeasonFilterJson, user2.SeasonFilterKey)
	}
}

func TestApproveRequestAdminReviewDowngradesExistingDispatchableMatches(t *testing.T) {
	ctx := context.Background()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	seedMediaUser(t, st, "u2")
	deps := seedMediaTargetDeps(t, st)
	autoPolicy := createMediaPolicy(t, st, "auto policy", "u1", 1, 100, "approval_required", 1)
	autoTarget := createMediaTargetWithMatchMode(t, st, deps, autoPolicy.ID, "Auto Target", "movie", 1, "auto_dispatch")
	reviewPolicy := createMediaPolicy(t, st, "review policy", "u2", 1, 100, "approval_required", 1)
	reviewTarget := createMediaTargetWithMatchMode(t, st, deps, reviewPolicy.ID, "Review Target", "movie", 1, "admin_review")
	fake := newFakeMetadataClient()
	fake.details["movie:7021"] = tmdb.Media{
		TMDBID:    "7021",
		MediaType: "movie",
		Title:     "Mode Merge Movie",
		RawJSON:   `{}`,
	}
	svc := Service{Store: st, TMDB: fake, Now: mediaNow}

	req1, err := svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "7021",
		MediaType: "movie",
		TargetID:  autoTarget.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u1 returned error: %v", err)
	}
	approved1, err := svc.ApproveRequest(ctx, adminMediaActor(), req1.ID, "")
	if err != nil {
		t.Fatalf("ApproveRequest u1 returned error: %v", err)
	}
	matchID := seedMediaDispatchableSubscriptionMatch(t, st, approved1.SubscriptionID, deps.RuleProfileID)
	assertMediaDispatchableMatches(t, st, matchID)

	req2, err := svc.CreateRequest(ctx, mediaActor("u2"), CreateRequestInput{
		TMDBID:    "7021",
		MediaType: "movie",
		TargetID:  reviewTarget.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u2 returned error: %v", err)
	}
	approved2, err := svc.ApproveRequest(ctx, adminMediaActor(), req2.ID, "")
	if err != nil {
		t.Fatalf("ApproveRequest u2 returned error: %v", err)
	}
	if approved2.SubscriptionID != approved1.SubscriptionID {
		t.Fatalf("subscription ids = %d/%d, want reused canonical", approved1.SubscriptionID, approved2.SubscriptionID)
	}
	canonical := getDiscoverySubscriptionByID(t, st, approved1.SubscriptionID)
	if canonical.MatchMode != "admin_review" {
		t.Fatalf("canonical match_mode = %q, want admin_review", canonical.MatchMode)
	}
	match, err := st.GetSubscriptionMatch(ctx, queries.GetSubscriptionMatchParams{ID: matchID})
	if err != nil {
		t.Fatalf("get downgraded match: %v", err)
	}
	if match.Decision != "review" || match.Reason != "admin_review" || match.DispatchState != "none" || match.DecidedAt.Valid {
		t.Fatalf("match state = decision:%s reason:%s dispatch:%s decided:%v, want review/admin_review/none/undecided", match.Decision, match.Reason, match.DispatchState, match.DecidedAt)
	}
	assertMediaDispatchableMatches(t, st)

	if _, err := st.AdminAcceptSubscriptionMatch(ctx, queries.AdminAcceptSubscriptionMatchParams{
		UpdatedAt: mediaTestNow + 10,
		ID:        matchID,
	}); err != nil {
		t.Fatalf("admin accept downgraded match: %v", err)
	}
	assertMediaDispatchableMatches(t, st, matchID)
}

func TestApproveRequestAdminReviewKeepsAdminAcceptedDispatchableMatches(t *testing.T) {
	ctx := context.Background()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	seedMediaUser(t, st, "u2")
	deps := seedMediaTargetDeps(t, st)
	autoPolicy := createMediaPolicy(t, st, "auto policy", "u1", 1, 100, "approval_required", 1)
	autoTarget := createMediaTargetWithMatchMode(t, st, deps, autoPolicy.ID, "Auto Target", "movie", 1, "auto_dispatch")
	reviewPolicy := createMediaPolicy(t, st, "review policy", "u2", 1, 100, "approval_required", 1)
	reviewTarget := createMediaTargetWithMatchMode(t, st, deps, reviewPolicy.ID, "Review Target", "movie", 1, "admin_review")
	fake := newFakeMetadataClient()
	fake.details["movie:7022"] = tmdb.Media{
		TMDBID:    "7022",
		MediaType: "movie",
		Title:     "Admin Accepted Movie",
		RawJSON:   `{}`,
	}
	svc := Service{Store: st, TMDB: fake, Now: mediaNow}

	req1, err := svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "7022",
		MediaType: "movie",
		TargetID:  autoTarget.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u1 returned error: %v", err)
	}
	approved1, err := svc.ApproveRequest(ctx, adminMediaActor(), req1.ID, "")
	if err != nil {
		t.Fatalf("ApproveRequest u1 returned error: %v", err)
	}
	matchID := seedMediaDispatchableSubscriptionMatch(t, st, approved1.SubscriptionID, deps.RuleProfileID)
	if _, err := st.AdminAcceptSubscriptionMatch(ctx, queries.AdminAcceptSubscriptionMatchParams{
		UpdatedAt: mediaTestNow + 10,
		ID:        matchID,
	}); err != nil {
		t.Fatalf("admin accept dispatchable match: %v", err)
	}
	assertMediaDispatchableMatches(t, st, matchID)

	req2, err := svc.CreateRequest(ctx, mediaActor("u2"), CreateRequestInput{
		TMDBID:    "7022",
		MediaType: "movie",
		TargetID:  reviewTarget.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u2 returned error: %v", err)
	}
	approved2, err := svc.ApproveRequest(ctx, adminMediaActor(), req2.ID, "")
	if err != nil {
		t.Fatalf("ApproveRequest u2 returned error: %v", err)
	}
	if approved2.SubscriptionID != approved1.SubscriptionID {
		t.Fatalf("subscription ids = %d/%d, want reused canonical", approved1.SubscriptionID, approved2.SubscriptionID)
	}
	canonical := getDiscoverySubscriptionByID(t, st, approved1.SubscriptionID)
	if canonical.MatchMode != "admin_review" {
		t.Fatalf("canonical match_mode = %q, want admin_review", canonical.MatchMode)
	}
	match, err := st.GetSubscriptionMatch(ctx, queries.GetSubscriptionMatchParams{ID: matchID})
	if err != nil {
		t.Fatalf("get admin accepted match: %v", err)
	}
	if match.Decision != "accept" || match.Reason != "admin_accept" || match.DispatchState != "none" {
		t.Fatalf("match state = decision:%s reason:%s dispatch:%s, want accept/admin_accept/none", match.Decision, match.Reason, match.DispatchState)
	}
	assertMediaDispatchableMatches(t, st, matchID)
}

func TestApproveRequestConcurrentCallsReturnSingleCanonicalSubscription(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:702"] = tmdb.Media{
		TMDBID:    "702",
		MediaType: "movie",
		Title:     "Concurrent Approval",
		RawJSON:   `{}`,
	}
	req, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "702",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}

	const callers = 12
	var wg sync.WaitGroup
	results := make(chan RequestDTO, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, approveErr := fixture.svc.ApproveRequest(ctx, adminMediaActor(), req.ID, "")
			if approveErr != nil {
				errs <- approveErr
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
	var subscriptionID int64
	seen := 0
	for got := range results {
		seen++
		if got.Status != "approved" {
			t.Fatalf("concurrent approval DTO = %+v, want approved", got)
		}
		if subscriptionID == 0 {
			subscriptionID = got.SubscriptionID
		}
		if got.SubscriptionID == 0 || got.SubscriptionID != subscriptionID {
			t.Fatalf("subscription id = %d, want stable %d", got.SubscriptionID, subscriptionID)
		}
	}
	if seen != callers {
		t.Fatalf("results = %d, want %d", seen, callers)
	}
	if countDiscoverySubscriptions(t, fixture.st) != 1 {
		t.Fatalf("canonical subscriptions = %d, want 1", countDiscoverySubscriptions(t, fixture.st))
	}
	if countUserMediaSubscriptions(t, fixture.st) != 1 {
		t.Fatalf("user subscriptions = %d, want 1", countUserMediaSubscriptions(t, fixture.st))
	}
	if approvedEvents := countRequestEventsByAction(t, fixture.st, req.ID, "approved"); approvedEvents != 1 {
		t.Fatalf("approved events = %d, want 1", approvedEvents)
	}
}

func TestApproveTwoRequestsConcurrentlyReuseOneCanonicalSubscription(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	seedMediaUser(t, fixture.st, "u2")
	policy2 := createMediaPolicy(t, fixture.st, "policy-u2-concurrent", "u2", 1, 100, "approval_required", 1)
	target2 := createMediaTarget(t, fixture.st, fixture.deps, policy2.ID, "Concurrent Target u2", "movie", 1)
	fixture.tmdb.details["movie:703"] = tmdb.Media{
		TMDBID:    "703",
		MediaType: "movie",
		Title:     "Shared Concurrent Movie",
		RawJSON:   `{}`,
	}
	req1, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "703",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u1 returned error: %v", err)
	}
	req2, err := fixture.svc.CreateRequest(ctx, mediaActor("u2"), CreateRequestInput{
		TMDBID:    "703",
		MediaType: "movie",
		TargetID:  target2.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest u2 returned error: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan RequestDTO, 2)
	errs := make(chan error, 2)
	for _, requestID := range []int64{req1.ID, req2.ID} {
		requestID := requestID
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, approveErr := fixture.svc.ApproveRequest(ctx, adminMediaActor(), requestID, "")
			if approveErr != nil {
				errs <- approveErr
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
	var subscriptionID int64
	seen := 0
	for got := range results {
		seen++
		if got.Status != "approved" {
			t.Fatalf("approved DTO = %+v, want approved", got)
		}
		if subscriptionID == 0 {
			subscriptionID = got.SubscriptionID
		}
		if got.SubscriptionID == 0 || got.SubscriptionID != subscriptionID {
			t.Fatalf("subscription id = %d, want reused canonical %d", got.SubscriptionID, subscriptionID)
		}
	}
	if seen != 2 {
		t.Fatalf("results = %d, want 2", seen)
	}
	if countDiscoverySubscriptions(t, fixture.st) != 1 {
		t.Fatalf("canonical subscriptions = %d, want 1", countDiscoverySubscriptions(t, fixture.st))
	}
	if countUserMediaSubscriptions(t, fixture.st) != 2 {
		t.Fatalf("user subscriptions = %d, want 2", countUserMediaSubscriptions(t, fixture.st))
	}
}

func TestApproveRejectRequireAdminScope(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:704"] = tmdb.Media{
		TMDBID:    "704",
		MediaType: "movie",
		Title:     "Admin Scope",
		RawJSON:   `{}`,
	}
	req, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "704",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
		UserNote:  "raw user secret",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}

	if _, err := fixture.svc.ApproveRequest(ctx, mediaActor("u1"), req.ID, "raw admin secret"); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("ApproveRequest non-admin error = %v, want %v", err, ErrPolicyDenied)
	}
	if _, err := fixture.svc.RejectRequest(ctx, mediaActor("u1"), req.ID, "raw admin secret"); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("RejectRequest non-admin error = %v, want %v", err, ErrPolicyDenied)
	}
	if _, err := fixture.svc.ListRequestsForAdmin(ctx, mediaActor("u1"), "", 10, 0); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("ListRequestsForAdmin non-admin error = %v, want %v", err, ErrPolicyDenied)
	}
	if _, err := fixture.svc.GetRequestForAdmin(ctx, mediaActor("u1"), req.ID); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("GetRequestForAdmin non-admin error = %v, want %v", err, ErrPolicyDenied)
	}

	listed, err := fixture.svc.ListRequestsForAdmin(ctx, adminMediaActor(), "pending_review", 10, 0)
	if err != nil {
		t.Fatalf("ListRequestsForAdmin returned error: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != req.ID || listed[0].Status != "pending_review" {
		t.Fatalf("admin list = %+v, want pending request", listed)
	}
	got, err := fixture.svc.GetRequestForAdmin(ctx, adminMediaActor(), req.ID)
	if err != nil {
		t.Fatalf("GetRequestForAdmin returned error: %v", err)
	}
	data, err := json.Marshal([]RequestDTO{got})
	if err != nil {
		t.Fatalf("marshal admin request DTO: %v", err)
	}
	jsonText := string(data)
	for _, forbidden := range []string{"raw user secret", "raw admin secret", "policy_id", "producer_profile_id", "rule_profile_id", "target_library_id"} {
		if strings.Contains(jsonText, forbidden) {
			t.Fatalf("admin request DTO JSON leaked %q: %s", forbidden, jsonText)
		}
	}
	if countDiscoverySubscriptions(t, fixture.st) != 0 || countUserMediaSubscriptions(t, fixture.st) != 0 {
		t.Fatalf("non-admin transitions touched discovery core")
	}
}

func TestRejectRequestLeavesDiscoveryCoreUntouched(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:705"] = tmdb.Media{
		TMDBID:    "705",
		MediaType: "movie",
		Title:     "Rejected Movie",
		RawJSON:   `{}`,
	}
	seedMediaUserSubscription(t, fixture.st, "u1", 0, fixture.deps, "Existing Active", "existing-705", "movie", "active")
	req, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "705",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
		UserNote:  "raw reject user secret",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	beforeCanonical := countDiscoverySubscriptions(t, fixture.st)
	beforeUserSubs := countUserMediaSubscriptions(t, fixture.st)
	beforeJobs := countJobs(t, fixture.st)

	got, err := fixture.svc.RejectRequest(ctx, adminMediaActor(), req.ID, " raw reject admin secret ")
	if err != nil {
		t.Fatalf("RejectRequest returned error: %v", err)
	}
	if got.Status != "rejected" || got.SubscriptionID != 0 || got.ReviewedAt != mediaTestNow {
		t.Fatalf("rejected DTO = %+v, want rejected without subscription", got)
	}
	row := getRequestByID(t, fixture.st, req.ID)
	if row.Status != "rejected" || row.SubscriptionID.Valid {
		t.Fatalf("stored rejected request = %+v, want no subscription link", row)
	}
	if !row.AdminNote.Valid || row.AdminNote.String != "raw reject admin secret" {
		t.Fatalf("admin note = %+v, want trimmed note", row.AdminNote)
	}
	if countDiscoverySubscriptions(t, fixture.st) != beforeCanonical {
		t.Fatalf("canonical subscriptions changed from %d to %d", beforeCanonical, countDiscoverySubscriptions(t, fixture.st))
	}
	if countUserMediaSubscriptions(t, fixture.st) != beforeUserSubs {
		t.Fatalf("user subscriptions changed from %d to %d", beforeUserSubs, countUserMediaSubscriptions(t, fixture.st))
	}
	if countJobs(t, fixture.st) != beforeJobs {
		t.Fatalf("jobs changed from %d to %d", beforeJobs, countJobs(t, fixture.st))
	}
	if actions := requestEventActions(t, fixture.st, req.ID); strings.Join(actions, ",") != "rejected,created" {
		t.Fatalf("event actions = %v, want rejected,created", actions)
	}
	assertRequestEventNotesRedacted(t, fixture.st, req.ID, "raw reject admin secret", "raw reject user secret")
}

func TestApproveRequestGrantsPlaybackWhenTargetAllowsGrant(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:706"] = tmdb.Media{
		TMDBID:    "706",
		MediaType: "movie",
		Title:     "Grant Playback",
		RawJSON:   `{}`,
	}
	req, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "706",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if canPlaybackLibrary(t, fixture.st, "u1", fixture.deps.LibraryID) {
		t.Fatalf("u1 can playback before approval, want missing access")
	}

	if _, err := fixture.svc.ApproveRequest(ctx, adminMediaActor(), req.ID, ""); err != nil {
		t.Fatalf("ApproveRequest returned error: %v", err)
	}
	if !canPlaybackLibrary(t, fixture.st, "u1", fixture.deps.LibraryID) {
		t.Fatalf("u1 can playback after approval = false, want granted access")
	}
	if grants := countLibraryPlaybackGrants(t, fixture.st, "u1", fixture.deps.LibraryID); grants != 1 {
		t.Fatalf("playback grants = %d, want 1", grants)
	}
}

func TestApproveRequestFailsWhenProfileOrRuleDisabled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, st *store.Store, deps mediaTargetDeps, target queries.DiscoveryPolicyTarget)
	}{
		{
			name: "target disabled",
			mutate: func(t *testing.T, st *store.Store, _ mediaTargetDeps, target queries.DiscoveryPolicyTarget) {
				t.Helper()
				if _, err := st.DB.ExecContext(context.Background(), `UPDATE discovery_policy_targets SET enabled = 0 WHERE id = ?`, target.ID); err != nil {
					t.Fatalf("disable target: %v", err)
				}
			},
		},
		{
			name: "producer disabled",
			mutate: func(t *testing.T, st *store.Store, deps mediaTargetDeps, _ queries.DiscoveryPolicyTarget) {
				t.Helper()
				if _, err := st.DB.ExecContext(context.Background(), `UPDATE discovery_producer_profiles SET enabled = 0 WHERE id = ?`, deps.ProducerProfileID); err != nil {
					t.Fatalf("disable producer profile: %v", err)
				}
			},
		},
		{
			name: "rule disabled",
			mutate: func(t *testing.T, st *store.Store, deps mediaTargetDeps, _ queries.DiscoveryPolicyTarget) {
				t.Helper()
				if _, err := st.DB.ExecContext(context.Background(), `UPDATE rule_profiles SET enabled = 0 WHERE id = ?`, deps.RuleProfileID); err != nil {
					t.Fatalf("disable rule profile: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
			fixture.tmdb.details["movie:707"] = tmdb.Media{
				TMDBID:    "707",
				MediaType: "movie",
				Title:     "Disabled Dependency",
				RawJSON:   `{}`,
			}
			req, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
				TMDBID:    "707",
				MediaType: "movie",
				TargetID:  fixture.target.ID,
			})
			if err != nil {
				t.Fatalf("CreateRequest returned error: %v", err)
			}
			tc.mutate(t, fixture.st, fixture.deps, fixture.target)

			_, err = fixture.svc.ApproveRequest(ctx, adminMediaActor(), req.ID, "")
			if !errors.Is(err, ErrPolicyDenied) {
				t.Fatalf("ApproveRequest error = %v, want %v", err, ErrPolicyDenied)
			}
			row := getRequestByID(t, fixture.st, req.ID)
			if row.Status != "pending_review" || row.SubscriptionID.Valid {
				t.Fatalf("request after failed approval = %+v, want pending without subscription", row)
			}
			if countDiscoverySubscriptions(t, fixture.st) != 0 {
				t.Fatalf("canonical subscriptions = %d, want 0", countDiscoverySubscriptions(t, fixture.st))
			}
			if countUserMediaSubscriptions(t, fixture.st) != 0 {
				t.Fatalf("user subscriptions = %d, want 0", countUserMediaSubscriptions(t, fixture.st))
			}
		})
	}
}

func TestApproveRequestExistingCanonicalProfileMismatchReturnsConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newRequestFixture(t, "u1", "approval_required", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	fixture.tmdb.details["movie:708"] = tmdb.Media{
		TMDBID:    "708",
		MediaType: "movie",
		Title:     "Profile Conflict",
		RawJSON:   `{}`,
	}
	req, err := fixture.svc.CreateRequest(ctx, mediaActor("u1"), CreateRequestInput{
		TMDBID:    "708",
		MediaType: "movie",
		TargetID:  fixture.target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	otherRule, err := fixture.st.CreateRuleProfile(ctx, queries.CreateRuleProfileParams{
		Name:      "alternate conflict rules",
		Version:   1,
		RulesJson: `{}`,
		Enabled:   1,
		CreatedAt: mediaTestNow,
		UpdatedAt: mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create alternate rule profile: %v", err)
	}
	_, err = fixture.st.CreateDiscoverySubscription(ctx, queries.CreateDiscoverySubscriptionParams{
		OwnerID:           "admin",
		TmdbID:            "708",
		MediaType:         "movie",
		TmdbLanguage:      "zh-CN",
		TitleSnapshot:     "Profile Conflict",
		LibraryID:         fixture.deps.LibraryID,
		ProducerProfileID: fixture.deps.ProducerProfileID,
		RuleProfileID:     otherRule.ID,
		Status:            "active",
		SeasonFilterJson:  sql.NullString{},
		NextCheckAt:       sql.NullInt64{},
		CreatedAt:         mediaTestNow,
		UpdatedAt:         mediaTestNow,
	})
	if err != nil {
		t.Fatalf("seed conflicting canonical subscription: %v", err)
	}

	_, err = fixture.svc.ApproveRequest(ctx, adminMediaActor(), req.ID, "")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ApproveRequest error = %v, want %v", err, ErrConflict)
	}
	row := getRequestByID(t, fixture.st, req.ID)
	if row.Status != "pending_review" || row.SubscriptionID.Valid {
		t.Fatalf("request after conflict = %+v, want pending without subscription", row)
	}
	if countDiscoverySubscriptions(t, fixture.st) != 1 {
		t.Fatalf("canonical subscriptions = %d, want unchanged 1", countDiscoverySubscriptions(t, fixture.st))
	}
	if countUserMediaSubscriptions(t, fixture.st) != 0 {
		t.Fatalf("user subscriptions = %d, want 0", countUserMediaSubscriptions(t, fixture.st))
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

func seedUnsafeFailedRequest(
	t *testing.T,
	st *store.Store,
	userID string,
	policy queries.DiscoveryAccessPolicy,
	target queries.DiscoveryPolicyTarget,
	deps mediaTargetDeps,
) queries.DiscoverySubscriptionRequest {
	t.Helper()
	request, err := st.CreateDiscoverySubscriptionRequest(context.Background(), queries.CreateDiscoverySubscriptionRequestParams{
		RequesterUserID:             userID,
		Status:                      "failed",
		TmdbID:                      "999",
		MediaType:                   "movie",
		TmdbLanguage:                "zh-CN",
		TitleSnapshot:               "Failed Movie",
		OriginalTitleSnapshot:       sql.NullString{},
		ReleaseYearSnapshot:         sql.NullInt64{},
		PosterPathSnapshot:          sql.NullString{},
		SeasonFilterJson:            sql.NullString{},
		PolicyIDSnapshot:            sql.NullInt64{Int64: policy.ID, Valid: true},
		PolicyTargetIDSnapshot:      sql.NullInt64{Int64: target.ID, Valid: true},
		TargetLabelSnapshot:         target.Label,
		TargetLibraryID:             deps.LibraryID,
		TargetLibraryNameSnapshot:   "Media",
		ProducerProfileIDSnapshot:   deps.ProducerProfileID,
		ProducerProfileNameSnapshot: "115 default",
		RuleProfileIDSnapshot:       deps.RuleProfileID,
		RuleProfileVersionSnapshot:  1,
		UserNote:                    sql.NullString{String: "raw user note secret", Valid: true},
		AdminNote:                   sql.NullString{String: "raw admin note secret", Valid: true},
		ReviewedBy:                  sql.NullString{},
		ReviewedAt:                  sql.NullInt64{},
		SubscriptionID:              sql.NullInt64{},
		IdempotencyKey:              "unsafe-failed-request-" + userID,
		LastErrorKind:               sql.NullString{String: "unsafe_kind raw_text_ref", Valid: true},
		LastErrorMessage:            sql.NullString{String: "last error secret", Valid: true},
		CreatedAt:                   mediaTestNow,
		UpdatedAt:                   mediaTestNow,
	})
	if err != nil {
		t.Fatalf("seed unsafe failed request: %v", err)
	}
	return request
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

func seedMediaDispatchableSubscriptionMatch(t *testing.T, st *store.Store, subscriptionID, ruleProfileID int64) int64 {
	t.Helper()
	ctx := context.Background()
	source, err := st.CreateDiscoverySource(ctx, queries.CreateDiscoverySourceParams{
		Kind:          "manual",
		Name:          "dispatchable source",
		Enabled:       1,
		ConfigJson:    "{}",
		SecretRef:     sql.NullString{},
		RateLimitJson: sql.NullString{},
		NextRunAt:     sql.NullInt64{},
		CreatedAt:     mediaTestNow,
		UpdatedAt:     mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create discovery source: %v", err)
	}
	resource, err := st.UpsertDiscoveredResource(ctx, queries.UpsertDiscoveredResourceParams{
		SourceID:         source.ID,
		Provider:         "115",
		LinkKind:         "115_share",
		ExternalKey:      "dispatchable-resource",
		TmdbID:           sql.NullString{String: "7021", Valid: true},
		MediaType:        sql.NullString{String: "movie", Valid: true},
		Title:            sql.NullString{String: "Mode Merge Movie", Valid: true},
		SeasonNumber:     sql.NullInt64{},
		EpisodeStart:     sql.NullInt64{},
		EpisodeEnd:       sql.NullInt64{},
		ShareCode:        sql.NullString{String: "share", Valid: true},
		ReceiveCode:      sql.NullString{String: "pass", Valid: true},
		ShareUrlRedacted: sql.NullString{String: "https://115.example/s/share?password=[REDACTED]", Valid: true},
		RawTextRedacted:  sql.NullString{String: "Mode Merge Movie", Valid: true},
		RawTextRef:       sql.NullString{},
		ParsedJson:       "{}",
		FeatureJson:      "{}",
		Status:           "candidate",
		FirstSeenAt:      mediaTestNow,
		LastSeenAt:       mediaTestNow,
	})
	if err != nil {
		t.Fatalf("upsert discovered resource: %v", err)
	}
	match, err := st.CreateSubscriptionMatch(ctx, queries.CreateSubscriptionMatchParams{
		SubscriptionID:     subscriptionID,
		ResourceID:         resource.ID,
		RuleProfileID:      ruleProfileID,
		RuleProfileVersion: 1,
		SeasonNumber:       sql.NullInt64{},
		EpisodeStart:       sql.NullInt64{},
		EpisodeEnd:         sql.NullInt64{},
		ScoreJson:          `{"score":1}`,
		PreviousScoreJson:  sql.NullString{},
		Decision:           "accept",
		Reason:             "rule accept",
		DispatchState:      "none",
		IdempotencyKey:     "dispatchable-match",
		CreatedAt:          mediaTestNow,
		UpdatedAt:          mediaTestNow,
		DecidedAt:          sql.NullInt64{Int64: mediaTestNow, Valid: true},
	})
	if err != nil {
		t.Fatalf("create subscription match: %v", err)
	}
	return match.ID
}

func assertMediaDispatchableMatches(t *testing.T, st *store.Store, want ...int64) {
	t.Helper()
	got, err := st.ListDueDiscoveryDispatchMatches(context.Background(), queries.ListDueDiscoveryDispatchMatchesParams{Limit: 10})
	if err != nil {
		t.Fatalf("list due discovery dispatch matches: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("dispatchable matches = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dispatchable matches = %v, want %v", got, want)
		}
	}
}

func tmdbMediaExists(t *testing.T, st *store.Store, tmdbID, mediaType, language string) bool {
	t.Helper()
	_, err := st.GetTMDBMedia(context.Background(), queries.GetTMDBMediaParams{
		TmdbID:    tmdbID,
		MediaType: mediaType,
		Language:  language,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("get tmdb media: %v", err)
	}
	return true
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

func adminMediaActor() Actor {
	actor := mediaActor("admin")
	actor.User.Role = "admin"
	actor.User.Scopes = []string{"admin"}
	return actor
}

func getRequestByID(t *testing.T, st *store.Store, id int64) queries.DiscoverySubscriptionRequest {
	t.Helper()
	row, err := st.GetDiscoverySubscriptionRequest(context.Background(), queries.GetDiscoverySubscriptionRequestParams{ID: id})
	if err != nil {
		t.Fatalf("get request %d: %v", id, err)
	}
	return row
}

func getDiscoverySubscriptionByID(t *testing.T, st *store.Store, id int64) queries.DiscoverySubscription {
	t.Helper()
	row, err := st.GetDiscoverySubscription(context.Background(), queries.GetDiscoverySubscriptionParams{ID: id})
	if err != nil {
		t.Fatalf("get discovery subscription %d: %v", id, err)
	}
	return row
}

func getOnlyUserMediaSubscription(t *testing.T, st *store.Store, userID string) queries.UserMediaSubscription {
	t.Helper()
	rows, err := st.ListUserMediaSubscriptionsForUser(context.Background(), queries.ListUserMediaSubscriptionsForUserParams{
		EchoUserID: userID,
		Limit:      10,
		Offset:     0,
	})
	if err != nil {
		t.Fatalf("list user media subscriptions for %s: %v", userID, err)
	}
	if len(rows) != 1 {
		t.Fatalf("user media subscriptions for %s = %+v, want one row", userID, rows)
	}
	return rows[0]
}

func requestEventActions(t *testing.T, st *store.Store, requestID int64) []string {
	t.Helper()
	events, err := st.ListDiscoverySubscriptionRequestEvents(context.Background(), queries.ListDiscoverySubscriptionRequestEventsParams{
		RequestID: requestID,
		Limit:     10,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("list request events: %v", err)
	}
	actions := make([]string, 0, len(events))
	for _, event := range events {
		actions = append(actions, event.Action)
	}
	return actions
}

func countRequestEventsByAction(t *testing.T, st *store.Store, requestID int64, action string) int64 {
	t.Helper()
	var count int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM discovery_subscription_request_events
WHERE request_id = ? AND action = ?`, requestID, action).Scan(&count); err != nil {
		t.Fatalf("count request events by action: %v", err)
	}
	return count
}

func assertRequestEventNotesRedacted(t *testing.T, st *store.Store, requestID int64, forbidden ...string) {
	t.Helper()
	events, err := st.ListDiscoverySubscriptionRequestEvents(context.Background(), queries.ListDiscoverySubscriptionRequestEventsParams{
		RequestID: requestID,
		Limit:     10,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("list request events: %v", err)
	}
	for _, event := range events {
		if !event.Note.Valid {
			continue
		}
		for _, value := range forbidden {
			if strings.Contains(event.Note.String, value) {
				t.Fatalf("event %q note leaked %q: %+v", event.Action, value, event.Note)
			}
		}
	}
}

func canPlaybackLibrary(t *testing.T, st *store.Store, userID string, libraryID int64) bool {
	t.Helper()
	allowed, err := st.UserCanPlaybackLibrary(context.Background(), queries.UserCanPlaybackLibraryParams{
		LibraryID:  libraryID,
		EchoUserID: userID,
	})
	if err != nil {
		t.Fatalf("check library playback: %v", err)
	}
	return allowed == 1
}

func countLibraryPlaybackGrants(t *testing.T, st *store.Store, userID string, libraryID int64) int64 {
	t.Helper()
	var count int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM library_grants
WHERE library_id = ? AND echo_user_id = ? AND permission = 'playback' AND enabled = 1`, libraryID, userID).Scan(&count); err != nil {
		t.Fatalf("count library playback grants: %v", err)
	}
	return count
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
