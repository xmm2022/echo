package discovery

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestLeaseDueSourcesDoesNotDoubleLease(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	ctx := context.Background()
	seedDueSource(t, st, 10)
	first, err := ds.LeaseDueSources(ctx, time.Unix(10, 0), time.Unix(70, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ds.LeaseDueSources(ctx, time.Unix(10, 0), time.Unix(70, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("lease counts = %d then %d, want 1 then 0", len(first), len(second))
	}
}

func TestClearSourceLeaseAdvancesNextRun(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	ctx := context.Background()
	sourceID := seedDueSource(t, st, 10)
	if _, err := ds.LeaseDueSources(ctx, time.Unix(10, 0), time.Unix(70, 0), 1); err != nil {
		t.Fatal(err)
	}
	if err := ds.ClearSourceLease(ctx, sourceID, time.Unix(130, 0)); err != nil {
		t.Fatal(err)
	}
	var lockedUntil sql.NullInt64
	var nextRunAt int64
	if err := st.DB.QueryRowContext(ctx, `SELECT locked_until, next_run_at FROM discovery_sources WHERE id = ?`, sourceID).Scan(&lockedUntil, &nextRunAt); err != nil {
		t.Fatal(err)
	}
	if lockedUntil.Valid || nextRunAt != 130 {
		t.Fatalf("locked=%v next=%d, want unlocked next=130", lockedUntil, nextRunAt)
	}
}

func TestClearSourceLeaseAfterBackoffMarksHealthy(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	ctx := context.Background()
	sourceID := seedDueSource(t, st, 10)
	if _, err := ds.LeaseDueSources(ctx, time.Unix(10, 0), time.Unix(70, 0), 1); err != nil {
		t.Fatal(err)
	}
	if err := ds.BackoffSource(ctx, sourceID, time.Unix(90, 0), "rate_limit", "retry later"); err != nil {
		t.Fatal(err)
	}
	if err := ds.ClearSourceLease(ctx, sourceID, time.Unix(130, 0)); err != nil {
		t.Fatal(err)
	}
	var schedulerState string
	var lockedUntil sql.NullInt64
	var nextRunAt int64
	if err := st.DB.QueryRowContext(ctx, `
SELECT scheduler_state, locked_until, next_run_at
FROM discovery_sources
WHERE id = ?`, sourceID).Scan(&schedulerState, &lockedUntil, &nextRunAt); err != nil {
		t.Fatal(err)
	}
	if schedulerState != "healthy" || lockedUntil.Valid || nextRunAt != 130 {
		t.Fatalf("state=%s locked=%v next=%d, want healthy unlocked next=130", schedulerState, lockedUntil, nextRunAt)
	}
}

func TestClearSubscriptionLeaseAdvancesNextCheck(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	ctx := context.Background()
	fixture := seedDiscoveryFixture(t, st)
	seedDueSubscription(t, st, fixture.SubscriptionID, 10)
	if _, err := ds.LeaseDueSubscriptions(ctx, time.Unix(10, 0), time.Unix(70, 0), 1); err != nil {
		t.Fatal(err)
	}
	if err := ds.ClearSubscriptionLease(ctx, fixture.SubscriptionID, time.Unix(130, 0)); err != nil {
		t.Fatal(err)
	}
	var lockedUntil sql.NullInt64
	var nextCheckAt int64
	if err := st.DB.QueryRowContext(ctx, `SELECT locked_until, next_check_at FROM discovery_subscriptions WHERE id = ?`, fixture.SubscriptionID).Scan(&lockedUntil, &nextCheckAt); err != nil {
		t.Fatal(err)
	}
	if lockedUntil.Valid || nextCheckAt != 130 {
		t.Fatalf("locked=%v next=%d, want unlocked next=130", lockedUntil, nextCheckAt)
	}
}

func TestLeaseWrappersRejectNonPositiveLimits(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	ctx := context.Background()

	for _, limit := range []int64{0, -1} {
		if _, err := ds.LeaseDueSources(ctx, time.Unix(10, 0), time.Unix(70, 0), limit); err == nil {
			t.Fatalf("LeaseDueSources limit %d: expected error", limit)
		}
		if _, err := ds.LeaseDueSubscriptions(ctx, time.Unix(10, 0), time.Unix(70, 0), limit); err == nil {
			t.Fatalf("LeaseDueSubscriptions limit %d: expected error", limit)
		}
	}
}

func TestSchedulerTickEnqueuesDueWork(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	fixture := seedDiscoveryFixture(t, st)
	seedDueSource(t, st, 10)
	seedDueSubscription(t, st, fixture.SubscriptionID, 10)
	seedDispatchableMatch(t, st, fixture.SubscriptionID, fixture.ResourceID, fixture.RuleProfileID)
	seedQueuedMatchForReconcile(t, st, fixture.SubscriptionID, fixture.ResourceID, fixture.RuleProfileID)
	seedDueTMDBMedia(t, st, "123", "movie", 10)
	enqueued := map[string]int{}
	s := NewScheduler(SchedulerConfig{
		Store:      ds,
		BatchLimit: 10,
		Now:        func() time.Time { return time.Unix(10, 0) },
		Enqueue: func(ctx context.Context, kind string, payload any) (int64, error) {
			enqueued[kind]++
			return int64(len(enqueued)), nil
		},
	})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{
		KindSourceCrawl,
		KindSubscriptionCheck,
		KindDispatch,
		KindReconcile,
		KindTMDBRefresh,
	} {
		if enqueued[kind] == 0 {
			t.Fatalf("missing enqueue for %s: %#v", kind, enqueued)
		}
	}
}
