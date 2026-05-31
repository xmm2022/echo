package restore

import (
	"context"
	"database/sql"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store/queries"
)

// maxLiveCopies bounds the candidate copies a single restore/stream request
// tries before giving up (spec §4 restore: ORDER BY ... LIMIT 5).
const maxLiveCopies = 5

// Failure cooldown / re-verify windows (spec §5). These are deliberately small:
// a suspect copy or a degraded account should re-enter the live pool quickly once
// the transient fault clears, because on real hardware we cannot distinguish a
// permanently-gone object from a temporary backend hiccup without re-probing.
const (
	// suspectVerifyAfter is how long a suspect-dead copy is hidden from the live
	// pool before it is eligible for re-verification.
	suspectVerifyAfter = 5 * time.Minute
	// accountCooldown is how long an account is cooled down / held unhealthy after
	// a storage or auth failure before it is retried.
	accountCooldown = 5 * time.Minute
)

// Querier is the subset of store queries the resolver depends on.
type Querier interface {
	GetLibraryEntryByID(ctx context.Context, arg queries.GetLibraryEntryByIDParams) (queries.LibraryEntry, error)
	ListLiveCopiesByBlob(ctx context.Context, arg queries.ListLiveCopiesByBlobParams) ([]queries.FileCopy, error)
	ListLiveCopiesByBlobPreferProvider(ctx context.Context, arg queries.ListLiveCopiesByBlobPreferProviderParams) ([]queries.FileCopy, error)
	MarkFileCopyConfirmedDead(ctx context.Context, arg queries.MarkFileCopyConfirmedDeadParams) error
	MarkFileCopySuspectDead(ctx context.Context, arg queries.MarkFileCopySuspectDeadParams) error
	MarkAccountSchedulerState(ctx context.Context, arg queries.MarkAccountSchedulerStateParams) error
}

// Resolver maps a file_id to its live copies and records copies the sidecar
// reports as gone.
type Resolver struct {
	q   Querier
	now func() time.Time
}

// NewResolver builds a Resolver. A nil now defaults to time.Now.
func NewResolver(q Querier, now func() time.Time) *Resolver {
	if now == nil {
		now = time.Now
	}
	return &Resolver{q: q, now: now}
}

// LiveCopies resolves file_id -> blob_id -> live copies. With an empty prefer the
// copies are ordered by last_seen DESC; otherwise the preferred provider sorts
// first (spec §4). A missing file_id surfaces as sql.ErrNoRows.
func (r *Resolver) LiveCopies(ctx context.Context, fileID int64, prefer string) ([]queries.FileCopy, error) {
	entry, err := r.q.GetLibraryEntryByID(ctx, queries.GetLibraryEntryByIDParams{ID: fileID})
	if err != nil {
		return nil, err
	}
	if prefer == "" {
		return r.q.ListLiveCopiesByBlob(ctx, queries.ListLiveCopiesByBlobParams{
			BlobID: entry.BlobID,
			Limit:  maxLiveCopies,
		})
	}
	return r.q.ListLiveCopiesByBlobPreferProvider(ctx, queries.ListLiveCopiesByBlobPreferProviderParams{
		PreferredProvider: prefer,
		BlobID:            entry.BlobID,
		Limit:             maxLiveCopies,
	})
}

// MarkConfirmedDead permanently retires a copy after a high-confidence object-
// missing signal (real hardware: an /api/fs/link 200 + envelope code != 200 whose
// message says the object is gone). It sets status='dead' and
// scheduler_state='confirmed_dead' so the copy never re-enters the live pool until
// a successful re-ingest revives it (see UpsertFileCopyLive).
func (r *Resolver) MarkConfirmedDead(ctx context.Context, copyID int64, err *sidecarclient.SidecarTypedError) error {
	now := r.now().Unix()
	return r.q.MarkFileCopyConfirmedDead(ctx, queries.MarkFileCopyConfirmedDeadParams{
		LastFailureAt:      sql.NullInt64{Int64: now, Valid: true},
		LastFailureKind:    failureKind(err),
		LastFailureCode:    failureCode(err),
		LastFailureMessage: failureMessage(err),
		DeadReason:         failureMessage(err),
		DeadAt:             sql.NullInt64{Int64: now, Valid: true},
		ID:                 copyID,
	})
}

// MarkSuspectDead hides a copy from the live pool without declaring it permanently
// gone: it sets scheduler_state='suspect_dead' (NO status change) and a verify_after
// horizon. This is the real-hardware default for any failure we cannot confirm —
// the copy is re-eligible once verify_after elapses, so a transient backend fault
// never causes a permanent loss.
func (r *Resolver) MarkSuspectDead(ctx context.Context, copyID int64, err *sidecarclient.SidecarTypedError) error {
	now := r.now()
	return r.q.MarkFileCopySuspectDead(ctx, queries.MarkFileCopySuspectDeadParams{
		LastFailureAt:      sql.NullInt64{Int64: now.Unix(), Valid: true},
		LastFailureKind:    failureKind(err),
		LastFailureCode:    failureCode(err),
		LastFailureMessage: failureMessage(err),
		VerifyAfter:        sql.NullInt64{Int64: now.Add(suspectVerifyAfter).Unix(), Valid: true},
		ID:                 copyID,
	})
}

// RecordAccountFailure marks an account as not-currently-usable after a storage or
// auth/token failure, so the live-copy queries skip every copy that account backs.
// The scheduler_state reflects the fault class:
//   - storage_missing   → unhealthy     (storage not registered; needs operator fix)
//   - storage_unhealthy → cooldown       (transient storage health; retry after cooldown)
//   - auth_or_account   → token_suspect  (auth/token problem; needs re-auth/recheck)
func (r *Resolver) RecordAccountFailure(ctx context.Context, accountID string, err *sidecarclient.SidecarTypedError) error {
	now := r.now()
	state := "cooldown"
	if err != nil {
		switch err.Kind {
		case sidecarclient.SidecarErrStorageMissing:
			state = "unhealthy"
		case sidecarclient.SidecarErrAuthOrAccount:
			state = "token_suspect"
		case sidecarclient.SidecarErrStorageUnhealthy:
			state = "cooldown"
		}
	}
	cooldownUntil := sql.NullInt64{Int64: now.Add(accountCooldown).Unix(), Valid: true}
	return r.q.MarkAccountSchedulerState(ctx, queries.MarkAccountSchedulerStateParams{
		SchedulerState:   state,
		CooldownUntil:    cooldownUntil,
		RecheckAfter:     cooldownUntil,
		StatusReason:     failureMessage(err),
		LastErrorAt:      sql.NullInt64{Int64: now.Unix(), Valid: true},
		LastErrorKind:    failureKind(err),
		LastErrorMessage: failureMessage(err),
		UpdatedAt:        now.Unix(),
		ID:               accountID,
	})
}

// MarkDead is a v0.1 compatibility wrapper: it confirm-dead's a copy as if the
// sidecar had reported a high-confidence object-missing link envelope. New code
// should call MarkConfirmedDead / MarkSuspectDead / RecordAccountFailure directly
// from the typed-error decision path; MarkDead must NOT be used for unconfirmed
// failures (e.g. /d/ HTML 500), which would wrongly retire a live copy.
func (r *Resolver) MarkDead(ctx context.Context, copyID int64) error {
	return r.MarkConfirmedDead(ctx, copyID, &sidecarclient.SidecarTypedError{
		Kind:          sidecarclient.SidecarErrObjectMissing,
		Operation:     "link",
		HTTPStatus:    200,
		OpenListCode:  500,
		SafeMessage:   "object not found",
		EvidenceClass: "json_envelope",
		Confidence:    "confirmed",
	})
}

// failureKind, failureCode and failureMessage extract the persisted failure fields
// from a typed error, tolerating a nil error.
func failureKind(err *sidecarclient.SidecarTypedError) sql.NullString {
	if err == nil || err.Kind == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: string(err.Kind), Valid: true}
}

func failureCode(err *sidecarclient.SidecarTypedError) sql.NullInt64 {
	if err == nil || err.OpenListCode == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(err.OpenListCode), Valid: true}
}

func failureMessage(err *sidecarclient.SidecarTypedError) sql.NullString {
	if err == nil || err.SafeMessage == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: err.SafeMessage, Valid: true}
}
