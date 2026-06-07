package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	apihandlers "github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/media"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

var appForbiddenFragments = []string{
	"/ui/",
	"/api/discovery/",
	"/api/discovery/matches",
	"/api/discovery/debug",
	"producer_profile_id",
	"rule_profile_id",
	"target_account",
	"storage_mount",
	"default_args_json",
}

func TestAppShellRendersSessionPanelsAndNoTokenBox(t *testing.T) {
	user := appUser("discovery")
	user.CredentialSource = auth.CredentialSession
	rec := httptest.NewRecorder()
	Deps{}.App(rec, webRequestWithUser(http.MethodGet, "/app", user))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	assertAppSecurityHeaders(t, rec)
	body := rec.Body.String()
	for _, want := range []string{
		"Echo App",
		`hx-get="/app/discover"`,
		`hx-get="/app/requests"`,
		`hx-get="/app/account"`,
		`/static/htmx.min.js`,
		`/static/app.js`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("app shell missing %q in body=%s", want, body)
		}
	}
	for _, bad := range []string{`id="app-token"`, "echo_app_token", "echo_admin_token"} {
		if strings.Contains(body, bad) {
			t.Fatalf("app shell rendered token UI %q: %s", bad, body)
		}
	}
	assertNoAppForbiddenFragments(t, body)
}

func TestAppUIRoutesRequireDiscoveryScope(t *testing.T) {
	for _, path := range []string{"/app/discover", "/app/requests", "/app/account"} {
		rec := serveAppFragmentAs(t, Deps{Media: &media.Service{}}, appUser("read"), path)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s, want 403", path, rec.Code, rec.Body.String())
		}
		assertAppSecurityHeaders(t, rec)
		assertNoAppForbiddenFragments(t, rec.Body.String())
	}

	rec := serveAppFragmentAs(t, Deps{}, appUser("discovery"), "/app/requests")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing media status=%d body=%s, want 503", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "media service not configured") {
		t.Fatalf("missing media body=%s, want safe service error", rec.Body.String())
	}
	assertAppSecurityHeaders(t, rec)
	assertNoAppForbiddenFragments(t, rec.Body.String())
}

func TestAppFragmentsNeverRenderAdminDiscoveryControls(t *testing.T) {
	fixture := newAppMediaFixture(t, "auto_approve")
	fixture.fake.movies["700"] = tmdb.Media{TMDBID: "700", MediaType: "movie", Title: "Plain Movie", RawJSON: `{}`}
	createAppMediaRequest(t, fixture.service, fixture.user.UserID, fixture.target.ID, "700", "movie")

	for _, path := range []string{"/app/discover", "/app/requests", "/app/account"} {
		rec := serveAppFragmentAs(t, Deps{Media: fixture.service}, fixture.user, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s, want 200", path, rec.Code, rec.Body.String())
		}
		assertAppSecurityHeaders(t, rec)
		assertNoAppForbiddenFragments(t, rec.Body.String())
	}

	r := chi.NewRouter()
	r.Use(appContextMiddleware(fixture.user))
	Deps{Media: fixture.service}.MountAppUI(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/jobs", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("MountAppUI registered admin /ui route: status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}

func TestAppJSSessionAuthGlueStatic(t *testing.T) {
	raw, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	js := string(raw)
	for _, want := range []string{
		`var csrfToken = "";`,
		`function refreshSession()`,
		`"/api/session/me"`,
		`"X-CSRF-Token"`,
		`window.location.href = "/login"`,
		`htmx.config.allowScriptTags = false`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
	for _, bad := range []string{"localStorage.getItem", "echo_admin_token", "echo_app_token", "Authorization"} {
		if strings.Contains(js, bad) {
			t.Fatalf("app.js still contains bearer-token glue %q", bad)
		}
	}
}

func TestAppJSSessionAuthGlueRuntime(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	script := `
const fs = require("fs");
const vm = require("vm");
const listeners = {};
const elements = {};
const document = {
  addEventListener: function (name, handler) {
    (listeners[name] || (listeners[name] = [])).push(handler);
  },
  getElementById: function (id) {
    return elements[id] || null;
  },
  querySelectorAll: function () { return []; }
};
const locationState = { origin: "https://echo.test", href: "https://echo.test/login" };
const window = {
  location: locationState,
  htmx: { config: { allowScriptTags: true }, trigger: function () {} }
};
const fetchCalls = [];
function response(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status: status,
    json: function () { return Promise.resolve(body); },
    text: function () { return Promise.resolve(JSON.stringify(body)); }
  };
}
const context = {
  URL: URL,
  alert: function (msg) { throw new Error(msg); },
  console: console,
  document: document,
  fetch: function (url, opts) {
    opts = opts || {};
    fetchCalls.push({ url: url, opts: opts });
    if (url === "/api/session/me") {
      return Promise.resolve(response(200, {
        authenticated: true,
        csrf_token: "csrf-session",
        user: { id: "u1", role: "user", scopes: ["discovery"] }
      }));
    }
    if (url === "/api/session/login") {
      const body = JSON.parse(opts.body || "{}");
      const admin = body.username === "admin";
      return Promise.resolve(response(200, {
        authenticated: true,
        csrf_token: "csrf-login",
        user: { id: admin ? "admin" : "u1", role: admin ? "admin" : "user", scopes: admin ? ["admin"] : ["discovery"] }
      }));
    }
    return Promise.resolve(response(204, {}));
  },
  location: locationState,
  window: window
};
context.globalThis = context;

function input(name, value, type) {
  return {
    getAttribute: function (attr) {
      if (attr === "name") return name;
      if (attr === "type") return type || "text";
      return "";
    },
    value: value,
    checked: false
  };
}

function loginForm(next, username) {
  return {
    tagName: "FORM",
    id: "login-form",
    getAttribute: function (name) {
      if (name === "id") return "login-form";
      if (name === "data-next") return next;
      return "";
    },
    querySelectorAll: function () {
      return [input("username", username, "text"), input("password", "pw", "password")];
    },
    closest: function () { return null; }
  };
}

function dispatch(name, evt) {
  (listeners[name] || []).forEach(function (handler) { handler(evt); });
}

function csrfFor(path, method) {
  const evt = { detail: { path: path, verb: method, headers: {} } };
  dispatch("htmx:configRequest", evt);
  return evt.detail.headers["X-CSRF-Token"] || "";
}

function tick() {
  return new Promise(function (resolve) { setImmediate(resolve); });
}

(async function () {
  vm.runInNewContext(fs.readFileSync("static/app.js", "utf8"), context, { filename: "app.js" });
  dispatch("DOMContentLoaded", {});
  await tick();
  await tick();

  if (window.htmx.config.allowScriptTags !== false) {
    throw new Error("htmx allowScriptTags was not disabled");
  }
  const meCall = fetchCalls.find(function (call) { return call.url === "/api/session/me"; });
  if (!meCall || meCall.opts.credentials !== "same-origin") {
    throw new Error("/api/session/me did not use same-origin credentials");
  }

  const checks = {
    postAPI: csrfFor("/api/me/discovery/requests", "POST"),
    getAPI: csrfFor("/api/me/discovery/catalog", "GET"),
    staticAsset: csrfFor("/static/app.js", "GET"),
    external: csrfFor("https://evil.test/api/me/discovery/requests", "POST")
  };
  if (checks.postAPI !== "csrf-session") {
    throw new Error("POST CSRF=" + JSON.stringify(checks.postAPI) + ", want csrf-session");
  }
  for (const key of ["getAPI", "staticAsset", "external"]) {
    if (checks[key] !== "") {
      throw new Error(key + " CSRF=" + JSON.stringify(checks[key]) + ", want empty");
    }
  }

  locationState.href = "https://echo.test/app";
  dispatch("htmx:responseError", { detail: { xhr: { status: 401 } } });
  if (window.location.href !== "/login") {
    throw new Error("401 redirect href=" + JSON.stringify(window.location.href));
  }

  const beforeLoginCalls = fetchCalls.length;
  dispatch("submit", {
    target: loginForm("/app", "u1"),
    preventDefault: function () {},
    stopPropagation: function () {}
  });
  await tick();
  await tick();
  const loginCall = fetchCalls.slice(beforeLoginCalls).find(function (call) { return call.url === "/api/session/login"; });
  if (!loginCall) {
    throw new Error("login form did not call /api/session/login");
  }
  if (loginCall.opts.credentials !== "same-origin") {
    throw new Error("login credentials=" + JSON.stringify(loginCall.opts.credentials) + ", want same-origin");
  }
  if (window.location.href !== "/app") {
    throw new Error("safe next redirect href=" + JSON.stringify(window.location.href) + ", want /app");
  }

  dispatch("submit", {
    target: loginForm("//evil.test", "admin"),
    preventDefault: function () {},
    stopPropagation: function () {}
  });
  await tick();
  await tick();
  if (window.location.href !== "/") {
    throw new Error("unsafe admin next href=" + JSON.stringify(window.location.href) + ", want /");
  }
})().catch(function (err) {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node app.js behavior failed: %v\n%s", err, out)
	}
}

func TestAppSearchFragmentEscapesHostileText(t *testing.T) {
	fixture := newAppMediaFixture(t, "approval_required")
	fixture.fake.searches["movie:hostile"] = []tmdb.Media{{
		TMDBID:      "999",
		MediaType:   "movie",
		Title:       `<img src=x onerror=alert(1)>`,
		ReleaseYear: 2026,
		PosterPath:  `"><script>alert(1)</script>`,
		RawJSON:     `{"default_args_json":"raw admin field"}`,
	}}

	rec := serveAppFragmentAs(t, Deps{Media: fixture.service}, fixture.user, "/app/discover?q=hostile&type=movie")
	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	assertAppSecurityHeaders(t, rec)
	body := rec.Body.String()
	assertNoAppForbiddenFragments(t, body)
	for _, bad := range []string{
		`<img src=x onerror=alert(1)>`,
		`"><script>alert(1)</script>`,
		`<script>alert(1)</script>`,
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("search fragment rendered hostile text %q: %s", bad, body)
		}
	}
	if regexp.MustCompile(`(?i)<[^>]+onerror\s*=`).MatchString(body) {
		t.Fatalf("search fragment rendered executable event handler: %s", body)
	}
	if !strings.Contains(body, "&lt;img") {
		t.Fatalf("search fragment did not render escaped hostile title: %s", body)
	}
	if !strings.Contains(body, `name="target_id"`) || !strings.Contains(body, `data-type="number"`) {
		t.Fatalf("request form must serialize target_id as a JSON number: %s", body)
	}
}

func TestAppMediaRateLimitRunsBeforeDBOrTMDBWork(t *testing.T) {
	searchLimiter := newFakeAppLimiter()
	searchLimiter.deny["search:user:u1"] = true
	searchTMDB := newFakeAppTMDB()
	rec := serveAppFragmentAs(t, Deps{Media: &media.Service{TMDB: searchTMDB, Limiter: searchLimiter, Now: appClock}}, appUser("discovery"), "/app/discover?q=matrix&type=movie")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("search limited app status=%d body=%s, want 429 before DB/TMDB work", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request-limit-reached") {
		t.Fatalf("search limited app body=%s, want safe rate-limit code", rec.Body.String())
	}
	if searchTMDB.searchCalls() != 0 {
		t.Fatalf("TMDB search calls=%d, want 0 when app search limiter denies", searchTMDB.searchCalls())
	}

	pollBeforeSearchLimiter := newFakeAppLimiter()
	pollBeforeSearchLimiter.deny["status-poll:user:u1"] = true
	rec = serveAppFragmentAs(t, Deps{Media: &media.Service{TMDB: newFakeAppTMDB(), Limiter: pollBeforeSearchLimiter, Now: appClock}}, appUser("discovery"), "/app/discover?q=matrix&type=movie")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("search page poll limited status=%d body=%s, want 429 before DB/TMDB work", rec.Code, rec.Body.String())
	}
	pollBeforeSearchLimiter.assertNotCommitted(t, "search:user:u1")
	pollBeforeSearchLimiter.assertNotCommitted(t, "tmdb:global:search")

	for _, path := range []string{"/app/discover", "/app/requests", "/app/account"} {
		pollLimiter := newFakeAppLimiter()
		pollLimiter.deny["status-poll:user:u1"] = true
		rec := serveAppFragmentAs(t, Deps{Media: &media.Service{Limiter: pollLimiter, Now: appClock}}, appUser("discovery"), path)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s poll limited status=%d body=%s, want 429 before DB work", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "request-limit-reached") {
			t.Fatalf("%s poll limited body=%s, want safe rate-limit code", path, rec.Body.String())
		}
	}
}

func TestPosterProxyRejectsUnsafePosterPathsIfAdded(t *testing.T) {
	fixture := newAppMediaFixture(t, "approval_required")
	fixture.fake.searches["movie:poster"] = []tmdb.Media{
		{
			TMDBID:      "safe",
			MediaType:   "movie",
			Title:       "Safe Poster",
			PosterPath:  "/safe.jpg",
			ReleaseYear: 2026,
			RawJSON:     `{}`,
		},
		{
			TMDBID:      "unsafe",
			MediaType:   "movie",
			Title:       "Unsafe Poster",
			PosterPath:  "http://169.254.169.254/latest/meta-data",
			ReleaseYear: 2026,
			RawJSON:     `{}`,
		},
	}

	rec := serveAppFragmentAs(t, Deps{Media: fixture.service}, fixture.user, "/app/discover?q=poster&type=movie")
	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://image.tmdb.org/t/p/w92/safe.jpg") {
		t.Fatalf("safe TMDB poster path did not render as direct TMDB image URL: %s", body)
	}
	for _, bad := range []string{
		"/api/me/discovery/poster",
		"/api/me/discovery/posters",
		"/app/poster",
		"169.254.169.254",
		"latest/meta-data",
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("poster path was proxied or leaked unsafe text %q: %s", bad, body)
		}
	}

	r := chi.NewRouter()
	r.Use(appContextMiddleware(fixture.user))
	Deps{Media: fixture.service}.MountAppUI(r)
	apihandlers.APIDeps{Media: fixture.service}.MountMedia(r)
	for _, path := range []string{
		"/app/poster?path=http://169.254.169.254/latest/meta-data",
		"/api/me/discovery/poster?path=http://169.254.169.254/latest/meta-data",
		"/api/me/discovery/posters/unsafe",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("poster proxy route %s status=%d body=%s, want 404", path, rec.Code, rec.Body.String())
		}
	}
}

func TestAppSearchResultsFilterTargetsByMediaType(t *testing.T) {
	fixture := newAppMediaFixture(t, "approval_required")
	seedAppMediaTarget(t, fixture.store, fixture.targetDeps, fixture.policy.ID, "Movie Only", "movie", 1)
	seedAppMediaTarget(t, fixture.store, fixture.targetDeps, fixture.policy.ID, "TV Only", "tv", 1)
	fixture.fake.searches["movie:matrix"] = []tmdb.Media{{
		TMDBID:      "603",
		MediaType:   "movie",
		Title:       "The Matrix",
		ReleaseYear: 1999,
		RawJSON:     `{}`,
	}}

	rec := serveAppFragmentAs(t, Deps{Media: fixture.service}, fixture.user, "/app/discover?q=matrix&type=movie")
	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	options := appSelectFragment(t, body, `media-target-movie-603`)
	for _, want := range []string{"Default Target", "Movie Only"} {
		if !strings.Contains(options, want) {
			t.Fatalf("movie request form missing eligible target %q: %s", want, options)
		}
	}
	if strings.Contains(options, "TV Only") {
		t.Fatalf("movie request form rendered tv-only target: %s", options)
	}
}

func TestAdminIndexDoesNotEmbedUserRequestData(t *testing.T) {
	fixture := newAppMediaFixture(t, "approval_required")
	fixture.fake.movies["701"] = tmdb.Media{TMDBID: "701", MediaType: "movie", Title: "User Request Leak Sentinel", RawJSON: `{}`}
	createAppMediaRequest(t, fixture.service, fixture.user.UserID, fixture.target.ID, "701", "movie")

	rec := httptest.NewRecorder()
	Deps{Store: fixture.store, Media: fixture.service}.Index(rec, webRequestWithUser(http.MethodGet, "/", adminSessionUser()))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin index status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, bad := range []string{"User Request Leak Sentinel", `id="app-token"`, `hx-get="/app/requests"`} {
		if strings.Contains(body, bad) {
			t.Fatalf("admin index embedded user app data/control %q: %s", bad, body)
		}
	}
}

func assertNoAppForbiddenFragments(t *testing.T, body string) {
	t.Helper()
	for _, fragment := range appForbiddenFragments {
		if strings.Contains(body, fragment) {
			t.Fatalf("app response leaked %q: %s", fragment, body)
		}
	}
}

func assertAppSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store; body=%s", got, rec.Body.String())
	}
	wantCSP := "default-src 'self'; script-src 'self'; img-src 'self' https://image.tmdb.org data:; object-src 'none'; base-uri 'none'"
	if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Fatalf("Content-Security-Policy=%q, want %q", got, wantCSP)
	}
}

func appSelectFragment(t *testing.T, body, id string) string {
	t.Helper()
	start := strings.Index(body, `<select id="`+id+`"`)
	if start < 0 {
		t.Fatalf("body missing select %s: %s", id, body)
	}
	end := strings.Index(body[start:], `</select>`)
	if end < 0 {
		t.Fatalf("select %s missing closing tag: %s", id, body[start:])
	}
	return body[start : start+end]
}

func serveAppFragmentAs(t *testing.T, deps Deps, user auth.UserContext, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Use(appContextMiddleware(user))
	deps.MountAppUI(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func appContextMiddleware(user auth.UserContext) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.NewContext(req.Context(), user)))
		})
	}
}

func appUser(scopes ...string) auth.UserContext {
	if len(scopes) == 0 {
		scopes = []string{"discovery"}
	}
	return auth.UserContext{UserID: "u1", Role: "user", Scopes: scopes, Now: time.Unix(1000, 0)}
}

type appMediaFixture struct {
	store      *store.Store
	service    *media.Service
	fake       *fakeAppTMDB
	user       auth.UserContext
	policy     queries.DiscoveryAccessPolicy
	targetDeps appTargetDeps
	target     queries.DiscoveryPolicyTarget
}

func newAppMediaFixture(t *testing.T, requestMode string) appMediaFixture {
	t.Helper()
	st := newWebStore(t)
	user := appUser("discovery")
	seedAppMediaUser(t, st, user.UserID)
	policy := seedAppMediaPolicy(t, st, "policy-"+requestMode, user.UserID, requestMode, 1)
	targetDeps := seedAppMediaTargetDeps(t, st)
	target := seedAppMediaTarget(t, st, targetDeps, policy.ID, "Default Target", "", 1)
	fake := newFakeAppTMDB()
	return appMediaFixture{
		store:      st,
		service:    &media.Service{Store: st, TMDB: fake, Now: appClock},
		fake:       fake,
		user:       user,
		policy:     policy,
		targetDeps: targetDeps,
		target:     target,
	}
}

func createAppMediaRequest(t *testing.T, svc *media.Service, userID string, targetID int64, tmdbID, mediaType string) media.RequestDTO {
	t.Helper()
	got, err := svc.CreateRequest(context.Background(), media.Actor{
		User: auth.UserContext{UserID: userID, Role: "user", Scopes: []string{"discovery"}, Now: time.Unix(1000, 0)},
		IP:   "127.0.0.1",
	}, media.CreateRequestInput{
		TMDBID:       tmdbID,
		MediaType:    mediaType,
		TMDBLanguage: "zh-CN",
		TargetID:     targetID,
	})
	if err != nil {
		t.Fatalf("create media request: %v", err)
	}
	return got
}

func seedAppMediaUser(t *testing.T, st *store.Store, id string) {
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

func seedAppMediaPolicy(t *testing.T, st *store.Store, name, userID, requestMode string, canSearch int64) queries.DiscoveryAccessPolicy {
	t.Helper()
	policy, err := st.CreateDiscoveryAccessPolicy(context.Background(), queries.CreateDiscoveryAccessPolicyParams{
		Name:          name,
		Enabled:       1,
		Priority:      100,
		SubjectUserID: appNullString(userID),
		RequestMode:   requestMode,
		CanSearch:     canSearch,
		CreatedBy:     appNullString("admin"),
		CreatedAt:     1000,
		UpdatedAt:     1000,
	})
	if err != nil {
		t.Fatalf("create media policy %s: %v", name, err)
	}
	return policy
}

type appTargetDeps struct {
	libraryID         int64
	producerProfileID int64
	ruleProfileID     int64
}

func seedAppMediaTargetDeps(t *testing.T, st *store.Store) appTargetDeps {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           "acc-115",
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
		Name:           "Media",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/echo-web-media-test",
		OwnerID:        "admin",
		CreatedAt:      1000,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	profile, err := st.CreateDiscoveryProducerProfile(ctx, queries.CreateDiscoveryProducerProfileParams{
		Name:                   "115 default",
		Provider:               "115",
		Tool:                   "115share2cas",
		TargetAccount:          "acc-115",
		TargetSubdirTemplate:   "{{.Title}}",
		LibraryRelPathTemplate: "{{.Title}}",
		DefaultArgsJson:        `{"secret":"producer args"}`,
		Enabled:                1,
		CreatedAt:              1000,
		UpdatedAt:              1000,
	})
	if err != nil {
		t.Fatalf("create producer profile: %v", err)
	}
	ruleProfile, err := st.CreateRuleProfile(ctx, queries.CreateRuleProfileParams{
		Name:      "default rules",
		Version:   1,
		RulesJson: `{"weights":["resolutions"],"secret":"rule secret"}`,
		Enabled:   1,
		CreatedAt: 1000,
	})
	if err != nil {
		t.Fatalf("create rule profile: %v", err)
	}
	return appTargetDeps{libraryID: library.ID, producerProfileID: profile.ID, ruleProfileID: ruleProfile.ID}
}

func seedAppMediaTarget(t *testing.T, st *store.Store, deps appTargetDeps, policyID int64, label, mediaType string, enabled int64) queries.DiscoveryPolicyTarget {
	t.Helper()
	target, err := st.CreateDiscoveryPolicyTarget(context.Background(), queries.CreateDiscoveryPolicyTargetParams{
		PolicyID:                policyID,
		Label:                   label,
		LibraryID:               deps.libraryID,
		ProducerProfileID:       deps.producerProfileID,
		RuleProfileID:           deps.ruleProfileID,
		PipelineOwnerID:         "admin",
		MediaType:               appNullString(mediaType),
		MatchMode:               "admin_review",
		GrantPlaybackOnApproval: 1,
		Enabled:                 enabled,
		DefaultTarget:           1,
		CreatedAt:               1000,
		UpdatedAt:               1000,
	})
	if err != nil {
		t.Fatalf("create target %s: %v", label, err)
	}
	return target
}

func appNullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func appClock() time.Time {
	return time.Unix(1000, 0)
}

type fakeAppTMDB struct {
	mu       sync.Mutex
	searches map[string][]tmdb.Media
	movies   map[string]tmdb.Media
	tv       map[string]tmdb.Media
	search   int
}

func newFakeAppTMDB() *fakeAppTMDB {
	return &fakeAppTMDB{
		searches: make(map[string][]tmdb.Media),
		movies:   make(map[string]tmdb.Media),
		tv:       make(map[string]tmdb.Media),
	}
}

func (f *fakeAppTMDB) Search(_ context.Context, query, mediaType string) ([]tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.search++
	return append([]tmdb.Media(nil), f.searches[mediaType+":"+query]...), nil
}

func (f *fakeAppTMDB) searchCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.search
}

func (f *fakeAppTMDB) MovieDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.movies[tmdbID]
	if !ok {
		return tmdb.Media{}, media.ErrMetadataUnavailable
	}
	return item, nil
}

func (f *fakeAppTMDB) TVDetails(_ context.Context, tmdbID string) (tmdb.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.tv[tmdbID]
	if !ok {
		return tmdb.Media{}, media.ErrMetadataUnavailable
	}
	return item, nil
}

type fakeAppLimiter struct {
	mu        sync.Mutex
	deny      map[string]bool
	committed []string
}

func newFakeAppLimiter() *fakeAppLimiter {
	return &fakeAppLimiter{deny: make(map[string]bool)}
}

func (l *fakeAppLimiter) Allow(key string, limit int, window time.Duration) bool {
	return l.AllowAll([]media.RateLimitCheck{{Key: key, Limit: limit, Window: window}})
}

func (l *fakeAppLimiter) AllowAll(checks []media.RateLimitCheck) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	allowed := true
	for _, check := range checks {
		if l.deny[check.Key] {
			allowed = false
		}
	}
	if allowed {
		for _, check := range checks {
			l.committed = append(l.committed, check.Key)
		}
	}
	return allowed
}

func (l *fakeAppLimiter) assertNotCommitted(t *testing.T, key string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, committed := range l.committed {
		if committed == key {
			t.Fatalf("limiter committed keys=%v, did not want %q", l.committed, key)
		}
	}
}
