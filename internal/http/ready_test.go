package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) PingContext(context.Context) error { return f.err }

// fakePing is the v0.2 readiness DB probe fake (ok-flag form, distinct from the
// legacy fakePinger error form).
type fakePing struct{ ok bool }

func (f fakePing) PingContext(context.Context) error {
	if f.ok {
		return nil
	}
	return errors.New("db down")
}

// fakeProbe is the v0.2 readiness dependency probe fake: it reports a fixed status.
type fakeProbe struct{ status string }

func (f fakeProbe) Check(context.Context) ProbeResult { return ProbeResult{Status: f.status} }

type fakeSidecarHealth struct {
	pingErr error
	version string
	verErr  error
}

func (f fakeSidecarHealth) Ping(context.Context) error { return f.pingErr }
func (f fakeSidecarHealth) Version(context.Context) (string, error) {
	return f.version, f.verErr
}

func serveReady(t *testing.T, rc *ReadyChecker) (*httptest.ResponseRecorder, readyBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	rc.handler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body readyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readyz body %q: %v", rec.Body.String(), err)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	return rec, body
}

type readyBody struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Checks  map[string]string `json:"checks"`
}

func TestReadyCheckerAllOK(t *testing.T) {
	rc := &ReadyChecker{
		DB:      fakePinger{},
		Sidecar: fakeSidecarHealth{version: "feat-cas-tools@abc"},
		Version: "v0.1.0",
	}
	rec, body := serveReady(t, rc)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if body.Version != "v0.1.0" {
		t.Errorf("version = %q, want v0.1.0", body.Version)
	}
	if body.Checks["db"] != "ok" {
		t.Errorf("db check = %q, want ok", body.Checks["db"])
	}
	if body.Checks["sidecar"] != "ok" {
		t.Errorf("sidecar check = %q, want ok", body.Checks["sidecar"])
	}
	if body.Checks["sidecar_version"] != "feat-cas-tools@abc" {
		t.Errorf("sidecar_version = %q, want the reported version", body.Checks["sidecar_version"])
	}
}

func TestReadyCheckerDBDown(t *testing.T) {
	rc := &ReadyChecker{
		DB:      fakePinger{err: errors.New("db unavailable")},
		Sidecar: fakeSidecarHealth{version: "v"},
	}
	rec, body := serveReady(t, rc)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body.Status != "not_ready" {
		t.Errorf("status = %q, want not_ready", body.Status)
	}
	if body.Checks["db"] == "ok" {
		t.Errorf("db check should report the failure, got %q", body.Checks["db"])
	}
}

func TestReadyCheckerSidecarUnreachable(t *testing.T) {
	rc := &ReadyChecker{
		DB:      fakePinger{},
		Sidecar: fakeSidecarHealth{pingErr: errors.New("dial tcp: connection refused")},
	}
	rec, body := serveReady(t, rc)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body.Checks["sidecar"] == "ok" {
		t.Errorf("sidecar check should report the failure, got %q", body.Checks["sidecar"])
	}
}

func TestReadyCheckerVersionTooOld(t *testing.T) {
	rc := &ReadyChecker{
		DB:      fakePinger{},
		Sidecar: fakeSidecarHealth{verErr: errors.New("required feat-cas-tools@abc got feat-cas-tools@old")},
	}
	rec, body := serveReady(t, rc)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body.Checks["sidecar_version"] == "ok" {
		t.Errorf("sidecar_version check should report the version error, got %q", body.Checks["sidecar_version"])
	}
}

func TestReadyzRouteUsesCheckerWhenWired(t *testing.T) {
	deps := Deps{
		Logger: discardLogger(),
		Ready: &ReadyChecker{
			DB:      fakePinger{},
			Sidecar: fakeSidecarHealth{version: "feat-cas-tools@abc"},
			Version: "v0.1.0",
		},
	}
	rec := httptest.NewRecorder()
	HandlerWithDeps(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz with healthy checker = %d, want 200", rec.Code)
	}
}

func TestV02ReadinessSoftAndHardDependencies(t *testing.T) {
	rc := NewReadyChecker(ReadyDeps{
		DB:              fakePing{ok: true},
		SidecarContract: fakeProbe{status: "unknown"},
		Emby:            fakeProbe{status: "unreachable"},
		Config:          ReadyConfig{},
	})
	rec, body := serveReady(t, rc)
	if rec.Code != http.StatusOK || body.Status != "degraded" {
		t.Fatalf("soft readiness status=%d body=%+v, want 200 degraded", rec.Code, body)
	}

	rc = NewReadyChecker(ReadyDeps{
		DB:              fakePing{ok: true},
		SidecarContract: fakeProbe{status: "unknown"},
		Emby:            fakeProbe{status: "unreachable"},
		Config:          ReadyConfig{RequireSidecarContract: true, RequireEmbyConnectivity: true},
	})
	rec, body = serveReady(t, rc)
	if rec.Code != http.StatusServiceUnavailable || body.Status != "not_ready" {
		t.Fatalf("hard readiness status=%d body=%+v, want 503 not_ready", rec.Code, body)
	}
}
