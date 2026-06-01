package playback

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store/queries"
)

// suspectRetryDelay is how long a copy demoted to scheduler_state 'suspect_dead' is held
// out of selection before it becomes eligible for re-verification (verify_after = now +
// this). It is deliberately short relative to a confirmed-dead copy (which is never
// auto-revived) because a suspect failure may be transient; one hour balances sparing the
// account from repeated hits against quickly recovering a still-good copy.
const suspectRetryDelay = 1 * time.Hour

// FailureQuerier is the subset of *queries.Queries the FailureRecorder needs.
// *queries.Queries satisfies it directly, so callers pass store.Queries. GetFileCopyByID
// is required (not just by tests): InsertCopyFailure needs the copy's sidecar_id and
// storage_mount (both NOT NULL), which the caller does not supply, so the recorder reads
// them off the copy row.
type FailureQuerier interface {
	GetFileCopyByID(ctx context.Context, arg queries.GetFileCopyByIDParams) (queries.FileCopy, error)
	InsertCopyFailure(ctx context.Context, arg queries.InsertCopyFailureParams) error
	MarkFileCopyConfirmedDead(ctx context.Context, arg queries.MarkFileCopyConfirmedDeadParams) error
	MarkFileCopySuspectDead(ctx context.Context, arg queries.MarkFileCopySuspectDeadParams) error
}

// FailureRecorder records sidecar copy failures into the copy_failures audit table and
// updates the failing file_copies row's scheduler state, using the Phase-0 typed sidecar
// error model to decide between a confirmed-dead and a suspect-dead outcome. It never logs
// or persists SidecarTypedError.RawMessage; only the scrubbed SafeMessage is stored.
type FailureRecorder struct {
	q   FailureQuerier
	now func() time.Time
}

// NewFailureRecorder builds a FailureRecorder over q, using now as its clock.
func NewFailureRecorder(q FailureQuerier, now func() time.Time) *FailureRecorder {
	return &FailureRecorder{q: q, now: now}
}

// ApplyCopyFailure records a single copy failure and demotes the copy.
//
// A failure is treated as a confirmed dead copy only for the narrow, high-confidence
// signal that OpenList itself reported the object missing on a link operation: kind
// object_missing, operation "link", HTTP 200 (the OpenList envelope rode on a 200), a
// non-success OpenList code, json_envelope evidence, and confirmed confidence. Anything
// else — a different operation, a different kind, lower confidence, transport/HTML
// evidence — is treated as suspect: the copy is held out for re-verification but its
// status is left 'live' so a transient blip does not permanently kill a good copy.
//
// copyID must reference an existing file_copies row; its sidecar_id/storage_mount are read
// to populate the NOT NULL columns on the copy_failures row. A missing copy surfaces as
// sql.ErrNoRows.
func (r *FailureRecorder) ApplyCopyFailure(ctx context.Context, copyID int64, accountID string, typed *sidecarclient.SidecarTypedError, requestID string) error {
	copyRow, err := r.q.GetFileCopyByID(ctx, queries.GetFileCopyByIDParams{ID: copyID})
	if err != nil {
		return err
	}
	msg := SafeFailureMessage(typed.SafeMessage)

	if typed.Kind == sidecarclient.SidecarErrObjectMissing &&
		typed.Operation == "link" &&
		typed.HTTPStatus == http.StatusOK &&
		typed.OpenListCode != 0 &&
		typed.OpenListCode != 200 &&
		typed.EvidenceClass == "json_envelope" &&
		typed.Confidence == "confirmed" {
		return r.markConfirmed(ctx, copyRow, accountID, typed, msg, requestID)
	}
	return r.markSuspect(ctx, copyRow, accountID, typed, msg, requestID)
}

// markConfirmed writes the audit row and flips the copy to status='dead' /
// scheduler_state='confirmed_dead'.
func (r *FailureRecorder) markConfirmed(ctx context.Context, copyRow queries.FileCopy, accountID string, typed *sidecarclient.SidecarTypedError, msg, requestID string) error {
	if err := r.insertFailure(ctx, copyRow, accountID, typed, msg, requestID); err != nil {
		return err
	}
	now := r.now().Unix()
	return r.q.MarkFileCopyConfirmedDead(ctx, queries.MarkFileCopyConfirmedDeadParams{
		LastFailureAt:      nullInt64FromInt(now),
		LastFailureKind:    nullStringFromVal(string(typed.Kind)),
		LastFailureCode:    failureCode(typed),
		LastFailureMessage: nullStringFromVal(msg),
		DeadReason:         nullStringFromVal(string(typed.Kind)),
		DeadAt:             nullInt64FromInt(now),
		ID:                 copyRow.ID,
	})
}

// markSuspect writes the audit row and demotes the copy to scheduler_state='suspect_dead'
// with a verify_after horizon, deliberately leaving file_copies.status untouched (the
// MarkFileCopySuspectDead query does not change it).
func (r *FailureRecorder) markSuspect(ctx context.Context, copyRow queries.FileCopy, accountID string, typed *sidecarclient.SidecarTypedError, msg, requestID string) error {
	if err := r.insertFailure(ctx, copyRow, accountID, typed, msg, requestID); err != nil {
		return err
	}
	now := r.now()
	return r.q.MarkFileCopySuspectDead(ctx, queries.MarkFileCopySuspectDeadParams{
		LastFailureAt:      nullInt64FromInt(now.Unix()),
		LastFailureKind:    nullStringFromVal(string(typed.Kind)),
		LastFailureCode:    failureCode(typed),
		LastFailureMessage: nullStringFromVal(msg),
		VerifyAfter:        nullInt64FromInt(now.Add(suspectRetryDelay).Unix()),
		ID:                 copyRow.ID,
	})
}

// insertFailure appends the copy_failures audit row shared by both branches. sidecar_id /
// storage_mount come from the fetched copy (they are NOT NULL and not passed by the
// caller); operation/kind/confidence/evidence_class come verbatim from the typed error,
// whose Phase-0 classifier already guarantees confidence/evidence_class fall within the
// table's CHECK sets.
func (r *FailureRecorder) insertFailure(ctx context.Context, copyRow queries.FileCopy, accountID string, typed *sidecarclient.SidecarTypedError, msg, requestID string) error {
	return r.q.InsertCopyFailure(ctx, queries.InsertCopyFailureParams{
		CopyID:        sql.NullInt64{Int64: copyRow.ID, Valid: true},
		AccountID:     nullStringFromVal(accountID),
		SidecarID:     copyRow.SidecarID,
		StorageMount:  copyRow.StorageMount,
		Operation:     typed.Operation,
		Kind:          string(typed.Kind),
		Confidence:    typed.Confidence,
		EvidenceClass: typed.EvidenceClass,
		HttpStatus:    nullInt64NonZero(int64(typed.HTTPStatus)),
		OpenlistCode:  nullInt64NonZero(int64(typed.OpenListCode)),
		SafeMessage:   nullStringFromVal(msg),
		ObservedAt:    r.now().Unix(),
		RequestID:     nullStringFromVal(requestID),
	})
}

// failureCode picks the most specific numeric code for the file_copies failure columns:
// the OpenList code when present, otherwise the HTTP status, otherwise NULL.
func failureCode(typed *sidecarclient.SidecarTypedError) sql.NullInt64 {
	if typed.OpenListCode != 0 {
		return sql.NullInt64{Int64: int64(typed.OpenListCode), Valid: true}
	}
	return nullInt64NonZero(int64(typed.HTTPStatus))
}

// nullInt64NonZero returns a non-NULL value only when v != 0, mapping the "unset" zero
// value of an int code to SQL NULL.
func nullInt64NonZero(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

// nullInt64FromInt always stores v (used for timestamps, which are meaningfully zero only
// in tests pinned to the Unix epoch but never "unset" here).
func nullInt64FromInt(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: true}
}

// nullStringFromVal maps "" to SQL NULL and any other string to a non-NULL value.
func nullStringFromVal(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}
