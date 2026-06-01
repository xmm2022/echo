package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func v02Deps(st *store.Store) APIDeps {
	return APIDeps{Store: st, Logger: apiLogger(), Now: apiClock()}
}

func createUserRow(t *testing.T, st *store.Store, id, role string) {
	t.Helper()
	if err := st.CreateUser(context.Background(), queries.CreateUserParams{
		ID: id, Username: id, Role: role, Status: "active", QuotaPolicyID: 1,
		CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
}

func insertPlaybackEventRow(t *testing.T, ctx context.Context, st *store.Store, userID, status string, bytesSent int64) {
	t.Helper()
	if _, err := st.InsertPlaybackEvent(ctx, queries.InsertPlaybackEventParams{
		RequestID:  "req-" + userID,
		EchoUserID: sql.NullString{String: userID, Valid: true},
		Operation:  "stream",
		Status:     status,
		BytesSent:  bytesSent,
		StartedAt:  1000,
	}); err != nil {
		t.Fatalf("insert playback event for %s: %v", userID, err)
	}
}

func doReqAs(t *testing.T, method, target, body string, user auth.UserContext, register func(chi.Router)) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	register(r)
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(auth.NewContext(req.Context(), user))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAdminCanManageEmbyServerUserLinkAndMapping(t *testing.T) {
	st := newAPIStore(t)
	deps := v02Deps(st)
	admin := auth.UserContext{UserID: "admin", Role: "admin", Scopes: []string{"admin"}}
	reg := func(r chi.Router) { deps.MountV02Management(r) }

	serverBody := `{"id":"default","name":"Main Emby","base_url":"http://emby:8096","api_key_ref":"env:EMBY_API_KEY","public_base_url":"https://echo.example.com","proxy_prefix":"/emby","enabled":true}`
	rec := doReqAs(t, http.MethodPost, "/api/emby/servers", serverBody, admin, reg)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "EMBY_API_KEY") {
		t.Fatal("response leaked api_key_ref")
	}

	linkBody := `{"emby_server_id":"default","emby_user_id":"emby-u1","echo_user_id":"admin","enabled":true}`
	rec = doReqAs(t, http.MethodPost, "/api/emby/user-links", linkBody, admin, reg)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user link status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}

	lib := createLibraryRow(t, st)
	mappingBody := fmt.Sprintf(`{"emby_server_id":"default","emby_path_prefix":"/mnt/media","library_id":%d,"echo_rel_prefix":"","enabled":true}`, lib.ID)
	rec = doReqAs(t, http.MethodPost, "/api/emby/library-mappings", mappingBody, admin, reg)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create mapping status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}

	rec = doReqAs(t, http.MethodGet, "/api/emby/servers", "", admin, reg)
	if rec.Code != http.StatusOK {
		t.Fatalf("list servers status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"default"`) {
		t.Fatalf("server list body=%s, want default", rec.Body.String())
	}
}

func TestUserCannotManageEmbyAdminResources(t *testing.T) {
	st := newAPIStore(t)
	deps := v02Deps(st)
	user := auth.UserContext{UserID: "user1", Role: "user", Scopes: []string{"read"}}
	rec := doReqAs(t, http.MethodPost, "/api/emby/servers", `{}`, user, func(r chi.Router) {
		deps.MountV02Management(r)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

// TestUpsertEmbyServerRouteIDEnforcement covers the string-PK PATCH-by-id contract:
// PATCH /api/emby/servers/{id} succeeds when the route id matches the body id and is
// rejected with 400 when they differ.
func TestUpsertEmbyServerRouteIDEnforcement(t *testing.T) {
	st := newAPIStore(t)
	deps := v02Deps(st)
	admin := auth.UserContext{UserID: "admin", Role: "admin", Scopes: []string{"admin"}}
	reg := func(r chi.Router) { deps.MountV02Management(r) }

	body := `{"id":"srvA","name":"Srv A","base_url":"http://emby:8096","public_base_url":"https://echo.example.com","enabled":true}`
	rec := doReqAs(t, http.MethodPatch, "/api/emby/servers/srvA", body, admin, reg)
	if rec.Code != http.StatusCreated {
		t.Fatalf("matching route id status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}

	rec = doReqAs(t, http.MethodPatch, "/api/emby/servers/srvB", body, admin, reg)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched route id status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}
