package embyproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/xmm2022/echo/internal/playback"
	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
)

// Sidecar is the NARROW subset of the sidecar client the stream byte path needs.
//
// Deviation from the plan's handlers.Sidecar: defining a local one-method interface
// here keeps embyproxy from importing the HTTP handlers package and documents the
// interface-segregation invariant that GET needs only Stream and HEAD must NEVER call
// it. Both the production *metrics.instrumentedSidecar (which already implements the
// full sidecar client) and the test fake satisfy this.
type Sidecar interface {
	Stream(ctx context.Context, req sidecarclient.StreamRequest) (*sidecarclient.StreamResult, error)
}

// streamMetrics is the NARROW, nil-safe slice of *metrics.Metrics the stream path
// touches. Following the local-interface pattern used for Sidecar above, it keeps
// embyproxy depending only on a tiny contract (not the concrete metrics type) so the
// handler stays testable with a nil/omitted metrics handle. *metrics.Metrics
// satisfies it; passing nil is a safe no-op (StreamHandler swaps a nil in for a
// noopStreamMetrics so call sites never dereference a nil interface).
type streamMetrics interface {
	PlaybackSessionStarted()
	PlaybackSessionEnded()
	PlaybackStreamBytes(provider, result string, n int64)
}

// noopStreamMetrics is the do-nothing streamMetrics used when StreamHandler is wired
// without a metrics handle (tests, unmetered builds). It avoids the nil-interface
// method-call panic that a bare nil streamMetrics would cause.
type noopStreamMetrics struct{}

func (noopStreamMetrics) PlaybackSessionStarted()                   {}
func (noopStreamMetrics) PlaybackSessionEnded()                     {}
func (noopStreamMetrics) PlaybackStreamBytes(string, string, int64) {}

// StreamHandler serves Echo's reserved /stream/{token} route. The token alone
// authorizes the stream: there is NO upstream Emby cookie, admin bearer, or upstream
// token on this path. GET re-authorizes the bearer of the token against the resolver,
// enforces quota, then proxies the sidecar byte stream across the resolved copies with
// fallback BEFORE any header is written. HEAD answers metadata only and never touches
// the sidecar byte path.
//
// m may be nil (unwired tests); a nil m is replaced with a no-op so the byte path
// never dereferences a nil interface.
func StreamHandler(mgr *SessionManager, resolver *playback.Resolver, quota *playback.Quota, sidecar Sidecar, failures *playback.FailureRecorder, m streamMetrics, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = noopStreamMetrics{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			handleStreamHead(w, r, mgr, resolver, quota, logger)
			return
		}
		handleStreamGet(w, r, mgr, resolver, quota, sidecar, failures, m, logger)
	})
}

func handleStreamGet(w http.ResponseWriter, r *http.Request, mgr *SessionManager, resolver *playback.Resolver, quota *playback.Quota, sidecar Sidecar, failures *playback.FailureRecorder, m streamMetrics, logger *slog.Logger) {
	ctx := r.Context()
	reqID := chimw.GetReqID(ctx)
	token := chi.URLParam(r, "token")

	session, err := mgr.LookupPlaybackSession(ctx, token)
	if err != nil {
		// Invalid/expired/revoked all collapse to a generic 404: never reveal which.
		writeStreamReason(w, http.StatusNotFound, "temporary_unavailable")
		return
	}

	copies, err := resolver.ResolveCopies(ctx, session.EchoUserID, session.LibraryEntryID.Int64, nullToString(session.PreferProvider), 5)
	switch {
	case errors.Is(err, playback.ErrUnauthorized):
		writeStreamReason(w, http.StatusForbidden, "unauthorized")
		return
	case errors.Is(err, playback.ErrEntryMissing):
		writeStreamReason(w, http.StatusNotFound, "temporary_unavailable")
		return
	case err != nil:
		logger.Error("emby stream: resolve copies", "request_id", reqID, "err", err)
		writeStreamReason(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if len(copies) == 0 {
		writeStreamReason(w, http.StatusServiceUnavailable, "temporary_unavailable")
		return
	}

	if err := quota.CheckStreamAllowed(ctx, session.EchoUserID); err != nil {
		if errors.Is(err, playback.ErrQuotaExceeded) {
			writeStreamReason(w, http.StatusTooManyRequests, "quota_exceeded")
			return
		}
		logger.Error("emby stream: check quota", "request_id", reqID, "err", err)
		writeStreamReason(w, http.StatusInternalServerError, "internal_error")
		return
	}

	eventID, err := quota.StartStream(ctx, playback.StartStreamInput{
		RequestID:      reqID,
		EchoUserID:     session.EchoUserID,
		Operation:      "stream",
		StartedAt:      time.Now().Unix(),
		SessionID:      &session.ID,
		LibraryEntryID: &session.LibraryEntryID.Int64,
		BlobID:         &session.BlobID.Int64,
		RangeHeader:    ptrIfNotEmpty(r.Header.Get("Range")),
	})
	if err != nil {
		// A failed event insert must not block playback; proceed without an event id.
		logger.Error("emby stream: start stream event", "request_id", reqID, "err", err)
	}

	// The stream lease is accepted (quota passed, StartStream attempted): mark it on
	// the active-sessions gauge and release it on every GET exit (success, mid-stream
	// break, or all-copies-failed). HEAD never reaches here. m is nil-safe.
	m.PlaybackSessionStarted()
	defer m.PlaybackSessionEnded()

	// Try each copy. Headers are written ONLY on the first success; once written we
	// never fall back (the Go ResponseWriter contract forbids changing the status).
	for i := range copies {
		row := copies[i]
		res, serr := sidecar.Stream(ctx, sidecarclient.StreamRequest{
			StorageMount: row.StorageMount,
			RemotePath:   row.RemotePath,
			Headers:      restore.ForwardHeaders(r.Header),
		})
		if serr != nil {
			var typed *sidecarclient.SidecarTypedError
			if errors.As(serr, &typed) {
				if aerr := failures.ApplyCopyFailure(ctx, row.ID, row.AccountID, typed, reqID); aerr != nil {
					logger.Error("emby stream: apply copy failure", "request_id", reqID, "copy_id", row.ID, "err", aerr)
				}
			}
			logger.Warn("emby stream: copy failed", "request_id", reqID, "copy_id", row.ID, "provider", row.Provider)
			continue
		}

		// Success: commit headers, then stream the body. No fallback past this point.
		dst := w.Header()
		for key, values := range res.Header {
			for _, value := range values {
				dst.Add(key, value)
			}
		}
		dst.Set("Referrer-Policy", "no-referrer")
		w.WriteHeader(res.StatusCode)
		cw := &countingWriter{w: w}
		streamResult := "ok"
		if res.Body != nil {
			_, copyErr := io.Copy(cw, res.Body)
			_ = res.Body.Close()
			if copyErr != nil {
				// Mid-stream break: headers already sent, so just record and stop.
				streamResult = "interrupted"
				logger.Warn("emby stream: copy interrupted", "request_id", reqID, "copy_id", row.ID, "err", copyErr)
			}
		}
		finishStream(ctx, quota, eventID, playback.FinishStreamInput{
			Status:     "ok",
			BytesSent:  cw.n,
			HTTPStatus: int64(res.StatusCode),
			FinishedAt: time.Now().Unix(),
		}, logger, reqID)
		// Record bytes streamed by provider + safe outcome enum. n<=0 is a no-op,
		// so a zero-byte success simply does not increment the byte counter.
		m.PlaybackStreamBytes(row.Provider, streamResult, cw.n)
		return
	}

	// Every copy failed without writing headers: record the terminal failure and fail
	// closed with a transient 503 (the player retries; this is never a permanent 404).
	finishStream(ctx, quota, eventID, playback.FinishStreamInput{
		Status:      "failed",
		BytesSent:   0,
		HTTPStatus:  http.StatusServiceUnavailable,
		FinishedAt:  time.Now().Unix(),
		FailureKind: ptr("no_live_copy"),
	}, logger, reqID)
	writeStreamReason(w, http.StatusServiceUnavailable, "temporary_unavailable")
}

func handleStreamHead(w http.ResponseWriter, r *http.Request, mgr *SessionManager, resolver *playback.Resolver, quota *playback.Quota, logger *slog.Logger) {
	ctx := r.Context()
	reqID := chimw.GetReqID(ctx)
	token := chi.URLParam(r, "token")

	session, err := mgr.LookupPlaybackSession(ctx, token)
	if err != nil {
		writeStreamReason(w, http.StatusNotFound, "temporary_unavailable")
		return
	}
	copies, err := resolver.ResolveCopies(ctx, session.EchoUserID, session.LibraryEntryID.Int64, nullToString(session.PreferProvider), 5)
	switch {
	case errors.Is(err, playback.ErrUnauthorized):
		writeStreamReason(w, http.StatusForbidden, "unauthorized")
		return
	case errors.Is(err, playback.ErrEntryMissing):
		writeStreamReason(w, http.StatusNotFound, "temporary_unavailable")
		return
	case err != nil:
		logger.Error("emby stream: resolve copies for probe", "request_id", reqID, "err", err)
		writeStreamReason(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if len(copies) == 0 {
		writeStreamReason(w, http.StatusServiceUnavailable, "temporary_unavailable")
		return
	}
	if err := quota.CheckStreamAllowed(ctx, session.EchoUserID); errors.Is(err, playback.ErrQuotaExceeded) {
		writeStreamReason(w, http.StatusTooManyRequests, "quota_exceeded")
		return
	}
	// Record a zero-byte probe so a HEAD shows up in the audit trail without ever
	// reaching the sidecar byte path.
	id, serr := quota.StartStream(ctx, playback.StartStreamInput{
		RequestID:  reqID,
		EchoUserID: session.EchoUserID,
		Operation:  "stream_probe",
		StartedAt:  time.Now().Unix(),
		SessionID:  &session.ID,
	})
	if serr != nil {
		logger.Error("emby stream: start probe event", "request_id", reqID, "err", serr)
	}
	finishStream(ctx, quota, id, playback.FinishStreamInput{
		Status:     "ok",
		BytesSent:  0,
		HTTPStatus: http.StatusOK,
		FinishedAt: time.Now().Unix(),
	}, logger, reqID)
	w.WriteHeader(http.StatusOK)
}

// finishStream writes the terminal event state, logging (but not surfacing) any error.
func finishStream(ctx context.Context, quota *playback.Quota, eventID int64, in playback.FinishStreamInput, logger *slog.Logger, reqID string) {
	if err := quota.FinishStream(ctx, eventID, in); err != nil {
		logger.Error("emby stream: finish stream event", "request_id", reqID, "err", err)
	}
}

// writeStreamReason emits the standard JSON error envelope plus the X-Echo-Reason
// header. reason is always a safe enum, never a raw error string.
func writeStreamReason(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Echo-Reason", reason)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
}

// countingWriter wraps an http.ResponseWriter to count the bytes streamed to the
// client (for the playback_events bytes_sent column) while forwarding Header,
// WriteHeader, and Write to the underlying writer unchanged.
type countingWriter struct {
	w http.ResponseWriter
	n int64
}

func (c *countingWriter) Header() http.Header { return c.w.Header() }

func (c *countingWriter) WriteHeader(status int) { c.w.WriteHeader(status) }

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// nullToString returns the inner string of a sql.NullString, or "" when NULL.
func nullToString(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

// ptr returns a pointer to v.
func ptr(v string) *string { return &v }

// ptrIfNotEmpty returns a pointer to v, or nil when v is empty (mapping "" to a SQL
// NULL range_header rather than an empty string).
func ptrIfNotEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
