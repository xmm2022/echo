//go:build integration_real

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/pathsafe"
	"github.com/xmm2022/echo/internal/sidecarclient"
)

// TestReal115ProducerIngestStream is the manual, secret-gated end-to-end test
// against a REAL sidecar and REAL 115 data (spec §7, plan §12). By default it
// runs the real 115share2cas producer, ingests the resulting CAS tree, then pulls
// the first 4 KiB of a restored file through the stream proxy. For release
// validation against a pre-generated, source-aware CAS tree, set
// ECHO_TEST_115_CAS_TREE + ECHO_TEST_115_MANIFEST; the test stages a single
// <=2GB manifest item and uses the same manual ingest -> restore -> stream chain.
// It is never part of the default CI run: it lives behind the `integration_real`
// build tag and skips unless the required ENV is present.
//
// Run it manually, e.g.:
//
//	ECHO_TEST_SIDECAR_URL=http://127.0.0.1:5244 \
//	ECHO_TEST_SIDECAR_TOKEN=... \
//	ECHO_TEST_115_STORAGE_MOUNT=/115-main \
//	ECHO_TEST_115_CAS_TREE=/secure/cas-tree \
//	ECHO_TEST_115_MANIFEST=/secure/cas-tree/manifest.jsonl \
//	go test -tags=integration_real -run TestReal115 -timeout 30m ./integration/...
//
// Or, for the producer path:
//
//	ECHO_TEST_SIDECAR_URL=http://127.0.0.1:5244 \
//	ECHO_TEST_SIDECAR_TOKEN=... \
//	ECHO_TEST_115_BINARY=/usr/local/bin/115share2cas \
//	ECHO_TEST_115_STORAGE_MOUNT=/115-main \
//	ECHO_TEST_115_SHARE_URL='https://115.com/s/xxxx?password=yyyy' \
//	ECHO_TEST_115_COOKIE_FILE=/secure/115-cookie.txt \
//	ECHO_TEST_115_LIMIT=1 \
//	go test -tags=integration_real -run TestReal115 -timeout 30m ./integration/...
func TestReal115ProducerIngestStream(t *testing.T) {
	sidecarURL := os.Getenv("ECHO_TEST_SIDECAR_URL")
	storageMount := os.Getenv("ECHO_TEST_115_STORAGE_MOUNT")
	if sidecarURL == "" || storageMount == "" {
		t.Skip("integration_real: set ECHO_TEST_SIDECAR_URL and ECHO_TEST_115_STORAGE_MOUNT to run")
	}
	preflightReal115Sidecar(t, sidecarURL, os.Getenv("ECHO_TEST_SIDECAR_TOKEN"), storageMount)

	timeout := parseDurationEnv(t, "ECHO_TEST_115_TIMEOUT", 15*time.Minute)
	casTree := os.Getenv("ECHO_TEST_115_CAS_TREE")
	manifest := os.Getenv("ECHO_TEST_115_MANIFEST")
	if (casTree == "") != (manifest == "") {
		t.Fatalf("integration_real: set both ECHO_TEST_115_CAS_TREE and ECHO_TEST_115_MANIFEST, or neither")
	}

	env := newEnv(t, envConfig{
		externalSidecarURL:   sidecarURL,
		externalSidecarToken: os.Getenv("ECHO_TEST_SIDECAR_TOKEN"),
		minVersion:           os.Getenv("ECHO_TEST_SIDECAR_MIN_VERSION"), // empty => accept any
		producerBinary:       os.Getenv("ECHO_TEST_115_BINARY"),
		producerTimeout:      timeout,
		requestTimeout:       2 * time.Minute,
		streamTimeout:        5 * time.Minute,
	})

	account := env.createAccountOn("real-115", "115", storageMount)
	lib := env.createLibrary("real-115")

	var jobID int64
	if casTree != "" {
		maxSize := parsePositiveInt64Env(t, "ECHO_TEST_115_MAX_SIZE_BYTES", 2_000_000_000)
		stagedTree, stagedManifest, relPath := stageExistingCASTree(t, env, casTree, manifest, maxSize)
		t.Logf("integration_real: staged existing CAS item %q (max_size=%d)", relPath, maxSize)
		jobID = env.submitManualIngest(lib.ID, account.ID, "real", stagedTree, stagedManifest)
	} else {
		jobID = submitRealProducerIngest(t, env, lib.ID, account.ID)
	}

	jr := env.waitForJob(jobID, timeout)
	if jr.Status != "done" {
		t.Fatalf("real 115 job status = %q (error %q), want done", jr.Status, jr.Error)
	}

	entries := env.listEntries(lib.ID)
	if len(entries) == 0 {
		t.Fatalf("real ingest produced no library entries (job progress: %s; payload: %s)", jr.Progress, jr.Payload)
	}
	var entry entryResp
	for _, e := range entries {
		if e.EchoWritten && e.LiveCopies > 0 {
			entry = e
			break
		}
	}
	if entry.ID == 0 {
		t.Fatalf("no live, echo-written entry among %d entries: %+v (job progress: %s; payload: %s)", len(entries), entries, jr.Progress, jr.Payload)
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

func TestReal115Preflight(t *testing.T) {
	sidecarURL := os.Getenv("ECHO_TEST_SIDECAR_URL")
	storageMount := os.Getenv("ECHO_TEST_115_STORAGE_MOUNT")
	if sidecarURL == "" || storageMount == "" {
		t.Skip("integration_real: set ECHO_TEST_SIDECAR_URL and ECHO_TEST_115_STORAGE_MOUNT to run")
	}
	preflightReal115Sidecar(t, sidecarURL, os.Getenv("ECHO_TEST_SIDECAR_TOKEN"), storageMount)
}

func preflightReal115Sidecar(t *testing.T, sidecarURL, sidecarToken, storageMount string) {
	t.Helper()

	const preflightTokenEnv = "ECHO_TEST_SIDECAR_PREFLIGHT_TOKEN"
	t.Setenv(preflightTokenEnv, sidecarToken)

	client := sidecarclient.New(sidecarclient.Config{
		BaseURL:        sidecarURL,
		AuthTokenEnv:   preflightTokenEnv,
		MinVersion:     os.Getenv("ECHO_TEST_SIDECAR_MIN_VERSION"),
		RequestTimeout: config.Duration{Duration: 15 * time.Second},
		StreamTimeout:  config.Duration{Duration: 15 * time.Second},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Fatalf("real 115 preflight: ping %s failed: %v; check ECHO_TEST_SIDECAR_URL", sidecarURL, err)
	}
	version, err := client.Version(ctx)
	if err != nil {
		t.Fatalf("real 115 preflight: version check on %s failed: %v; check ECHO_TEST_SIDECAR_URL and ECHO_TEST_SIDECAR_MIN_VERSION", sidecarURL, err)
	}
	storages, err := client.ListStorages(ctx)
	if err != nil {
		if code, message, ok := sidecarclient.OpenListEnvelopeErrorDetails(err); ok {
			t.Fatalf("real 115 preflight: storage list on %s failed with OpenList code=%d message=%q; check ECHO_TEST_SIDECAR_TOKEN belongs to the running sidecar configured by ECHO_TEST_SIDECAR_URL", sidecarURL, code, message)
		}
		t.Fatalf("real 115 preflight: storage list on %s failed: %v; check ECHO_TEST_SIDECAR_URL and ECHO_TEST_SIDECAR_TOKEN", sidecarURL, err)
	}
	for _, storage := range storages {
		if storage.MountPath == storageMount {
			t.Logf("real 115 preflight OK: sidecar=%s version=%q storage_mount=%q storage_count=%d", sidecarURL, version, storageMount, len(storages))
			return
		}
	}
	t.Fatalf("real 115 preflight: ECHO_TEST_115_STORAGE_MOUNT=%q not found on sidecar %s (version %q); available storages: %s", storageMount, sidecarURL, version, real115StorageSummaries(storages))
}

func real115StorageSummaries(storages []sidecarclient.Storage) string {
	if len(storages) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, min(len(storages), 10))
	for i, storage := range storages {
		if i == 10 {
			parts = append(parts, fmt.Sprintf("... %d more", len(storages)-10))
			break
		}
		parts = append(parts, fmt.Sprintf("{id:%s driver:%s mount_path:%s status:%s}", storage.ID, storage.Provider, storage.MountPath, storage.Status))
	}
	return strings.Join(parts, ", ")
}

func submitRealProducerIngest(t *testing.T, env *testEnv, libraryID int64, accountID string) int64 {
	t.Helper()
	binary := os.Getenv("ECHO_TEST_115_BINARY")
	shareURL := os.Getenv("ECHO_TEST_115_SHARE_URL")
	cookieFile := os.Getenv("ECHO_TEST_115_COOKIE_FILE")
	if binary == "" || shareURL == "" || cookieFile == "" {
		t.Skip("integration_real producer path: set ECHO_TEST_115_BINARY, ECHO_TEST_115_SHARE_URL, and ECHO_TEST_115_COOKIE_FILE")
	}
	mode := envOrDefault("ECHO_TEST_115_MODE", "transfer-batch")

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
	if limit := parsePositiveIntEnv(t, "ECHO_TEST_115_LIMIT"); limit > 0 {
		args["limit"] = limit
	}

	var out struct {
		JobID int64 `json:"job_id"`
	}
	env.doJSON(http.MethodPost, "/api/ingest/producer", map[string]any{
		"library_id":     libraryID,
		"target_account": accountID,
		"target_subdir":  "real",
		"tool":           "115share2cas",
		"args":           args,
	}, http.StatusAccepted, &out)
	return out.JobID
}

func stageExistingCASTree(t *testing.T, env *testEnv, sourceTree, sourceManifest string, maxSize int64) (string, string, string) {
	t.Helper()
	sourceTree, err := filepath.Abs(sourceTree)
	if err != nil {
		t.Fatalf("resolve source CAS tree: %v", err)
	}
	if info, err := os.Stat(sourceTree); err != nil || !info.IsDir() {
		t.Fatalf("source CAS tree %s is not an existing directory", sourceTree)
	}
	sourceManifest, err = pathsafe.ResolveExistingUnderRoot(sourceTree, sourceManifest)
	if err != nil {
		t.Fatalf("resolve source manifest under CAS tree: %v", err)
	}
	mf, err := os.Open(sourceManifest)
	if err != nil {
		t.Fatalf("open source manifest: %v", err)
	}
	defer mf.Close()

	stagedTree := mkdirT(t, filepath.Join(env.importRoot, "real-115-existing-cas"))
	stagedManifest := filepath.Join(stagedTree, "manifest.jsonl")
	out, err := os.OpenFile(stagedManifest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create staged manifest: %v", err)
	}
	defer out.Close()

	scanner := bufio.NewScanner(mf)
	const maxManifestLine = 1024 * 1024
	scanner.Buffer(make([]byte, 64*1024), maxManifestLine)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		item, ok := parseStageableManifestItem(t, line, maxSize)
		if !ok {
			continue
		}
		srcCAS, err := pathsafe.ResolveExistingUnderRoot(sourceTree, filepath.FromSlash(item.RelPath)+".cas")
		if err != nil {
			t.Fatalf("resolve source CAS for %q: %v", item.RelPath, err)
		}
		dstCAS := filepath.Join(stagedTree, filepath.FromSlash(item.RelPath)+".cas")
		copyRegularFile(t, srcCAS, dstCAS)
		if _, err := out.Write(append(append([]byte(nil), line...), '\n')); err != nil {
			t.Fatalf("write staged manifest: %v", err)
		}
		if err := out.Close(); err != nil {
			t.Fatalf("close staged manifest: %v", err)
		}
		return stagedTree, stagedManifest, item.RelPath
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan source manifest: %v", err)
	}
	t.Fatalf("source manifest contains no file item with 0 < size <= %d", maxSize)
	return "", "", ""
}

type stageableManifestItem struct {
	RelPath string
	Size    int64
}

func parseStageableManifestItem(t *testing.T, line []byte, maxSize int64) (stageableManifestItem, bool) {
	t.Helper()
	var rec struct {
		RelPath string `json:"rel_path"`
		Path    string `json:"path"`
		Size    *int64 `json:"size"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("decode source manifest line: %v", err)
	}
	relPath := rec.RelPath
	if relPath == "" {
		relPath = rec.Path
	}
	if relPath == "" || rec.Size == nil || *rec.Size <= 0 || *rec.Size > maxSize {
		return stageableManifestItem{}, false
	}
	if err := pathsafe.ValidateRelPath(relPath); err != nil {
		t.Fatalf("source manifest rel_path %q is unsafe: %v", relPath, err)
	}
	return stageableManifestItem{RelPath: relPath, Size: *rec.Size}, true
}

func copyRegularFile(t *testing.T, src, dst string) {
	t.Helper()
	info, err := os.Lstat(src)
	if err != nil {
		t.Fatalf("stat source file: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("source file %s is not regular", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir staged CAS parent: %v", err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open source file: %v", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create staged file: %v", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy staged file: %v", err)
	}
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

func parsePositiveIntEnv(t *testing.T, key string) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		t.Fatalf("%s: invalid positive integer %q", key, raw)
	}
	return n
}

func parsePositiveInt64Env(t *testing.T, key string, def int64) int64 {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		t.Fatalf("%s: invalid positive integer %q", key, raw)
	}
	return n
}
