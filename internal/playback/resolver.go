package playback

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"sort"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

var (
	// ErrUnauthorized is returned when the user neither owns the library nor holds
	// an enabled playback grant for it.
	ErrUnauthorized = errors.New("playback unauthorized")
	// ErrEntryMissing is returned when the requested library entry does not exist.
	ErrEntryMissing = errors.New("library entry missing")
)

// defaultStreamTimeout bounds how far back an in-flight stream is considered active
// when the resolver's concurrency / daily-bytes filters look at playback_events.
// TODO: wire this to config in a later phase instead of hardcoding it here.
const defaultStreamTimeout = 6 * time.Hour

// maxResolvedCopies caps how many candidate copies a single resolve returns.
const maxResolvedCopies = 5

// Querier is the subset of *queries.Queries the resolver needs. *queries.Queries
// satisfies it directly, so callers pass store.Queries.
type Querier interface {
	GetLibraryEntryByID(ctx context.Context, arg queries.GetLibraryEntryByIDParams) (queries.LibraryEntry, error)
	UserCanPlaybackLibrary(ctx context.Context, arg queries.UserCanPlaybackLibraryParams) (int64, error)
	ListPlayableCopiesForUser(ctx context.Context, arg queries.ListPlayableCopiesForUserParams) ([]queries.ListPlayableCopiesForUserRow, error)
}

// Resolver turns a (user, library entry) request into the ordered set of file copies
// the user is allowed to stream, applying authorization and the scheduler/pool filters
// baked into ListPlayableCopiesForUser.
type Resolver struct {
	q             Querier
	now           func() time.Time
	streamTimeout time.Duration
}

// NewResolver builds a Resolver. streamTimeout uses defaultStreamTimeout for now; a
// later phase will wire it to config.
func NewResolver(q Querier, now func() time.Time) *Resolver {
	if now == nil {
		now = time.Now
	}
	return &Resolver{
		q:             q,
		now:           now,
		streamTimeout: defaultStreamTimeout,
	}
}

// ResolveCopies returns the playable copies for echoUserID against libraryEntryID,
// ordered deterministically (lower pool priority first, ties broken by a stable hash).
// preferProvider biases the DB-side provider ordering; limit is clamped to 1..5.
func (r *Resolver) ResolveCopies(ctx context.Context, echoUserID string, libraryEntryID int64, preferProvider string, limit int64) ([]queries.ListPlayableCopiesForUserRow, error) {
	entry, err := r.q.GetLibraryEntryByID(ctx, queries.GetLibraryEntryByIDParams{ID: libraryEntryID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEntryMissing
	}
	if err != nil {
		return nil, err
	}

	allowed, err := r.q.UserCanPlaybackLibrary(ctx, queries.UserCanPlaybackLibraryParams{
		LibraryID:  entry.LibraryID,
		EchoUserID: echoUserID,
	})
	if err != nil {
		return nil, err
	}
	// UserCanPlaybackLibrary is a SQL EXISTS, surfaced by sqlc as int64 (1 = allowed).
	if allowed == 0 {
		return nil, ErrUnauthorized
	}

	if limit <= 0 || limit > maxResolvedCopies {
		limit = maxResolvedCopies
	}

	now := r.now()
	dayStart := dayStart(now)
	rows, err := r.q.ListPlayableCopiesForUser(ctx, queries.ListPlayableCopiesForUserParams{
		BlobID:         entry.BlobID,
		EchoUserID:     echoUserID,
		PreferProvider: preferProvider,
		Now:            sql.NullInt64{Int64: now.Unix(), Valid: true},
		ActiveSince:    now.Add(-r.streamTimeout).Unix(),
		DayStart:       dayStart.Unix(),
		DayEnd:         dayStart.Add(24 * time.Hour).Unix(),
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}

	// Phase 2 has no session selector yet, so the stable-hash key omits it (passed as
	// ""). Phase 3 will thread a real selector through this 4th argument.
	return stableWeightedOrder(rows, echoUserID, libraryEntryID, ""), nil
}

// stableWeightedOrder orders copies so that lower pool priority comes first and rows
// with equal priority are broken by a deterministic hash of
// (echoUserID + libraryEntryID + copyID), optionally salted by sessionSelector. The
// DB already returns rows ordered by provider_rank/priority/last_seen; this re-sort
// only changes the relative order of equal-priority rows, keeping provider preference
// intact within a priority band and producing the same order on every call.
func stableWeightedOrder(rows []queries.ListPlayableCopiesForUserRow, echoUserID string, libraryEntryID int64, sessionSelector string) []queries.ListPlayableCopiesForUserRow {
	if len(rows) < 2 {
		return rows
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Priority != rows[j].Priority {
			return rows[i].Priority < rows[j].Priority
		}
		hi := stableHash(echoUserID, libraryEntryID, rows[i].ID, sessionSelector)
		hj := stableHash(echoUserID, libraryEntryID, rows[j].ID, sessionSelector)
		if hi != hj {
			return hi < hj
		}
		// Final tiebreak on copy ID guarantees a total, stable ordering even on the
		// astronomically unlikely hash collision.
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// stableHash derives a deterministic 64-bit key from the routing inputs.
func stableHash(echoUserID string, libraryEntryID, copyID int64, sessionSelector string) uint64 {
	h := sha256.New()
	h.Write([]byte(sessionSelector))
	h.Write([]byte{0})
	h.Write([]byte(echoUserID))
	h.Write([]byte{0})
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(libraryEntryID))
	h.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], uint64(copyID))
	h.Write(buf[:])
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}

// dayStart returns midnight at the start of t's day. It uses UTC for determinism;
// TODO: the timezone becomes configurable (per-deployment / per-user) in a later phase.
func dayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
