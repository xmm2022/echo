package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
)

func TestBootstrapCreatesAdminToken(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Now: apiClock(), Logger: apiLogger(), BootstrapAdminToken: "boot"}
	register := func(r chi.Router) { deps.MountBootstrap(r) }

	if rec := doReq(t, http.MethodPost, "/api/bootstrap/admin-token", `{"name":"cli"}`, register); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without bootstrap header = %d, want 401", rec.Code)
	}

	r := chi.NewRouter()
	register(r)
	req := httptest.NewRequest(http.MethodPost, "/api/bootstrap/admin-token", strings.NewReader(`{"name":"cli"}`))
	req.Header.Set("Authorization", "Bearer boot")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	decodeBody(t, rec, &body)
	if body.ID == "" || body.Token == "" {
		t.Fatalf("id/token must be non-empty: %#v", body)
	}
	if strings.Contains(rec.Body.String(), auth.HashToken(body.Token)) {
		t.Fatal("response leaked stored token hash")
	}
}
