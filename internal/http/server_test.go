package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/xmm2022/echo/internal/embyproxy"
	"github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/web"
)

func TestHealthzAndReadyz(t *testing.T) {
	handler := Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	tests := []struct {
		path string
		want int
	}{
		{path: "/healthz", want: http.StatusOK},
		{path: "/readyz", want: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != tt.want {
			t.Fatalf("%s status = %d, want %d", tt.path, rec.Code, tt.want)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("%s Content-Type = %q, want application/json", tt.path, got)
		}
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openServerTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "echo.db"))
	st, err := store.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestHandlerWithoutDepsHasNoRestoreRoutes(t *testing.T) {
	h := Handler(discardLogger())
	for _, path := range []string{"/api/restore/1", "/api/stream/1"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404 (route not mounted)", path, rec.Code)
		}
	}
}

func TestHandlerWithDepsMountsRestoreAndStream(t *testing.T) {
	st := openServerTestStore(t)
	deps := Deps{
		Logger:     discardLogger(),
		AdminToken: "tok",
		Restore: &handlers.RestoreDeps{
			Resolver: restore.NewResolver(st.Queries, nil),
			Cache:    restore.NewLinkCache(nil),
			Logger:   discardLogger(),
		},
		Stream: &handlers.StreamDeps{
			Resolver: restore.NewResolver(st.Queries, nil),
			Logger:   discardLogger(),
		},
	}
	h := HandlerWithDeps(deps)

	// With the bearer token, a malformed file_id reaches the handler and yields
	// 400, proving the route is mounted behind auth (an unmounted route 404s).
	for _, path := range []string{"/api/restore/abc", "/api/stream/abc"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400 (route mounted, handler reached)", path, rec.Code)
		}
	}

	// Without the token, the same routes are rejected by auth before the handler.
	for _, path := range []string{"/api/restore/abc", "/api/stream/abc"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token = %d, want 401", path, rec.Code)
		}
	}
}

func TestAuthedRoutesRequireToken(t *testing.T) {
	st := openServerTestStore(t)
	deps := Deps{
		Logger:     discardLogger(),
		AdminToken: "sekret",
		API:        &handlers.APIDeps{Store: st, Logger: discardLogger()},
		Web:        &web.Deps{Store: st, Logger: discardLogger()},
	}
	h := HandlerWithDeps(deps)

	for _, path := range []string{
		"/api/accounts",
		"/api/jobs",
		"/api/conflicts",
		"/api/discovery/subscriptions",
		"/ui/jobs",
		"/ui/conflicts",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token = %d, want 401", path, rec.Code)
		}
	}

	for _, path := range []string{
		"/api/accounts",
		"/api/jobs",
		"/api/conflicts",
		"/api/discovery/subscriptions",
		"/ui/jobs",
		"/ui/conflicts",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer sekret")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s with token = %d, want 200", path, rec.Code)
		}
	}
}

func TestPublicRoutesAreOpen(t *testing.T) {
	st := openServerTestStore(t)
	deps := Deps{
		Logger:     discardLogger(),
		AdminToken: "sekret",
		Web:        &web.Deps{Store: st, Logger: discardLogger()},
	}
	h := HandlerWithDeps(deps)

	cases := map[string]int{
		"/healthz":            http.StatusOK,
		"/readyz":             http.StatusServiceUnavailable,
		"/":                   http.StatusOK,
		"/static/htmx.min.js": http.StatusOK,
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		// No Authorization header: public routes must still respond.
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Fatalf("%s = %d, want %d (public, no auth)", path, rec.Code, want)
		}
	}
}

func TestEmbyReservedRoutesDoNotFallThroughToProxy(t *testing.T) {
	deps := Deps{
		Logger: discardLogger(),
		Emby: &embyproxy.Deps{
			ProxyPrefix: "/emby",
			Stream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Test-Route", "stream")
				w.WriteHeader(http.StatusTeapot)
			}),
			Error: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Test-Route", "error")
				w.WriteHeader(http.StatusTooManyRequests)
			}),
			Upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("reserved route fell through to upstream: %s", r.URL.Path)
			}),
		},
	}
	h := HandlerWithDeps(deps)

	for path, wantRoute := range map[string]string{
		"/emby/stream/selector.secret": "stream",
		"/emby/error/selector.secret":  "error",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Header().Get("X-Test-Route") != wantRoute {
			t.Fatalf("%s route = %q, want %q", path, rec.Header().Get("X-Test-Route"), wantRoute)
		}
	}
}

func TestEmbyReservedMalformedRoutesDoNotFallThroughToProxy(t *testing.T) {
	deps := Deps{
		Logger: discardLogger(),
		Emby: &embyproxy.Deps{
			ProxyPrefix: "/emby",
			Upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("malformed reserved route fell through to upstream: %s", r.URL.Path)
			}),
		},
	}
	h := HandlerWithDeps(deps)

	for _, path := range []string{"/emby/stream", "/emby/error", "/emby/stream/foo/bar", "/emby/error/foo/bar"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400 or 404 from Echo reserved handler", path, rec.Code)
		}
	}
}

func TestPhase3PlaybackInfoFailsClosedBeforeRewrite(t *testing.T) {
	deps := Deps{
		Logger: discardLogger(),
		Emby: &embyproxy.Deps{
			ProxyPrefix: "/emby",
			Upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("PlaybackInfo fell through to upstream before rewrite: %s", r.URL.Path)
			}),
		},
	}
	h := HandlerWithDeps(deps)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/emby/Items/item1/PlaybackInfo?UserId=u1", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s PlaybackInfo status = %d, want 503 fail closed", method, rec.Code)
		}
		if got := rec.Header().Get("X-Echo-Reason"); got != "temporary_unavailable" {
			t.Fatalf("X-Echo-Reason = %q, want temporary_unavailable", got)
		}
	}
}
