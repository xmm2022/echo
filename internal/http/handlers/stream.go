package handlers

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
)

// StreamDeps wires the stream proxy handler. It needs no link cache: streaming
// goes straight through sidecar.Stream rather than resolving a direct link.
type StreamDeps struct {
	Resolver *restore.Resolver
	Sidecar  Sidecar
	Logger   *slog.Logger
}

// Stream serves GET /api/stream/{file_id}?prefer= — Echo reverse-proxies the
// sidecar byte stream so clients (e.g. Emby) need not handle signed direct links.
// Entry is by file_id only — no path-based lookup (spec §13).
func Stream(deps StreamDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fileID, ok := parseFileID(w, r)
		if !ok {
			return
		}
		prefer := r.URL.Query().Get("prefer")

		copies, err := deps.Resolver.LiveCopies(r.Context(), fileID, prefer)
		if errors.Is(err, sql.ErrNoRows) {
			writeStreamError(w, http.StatusNotFound, "file-not-found")
			return
		}
		if err != nil {
			deps.Logger.Error("stream: resolve live copies", "file_id", fileID, "err", err)
			writeStreamError(w, http.StatusInternalServerError, "internal-error")
			return
		}

		for _, fc := range copies {
			req := restore.StreamRequestFor(fc, r.Header)
			result, err := deps.Sidecar.Stream(r.Context(), req)
			if err == nil {
				if cErr := writeStreamResult(w, result); cErr != nil {
					// Mid-stream break: headers are already sent, so we cannot
					// change the status — just record it and stop (no retry).
					deps.Logger.Warn("stream: copy interrupted", "file_id", fileID, "copy_id", fc.ID, "err", cErr)
				}
				return
			}
			reason, abort := handleCopyFailure(r.Context(), deps.Resolver, deps.Logger, "stream", fileID, fc, err)
			if abort {
				writeStreamError(w, http.StatusServiceUnavailable, reason)
				return
			}
		}
		writeStreamError(w, http.StatusNotFound, "all-copies-dead")
	}
}

// writeStreamResult transcribes the sidecar response to the client following the
// Go ResponseWriter contract (spec §4 / R2): headers first, then the status code,
// then the body if present. result.Header is already the allowlisted subset the
// sidecar client produced.
func writeStreamResult(w http.ResponseWriter, result *sidecarclient.StreamResult) error {
	dst := w.Header()
	for key, values := range result.Header {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	w.WriteHeader(result.StatusCode)
	if result.Body == nil {
		return nil
	}
	defer result.Body.Close()
	_, err := io.Copy(w, result.Body)
	return err
}

func writeStreamError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("X-Echo-Reason", reason)
	writeJSON(w, status, map[string]string{"error": reason})
}
