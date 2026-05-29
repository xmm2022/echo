//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/xmm2022/echo/internal/sidecarclient/fakesidecar"
)

// TestSidecarVersionMismatch verifies that when the sidecar reports a version that
// does not match the configured min_version, /readyz fails and the per-check
// detail names the required version (R1 decision: exact-string version equality).
func TestSidecarVersionMismatch(t *testing.T) {
	const requiredVersion = "echo-requires-cas-tools-v2"
	env := newEnv(t, envConfig{
		minVersion: requiredVersion,
		fakeOpts:   fakesidecar.Options{Version: "sidecar-running-v1", Commit: "sidecar-running-v1"},
	})

	var ready readyResp
	resp := env.do(http.MethodGet, "/readyz", nil)
	body := decodeBody(t, resp, &ready)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503 (body: %s)", resp.StatusCode, body)
	}
	if ready.Status != "not_ready" {
		t.Errorf("readyz status field = %q, want not_ready", ready.Status)
	}

	// The sidecar is up, so its ping succeeds; only the version check fails.
	if ready.Checks["sidecar"] != "ok" {
		t.Errorf("readyz sidecar (ping) check = %q, want ok", ready.Checks["sidecar"])
	}
	versionCheck := ready.Checks["sidecar_version"]
	if versionCheck == "ok" {
		t.Fatalf("readyz sidecar_version check = ok, want a version error")
	}
	if !strings.Contains(versionCheck, requiredVersion) {
		t.Errorf("readyz sidecar_version check = %q, want it to name the required version %q", versionCheck, requiredVersion)
	}
}
