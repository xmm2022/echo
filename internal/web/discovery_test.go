package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
)

func TestDiscoveryUIRequiresAdmin(t *testing.T) {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.NewContext(req.Context(), auth.UserContext{UserID: "u1", Scopes: []string{"read"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	Deps{}.MountUI(r)
	req := httptest.NewRequest(http.MethodGet, "/ui/discovery/subscriptions", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}
