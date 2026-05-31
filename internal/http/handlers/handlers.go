package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store/queries"
)

// Sidecar is the subset of the sidecar client the restore and stream handlers use.
type Sidecar interface {
	Link(ctx context.Context, storageMount, remotePath string) (*sidecarclient.DirectLink, error)
	Stream(ctx context.Context, req sidecarclient.StreamRequest) (*sidecarclient.StreamResult, error)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// parseFileID reads the {file_id} path param. On a malformed id it writes 400 and
// returns ok=false.
func parseFileID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "file_id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid file_id"})
		return 0, false
	}
	return id, true
}

// handleCopyFailure classifies a sidecar call error for one copy during fallback,
// shared by the restore and stream handlers (spec §4 / §5). On real hardware there
// is NO 404/410 dead signal — the sidecar reports object/storage/auth faults as an
// HTTP 200 envelope (code != 200) classified into a *SidecarTypedError. The typed
// kind + confidence decide the action:
//   - transport (dial/timeout)                       → abort the whole request (caller 503)
//   - object_missing, high-confidence link envelope  → MarkConfirmedDead, try next copy
//   - object_missing, anything else                  → MarkSuspectDead, try next copy
//   - storage/auth                                    → RecordAccountFailure, try next copy
//   - any other typed failure                         → MarkSuspectDead, try next copy
//
// Only a CONFIRMED object-missing retires a copy permanently (reason "gone"); every
// other failure leaves the copy revivable and falls back to the next copy so a
// healthy copy B still serves the request. The ONLY immediate abort is transport
// (sidecar unreachable) — there is no point trying further copies if we cannot reach
// the sidecar at all. Every non-confirmed failure carries a non-"gone" reason, which
// the caller's exhaustion logic reads to decide 503 (temporary) vs 404 (all dead):
// 404 is returned only when EVERY failed copy was confirmed-dead.
//
// Non-typed errors (a raw SidecarHTTPError, ErrLinkNotAvailable, etc.) fall through
// to the "this-copy-unavailable, try the next copy, do NOT mark dead" path.
//
// It returns a short reason string and abort=true only for the transport failure
// that should stop the whole request with 503.
func handleCopyFailure(ctx context.Context, resolver *restore.Resolver, logger *slog.Logger, scope string, fileID int64, fc queries.FileCopy, err error) (reason string, abort bool) {
	if errors.Is(err, sidecarclient.ErrSidecarUnreachable) {
		logger.Warn(scope+": sidecar unreachable", "file_id", fileID, "copy_id", fc.ID, "err", err)
		return reasonSidecarUnreachable, true
	}

	var typed *sidecarclient.SidecarTypedError
	if errors.As(err, &typed) {
		switch typed.Kind {
		case sidecarclient.SidecarErrTransport:
			logger.Warn(scope+": sidecar transport error", "file_id", fileID, "copy_id", fc.ID, "kind", typed.Kind)
			return reasonSidecarUnreachable, true
		case sidecarclient.SidecarErrObjectMissing:
			if typed.Operation == "link" &&
				typed.HTTPStatus == http.StatusOK &&
				typed.OpenListCode != 0 &&
				typed.OpenListCode != 200 &&
				typed.EvidenceClass == "json_envelope" &&
				typed.Confidence == "confirmed" {
				// Confirmed only for high-confidence link JSON-envelope evidence: this
				// is the single real-hardware signal that an object is truly gone.
				if mErr := resolver.MarkConfirmedDead(ctx, fc.ID, typed); mErr != nil {
					logger.Error(scope+": mark copy confirmed dead", "copy_id", fc.ID, "err", mErr)
				}
				logger.Warn(scope+": copy gone", "file_id", fileID, "copy_id", fc.ID, "code", typed.OpenListCode)
				return reasonConfirmedDead, false
			}
			// Unconfirmed object-missing: suspect (re-verified later), not gone. Fall
			// back to the next copy; if all copies fail this way the caller surfaces
			// 503 "try later", never a permanent 404.
			if mErr := resolver.MarkSuspectDead(ctx, fc.ID, typed); mErr != nil {
				logger.Error(scope+": mark copy suspect dead", "copy_id", fc.ID, "err", mErr)
			}
			logger.Warn(scope+": copy suspect (object missing, unconfirmed)", "file_id", fileID, "copy_id", fc.ID, "confidence", typed.Confidence)
			return reasonTemporaryUnavailable, false
		case sidecarclient.SidecarErrStorageMissing, sidecarclient.SidecarErrStorageUnhealthy, sidecarclient.SidecarErrAuthOrAccount:
			// Storage/auth fault: cool the account down but keep trying other copies
			// (a different account may hold a healthy copy). Exhausting all copies on
			// account faults surfaces 503 "try later".
			if mErr := resolver.RecordAccountFailure(ctx, fc.AccountID, typed); mErr != nil {
				logger.Error(scope+": record account failure", "account_id", fc.AccountID, "err", mErr)
			}
			logger.Warn(scope+": account unavailable", "file_id", fileID, "copy_id", fc.ID, "account_id", fc.AccountID, "kind", typed.Kind)
			return reasonAccountUnavailable, false
		default:
			// Any other typed failure is suspect: fall back, never confirm-dead.
			if mErr := resolver.MarkSuspectDead(ctx, fc.ID, typed); mErr != nil {
				logger.Error(scope+": mark copy suspect dead", "copy_id", fc.ID, "err", mErr)
			}
			logger.Warn(scope+": copy suspect", "file_id", fileID, "copy_id", fc.ID, "kind", typed.Kind, "confidence", typed.Confidence)
			return reasonTemporaryUnavailable, false
		}
	}

	// Non-typed errors: never mark dead on an unknown signal. A raw SidecarHTTPError
	// (any status, including 404/410 which no longer exists as a dead signal on real
	// hardware) or a no-link error is treated as this-copy-unavailable — try the next
	// copy, but leave the copy live.
	var httpErr *sidecarclient.SidecarHTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode >= 500 {
			logger.Warn(scope+": sidecar error", "file_id", fileID, "copy_id", fc.ID, "status", httpErr.StatusCode)
			return reasonSidecarError, false
		}
		logger.Warn(scope+": link rejected", "file_id", fileID, "copy_id", fc.ID, "status", httpErr.StatusCode)
		return reasonRejected, false
	}
	logger.Warn(scope+": link unavailable", "file_id", fileID, "copy_id", fc.ID, "err", err)
	return reasonNoLink, false
}

// reasonConfirmedDead is the only per-copy failure reason that means a copy is
// permanently gone (returned by handleCopyFailure after MarkConfirmedDead). Every
// other non-aborting reason (reasonTemporaryUnavailable, reasonAccountUnavailable,
// reasonSidecarError, reasonRejected, reasonNoLink) is a transient/suspect failure
// that must NOT collapse the request to a permanent 404. reasonSidecarUnreachable is
// the only abort=true reason (the request stops with 503). These are the byte values
// emitted in the X-Echo-Reason header and the "error"/"reason" JSON fields, so the
// literals are part of the wire contract — do not change them.
const (
	reasonConfirmedDead        = "gone"
	reasonSidecarUnreachable   = "sidecar-unreachable"
	reasonTemporaryUnavailable = "temporary-unavailable"
	reasonAccountUnavailable   = "account-unavailable"
	reasonSidecarError         = "sidecar-error"
	reasonRejected             = "rejected"
	reasonNoLink               = "no-link"
)

// exhaustionStatus decides the final status when the copy loop tried every live copy
// without a success (spec §5: "无 live copy / all confirmed dead: 404",
// "临时 sidecar/account 问题: 503"). It is reason-aware:
//   - liveCopies == 0                       → 404 (no live copy to begin with).
//   - all failed copies were confirmed-dead → 404 (all copies dead).
//   - any failure was transient/suspect     → 503 (temporary; retry later).
//
// reasons holds one per-copy failure reason for every copy that did NOT abort.
func exhaustionStatus(liveCopies int, reasons []string) (status int, reason string) {
	if liveCopies == 0 {
		return http.StatusNotFound, "all-copies-dead"
	}
	for _, r := range reasons {
		if r != reasonConfirmedDead {
			// At least one copy failed transiently — do not permanently 404.
			return http.StatusServiceUnavailable, "temporary-unavailable"
		}
	}
	return http.StatusNotFound, "all-copies-dead"
}
