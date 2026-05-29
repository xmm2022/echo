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
// shared by the restore and stream handlers (spec §4 / §5):
//   - ErrSidecarUnreachable (dial/timeout) → abort the whole request (caller 503)
//   - SidecarHTTPError 404/410 → mark the copy dead, try the next copy
//   - SidecarHTTPError 5xx → leave status untouched, warn, try the next copy
//   - other 4xx / no link → try the next copy
//
// It returns a short reason string and abort=true only for the unreachable case.
func handleCopyFailure(ctx context.Context, resolver *restore.Resolver, logger *slog.Logger, scope string, fileID int64, fc queries.FileCopy, err error) (reason string, abort bool) {
	if errors.Is(err, sidecarclient.ErrSidecarUnreachable) {
		logger.Warn(scope+": sidecar unreachable", "file_id", fileID, "copy_id", fc.ID, "err", err)
		return "sidecar-unreachable", true
	}
	var httpErr *sidecarclient.SidecarHTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusGone:
			if mErr := resolver.MarkDead(ctx, fc.ID); mErr != nil {
				logger.Error(scope+": mark copy dead", "copy_id", fc.ID, "err", mErr)
			}
			logger.Warn(scope+": copy gone", "file_id", fileID, "copy_id", fc.ID, "status", httpErr.StatusCode)
			return "gone", false
		case httpErr.StatusCode >= 500:
			logger.Warn(scope+": sidecar error", "file_id", fileID, "copy_id", fc.ID, "status", httpErr.StatusCode)
			return "sidecar-error", false
		default:
			// Other 4xx (e.g. 401/403 auth): treat as this-copy-unavailable and try
			// the next copy, but do NOT mark it dead — it may be a transient auth
			// problem. Reconciling account.status=token_expired is a later phase.
			logger.Warn(scope+": link rejected", "file_id", fileID, "copy_id", fc.ID, "status", httpErr.StatusCode)
			return "rejected", false
		}
	}
	logger.Warn(scope+": link unavailable", "file_id", fileID, "copy_id", fc.ID, "err", err)
	return "no-link", false
}
