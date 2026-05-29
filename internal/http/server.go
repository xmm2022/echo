package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/http/handlers"
)

func New(cfg *config.Config, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:         cfg.Server.Bind,
		Handler:      Handler(logger),
		ReadTimeout:  cfg.Server.ReadTimeout.Duration,
		WriteTimeout: cfg.Server.WriteTimeout.Duration,
	}
}

// Deps configures the HTTP handler. Restore and Stream are optional; when nil
// their routes are not mounted. cmd/echo supplies them once the sidecar client
// and store are wired (Phase 9).
type Deps struct {
	Logger  *slog.Logger
	Restore *handlers.RestoreDeps
	Stream  *handlers.StreamDeps
}

func Handler(logger *slog.Logger) http.Handler {
	return HandlerWithDeps(Deps{Logger: logger})
}

// HandlerWithDeps builds the router, mounting the restore and stream routes only
// when their dependencies are provided.
func HandlerWithDeps(deps Deps) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz)
	r.Handle("/metrics", promhttp.Handler())
	if deps.Restore != nil {
		r.Get("/api/restore/{file_id}", handlers.Restore(*deps.Restore))
	}
	if deps.Stream != nil {
		r.Get("/api/stream/{file_id}", handlers.Stream(*deps.Stream))
	}
	return requestLogger(deps.Logger, r)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"status": "not_ready",
		"checks": map[string]string{
			"sidecar": "not_connected",
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.URL.Path != "/metrics" {
			logger.Debug("http request", "method", r.Method, "path", r.URL.Path)
		}
	})
}
