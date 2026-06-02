package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/discovery"
)

func TestDiscoveryRoutesRequireAdmin(t *testing.T) {
	deps := APIDeps{}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.NewContext(req.Context(), auth.UserContext{UserID: "u1", Scopes: []string{"read"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	deps.MountDiscovery(r)
	req := httptest.NewRequest(http.MethodGet, "/api/discovery/subscriptions", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

type discoveryAPITestDeps struct {
	Router *chi.Mux
	Jobs   *fakeDiscoveryJobs
}

type fakeDiscoveryJobs struct {
	enqueued []struct {
		kind    string
		payload any
	}
}

func (f *fakeDiscoveryJobs) Enqueue(ctx context.Context, kind string, payload any) (int64, error) {
	f.enqueued = append(f.enqueued, struct {
		kind    string
		payload any
	}{kind: kind, payload: payload})
	return int64(len(f.enqueued)), nil
}

func (f *fakeDiscoveryJobs) Cancel(jobID int64) bool {
	return false
}

func newDiscoveryAPITestDeps(t *testing.T, admin bool) discoveryAPITestDeps {
	t.Helper()
	jobs := &fakeDiscoveryJobs{}
	deps := APIDeps{Jobs: jobs}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			scopes := []string{"read"}
			if admin {
				scopes = append(scopes, "admin")
			}
			ctx := auth.NewContext(req.Context(), auth.UserContext{UserID: "admin", Scopes: scopes})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	deps.MountDiscovery(r)
	return discoveryAPITestDeps{Router: r, Jobs: jobs}
}

func TestDiscoveryRunSourceReturnsAccepted(t *testing.T) {
	deps := newDiscoveryAPITestDeps(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/discovery/run/source/1", nil)
	req = req.WithContext(auth.NewContext(req.Context(), auth.UserContext{UserID: "admin", Scopes: []string{"admin"}}))
	rr := httptest.NewRecorder()
	deps.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	if len(deps.Jobs.enqueued) != 1 || deps.Jobs.enqueued[0].kind != discovery.KindSourceCrawl {
		t.Fatalf("enqueued = %#v", deps.Jobs.enqueued)
	}
}

func TestDiscoveryRawDebugDisabledByDefault(t *testing.T) {
	deps := newDiscoveryAPITestDeps(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/discovery/debug/resources/1/raw", nil)
	rr := httptest.NewRecorder()
	deps.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when raw debug is disabled", rr.Code)
	}
}

func TestDiscoveryAdminEndpointsRejectNonAdmin(t *testing.T) {
	deps := newDiscoveryAPITestDeps(t, false)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/discovery/subscriptions"},
		{http.MethodPost, "/api/discovery/subscriptions"},
		{http.MethodPatch, "/api/discovery/subscriptions/1"},
		{http.MethodGet, "/api/discovery/tmdb/search?q=known&type=movie"},
		{http.MethodGet, "/api/discovery/sources"},
		{http.MethodPost, "/api/discovery/sources"},
		{http.MethodPatch, "/api/discovery/sources/1"},
		{http.MethodGet, "/api/discovery/producer-profiles"},
		{http.MethodPost, "/api/discovery/producer-profiles"},
		{http.MethodPatch, "/api/discovery/producer-profiles/1"},
		{http.MethodGet, "/api/discovery/rule-profiles"},
		{http.MethodPost, "/api/discovery/rule-profiles"},
		{http.MethodPatch, "/api/discovery/rule-profiles/1"},
		{http.MethodPost, "/api/discovery/rule-profiles/1/test"},
		{http.MethodGet, "/api/discovery/candidates"},
		{http.MethodGet, "/api/discovery/matches"},
		{http.MethodPost, "/api/discovery/matches/1/accept"},
		{http.MethodPost, "/api/discovery/matches/1/reject"},
		{http.MethodPost, "/api/discovery/matches/1/retry"},
		{http.MethodPost, "/api/discovery/run/source/1"},
		{http.MethodPost, "/api/discovery/run/subscription/1"},
		{http.MethodGet, "/api/discovery/debug/resources/1/raw"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()
		deps.Router.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403", tc.method, tc.path, rr.Code)
		}
	}
}
