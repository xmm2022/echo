package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/xmm2022/echo/internal/config"
)

func New(cfg *config.Config, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:         cfg.Server.Bind,
		Handler:      Handler(logger),
		ReadTimeout:  cfg.Server.ReadTimeout.Duration,
		WriteTimeout: cfg.Server.WriteTimeout.Duration,
	}
}

func Handler(logger *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz)
	r.Handle("/metrics", promhttp.Handler())
	return requestLogger(logger, r)
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
