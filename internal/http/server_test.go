package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/store"
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
		Logger: discardLogger(),
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

	// A malformed file_id reaches the handler and yields 400, proving the route
	// is mounted (an unmounted route would 404 at the router).
	for _, path := range []string{"/api/restore/abc", "/api/stream/abc"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400 (route mounted, handler reached)", path, rec.Code)
		}
	}
}
