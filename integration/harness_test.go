//go:build integration || integration_real

// Package integration holds Echo's end-to-end tests. They assemble the real
// store, sidecar client, job runner, and HTTP handlers (mirroring cmd/echo's
// wiring) behind an httptest server and drive the system over HTTP, exactly as
// an operator would. Build tag `integration` runs against a fake sidecar (no
// network/secrets); `integration_real` (real_115_test.go) talks to a real
// sidecar. Run locally with:
//
//	go test -tags=integration ./integration/...
package integration

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/xmm2022/echo/internal/config"
	httpserver "github.com/xmm2022/echo/internal/http"
	"github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/metrics"
	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/sidecarclient/fakesidecar"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/web"
)

const (
	testAdminToken    = "integration-admin-token"
	testSidecarVer    = "sidecar-test-commit"
	sidecarTokenEnv   = "ECHO_TEST_SIDECAR_TOKEN"
	testStorageMount  = "/115-main" // a mount the fake sidecar advertises
	testStorageProv   = "115"
	defaultJobTimeout = 30 * time.Second
)

// envConfig tunes a test environment. Zero values pick sensible defaults.
type envConfig struct {
	maxConcurrent int
	workerPerJob  int
	// minVersion the sidecar client requires; defaults to the fake's reported
	// version so /readyz passes. Set it different to exercise a version mismatch.
	minVersion string
	fakeOpts   fakesidecar.Options
	// producerBinary, when set, registers the 115share2cas tool pointing at it.
	producerBinary string
	// producerTimeout bounds a producer exec; defaults to 30s. integration_real
	// sets it generously since a real 115 share download is slow.
	producerTimeout time.Duration
	// requestTimeout / streamTimeout override the sidecar client timeouts
	// (defaults 10s each).
	requestTimeout time.Duration
	streamTimeout  time.Duration
	// externalSidecarURL, when set, points the client at a real running sidecar
	// instead of an in-process fake (used by integration_real). The fake is not
	// created in that mode (env.fake is nil). externalSidecarToken is passed as
	// the raw OpenList Authorization token.
	externalSidecarURL   string
	externalSidecarToken string
}

// testEnv is a fully wired Echo instance under test.
type testEnv struct {
	t       *testing.T
	fake    *fakesidecar.Server // nil when an external (real) sidecar is used
	baseURL string

	echoBase    string // root that library echo_output_path must live under
	importRoot  string // a configured manual_import_root
	secretsRoot string // producer secrets_root (integration_real stages a cookie here)
	adminToken  string
}

// newEnv assembles store + sidecar client + job runner + HTTP handlers behind an
// httptest server, mirroring cmd/echo's runServe wiring. Everything is torn down
// via t.Cleanup.
func newEnv(t *testing.T, cfg envConfig) *testEnv {
	t.Helper()

	if cfg.maxConcurrent <= 0 {
		cfg.maxConcurrent = 2
	}
	if cfg.workerPerJob <= 0 {
		cfg.workerPerJob = 2
	}

	root := t.TempDir()
	echoBase := mkdirT(t, filepath.Join(root, "output"))
	importRoot := mkdirT(t, filepath.Join(root, "import"))
	producerRoot := mkdirT(t, filepath.Join(root, "producer"))
	secretsRoot := mkdirT(t, filepath.Join(root, "secrets"))
	dbPath := filepath.Join(root, "echo.db")

	// Sidecar: a real one (integration_real) or an in-process fake.
	var fake *fakesidecar.Server
	var sidecarURL string
	if cfg.externalSidecarURL != "" {
		sidecarURL = cfg.externalSidecarURL
		t.Setenv(sidecarTokenEnv, cfg.externalSidecarToken)
	} else {
		if cfg.fakeOpts.Version == "" {
			cfg.fakeOpts.Version = testSidecarVer
		}
		fake = fakesidecar.New(t, cfg.fakeOpts)
		sidecarURL = fake.URL()
		// The sidecar client reads its raw OpenList Authorization token from this env.
		t.Setenv(sidecarTokenEnv, "sidecar-test-token")
	}

	minVersion := cfg.minVersion
	if minVersion == "" && fake != nil {
		// The client compares against the commit the fake reports (commit falls
		// back to version when unset), so a default env is ready out of the box.
		minVersion = firstNonEmpty(cfg.fakeOpts.Commit, cfg.fakeOpts.Version)
	}

	requestTimeout := cfg.requestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 10 * time.Second
	}
	streamTimeout := cfg.streamTimeout
	if streamTimeout <= 0 {
		streamTimeout = 10 * time.Second
	}
	producerTimeout := cfg.producerTimeout
	if producerTimeout <= 0 {
		producerTimeout = 30 * time.Second
	}

	st, err := store.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))

	registry := prometheus.NewRegistry()
	m := metrics.New(registry)
	m.SetBuildInfo("v0.1.0-test", minVersion)

	rawSidecar := sidecarclient.New(sidecarclient.Config{
		BaseURL:        sidecarURL,
		AuthTokenEnv:   sidecarTokenEnv,
		MinVersion:     minVersion,
		RequestTimeout: config.Duration{Duration: requestTimeout},
		StreamTimeout:  config.Duration{Duration: streamTimeout},
	})
	sidecar := metrics.InstrumentSidecar(rawSidecar, m, "default")

	producerCfg := ingest.ProducerConfig{
		WorkdirRoot:    producerRoot,
		SecretsRoot:    secretsRoot,
		DefaultTimeout: producerTimeout,
	}
	if cfg.producerBinary != "" {
		producerCfg.Tools = map[string]ingest.ProducerToolConfig{
			"115share2cas": {
				Binary: cfg.producerBinary,
				APIArgsAllowlist: []string{
					"share_url", "share_code", "receive_code", "cookie_file", "mode",
					"batch_size", "temp_parent_cid", "recycle_password_file", "keep_temp", "limit",
				},
			},
		}
	}

	ingestCfg := ingest.Config{WorkerPerJob: cfg.workerPerJob, Producer: producerCfg}
	runner, err := job.New(job.Config{
		Store: st,
		Handlers: job.IngestHandlers(ingest.Deps{
			Store: st, Sidecar: sidecar, Config: ingestCfg, Metrics: m, Logger: logger,
		}),
		MaxConcurrent: cfg.maxConcurrent,
		Logger:        logger,
		Metrics:       m,
	})
	if err != nil {
		st.Close()
		t.Fatalf("build job runner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(ctx); err != nil {
		cancel()
		st.Close()
		t.Fatalf("start runner: %v", err)
	}

	resolver := restore.NewResolver(st.Queries, nil)
	cache := restore.NewLinkCache(nil)
	deps := httpserver.Deps{
		Logger:     logger,
		AdminToken: testAdminToken,
		Restore:    &handlers.RestoreDeps{Resolver: resolver, Sidecar: sidecar, Cache: cache, Logger: logger, Metrics: m},
		Stream:     &handlers.StreamDeps{Resolver: resolver, Sidecar: sidecar, Logger: logger, Metrics: m},
		API: &handlers.APIDeps{
			Store:   st,
			Sidecar: sidecar,
			Jobs:    runner,
			Config: handlers.APIConfig{
				ManualImportRoots:   []string{importRoot},
				ProducerWorkdirRoot: producerRoot,
				EchoOutputBasePath:  echoBase,
				Producer:            producerCfg,
			},
			Logger: logger,
		},
		Web:      &web.Deps{Store: st, Logger: logger},
		Ready:    &httpserver.ReadyChecker{DB: st.DB, Sidecar: rawSidecar, Version: "v0.1.0-test"},
		Registry: registry,
	}

	server := httptest.NewServer(httpserver.HandlerWithDeps(deps))

	env := &testEnv{
		t:           t,
		fake:        fake,
		baseURL:     server.URL,
		echoBase:    echoBase,
		importRoot:  importRoot,
		secretsRoot: secretsRoot,
		adminToken:  testAdminToken,
	}

	t.Cleanup(func() {
		server.Close()
		cancel()
		runner.Stop()
		st.Close()
	})
	return env
}

// --- HTTP helpers ---

// do issues an authenticated request to the test server. A non-nil body is JSON
// encoded. The caller closes the returned response body.
func (e *testEnv) do(method, path string, body any) *http.Response {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.baseURL+path, reader)
	if err != nil {
		e.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+e.adminToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("do %s %s: %v", method, path, err)
	}
	return resp
}

// doJSON issues a request, asserts the status code, and decodes the JSON body
// into out (out may be nil to ignore the body).
func (e *testEnv) doJSON(method, path string, body any, wantStatus int, out any) {
	e.t.Helper()
	resp := e.do(method, path, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		e.t.Fatalf("%s %s: status = %d, want %d (body: %s)", method, path, resp.StatusCode, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: decode body %q: %v", method, path, raw, err)
		}
	}
}

// decodeBody reads and closes resp.Body, decoding it into out (when non-nil), and
// returns the raw bytes for diagnostics. Unlike doJSON it does not assert status,
// so callers can inspect the status code themselves.
func decodeBody(t *testing.T, resp *http.Response, out any) []byte {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode response body %q: %v", raw, err)
		}
	}
	return raw
}

// --- response shapes (handler structs are unexported) ---

type accountResp struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	SidecarID    string `json:"sidecar_id"`
	StorageMount string `json:"storage_mount"`
	Status       string `json:"status"`
}

type libraryResp struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	EchoOutputKind string `json:"echo_output_kind"`
	EchoOutputPath string `json:"echo_output_path"`
}

type jobResp struct {
	ID           int64           `json:"id"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	Progress     json.RawMessage `json:"progress,omitempty"`
	Error        string          `json:"error,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	ProducerRuns []producerRun   `json:"producer_runs,omitempty"`
}

type producerRun struct {
	ID       int64  `json:"id"`
	Tool     string `json:"tool"`
	Cmdline  string `json:"cmdline"`
	ExitCode *int64 `json:"exit_code,omitempty"`
}

type entryResp struct {
	ID          int64  `json:"id"`
	RelPath     string `json:"rel_path"`
	Name        string `json:"name"`
	BlobID      int64  `json:"blob_id"`
	EchoWritten bool   `json:"echo_written"`
	LiveCopies  int64  `json:"live_copies"`
}

type restoreResp struct {
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Provider string            `json:"provider"`
	CopyID   int64             `json:"copy_id"`
}

type readyResp struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Checks  map[string]string `json:"checks"`
}

// --- domain helpers ---

// createAccount binds an Echo account to a storage the fake sidecar advertises.
func (e *testEnv) createAccount(id string) accountResp {
	e.t.Helper()
	return e.createAccountOn(id, testStorageProv, testStorageMount)
}

// createAccountOn binds an Echo account to a specific provider + storage mount
// (the mount must already exist on the sidecar). integration_real uses this with
// the real sidecar's mount.
func (e *testEnv) createAccountOn(id, provider, mount string) accountResp {
	e.t.Helper()
	var out accountResp
	e.doJSON(http.MethodPost, "/api/accounts", map[string]string{
		"id":            id,
		"provider":      provider,
		"sidecar_id":    "default",
		"storage_mount": mount,
	}, http.StatusCreated, &out)
	return out
}

// createLibrary makes a fresh output directory under echoBase and registers a
// library writing its .echo files there.
func (e *testEnv) createLibrary(name string) libraryResp {
	e.t.Helper()
	outDir := mkdirT(e.t, filepath.Join(e.echoBase, name))
	var out libraryResp
	e.doJSON(http.MethodPost, "/api/libraries", map[string]string{
		"name":             name,
		"echo_output_path": outDir,
	}, http.StatusCreated, &out)
	return out
}

// casFixture is one manifest entry plus its .cas payload bytes.
type casFixture struct {
	relPath string
	content []byte
}

// writeCASTree writes a CAS tree (<dir>/<relPath>.cas) and manifest.jsonl under a
// fresh subdir of the import root, returning the tree path and manifest path.
func (e *testEnv) writeCASTree(sub string, items []casFixture) (casTree, manifest string) {
	e.t.Helper()
	casTree = mkdirT(e.t, filepath.Join(e.importRoot, sub))
	var buf bytes.Buffer
	for _, it := range items {
		casFile := filepath.Join(casTree, filepath.FromSlash(it.relPath)+".cas")
		if err := os.MkdirAll(filepath.Dir(casFile), 0o755); err != nil {
			e.t.Fatalf("mkdir cas parent: %v", err)
		}
		if err := os.WriteFile(casFile, it.content, 0o644); err != nil {
			e.t.Fatalf("write cas file: %v", err)
		}
		sum := sha1.Sum(it.content)
		line, _ := json.Marshal(map[string]any{
			"rel_path": it.relPath,
			"name":     filepath.Base(it.relPath),
			"size":     len(it.content),
			"sha1":     hex.EncodeToString(sum[:]),
			"provider": testStorageProv,
		})
		buf.Write(line)
		buf.WriteByte('\n')
	}
	manifest = filepath.Join(casTree, "manifest.jsonl")
	if err := os.WriteFile(manifest, buf.Bytes(), 0o644); err != nil {
		e.t.Fatalf("write manifest: %v", err)
	}
	return casTree, manifest
}

// submitManualIngest POSTs a manual ingest job and returns its id. targetSubdir
// must be non-empty (RunManual rejects an empty relative path).
func (e *testEnv) submitManualIngest(libraryID int64, account, targetSubdir, casTree, manifest string) int64 {
	e.t.Helper()
	var out struct {
		JobID int64 `json:"job_id"`
	}
	e.doJSON(http.MethodPost, "/api/ingest/manual", map[string]any{
		"library_id":     libraryID,
		"target_account": account,
		"target_subdir":  targetSubdir,
		"cas_tree_path":  casTree,
		"manifest_path":  manifest,
	}, http.StatusAccepted, &out)
	return out.JobID
}

// waitForJob polls a job until it reaches a terminal status (done/failed) or the
// timeout elapses.
func (e *testEnv) waitForJob(id int64, timeout time.Duration) jobResp {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var jr jobResp
		e.doJSON(http.MethodGet, fmt.Sprintf("/api/jobs/%d", id), nil, http.StatusOK, &jr)
		if jr.Status == "done" || jr.Status == "failed" {
			return jr
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("job %d did not finish within %s (last status %q)", id, timeout, jr.Status)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// listEntries returns a library's entries.
func (e *testEnv) listEntries(libraryID int64) []entryResp {
	e.t.Helper()
	var out []entryResp
	e.doJSON(http.MethodGet, fmt.Sprintf("/api/libraries/%d/entries", libraryID), nil, http.StatusOK, &out)
	return out
}

// runningJobCount returns the number of jobs currently in the running state, or
// -1 on a transient error. It is safe to call from a sampling goroutine: unlike
// the doJSON helpers it never calls t.Fatal (which is illegal off the test
// goroutine), so a sampler can ignore -1 results and keep polling.
func (e *testEnv) runningJobCount() int {
	req, err := http.NewRequest(http.MethodGet, e.baseURL+"/api/jobs?status=running", nil)
	if err != nil {
		return -1
	}
	req.Header.Set("Authorization", "Bearer "+e.adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	var jobs []jobResp
	if err := json.NewDecoder(resp.Body).Decode(&jobs); err != nil {
		return -1
	}
	return len(jobs)
}

// --- small utilities ---

func mkdirT(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
