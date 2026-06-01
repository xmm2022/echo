package playback

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestQuotaCountsActualBytesAndActiveStreams(t *testing.T) {
	st := newPlaybackTestStore(t)
	ctx := context.Background()
	user := createPlaybackUserWithPolicy(t, ctx, st, "u1", "alice", "day", ptrInt64(100), ptrInt64(1), nil)
	now := time.Unix(1000, 0)
	q := NewQuota(st.Queries, nowFunc(now), time.Hour)

	if err := q.CheckStreamAllowed(ctx, user.ID); err != nil {
		t.Fatalf("initial quota denied: %v", err)
	}
	eventID, err := q.StartStream(ctx, StartStreamInput{
		RequestID: "req1", EchoUserID: user.ID, Operation: "stream", StartedAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.CheckStreamAllowed(ctx, user.ID); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("active stream check = %v, want ErrQuotaExceeded", err)
	}
	if err := q.FinishStream(ctx, eventID, FinishStreamInput{Status: "ok", BytesSent: 60, HTTPStatus: 206, FinishedAt: now.Add(time.Second).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := q.CheckStreamAllowed(ctx, user.ID); err != nil {
		t.Fatalf("after finish denied: %v", err)
	}
	eventID, err = q.StartStream(ctx, StartStreamInput{RequestID: "req2", EchoUserID: user.ID, Operation: "stream", StartedAt: now.Add(2 * time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.FinishStream(ctx, eventID, FinishStreamInput{Status: "ok", BytesSent: 50, HTTPStatus: 206, FinishedAt: now.Add(3 * time.Second).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := q.CheckStreamAllowed(ctx, user.ID); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("bytes quota check = %v, want ErrQuotaExceeded", err)
	}
}

// TestQuotaActiveStreamUnionDoesNotDoubleCount guards the spec requirement that
// CheckStreamAllowed combines in-memory leases with unfinished DB events by SET UNION
// (dedupe by event id), not by adding two counts. StartStream both inserts an
// unfinished DB row AND registers a lease for the same event id; with MaxStreams=1 the
// union must see a count of exactly 1 (-> exceeded). A separate purely in-memory lease
// for a different id is still counted distinctly.
func TestQuotaReconcileUsesDefaultClockWhenNil(t *testing.T) {
	st := newPlaybackTestStore(t)
	q := NewQuota(st.Queries, nil, time.Hour)
	if err := q.ReconcileInterruptedStreams(context.Background()); err != nil {
		t.Fatalf("reconcile interrupted streams with nil clock: %v", err)
	}
}

func TestQuotaActiveStreamUnionDoesNotDoubleCount(t *testing.T) {
	st := newPlaybackTestStore(t)
	ctx := context.Background()
	user := createPlaybackUserWithPolicy(t, ctx, st, "u1", "alice", "day", nil, ptrInt64(1), nil)
	now := time.Unix(1000, 0)
	q := NewQuota(st.Queries, nowFunc(now), time.Hour)

	// One StartStream => one DB event id + one lease for that same id. If the union
	// double-counted (lease count + DB count = 2), this would still be "exceeded", so
	// the assertion below alone is necessary but not sufficient. The real guard is the
	// finish-then-allow step: after FinishStream the DB row is finished and the lease is
	// released, so the union must drop to 0.
	eventID, err := q.StartStream(ctx, StartStreamInput{
		RequestID: "req1", EchoUserID: user.ID, Operation: "stream", StartedAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.CheckStreamAllowed(ctx, user.ID); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("single active stream check = %v, want ErrQuotaExceeded", err)
	}

	// Finish releases the lease AND finishes the DB row. If StartStream had only
	// registered a lease without dedupe against the DB row (or vice versa), the
	// remaining side would keep the count at 1 and this check would wrongly deny.
	if err := q.FinishStream(ctx, eventID, FinishStreamInput{Status: "ok", BytesSent: 0, HTTPStatus: 200, FinishedAt: now.Add(time.Second).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := q.CheckStreamAllowed(ctx, user.ID); err != nil {
		t.Fatalf("after finish union should be empty, denied: %v", err)
	}

	// A purely in-memory lease (no DB event) must still be counted by the union. Use a
	// synthetic id that cannot collide with any DB-assigned event id.
	const memOnlyEventID int64 = 1 << 40
	q.leases.Acquire(memOnlyEventID, user.ID)
	if err := q.CheckStreamAllowed(ctx, user.ID); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("in-memory-only lease check = %v, want ErrQuotaExceeded", err)
	}
	q.leases.Release(memOnlyEventID)
	if err := q.CheckStreamAllowed(ctx, user.ID); err != nil {
		t.Fatalf("after releasing in-memory lease, denied: %v", err)
	}
}

// --- helpers ---

func ptrInt64(v int64) *int64 { return &v }

// createPlaybackUserWithPolicy creates a dedicated quota policy with the given period
// and limits, then a user bound to that policy, returning the persisted user. Limits
// passed as nil become NULL (unlimited for that dimension).
func createPlaybackUserWithPolicy(t *testing.T, ctx context.Context, st *store.Store, id, username, period string, maxBytes, maxStreams, maxSessions *int64) queries.User {
	t.Helper()
	policy, err := st.CreateQuotaPolicy(ctx, queries.CreateQuotaPolicyParams{
		Name:                username + "-policy",
		Period:              period,
		MaxBytes:            nullInt64FromPtr(maxBytes),
		MaxStreams:          nullInt64FromPtr(maxStreams),
		MaxPlaybackSessions: nullInt64FromPtr(maxSessions),
		CreatedAt:           1,
		UpdatedAt:           1,
	})
	if err != nil {
		t.Fatalf("create quota policy: %v", err)
	}
	if err := st.CreateUser(ctx, queries.CreateUserParams{
		ID:            id,
		Username:      username,
		Role:          "user",
		Status:        "active",
		QuotaPolicyID: policy.ID,
		CreatedAt:     1,
		UpdatedAt:     1,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, err := st.GetUser(ctx, queries.GetUserParams{ID: id})
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	return user
}

func nullInt64FromPtr(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}
