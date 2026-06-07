package httpserver

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/embyproxy"
	"github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/media"
	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
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

func TestControlPlaneCSRFSkipsBearerAndRejectsSessionMutations(t *testing.T) {
	st := openServerTestStore(t)
	bearerConflict := seedServerHashConflict(t, st, "bearer")
	sessionConflict := seedServerHashConflict(t, st, "session")
	authCheck := func(r *http.Request) (auth.UserContext, bool) {
		if auth.BearerToken(r.Header.Get("Authorization")) == "api" {
			return auth.UserContext{
				UserID:           "admin",
				Role:             "admin",
				Scopes:           []string{"admin"},
				CredentialSource: auth.CredentialBearer,
			}, true
		}
		if r.Header.Get("X-Test-Session") == "1" {
			return auth.UserContext{
				UserID:           "admin",
				Role:             "admin",
				Scopes:           []string{"admin"},
				CredentialSource: auth.CredentialSession,
				SessionSelector:  "session-selector",
				CSRFHash:         auth.HashToken("csrf"),
			}, true
		}
		return auth.UserContext{}, false
	}
	deps := Deps{
		Logger:    discardLogger(),
		AuthCheck: authCheck,
		API:       &handlers.APIDeps{Store: st, Logger: discardLogger()},
	}
	h := HandlerWithDeps(deps)

	req := httptest.NewRequest(http.MethodPost, "/api/conflicts/"+strconv.FormatInt(bearerConflict.ID, 10)+"/dismiss", nil)
	req.Header.Set("Authorization", "Bearer api")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer POST without CSRF status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/conflicts/"+strconv.FormatInt(sessionConflict.ID, 10)+"/dismiss", nil)
	req.Header.Set("X-Test-Session", "1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("session POST without CSRF status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "csrf-token-invalid") {
		t.Fatalf("session POST without CSRF body=%q, want csrf-token-invalid", rec.Body.String())
	}
	got, err := st.GetHashConflict(context.Background(), queries.GetHashConflictParams{ID: sessionConflict.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "open" {
		t.Fatalf("session conflict status = %q, want open after CSRF rejection", got.Status)
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
		"/login":              http.StatusOK,
		"/app":                http.StatusOK,
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

func TestSessionRoutesAreMountedForBrowserAuth(t *testing.T) {
	st := openServerTestStore(t)
	deps := Deps{
		Logger:  discardLogger(),
		API:     &handlers.APIDeps{Store: st, Logger: discardLogger()},
		Session: handlers.SessionHTTPConfig{CookieName: "echo_session", TTL: time.Hour, SecureCookies: "never"},
	}
	h := HandlerWithDeps(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/session/login", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("/api/session/login without auth status=%d body=%s, want 400 from public handler", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/session/me", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/session/me without auth status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"authenticated":false`) {
		t.Fatalf("/api/session/me body=%q, want unauthenticated response", rec.Body.String())
	}
}

func TestAppRoutesUsePublicShellAndAuthenticatedDiscoveryFragments(t *testing.T) {
	st := openServerTestStore(t)
	seedServerAppUser(t, st, "u1")
	policy := seedServerAppPolicy(t, st, "u1")
	targetDeps := seedServerAppTargetDeps(t, st)
	seedServerAppTarget(t, st, targetDeps, policy.ID)

	authCheck := func(r *http.Request) (auth.UserContext, bool) {
		switch auth.BearerToken(r.Header.Get("Authorization")) {
		case "discover":
			return auth.UserContext{UserID: "u1", Role: "user", Scopes: []string{"discovery"}, Now: time.Unix(1000, 0)}, true
		case "read":
			return auth.UserContext{UserID: "u1", Role: "user", Scopes: []string{"read"}, Now: time.Unix(1000, 0)}, true
		default:
			return auth.UserContext{}, false
		}
	}
	deps := Deps{
		Logger:    discardLogger(),
		AuthCheck: authCheck,
		API:       &handlers.APIDeps{Store: st, Media: &media.Service{Store: st, Now: serverAppClock}, Logger: discardLogger(), Now: serverAppClock},
		Web:       &web.Deps{Store: st, Media: &media.Service{Store: st, Now: serverAppClock}, Logger: discardLogger()},
	}
	h := HandlerWithDeps(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/app without token status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("/app Cache-Control=%q, want no-store", rec.Header().Get("Cache-Control"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/discover", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/app/discover without token status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app/discover", nil)
	req.Header.Set("Authorization", "Bearer discover")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/app/discover discovery token status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("/app/discover Cache-Control=%q, want no-store", rec.Header().Get("Cache-Control"))
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/app/discover", nil)
	req.Header.Set("Authorization", "Bearer read")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/app/discover read token status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/ui/jobs", nil)
	req.Header.Set("Authorization", "Bearer discover")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/ui/jobs discovery token status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/discovery/subscriptions", nil)
	req.Header.Set("Authorization", "Bearer discover")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/api/discovery/subscriptions discovery token status=%d body=%s, want 403", rec.Code, rec.Body.String())
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

func seedServerAppUser(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if err := st.CreateUser(context.Background(), queries.CreateUserParams{
		ID:            id,
		Username:      id,
		Role:          "user",
		Status:        "active",
		QuotaPolicyID: 1,
		CreatedAt:     1000,
		UpdatedAt:     1000,
	}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
}

func seedServerHashConflict(t *testing.T, st *store.Store, reason string) queries.HashConflict {
	t.Helper()
	ctx := context.Background()
	a, err := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 1, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 2, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.InsertHashConflict(ctx, queries.InsertHashConflictParams{
		BlobIDA: a.ID, BlobIDB: b.ID, Reason: reason, Detail: `{"k":"v"}`, ObservedAt: 5, Status: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func seedServerAppPolicy(t *testing.T, st *store.Store, userID string) queries.DiscoveryAccessPolicy {
	t.Helper()
	policy, err := st.CreateDiscoveryAccessPolicy(context.Background(), queries.CreateDiscoveryAccessPolicyParams{
		Name:          "server app policy",
		Enabled:       1,
		Priority:      100,
		SubjectUserID: serverAppNullString(userID),
		RequestMode:   "approval_required",
		CanSearch:     1,
		CreatedBy:     serverAppNullString("admin"),
		CreatedAt:     1000,
		UpdatedAt:     1000,
	})
	if err != nil {
		t.Fatalf("create app policy: %v", err)
	}
	return policy
}

type serverAppTargetDeps struct {
	libraryID         int64
	producerProfileID int64
	ruleProfileID     int64
}

func seedServerAppTargetDeps(t *testing.T, st *store.Store) serverAppTargetDeps {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           "server-app-acc-115",
		Provider:     "115",
		SidecarID:    "sidecar-1",
		StorageMount: "/115",
		Status:       "active",
		OwnerID:      "admin",
		CreatedAt:    1000,
		UpdatedAt:    1000,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	library, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "Server App Media",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/echo-server-app-test",
		OwnerID:        "admin",
		CreatedAt:      1000,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	profile, err := st.CreateDiscoveryProducerProfile(ctx, queries.CreateDiscoveryProducerProfileParams{
		Name:                   "server app 115",
		Provider:               "115",
		Tool:                   "115share2cas",
		TargetAccount:          "server-app-acc-115",
		TargetSubdirTemplate:   "{{.Title}}",
		LibraryRelPathTemplate: "{{.Title}}",
		DefaultArgsJson:        `{}`,
		Enabled:                1,
		CreatedAt:              1000,
		UpdatedAt:              1000,
	})
	if err != nil {
		t.Fatalf("create producer profile: %v", err)
	}
	ruleProfile, err := st.CreateRuleProfile(ctx, queries.CreateRuleProfileParams{
		Name:      "server app rules",
		Version:   1,
		RulesJson: `{}`,
		Enabled:   1,
		CreatedAt: 1000,
	})
	if err != nil {
		t.Fatalf("create rule profile: %v", err)
	}
	return serverAppTargetDeps{libraryID: library.ID, producerProfileID: profile.ID, ruleProfileID: ruleProfile.ID}
}

func seedServerAppTarget(t *testing.T, st *store.Store, deps serverAppTargetDeps, policyID int64) {
	t.Helper()
	if _, err := st.CreateDiscoveryPolicyTarget(context.Background(), queries.CreateDiscoveryPolicyTargetParams{
		PolicyID:                policyID,
		Label:                   "Server App Target",
		LibraryID:               deps.libraryID,
		ProducerProfileID:       deps.producerProfileID,
		RuleProfileID:           deps.ruleProfileID,
		PipelineOwnerID:         "admin",
		MediaType:               sql.NullString{},
		MatchMode:               "admin_review",
		GrantPlaybackOnApproval: 1,
		Enabled:                 1,
		DefaultTarget:           1,
		CreatedAt:               1000,
		UpdatedAt:               1000,
	}); err != nil {
		t.Fatalf("create policy target: %v", err)
	}
}

func serverAppNullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func serverAppClock() time.Time {
	return time.Unix(1000, 0)
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
