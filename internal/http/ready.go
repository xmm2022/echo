package httpserver

import (
	"context"
	"net/http"
	"time"
)

// defaultReadyTimeout bounds the readiness probes so /readyz cannot hang on a
// slow database or sidecar.
const defaultReadyTimeout = 5 * time.Second

// Pinger is the database readiness probe; *sql.DB satisfies it.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// SidecarHealth is the sidecar readiness surface: reachability (Ping) plus
// version compatibility (Version returns ErrSidecarVersionTooOld on mismatch).
type SidecarHealth interface {
	Ping(ctx context.Context) error
	Version(ctx context.Context) (string, error)
}

// ReadyChecker backs GET /readyz with real probes (spec §8): DB ping, sidecar
// ping, and sidecar version compatibility. The server reports the running Echo
// version alongside the per-check results.
type ReadyChecker struct {
	DB      Pinger
	Sidecar SidecarHealth
	Version string
	Timeout time.Duration
}

func (rc *ReadyChecker) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		timeout := rc.Timeout
		if timeout <= 0 {
			timeout = defaultReadyTimeout
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		checks := make(map[string]string, 3)
		ready := true

		if err := rc.DB.PingContext(ctx); err != nil {
			checks["db"] = err.Error()
			ready = false
		} else {
			checks["db"] = "ok"
		}

		if err := rc.Sidecar.Ping(ctx); err != nil {
			checks["sidecar"] = err.Error()
			ready = false
		} else {
			checks["sidecar"] = "ok"
		}

		if version, err := rc.Sidecar.Version(ctx); err != nil {
			checks["sidecar_version"] = err.Error()
			ready = false
		} else {
			checks["sidecar_version"] = version
		}

		status := "ok"
		code := http.StatusOK
		if !ready {
			status = "not_ready"
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{
			"status":  status,
			"version": rc.Version,
			"checks":  checks,
		})
	}
}
