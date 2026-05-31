package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/http/handlers"
	authmw "github.com/xmm2022/echo/internal/http/middleware"
	"github.com/xmm2022/echo/internal/web"
)

// controlPlaneTimeout bounds JSON API and dashboard requests. It is deliberately
// NOT applied to /api/stream: media proxying runs far longer than any control
// request and is bounded instead by the sidecar stream timeout + server write
// timeout.
const controlPlaneTimeout = 60 * time.Second

// New builds the HTTP server from config and the wired dependencies.
func New(cfg *config.Config, deps Deps) *http.Server {
	return &http.Server{
		Addr:         cfg.Server.Bind,
		Handler:      HandlerWithDeps(deps),
		ReadTimeout:  cfg.Server.ReadTimeout.Duration,
		WriteTimeout: cfg.Server.WriteTimeout.Duration,
	}
}

// Deps configures the HTTP handler. Restore, Stream, API, Bootstrap, and Web are
// optional; when nil their routes are not mounted. AuthCheck guards every API and
// UI route when wired; AdminToken remains the static-token fallback for tests and
// compatibility (an empty token fails closed — see middleware.Auth).
type Deps struct {
	Logger     *slog.Logger
	AdminToken string
	AuthCheck  authmw.CheckFunc
	Restore    *handlers.RestoreDeps
	Stream     *handlers.StreamDeps
	API        *handlers.APIDeps
	Bootstrap  *handlers.APIDeps
	Web        *web.Deps
	// Ready backs /readyz with real DB + sidecar probes. When nil, /readyz
	// reports not_ready (503) — the pre-wiring default used in tests.
	Ready *ReadyChecker
	// Registry serves /metrics. When nil the default Prometheus registry is used.
	Registry *prometheus.Registry
}

// Handler builds a minimal handler exposing only the operational endpoints. Used
// in tests and when no business dependencies are wired.
func Handler(logger *slog.Logger) http.Handler {
	return HandlerWithDeps(Deps{Logger: logger})
}

// HandlerWithDeps builds the router. /healthz, /readyz, /metrics and the data-free
// dashboard shell (/, /static) are public; every data or action route is mounted
// inside an authenticated group so none escapes the admin token (spec §6 / plan
// §10 acceptance).
func HandlerWithDeps(deps Deps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)

	// Operational endpoints: always public (liveness/readiness probes, scrape).
	r.Get("/healthz", healthz)
	if deps.Ready != nil {
		r.Get("/readyz", deps.Ready.handler())
	} else {
		r.Get("/readyz", readyz)
	}
	if deps.Registry != nil {
		r.Handle("/metrics", promhttp.HandlerFor(deps.Registry, promhttp.HandlerOpts{}))
	} else {
		r.Handle("/metrics", promhttp.Handler())
	}

	// Public, data-free dashboard shell + vendored assets (browser auth happens
	// client-side: the admin pastes the token, app.js attaches it to /ui + /api).
	if deps.Web != nil {
		r.Get("/", deps.Web.Index)
		r.Handle("/static/*", web.Static())
	}

	if deps.Bootstrap != nil {
		deps.Bootstrap.MountBootstrap(r)
	}

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		if deps.AuthCheck != nil {
			r.Use(authmw.AuthFunc(deps.AuthCheck))
		} else {
			r.Use(authmw.Auth(deps.AdminToken))
		}

		// Restore/stream carry no request timeout: a stream proxies bytes for as
		// long as the client reads.
		if deps.Restore != nil {
			r.Get("/api/restore/{file_id}", handlers.Restore(*deps.Restore))
		}
		if deps.Stream != nil {
			r.Get("/api/stream/{file_id}", handlers.Stream(*deps.Stream))
		}

		// Control-plane JSON + dashboard fragments: bounded by a request timeout.
		r.Group(func(r chi.Router) {
			r.Use(chimw.Timeout(controlPlaneTimeout))
			if deps.API != nil {
				deps.API.Mount(r)
			}
			if deps.Web != nil {
				deps.Web.MountUI(r)
			}
		})
	})

	return requestLogger(logger, r)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is a stub in v0.1 Phase 9; the real DB + sidecar version checks land with
// the monitoring work (Phase 10).
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
			logger.Debug("http request", "method", r.Method, "path", r.URL.Path, "request_id", chimw.GetReqID(r.Context()))
		}
	})
}
