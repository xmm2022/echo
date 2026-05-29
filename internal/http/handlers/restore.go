package handlers

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store/queries"
)

// RestoreDeps wires the restore JSON handler.
type RestoreDeps struct {
	Resolver *restore.Resolver
	Sidecar  Sidecar
	Cache    *restore.LinkCache
	Logger   *slog.Logger
}

type linkResponse struct {
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt string            `json:"expires_at,omitempty"`
	Provider  string            `json:"provider"`
	CopyID    int64             `json:"copy_id"`
}

type deadCopy struct {
	CopyID   int64  `json:"copy_id"`
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

type restoreErrorResponse struct {
	Error      string     `json:"error"`
	DeadCopies []deadCopy `json:"dead_copies,omitempty"`
}

// Restore serves GET /api/restore/{file_id}?prefer= — it returns a sidecar direct
// link as JSON, falling back across live copies and recording dead ones. Entry is
// by file_id only — no path-based lookup (spec §13).
func Restore(deps RestoreDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fileID, ok := parseFileID(w, r)
		if !ok {
			return
		}
		prefer := r.URL.Query().Get("prefer")

		copies, err := deps.Resolver.LiveCopies(r.Context(), fileID, prefer)
		if errors.Is(err, sql.ErrNoRows) {
			writeRestoreError(w, http.StatusNotFound, "file-not-found", nil)
			return
		}
		if err != nil {
			deps.Logger.Error("restore: resolve live copies", "file_id", fileID, "err", err)
			writeRestoreError(w, http.StatusInternalServerError, "internal-error", nil)
			return
		}

		var dead []deadCopy
		for _, fc := range copies {
			if link, ok := deps.Cache.Get(fc.BlobID, fc.ID); ok {
				// The cache is a 60s optimization (spec §4): a cached link is served
				// even if another request marked this copy dead within the TTL
				// window — the sidecar's own link validity is the real floor.
				writeJSON(w, http.StatusOK, newLinkResponse(fc, link))
				return
			}
			link, err := deps.Sidecar.Link(r.Context(), fc.StorageMount, fc.RemotePath)
			if err == nil {
				deps.Cache.Put(fc.BlobID, fc.ID, link)
				writeJSON(w, http.StatusOK, newLinkResponse(fc, link))
				return
			}
			reason, abort := handleCopyFailure(r.Context(), deps.Resolver, deps.Logger, "restore", fileID, fc, err)
			if abort {
				writeRestoreError(w, http.StatusServiceUnavailable, reason, nil)
				return
			}
			dead = append(dead, deadCopy{CopyID: fc.ID, Provider: fc.Provider, Reason: reason})
		}
		writeRestoreError(w, http.StatusNotFound, "all-copies-dead", dead)
	}
}

func newLinkResponse(fc queries.FileCopy, link *sidecarclient.DirectLink) linkResponse {
	return linkResponse{
		URL:       link.URL,
		Headers:   flattenHeaders(link.Headers),
		ExpiresAt: formatExpiry(link.ExpiresAt),
		Provider:  fc.Provider,
		CopyID:    fc.ID,
	}
}

// flattenHeaders collapses link headers to one value per key. DirectLink headers
// are built with Header.Set (single value per key), so no values are lost.
func flattenHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for key := range h {
		out[key] = h.Get(key)
	}
	return out
}

func formatExpiry(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func writeRestoreError(w http.ResponseWriter, status int, reason string, dead []deadCopy) {
	w.Header().Set("X-Echo-Reason", reason)
	writeJSON(w, status, restoreErrorResponse{Error: reason, DeadCopies: dead})
}
