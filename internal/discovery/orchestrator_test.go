package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tmdbpkg "github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/ingest"
	jobpkg "github.com/xmm2022/echo/internal/job"
	storeq "github.com/xmm2022/echo/internal/store/queries"
)

type fakeSourceAdapter struct {
	result SourceCrawlResult
	err    error
}

func (f fakeSourceAdapter) Crawl(ctx context.Context, source Source) (SourceCrawlResult, error) {
	return f.result, f.err
}

type captureTelegramCursorAdapter struct {
	seenLastMessageIDs []int64
}

func (f *captureTelegramCursorAdapter) Crawl(ctx context.Context, source Source) (SourceCrawlResult, error) {
	var cfg struct {
		Channels []struct {
			Ref           string `json:"ref"`
			LastMessageID int64  `json:"last_message_id"`
		} `json:"channels"`
	}
	if err := json.Unmarshal([]byte(source.ConfigText()), &cfg); err != nil {
		return SourceCrawlResult{}, err
	}
	if len(cfg.Channels) != 1 {
		return SourceCrawlResult{}, errors.New("expected one telegram channel")
	}
	f.seenLastMessageIDs = append(f.seenLastMessageIDs, cfg.Channels[0].LastMessageID)
	return SourceCrawlResult{TelegramCursors: []TelegramCursorUpdate{{
		ChannelRef:      cfg.Channels[0].Ref,
		LastMessageID:   10,
		LastMessageDate: 100,
	}}}, nil
}

type retryAfterErr struct {
	d time.Duration
}

func (e retryAfterErr) Error() string {
	return "retry later"
}

func (e retryAfterErr) RetryAfter() time.Duration {
	return e.d
}

func TestRunSourceCrawlWritesCandidatesAndAdvancesCursor(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	sourceID := seedDiscoverySource(t, st, string(SourceTelegramMTProto))
	seedTelegramChannel(t, st, sourceID, "channel")
	orch := NewOrchestrator(Deps{
		Store: ds,
		SourceAdapters: map[SourceKind]SourceAdapter{
			SourceTelegramMTProto: fakeSourceAdapter{result: SourceCrawlResult{
				Items: []ParsedResource{{
					Provider:    Provider115,
					LinkKind:    Link115Share,
					ExternalKey: "tg:10",
					Title:       "Known Movie",
					ShareCode:   "abc",
					ReceiveCode: "pass",
					ParsedJSON:  "{}",
					FeatureJSON: "{}",
					ObservedAt:  time.Unix(100, 0),
				}},
				TelegramCursors: []TelegramCursorUpdate{{
					ChannelRef:      "channel",
					LastMessageID:   10,
					LastMessageDate: 100,
				}},
			}},
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunSourceCrawl(context.Background(), SourceCrawlPayload{SourceID: sourceID}); err != nil {
		t.Fatal(err)
	}
	if got := countDiscoveredResources(t, st); got != 1 {
		t.Fatalf("resources = %d, want 1", got)
	}
	if got := lastTelegramMessageID(t, st, sourceID, "channel"); got != 10 {
		t.Fatalf("last telegram message id = %d, want 10", got)
	}
}

func TestRunSourceCrawlUsesPersistedTelegramCursorOnNextRun(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	sourceID := seedDiscoverySource(t, st, string(SourceTelegramMTProto))
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE discovery_sources
SET config_json = '{"channels":[{"ref":"channel","session_ref":"ref:session.json","last_message_id":1,"last_message_date":10}]}'
WHERE id = ?`, sourceID); err != nil {
		t.Fatal(err)
	}
	seedTelegramChannel(t, st, sourceID, "channel")
	adapter := &captureTelegramCursorAdapter{}
	orch := NewOrchestrator(Deps{
		Store: ds,
		SourceAdapters: map[SourceKind]SourceAdapter{
			SourceTelegramMTProto: adapter,
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	for i := 0; i < 2; i++ {
		if err := orch.RunSourceCrawl(context.Background(), SourceCrawlPayload{SourceID: sourceID}); err != nil {
			t.Fatal(err)
		}
	}
	if len(adapter.seenLastMessageIDs) != 2 {
		t.Fatalf("adapter calls = %#v, want two", adapter.seenLastMessageIDs)
	}
	if adapter.seenLastMessageIDs[0] != 1 || adapter.seenLastMessageIDs[1] != 10 {
		t.Fatalf("adapter cursors = %#v, want [1 10]", adapter.seenLastMessageIDs)
	}
}

func TestRunSourceCrawlBacksOffMalformedTelegramConfig(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	sourceID := seedDiscoverySource(t, st, string(SourceTelegramMTProto))
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE discovery_sources
SET config_json = '{bad'
WHERE id = ?`, sourceID); err != nil {
		t.Fatal(err)
	}
	seedTelegramChannel(t, st, sourceID, "channel")
	orch := NewOrchestrator(Deps{
		Store: ds,
		SourceAdapters: map[SourceKind]SourceAdapter{
			SourceTelegramMTProto: fakeSourceAdapter{},
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunSourceCrawl(context.Background(), SourceCrawlPayload{SourceID: sourceID}); err == nil {
		t.Fatal("expected malformed config error")
	}
	var schedulerState, sourceKind string
	var sourceBackoff sql.NullInt64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT scheduler_state, backoff_until, COALESCE(last_error_kind, '')
FROM discovery_sources
WHERE id = ?`, sourceID).Scan(&schedulerState, &sourceBackoff, &sourceKind); err != nil {
		t.Fatal(err)
	}
	if schedulerState != "backoff" || !sourceBackoff.Valid || sourceBackoff.Int64 != 400 || sourceKind != "invalid_config" {
		t.Fatalf("source state=%s backoff=%v kind=%q, want backoff/400/invalid_config", schedulerState, sourceBackoff, sourceKind)
	}
	var channelBackoff sql.NullInt64
	var channelKind string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT backoff_until, COALESCE(last_error_kind, '')
FROM telegram_channels
WHERE source_id = ? AND channel_ref = 'channel'`, sourceID).Scan(&channelBackoff, &channelKind); err != nil {
		t.Fatal(err)
	}
	if !channelBackoff.Valid || channelBackoff.Int64 != 400 || channelKind != "invalid_config" {
		t.Fatalf("channel backoff=%v kind=%q, want 400/invalid_config", channelBackoff, channelKind)
	}
}

func TestRunSourceCrawlBacksOffTelegramChannelsOnRateLimit(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	sourceID := seedDiscoverySource(t, st, string(SourceTelegramMTProto))
	seedTelegramChannel(t, st, sourceID, "channel")
	orch := NewOrchestrator(Deps{
		Store: ds,
		SourceAdapters: map[SourceKind]SourceAdapter{
			SourceTelegramMTProto: fakeSourceAdapter{err: errors.New("telegram flood wait 60s")},
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunSourceCrawl(context.Background(), SourceCrawlPayload{SourceID: sourceID}); err == nil {
		t.Fatal("expected crawl error")
	}
	var sourceKind string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COALESCE(last_error_kind, '')
FROM discovery_sources
WHERE id = ?`, sourceID).Scan(&sourceKind); err != nil {
		t.Fatal(err)
	}
	if sourceKind != "rate_limited" {
		t.Fatalf("source error kind = %q, want rate_limited", sourceKind)
	}
	var backoffUntil sql.NullInt64
	var channelKind string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT backoff_until, COALESCE(last_error_kind, '')
FROM telegram_channels
WHERE source_id = ? AND channel_ref = 'channel'`, sourceID).Scan(&backoffUntil, &channelKind); err != nil {
		t.Fatal(err)
	}
	if !backoffUntil.Valid || backoffUntil.Int64 != 400 || channelKind != "rate_limited" {
		t.Fatalf("channel backoff=%v kind=%q, want backoff=400 kind=rate_limited", backoffUntil, channelKind)
	}
}

func TestRunSourceCrawlClassifiesAuthFailedError(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	sourceID := seedDiscoverySource(t, st, string(SourceTelegramMTProto))
	seedTelegramChannel(t, st, sourceID, "channel")
	orch := NewOrchestrator(Deps{
		Store: ds,
		SourceAdapters: map[SourceKind]SourceAdapter{
			SourceTelegramMTProto: fakeSourceAdapter{err: AuthFailedError{Message: "telegram config: resolve api hash failed"}},
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunSourceCrawl(context.Background(), SourceCrawlPayload{SourceID: sourceID}); err == nil {
		t.Fatal("expected telegram auth setup error")
	}

	var sourceKind string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COALESCE(last_error_kind, '')
FROM discovery_sources
WHERE id = ?`, sourceID).Scan(&sourceKind); err != nil {
		t.Fatal(err)
	}
	if sourceKind != "auth_failed" {
		t.Fatalf("source error kind = %q, want auth_failed", sourceKind)
	}
	var channelKind string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COALESCE(last_error_kind, '')
FROM telegram_channels
WHERE source_id = ? AND channel_ref = 'channel'`, sourceID).Scan(&channelKind); err != nil {
		t.Fatal(err)
	}
	if channelKind != "auth_failed" {
		t.Fatalf("channel error kind = %q, want auth_failed", channelKind)
	}
}

func TestRunSourceCrawlUsesStructuredRetryAfterBackoff(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	sourceID := seedDiscoverySource(t, st, string(SourceTelegramMTProto))
	seedTelegramChannel(t, st, sourceID, "channel")
	orch := NewOrchestrator(Deps{
		Store: ds,
		SourceAdapters: map[SourceKind]SourceAdapter{
			SourceTelegramMTProto: fakeSourceAdapter{err: retryAfterErr{d: 20 * time.Minute}},
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunSourceCrawl(context.Background(), SourceCrawlPayload{SourceID: sourceID}); err == nil {
		t.Fatal("expected crawl error")
	}
	var sourceBackoff int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COALESCE(backoff_until, 0)
FROM discovery_sources
WHERE id = ?`, sourceID).Scan(&sourceBackoff); err != nil {
		t.Fatal(err)
	}
	if sourceBackoff != 1300 {
		t.Fatalf("source backoff = %d, want 1300", sourceBackoff)
	}
	var channelBackoff int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COALESCE(backoff_until, 0)
FROM telegram_channels
WHERE source_id = ? AND channel_ref = 'channel'`, sourceID).Scan(&channelBackoff); err != nil {
		t.Fatal(err)
	}
	if channelBackoff != 1300 {
		t.Fatalf("channel backoff = %d, want 1300", channelBackoff)
	}
}

func TestRunSourceCrawlReturnsRawStorePutErrorWhenConfigured(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	sourceID := seedDiscoverySource(t, st, string(SourcePosterHTTP))
	orch := NewOrchestrator(Deps{
		Store:    ds,
		RawStore: NewRawStore(RawStoreConfig{Root: "", MaxBytes: 128}),
		SourceAdapters: map[SourceKind]SourceAdapter{
			SourcePosterHTTP: fakeSourceAdapter{result: SourceCrawlResult{Items: []ParsedResource{{
				Provider:    Provider115,
				LinkKind:    Link115Share,
				ExternalKey: "poster:1",
				Title:       "Known Movie",
				ShareCode:   "abc",
				ReceiveCode: "pass",
				RawText:     []byte("Movie https://115.com/s/abc?password=pass"),
				ParsedJSON:  "{}",
				FeatureJSON: "{}",
				ObservedAt:  time.Unix(100, 0),
			}}}},
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunSourceCrawl(context.Background(), SourceCrawlPayload{SourceID: sourceID}); err == nil {
		t.Fatal("expected raw store error")
	}
	if got := countDiscoveredResources(t, st); got != 0 {
		t.Fatalf("resources = %d, want 0", got)
	}
}

func TestRunDispatchClaimsMatchOnce(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	fixture := seedDiscoveryFixture(t, st)
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE discovery_producer_profiles
SET default_args_json = '{"mode":"direct"}'
WHERE id = ?`, fixture.ProducerProfileID); err != nil {
		t.Fatal(err)
	}
	match, err := ds.CreateOrGetMatch(context.Background(), AcceptMatchParams{
		SubscriptionID:     fixture.SubscriptionID,
		ResourceID:         fixture.ResourceID,
		RuleProfileID:      fixture.RuleProfileID,
		RuleProfileVersion: 1,
		ScoreJSON:          `{"tuple":[1]}`,
		Decision:           "accept",
		Reason:             "admin accept",
		DispatchState:      "none",
		IdempotencyKey:     "dispatch-once",
		Now:                time.Unix(100, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	notified := 0
	orch := NewOrchestrator(Deps{
		Store: ds,
		ProducerConfig: ingest.ProducerConfig{
			Tools: map[string]ingest.ProducerToolConfig{
				"115share2cas": {
					Binary:           "/bin/true",
					APIArgsAllowlist: []string{"share_code", "receive_code", "cookie_file", "recycle_password_file", "mode"},
				},
			},
		},
		NotifyJob: func(jobID int64) {
			notified++
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunDispatch(context.Background(), DispatchPayload{MatchID: match.ID}); err != nil {
		t.Fatal(err)
	}
	if err := orch.RunDispatch(context.Background(), DispatchPayload{MatchID: match.ID}); err != nil {
		t.Fatal(err)
	}
	if notified != 1 {
		t.Fatalf("producer notifications = %d, want 1", notified)
	}
}

func TestRunSubscriptionCheckCreatesMatchAndClearsLease(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	fixture := seedDiscoveryFixture(t, st)
	seedDueSubscription(t, st, fixture.SubscriptionID, 100)
	orch := NewOrchestrator(Deps{
		Store: ds,
		Now:   func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunSubscriptionCheck(context.Background(), SubscriptionCheckPayload{SubscriptionID: fixture.SubscriptionID}); err != nil {
		t.Fatal(err)
	}
	var matches int
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM subscription_matches
WHERE subscription_id = ? AND resource_id = ? AND decision = 'review'`,
		fixture.SubscriptionID, fixture.ResourceID).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("matches = %d, want 1", matches)
	}
	var lockedUntil sql.NullInt64
	var nextCheckAt int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT locked_until, next_check_at
FROM discovery_subscriptions
WHERE id = ?`, fixture.SubscriptionID).Scan(&lockedUntil, &nextCheckAt); err != nil {
		t.Fatal(err)
	}
	if lockedUntil.Valid || nextCheckAt != 3700 {
		t.Fatalf("locked=%v next=%d, want unlocked next=3700", lockedUntil, nextCheckAt)
	}
}

func TestRunSubscriptionCheckMarksUnsupportedCandidateStatus(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	fixture := seedDiscoveryFixture(t, st)
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE discovered_resources
SET provider = '139', link_kind = 'unknown'
WHERE id = ?`, fixture.ResourceID); err != nil {
		t.Fatal(err)
	}
	orch := NewOrchestrator(Deps{
		Store: ds,
		Now:   func() time.Time { return time.Unix(100, 0) },
	})
	if err := orch.RunSubscriptionCheck(context.Background(), SubscriptionCheckPayload{SubscriptionID: fixture.SubscriptionID}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT status
FROM discovered_resources
WHERE id = ?`, fixture.ResourceID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "unsupported_provider" {
		t.Fatalf("resource status = %q, want unsupported_provider", status)
	}
}

func TestRunReconcileDoneWithFailedItemsMarksMatchFailed(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	fixture := seedDiscoveryFixture(t, st)
	match, err := ds.CreateOrGetMatch(context.Background(), AcceptMatchParams{
		SubscriptionID:     fixture.SubscriptionID,
		ResourceID:         fixture.ResourceID,
		RuleProfileID:      fixture.RuleProfileID,
		RuleProfileVersion: 1,
		ScoreJSON:          `{"tuple":[1]}`,
		Decision:           "accept",
		Reason:             "admin accept",
		DispatchState:      "none",
		IdempotencyKey:     "reconcile-failed",
		Now:                time.Unix(100, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID, claimed, err := ds.ClaimAndCreateMatchDispatchJob(context.Background(), match.ID, jobpkg.IngestPayload{
		LibraryID:     fixture.LibraryID,
		TargetAccount: "acc-115",
		TargetSubdir:  "Known Movie",
		Tool:          "115share2cas",
		Args:          map[string]any{"share_code": "abc", "receive_code": "pass", "mode": "direct"},
	}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("dispatch job was not claimed")
	}
	if err := st.UpdateJobProgress(context.Background(), storeq.UpdateJobProgressParams{
		ID:       jobID,
		Progress: sql.NullString{String: `{"current":1,"total":1,"warnings":1,"failed_items":[{"rel_path":"a.mkv","reason":"copy failed"}]}`, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishJob(context.Background(), storeq.FinishJobParams{
		ID:         jobID,
		Status:     "done",
		FinishedAt: sql.NullInt64{Int64: 110, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	orch := NewOrchestrator(Deps{
		Store: ds,
		Now:   func() time.Time { return time.Unix(120, 0) },
	})
	if err := orch.RunReconcile(context.Background(), ReconcilePayload{MatchID: match.ID, JobID: jobID}); err != nil {
		t.Fatal(err)
	}
	var decision, dispatchState, failureKind string
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT decision, dispatch_state, COALESCE(failure_kind, '')
FROM subscription_matches
WHERE id = ?`, match.ID).Scan(&decision, &dispatchState, &failureKind); err != nil {
		t.Fatal(err)
	}
	if decision != "failed" || dispatchState != "failed" || failureKind != "ingest_failed_items" {
		t.Fatalf("match result = %s/%s/%s, want failed/failed/ingest_failed_items", decision, dispatchState, failureKind)
	}
}

func TestRunTMDBRefreshTreatsStaleFallback429AsBackoff(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	ctx := context.Background()
	now := time.Unix(time.Now().Unix(), 0)
	if _, err := st.UpsertTMDBMedia(ctx, storeq.UpsertTMDBMediaParams{
		TmdbID:        "123",
		MediaType:     "movie",
		Language:      "zh-CN",
		Title:         "Stale Movie",
		RawJson:       `{"id":123,"title":"Stale Movie"}`,
		FetchedAt:     now.Add(-time.Hour).Unix(),
		NextRefreshAt: now.Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	client := tmdbpkg.NewClient(tmdbpkg.Config{
		BaseURL:  srv.URL,
		APIKey:   "key",
		Language: "zh-CN",
		CacheTTL: time.Hour,
	}, tmdbpkg.NewSQLiteCache(st.Queries, "zh-CN"))
	orch := NewOrchestrator(Deps{
		Store: ds,
		TMDB:  client,
		Now:   func() time.Time { return now },
	})
	if err := orch.RunTMDBRefresh(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetTMDBMedia(ctx, storeq.GetTMDBMediaParams{TmdbID: "123", MediaType: "movie", Language: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Title != "Stale Movie" || row.FetchedAt != now.Add(-time.Hour).Unix() {
		t.Fatalf("stale row changed title=%q fetched_at=%d", row.Title, row.FetchedAt)
	}
	if row.NextRefreshAt != now.Add(defaultBackoffDelay).Unix() {
		t.Fatalf("next_refresh_at = %d, want %d", row.NextRefreshAt, now.Add(defaultBackoffDelay).Unix())
	}
	if !row.LastErrorKind.Valid || row.LastErrorKind.String != "rate_limited" {
		t.Fatalf("last_error_kind = %#v, want rate_limited", row.LastErrorKind)
	}
}
