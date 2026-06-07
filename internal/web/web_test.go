package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func newWebStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("file:" + t.TempDir() + "/echo.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestIndexServesSessionAwareAdminShell(t *testing.T) {
	d := Deps{Store: newWebStore(t)}
	rec := httptest.NewRecorder()
	d.Index(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login?next=/" {
		t.Fatalf("unauthenticated Location = %q, want /login?next=/", got)
	}

	rec = httptest.NewRecorder()
	d.Index(rec, webRequestWithUser(http.MethodGet, "/", adminSessionUser()))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"Echo Admin", "/static/htmx.min.js", "/static/app.js", `hx-get="/ui/jobs"`, `hx-get="/ui/conflicts"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}
	for _, bad := range []string{`id="admin-token"`, "echo_admin_token", "paste ADMIN_TOKEN"} {
		if strings.Contains(body, bad) {
			t.Fatalf("dashboard rendered token UI %q: %s", bad, body)
		}
	}
}

func TestBrowserShellsRedirectWhenUnauthenticated(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		serve    func(Deps, http.ResponseWriter, *http.Request)
		location string
	}{
		{name: "admin", path: "/", serve: Deps.Index, location: "/login?next=/"},
		{name: "app", path: "/app", serve: Deps.App, location: "/login?next=/app"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.serve(Deps{}, rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("%s status = %d, want 303", tc.path, rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tc.location {
				t.Fatalf("%s Location = %q, want %q", tc.path, got, tc.location)
			}
		})
	}
}

func TestAdminShellDoesNotRenderTokenBox(t *testing.T) {
	rec := httptest.NewRecorder()
	Deps{}.Index(rec, webRequestWithUser(http.MethodGet, "/", adminSessionUser()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, bad := range []string{`id="admin-token"`, "echo_admin_token", "paste ADMIN_TOKEN"} {
		if strings.Contains(body, bad) {
			t.Fatalf("admin shell rendered token UI %q: %s", bad, body)
		}
	}
}

func TestLoginRendersFormWithSafeNext(t *testing.T) {
	rec := httptest.NewRecorder()
	Deps{}.Login(rec, httptest.NewRequest(http.MethodGet, "/login?next=/app", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="login-form"`,
		`method="post"`,
		`action="/api/session/login"`,
		`data-next="/app"`,
		`id="login-username"`,
		`name="username"`,
		`autocomplete="username"`,
		`id="login-password"`,
		`name="password"`,
		`autocomplete="current-password"`,
		`required`,
		`id="login-error"`,
		`/static/app.js`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("login form missing %q in body=%s", want, body)
		}
	}
}

func TestLoginRedirectsAuthenticatedUsers(t *testing.T) {
	cases := []struct {
		name     string
		user     auth.UserContext
		location string
	}{
		{name: "admin", user: adminSessionUser(), location: "/"},
		{name: "ordinary", user: auth.UserContext{UserID: "u1", Role: "user", Scopes: []string{"discovery"}, CredentialSource: auth.CredentialSession}, location: "/app"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Deps{}.Login(rec, webRequestWithUser(http.MethodGet, "/login?next=/app", tc.user))
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tc.location {
				t.Fatalf("Location = %q, want %q", got, tc.location)
			}
		})
	}
}

func TestLoginDoesNotReflectUnsafeNext(t *testing.T) {
	for _, path := range []string{"/login?next=//evil.example/app", "/login?next=/%5Cevil"} {
		rec := httptest.NewRecorder()
		Deps{}.Login(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		body := rec.Body.String()
		for _, bad := range []string{"//evil.example", "\\evil", `data-next="//evil.example/app"`, `data-next="/\evil"`} {
			if strings.Contains(body, bad) {
				t.Fatalf("%s reflected unsafe next %q: %s", path, bad, body)
			}
		}
	}
}

func TestStaticServesVendoredHtmx(t *testing.T) {
	rec := httptest.NewRecorder()
	Static().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "htmx") {
		t.Fatal("htmx.min.js body does not look like htmx")
	}
}

func TestUIJobsRendersTable(t *testing.T) {
	st := newWebStore(t)
	if _, err := st.CreateJob(context.Background(), queries.CreateJobParams{
		Kind: "ingest_manual", Status: "running", Payload: "{}", OwnerID: "admin", CreatedAt: 7,
	}); err != nil {
		t.Fatal(err)
	}
	d := Deps{Store: st}
	rec := httptest.NewRecorder()
	d.UIJobs(rec, httptest.NewRequest(http.MethodGet, "/ui/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ingest_manual") || !strings.Contains(body, "running") {
		t.Fatalf("jobs fragment missing job data: %s", body)
	}
}

func TestUIConflictsRendersOpenOnly(t *testing.T) {
	ctx := context.Background()
	st := newWebStore(t)
	a, _ := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 1, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	b, _ := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 2, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	conflict, err := st.InsertHashConflict(ctx, queries.InsertHashConflictParams{
		BlobIDA: a.ID, BlobIDB: b.ID, Reason: "hash_multi_blob", Detail: "{}", ObservedAt: 1, Status: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{Store: st}
	rec := httptest.NewRecorder()
	d.UIConflicts(rec, httptest.NewRequest(http.MethodGet, "/ui/conflicts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hash_multi_blob") {
		t.Fatalf("conflicts fragment missing conflict: %s", body)
	}
	dismissPath := "/api/conflicts/" + strconv.FormatInt(conflict.ID, 10) + "/dismiss"
	for _, want := range []string{`<form`, `hx-post="` + dismissPath + `"`, `hx-swap="none"`, `<button type="submit">Dismiss</button>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("conflicts fragment missing form-based dismiss control %q: %s", want, body)
		}
	}
	if !strings.Contains(body, "/api/conflicts/") {
		t.Fatalf("conflicts fragment missing dismiss route: %s", body)
	}
	if strings.Contains(body, `hx-swap="delete"`) {
		t.Fatalf("conflicts fragment still uses direct htmx row delete: %s", body)
	}
}

func TestIndexContainsV02ManagementPanels(t *testing.T) {
	rec := httptest.NewRecorder()
	Deps{}.Index(rec, webRequestWithUser(http.MethodGet, "/", adminSessionUser()))
	body := rec.Body.String()
	for _, want := range []string{
		`hx-get="/ui/emby/servers"`,
		`hx-get="/ui/emby/user-links"`,
		`hx-get="/ui/emby/library-mappings"`,
		`hx-get="/ui/account-pools"`,
		`hx-get="/ui/quota/policies"`,
		`hx-get="/ui/quota/usage"`,
		`hx-get="/ui/playback/sessions"`,
		`hx-get="/ui/playback/events"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("index missing %s in body=%s", want, body)
		}
	}
}

func TestIndexDescribesCurrentV03Scope(t *testing.T) {
	rec := httptest.NewRecorder()
	Deps{}.Index(rec, webRequestWithUser(http.MethodGet, "/", adminSessionUser()))
	body := rec.Body.String()
	if !strings.Contains(body, "v0.3 discovery") {
		t.Fatalf("index missing current v0.3 scope in body=%s", body)
	}
	if strings.Contains(body, "v0.1") {
		t.Fatalf("index still describes the dashboard as v0.1-only: %s", body)
	}
}

func webRequestWithUser(method, target string, user auth.UserContext) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	return req.WithContext(auth.NewContext(req.Context(), user))
}

func adminSessionUser() auth.UserContext {
	return auth.UserContext{
		UserID:           "admin",
		Role:             "admin",
		Scopes:           []string{"admin"},
		CredentialSource: auth.CredentialSession,
	}
}

func TestV02FragmentsDoNotRenderSecrets(t *testing.T) {
	st := newWebStore(t)
	if _, err := st.UpsertEmbyServer(context.Background(), queries.UpsertEmbyServerParams{
		ID: "default", Name: "Main", BaseUrl: "http://emby:8096", ApiKeyRef: sql.NullString{String: "env:EMBY_API_KEY", Valid: true},
		PublicBaseUrl: "https://echo.example.com", ProxyPrefix: "/emby", Enabled: 1, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: st}
	rec := httptest.NewRecorder()
	deps.UIEmbyServers(rec, httptest.NewRequest(http.MethodGet, "/ui/emby/servers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "EMBY_API_KEY") || strings.Contains(body, "api_key_ref") {
		t.Fatalf("fragment leaked secret reference: %s", body)
	}
	if !strings.Contains(body, "Main") || !strings.Contains(body, "http://emby:8096") {
		t.Fatalf("fragment body=%s, want safe server fields", body)
	}
	for _, want := range []string{`hx-post="/api/emby/servers"`, `hx-patch="/api/emby/servers/default"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("server fragment missing management control %s in body=%s", want, body)
		}
	}
}

// TestPlaybackSessionsFragmentRedactsSecrets guards the sessions fragment against a
// future edit reintroducing row.Selector / row.TokenHash — the live-token halves
// whose leak the JSON API also redacts. We seed an expired session (so the schema
// CHECK does not require library_entry_id/blob_id) with sentinel secret values via
// the built-in admin user and a freshly upserted Emby server (its FK target), then
// assert the rendered fragment renders the row (its id) but never the secrets.
func TestPlaybackSessionsFragmentRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	st := newWebStore(t)
	if _, err := st.UpsertEmbyServer(ctx, queries.UpsertEmbyServerParams{
		ID: "srv-LEAKME", Name: "Main", BaseUrl: "http://emby:8096",
		PublicBaseUrl: "https://echo.example.com", ProxyPrefix: "/emby", Enabled: 1, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePlaybackSession(ctx, queries.CreatePlaybackSessionParams{
		ID: "sess-LEAKME", Selector: "sel-LEAKME", TokenHash: "hash-LEAKME",
		EchoUserID: "admin", EmbyServerID: "srv-LEAKME", EmbyUserID: "emby-user",
		ItemID: "item-1", MediaSourceID: "ms-1", State: "expired",
		CreatedAt: 1, LastSeenAt: 1, ExpiresAt: 2,
	}); err != nil {
		t.Fatal(err)
	}

	deps := Deps{Store: st}
	rec := httptest.NewRecorder()
	deps.UIPlaybackSessions(rec, httptest.NewRequest(http.MethodGet, "/ui/playback/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, bad := range []string{"sel-LEAKME", "hash-LEAKME", "selector", "token_hash"} {
		if strings.Contains(body, bad) {
			t.Fatalf("playback sessions fragment leaked %q: %s", bad, body)
		}
	}
	if !strings.Contains(body, "sess-LEAKME") {
		t.Fatalf("playback sessions fragment did not render the seeded row: %s", body)
	}
}

func TestV02ManagementFragmentsExposeMutationForms(t *testing.T) {
	st := newWebStore(t)
	deps := Deps{Store: st}
	cases := []struct {
		path  string
		serve func(http.ResponseWriter, *http.Request)
		want  string
	}{
		{"/ui/emby/user-links", deps.UIEmbyUserLinks, `hx-post="/api/emby/user-links"`},
		{"/ui/emby/library-mappings", deps.UIEmbyLibraryMappings, `hx-post="/api/emby/library-mappings"`},
		{"/ui/account-pools", deps.UIAccountPools, `hx-post="/api/account-pools"`},
		{"/ui/quota/policies", deps.UIQuotaPolicies, `hx-post="/api/quota/policies"`},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		tc.serve(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s missing %s in body=%s", tc.path, tc.want, rec.Body.String())
		}
	}
}

func TestMountUIRegistersRoutes(t *testing.T) {
	d := Deps{Store: newWebStore(t)}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.NewContext(req.Context(), auth.UserContext{UserID: "admin", Scopes: []string{"admin"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	d.MountUI(r)
	for _, path := range []string{
		"/ui/jobs", "/ui/conflicts",
		"/ui/emby/servers", "/ui/emby/user-links", "/ui/emby/library-mappings",
		"/ui/account-pools", "/ui/quota/policies", "/ui/quota/usage",
		"/ui/playback/sessions", "/ui/playback/events",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200 (route mounted)", path, rec.Code)
		}
	}
}

func TestMountUIRequiresAdmin(t *testing.T) {
	d := Deps{Store: newWebStore(t)}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.NewContext(req.Context(), auth.UserContext{UserID: "u1", Scopes: []string{"read"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	d.MountUI(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/jobs", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
