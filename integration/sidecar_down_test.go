//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// TestSidecarDownDegradation verifies the sidecar-unavailable degradation (spec §7
// "杀掉 sidecar 容器, 验证 readyz fail / ingest 请求 503").
//
// Echo's ingest is asynchronous: POST /api/ingest/manual only validates and
// enqueues, so the synchronous 503 surfaces at the sidecar-touching ingest
// precondition — binding an account (POST /api/accounts, which lists storages on
// the sidecar). The ingest *job* itself then fails when PutCAS cannot reach the
// sidecar. This test asserts all three: readyz 503, account-bind 503, and a job
// that ends failed with an unreachable error.
func TestSidecarDownDegradation(t *testing.T) {
	env := newEnv(t, envConfig{})

	// Set up an account + a CAS tree while the sidecar is still reachable.
	account := env.createAccount("acc-115")
	lib := env.createLibrary("down-lib")
	casTree, manifest := env.writeCASTree("batch-down", []casFixture{
		{relPath: "down/movie.mkv", content: []byte("payload before the sidecar dies")},
	})

	// Kill the sidecar.
	env.fake.Close()

	// readyz now reports not_ready with a failing sidecar check.
	t.Run("readyz 503", func(t *testing.T) {
		var ready readyResp
		resp := env.do(http.MethodGet, "/readyz", nil)
		body := decodeBody(t, resp, &ready)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("readyz status = %d, want 503 (body: %s)", resp.StatusCode, body)
		}
		if ready.Status != "not_ready" {
			t.Errorf("readyz status field = %q, want not_ready", ready.Status)
		}
		if ready.Checks["sidecar"] == "ok" || !strings.Contains(ready.Checks["sidecar"], "unreachable") {
			t.Errorf("readyz sidecar check = %q, want an unreachable error", ready.Checks["sidecar"])
		}
		if ready.Checks["db"] != "ok" {
			t.Errorf("readyz db check = %q, want ok (db is local)", ready.Checks["db"])
		}
	})

	// The sidecar-touching ingest precondition (account binding) returns 503.
	t.Run("account bind 503", func(t *testing.T) {
		var errBody struct {
			Error string `json:"error"`
		}
		resp := env.do(http.MethodPost, "/api/accounts", map[string]string{
			"id":            "acc-115-2",
			"provider":      testStorageProv,
			"sidecar_id":    "default",
			"storage_mount": testStorageMount,
		})
		body := decodeBody(t, resp, &errBody)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("create account status = %d, want 503 (body: %s)", resp.StatusCode, body)
		}
		if errBody.Error != "sidecar-unreachable" {
			t.Errorf("create account error = %q, want sidecar-unreachable", errBody.Error)
		}
	})

	// An ingest job submitted with the sidecar down enqueues (202) but the job
	// fails when PutCAS cannot reach the sidecar.
	t.Run("ingest job fails", func(t *testing.T) {
		jobID := env.submitManualIngest(lib.ID, account.ID, "imports", casTree, manifest)
		jr := env.waitForJob(jobID, defaultJobTimeout)
		if jr.Status != "failed" {
			t.Fatalf("ingest job status = %q, want failed", jr.Status)
		}
		if !strings.Contains(jr.Error, "unreachable") {
			t.Errorf("ingest job error = %q, want an unreachable error", jr.Error)
		}
		// Nothing was committed: no live copies, .echo not written.
		entries := env.listEntries(lib.ID)
		for _, e := range entries {
			if e.EchoWritten || e.LiveCopies > 0 {
				t.Errorf("entry %q unexpectedly live after sidecar-down ingest: %+v", e.RelPath, e)
			}
		}
	})
}
