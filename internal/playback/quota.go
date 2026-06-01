package playback

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

// ErrQuotaExceeded is returned by CheckStreamAllowed when starting a new stream would
// violate the user's quota policy (active stream count or accumulated bytes at/over the
// configured limit). It is a sentinel for callers to map to a 429/403 response.
var ErrQuotaExceeded = errors.New("quota exceeded")

// QuotaQuerier is the subset of *queries.Queries the quota enforcer needs. *queries.Queries
// satisfies it directly, so callers pass store.Queries.
type QuotaQuerier interface {
	GetUser(ctx context.Context, arg queries.GetUserParams) (queries.User, error)
	GetQuotaPolicy(ctx context.Context, arg queries.GetQuotaPolicyParams) (queries.QuotaPolicy, error)
	SumPlaybackBytesInWindow(ctx context.Context, arg queries.SumPlaybackBytesInWindowParams) (int64, error)
	ListActiveStreamEventIDs(ctx context.Context, arg queries.ListActiveStreamEventIDsParams) ([]int64, error)
	InsertPlaybackEvent(ctx context.Context, arg queries.InsertPlaybackEventParams) (int64, error)
	FinishPlaybackEvent(ctx context.Context, arg queries.FinishPlaybackEventParams) error
	MarkStalePlaybackEventsInterrupted(ctx context.Context, arg queries.MarkStalePlaybackEventsInterruptedParams) error
}

// Quota enforces per-user playback quota policies against the authoritative
// playback_events table, combined with in-process stream leases. Quota decisions follow
// actual bytes written to the client and the count of in-flight streams; the future
// quota_usage cache table is intentionally not consulted in this phase.
type Quota struct {
	q             QuotaQuerier
	now           func() time.Time
	streamTimeout time.Duration
	leases        *LeaseRegistry
}

// NewQuota builds a Quota with its own in-memory LeaseRegistry. streamTimeout bounds how
// far back an unfinished DB stream event is still treated as active (older ones are
// assumed orphaned and are reclaimed by ReconcileInterruptedStreams).
func NewQuota(q QuotaQuerier, now func() time.Time, streamTimeout time.Duration) *Quota {
	return &Quota{
		q:             q,
		now:           now,
		streamTimeout: streamTimeout,
		leases:        NewLeaseRegistry(),
	}
}

func (q *Quota) clock() time.Time {
	if q.now != nil {
		return q.now()
	}
	return time.Now()
}

// StartStreamInput describes a stream to record. Phase 2 only sets RequestID,
// EchoUserID, Operation and StartedAt; the remaining fields are wired through to the
// playback_events row but left zero/nil (NULL) by current callers.
type StartStreamInput struct {
	RequestID      string
	EchoUserID     string
	Operation      string
	StartedAt      int64
	SessionID      *string
	ErrorTokenID   *string
	LibraryEntryID *int64
	BlobID         *int64
	CopyID         *int64
	Provider       *string
	AccountID      *string
	RangeHeader    *string
}

// FinishStreamInput carries the terminal state of a stream. Phase 2 sets Status,
// BytesSent, HTTPStatus and FinishedAt; FailureKind/FailureMessage are wired for the
// failure-recording path that lands in a later task.
type FinishStreamInput struct {
	Status         string
	BytesSent      int64
	HTTPStatus     int64
	FinishedAt     int64
	FailureKind    *string
	FailureMessage *string
}

// CheckStreamAllowed reports whether echoUserID may start a NEW stream under its quota
// policy. It is the gate callers invoke BEFORE StartStream. period 'none' is always
// allowed. For metered periods it enforces MaxStreams (active stream count) and MaxBytes
// (bytes written in the period window), each only when the policy sets it. The active
// stream count is the SET UNION of in-process leases and unfinished DB stream events
// (deduped by event id) so a stream this process already started is not counted twice.
// max_playback_sessions is intentionally not enforced in this phase.
func (q *Quota) CheckStreamAllowed(ctx context.Context, echoUserID string) error {
	user, err := q.q.GetUser(ctx, queries.GetUserParams{ID: echoUserID})
	if err != nil {
		return err
	}
	policy, err := q.q.GetQuotaPolicy(ctx, queries.GetQuotaPolicyParams{ID: user.QuotaPolicyID})
	if err != nil {
		return err
	}
	if policy.Period == "none" {
		return nil
	}
	now := q.clock()
	start, end, ok := periodWindow(policy.Period, now)
	if !ok {
		// Unknown period: fail closed rather than silently allowing unmetered access.
		return ErrQuotaExceeded
	}

	if policy.MaxStreams.Valid {
		active, err := q.activeStreamCount(ctx, echoUserID, now)
		if err != nil {
			return err
		}
		if active >= policy.MaxStreams.Int64 {
			return ErrQuotaExceeded
		}
	}

	if policy.MaxBytes.Valid {
		used, err := q.q.SumPlaybackBytesInWindow(ctx, queries.SumPlaybackBytesInWindowParams{
			EchoUserID:  sql.NullString{String: echoUserID, Valid: true},
			StartedAt:   start,
			StartedAt_2: end,
		})
		if err != nil {
			return err
		}
		if used >= policy.MaxBytes.Int64 {
			return ErrQuotaExceeded
		}
	}

	return nil
}

// activeStreamCount returns the number of distinct in-flight streams for echoUserID,
// computed as the union of in-memory lease event ids and unfinished DB stream event ids
// newer than streamTimeout. Deduping by event id is what keeps a stream that this
// process both holds a lease for AND has already inserted a DB event for from counting
// as two.
func (q *Quota) activeStreamCount(ctx context.Context, echoUserID string, now time.Time) (int64, error) {
	dbIDs, err := q.q.ListActiveStreamEventIDs(ctx, queries.ListActiveStreamEventIDsParams{
		EchoUserID: sql.NullString{String: echoUserID, Valid: true},
		StartedAt:  now.Add(-q.streamTimeout).Unix(),
	})
	if err != nil {
		return 0, err
	}
	union := make(map[int64]struct{}, len(dbIDs)+1)
	for _, id := range dbIDs {
		union[id] = struct{}{}
	}
	for _, id := range q.leases.ActiveEventIDs(echoUserID) {
		union[id] = struct{}{}
	}
	return int64(len(union)), nil
}

// StartStream records the start of a stream and registers an in-process lease for it.
// It does NOT re-check quota (CheckStreamAllowed is the gate the caller runs first), so
// it never returns ErrQuotaExceeded. The event is inserted with operation 'stream',
// bytes_sent 0 and finished_at NULL; the returned id is later passed to FinishStream.
func (q *Quota) StartStream(ctx context.Context, in StartStreamInput) (int64, error) {
	id, err := q.q.InsertPlaybackEvent(ctx, queries.InsertPlaybackEventParams{
		RequestID:      in.RequestID,
		SessionID:      nullString(in.SessionID),
		ErrorTokenID:   nullString(in.ErrorTokenID),
		EchoUserID:     sql.NullString{String: in.EchoUserID, Valid: true},
		LibraryEntryID: nullInt64(in.LibraryEntryID),
		BlobID:         nullInt64(in.BlobID),
		CopyID:         nullInt64(in.CopyID),
		Provider:       nullString(in.Provider),
		AccountID:      nullString(in.AccountID),
		Operation:      in.Operation,
		Status:         "started",
		BytesSent:      0,
		RangeHeader:    nullString(in.RangeHeader),
		HttpStatus:     sql.NullInt64{},
		FailureKind:    sql.NullString{},
		FailureMessage: sql.NullString{},
		StartedAt:      in.StartedAt,
		FinishedAt:     sql.NullInt64{},
	})
	if err != nil {
		return 0, err
	}
	q.leases.Acquire(id, in.EchoUserID)
	return id, nil
}

// FinishStream writes the terminal state of the stream event id (status, bytes_sent,
// http_status, failure_*, finished_at) and releases the in-process lease for it. The
// underlying UPDATE is a no-op if the event was already finalized (e.g. reclaimed by
// reconciliation), so releasing the lease unconditionally is safe.
func (q *Quota) FinishStream(ctx context.Context, id int64, in FinishStreamInput) error {
	err := q.q.FinishPlaybackEvent(ctx, queries.FinishPlaybackEventParams{
		Status:         in.Status,
		BytesSent:      in.BytesSent,
		HttpStatus:     sql.NullInt64{Int64: in.HTTPStatus, Valid: in.HTTPStatus != 0},
		FailureKind:    nullString(in.FailureKind),
		FailureMessage: nullString(in.FailureMessage),
		FinishedAt:     sql.NullInt64{Int64: in.FinishedAt, Valid: true},
		ID:             id,
	})
	q.leases.Release(id)
	return err
}

// ReconcileInterruptedStreams marks stream events still unfinished and older than
// streamTimeout as 'interrupted', reclaiming streams orphaned by a crash. It is wired at
// startup in a later phase.
func (q *Quota) ReconcileInterruptedStreams(ctx context.Context) error {
	now := q.clock()
	return q.q.MarkStalePlaybackEventsInterrupted(ctx, queries.MarkStalePlaybackEventsInterruptedParams{
		FinishedAt: sql.NullInt64{Int64: now.Unix(), Valid: true},
		StartedAt:  now.Add(-q.streamTimeout).Unix(),
	})
}

// LeaseRegistry tracks streams in flight in THIS process, keyed by playback_events id and
// mapped back to the owning echo_user_id so a lease can be released knowing only the
// event id. It is concurrency-safe because a later phase acquires/releases leases across
// request goroutines.
type LeaseRegistry struct {
	mu     sync.Mutex
	owners map[int64]string // eventID -> echoUserID
}

// NewLeaseRegistry returns an empty registry.
func NewLeaseRegistry() *LeaseRegistry {
	return &LeaseRegistry{owners: make(map[int64]string)}
}

// Acquire registers an in-flight stream for echoUserID under its playback_events id.
func (r *LeaseRegistry) Acquire(eventID int64, echoUserID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners[eventID] = echoUserID
}

// Release drops the lease for eventID. It is a no-op if the lease is unknown, so it is
// safe to call unconditionally from FinishStream.
func (r *LeaseRegistry) Release(eventID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.owners, eventID)
}

// ActiveEventIDs returns the event ids currently leased by echoUserID. The result feeds
// the union in CheckStreamAllowed, so it is just the keys (order does not matter).
func (r *LeaseRegistry) ActiveEventIDs(echoUserID string) []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []int64
	for id, owner := range r.owners {
		if owner == echoUserID {
			ids = append(ids, id)
		}
	}
	return ids
}

// periodWindow returns the [start, end) Unix-second window for a quota period, and ok
// false for periods it does not understand. 'day' reuses the UTC-midnight dayStart
// helper; 'month' is [first-of-month 00:00 UTC, first-of-next-month 00:00 UTC);
// 'rolling_24h' is [now-24h, now).
func periodWindow(period string, now time.Time) (start, end int64, ok bool) {
	switch period {
	case "day":
		s := dayStart(now)
		return s.Unix(), s.Add(24 * time.Hour).Unix(), true
	case "month":
		s := monthStart(now)
		return s.Unix(), s.AddDate(0, 1, 0).Unix(), true
	case "rolling_24h":
		return now.Add(-24 * time.Hour).Unix(), now.Unix(), true
	default:
		return 0, 0, false
	}
}

// monthStart returns midnight UTC on the first day of now's month. It mirrors dayStart's
// use of UTC for determinism; timezone configurability is a later-phase concern.
func monthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func nullString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

func nullInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}
