//go:build integration_real

package integration

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReal115ProducerIngestStream is the manual, secret-gated end-to-end test
// against a REAL sidecar and a REAL 115 test share (spec §7, plan §12). It runs
// the real 115share2cas producer, ingests the resulting CAS tree, then pulls the
// first 4 KiB of a restored file through the stream proxy. It is never part of the
// default CI run: it lives behind the `integration_real` build tag and skips
// unless the required ENV is present.
//
// Run it manually, e.g.:
//
//	ECHO_TEST_SIDECAR_URL=http://127.0.0.1:5244 \
//	ECHO_TEST_SIDECAR_TOKEN=... \
//	ECHO_TEST_115_BINARY=/usr/local/bin/115share2cas \
//	ECHO_TEST_115_STORAGE_MOUNT=/115-main \
//	ECHO_TEST_115_SHARE_URL='https://115.com/s/xxxx?password=yyyy' \
//	ECHO_TEST_115_COOKIE_FILE=/secure/115-cookie.txt \
//	go test -tags=integration_real -run TestReal115 -timeout 30m ./integration/...
func TestReal115ProducerIngestStream(t *testing.T) {
	sidecarURL := os.Getenv("ECHO_TEST_SIDECAR_URL")
	binary := os.Getenv("ECHO_TEST_115_BINARY")
	storageMount := os.Getenv("ECHO_TEST_115_STORAGE_MOUNT")
	shareURL := os.Getenv("ECHO_TEST_115_SHARE_URL")
	cookieFile := os.Getenv("ECHO_TEST_115_COOKIE_FILE")
	if sidecarURL == "" || binary == "" || storageMount == "" || shareURL == "" || cookieFile == "" {
		t.Skip("integration_real: set ECHO_TEST_SIDECAR_URL, ECHO_TEST_115_BINARY, " +
			"ECHO_TEST_115_STORAGE_MOUNT, ECHO_TEST_115_SHARE_URL, ECHO_TEST_115_COOKIE_FILE to run")
	}

	timeout := parseDurationEnv(t, "ECHO_TEST_115_TIMEOUT", 15*time.Minute)
	mode := envOrDefault("ECHO_TEST_115_MODE", "transfer-batch")

	env := newEnv(t, envConfig{
		externalSidecarURL:   sidecarURL,
		externalSidecarToken: os.Getenv("ECHO_TEST_SIDECAR_TOKEN"),
		minVersion:           os.Getenv("ECHO_TEST_SIDECAR_MIN_VERSION"), // empty => accept any
		producerBinary:       binary,
		producerTimeout:      timeout,
		requestTimeout:       2 * time.Minute,
		streamTimeout:        5 * time.Minute,
	})

	account := env.createAccountOn("real-115", "115", storageMount)
	lib := env.createLibrary("real-115")

	// Stage the cookie under the producer secrets root so it is reachable via ref:.
	cookieBytes, err := os.ReadFile(cookieFile)
	if err != nil {
		t.Fatalf("read cookie file %s: %v", cookieFile, err)
	}
	if err := os.WriteFile(filepath.Join(env.secretsRoot, "cookie.txt"), cookieBytes, 0o600); err != nil {
		t.Fatalf("stage cookie: %v", err)
	}

	args := map[string]any{
		"share_url":   shareURL,
		"mode":        mode,
		"cookie_file": "ref:cookie.txt",
		"keep_temp":   true, // avoids requiring recycle_password_file
	}
	if rc := os.Getenv("ECHO_TEST_115_RECEIVE_CODE"); rc != "" {
		args["receive_code"] = rc
	}

	var out struct {
		JobID int64 `json:"job_id"`
	}
	env.doJSON(http.MethodPost, "/api/ingest/producer", map[string]any{
		"library_id":     lib.ID,
		"target_account": account.ID,
		"target_subdir":  "real",
		"tool":           "115share2cas",
		"args":           args,
	}, http.StatusAccepted, &out)

	jr := env.waitForJob(out.JobID, timeout)
	if jr.Status != "done" {
		t.Fatalf("real producer job status = %q (error %q), want done", jr.Status, jr.Error)
	}

	entries := env.listEntries(lib.ID)
	if len(entries) == 0 {
		t.Fatalf("real ingest produced no library entries")
	}
	var entry entryResp
	for _, e := range entries {
		if e.EchoWritten && e.LiveCopies > 0 {
			entry = e
			break
		}
	}
	if entry.ID == 0 {
		t.Fatalf("no live, echo-written entry among %d entries: %+v", len(entries), entries)
	}

	// Pull the first 4 KiB through the stream proxy.
	req, _ := http.NewRequest(http.MethodGet, env.baseURL+fmt.Sprintf("/api/stream/%d", entry.ID), nil)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	req.Header.Set("Range", "bytes=0-4095")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 206 or 200", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		t.Fatalf("read stream body: %v", err)
	}
	if len(body) == 0 {
		t.Fatalf("stream returned 0 bytes, want the first chunk of the file")
	}
	t.Logf("real 115 chain OK: ingested %q, streamed %d bytes", entry.RelPath, len(body))
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseDurationEnv(t *testing.T, key string, def time.Duration) time.Duration {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("%s: invalid duration %q: %v", key, raw, err)
	}
	return d
}
