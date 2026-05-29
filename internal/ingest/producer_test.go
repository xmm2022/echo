package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	ingestexec "github.com/xmm2022/echo/internal/ingest/exec"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestRunProducerRejectsUnauthorizedToolAndFlag(t *testing.T) {
	ctx := context.Background()
	fixture := newProducerFixture(t)

	tests := []struct {
		name string
		job  Job
	}{
		{
			name: "cas139 not allowed",
			job:  fixture.jobWithArgs("cas139", map[string]any{"share_url": "https://115.com/s/x"}),
		},
		{
			name: "unknown flag not allowed",
			job: fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url": "https://115.com/s/x",
				"page_size": 100,
			}),
		},
		{
			name: "echo injected flag rejected",
			job: fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url": "https://115.com/s/x",
				"manifest":  "/tmp/manifest.jsonl",
			}),
		},
		{
			name: "missing required combination",
			job:  fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{"share_code": "abc"}),
		},
		{
			name: "transfer batch requires cookie",
			job: fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url":             "https://115.com/s/x",
				"recycle_password_file": "ref:recycle.txt",
			}),
		},
		{
			name: "transfer batch requires recycle password without keep temp",
			job: fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url":   "https://115.com/s/x",
				"cookie_file": "ref:cookie.txt",
			}),
		},
		{
			name: "invalid limit type",
			job: fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url":   "https://115.com/s/x",
				"cookie_file": "ref:cookie.txt",
				"keep_temp":   true,
				"limit":       "not-int",
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RunProducer(ctx, tt.job, fixture.deps(&recordingRunner{}))
			if !errors.Is(err, ErrProducerUnauthorized) {
				t.Fatalf("RunProducer error = %v, want ErrProducerUnauthorized", err)
			}
			runs, err := fixture.store.ListProducerRunsByJob(ctx, queries.ListProducerRunsByJobParams{JobID: tt.job.ID})
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 0 {
				t.Fatalf("producer_runs = %d, want 0", len(runs))
			}
		})
	}
}

func TestProducerArgTypeValidationHappensBeforeWorkdirCreation(t *testing.T) {
	fixture := newProducerFixture(t)
	job := fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
		"share_url":   "https://115.com/s/x",
		"cookie_file": "ref:cookie.txt",
		"keep_temp":   true,
		"limit":       "not-int",
	})
	err := RunProducer(context.Background(), job, fixture.deps(&recordingRunner{}))
	if !errors.Is(err, ErrProducerUnauthorized) {
		t.Fatalf("RunProducer error = %v, want ErrProducerUnauthorized", err)
	}
	if _, statErr := os.Lstat(filepath.Join(fixture.workdirRoot, fmt.Sprintf("job-%d", job.ID))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("workdir stat error = %v, want not exist", statErr)
	}
}

func TestRunProducerRejectsUnsafeRefsAndWorkdirEscape(t *testing.T) {
	ctx := context.Background()
	fixture := newProducerFixture(t)
	if err := os.Symlink("/etc", filepath.Join(fixture.secretsRoot, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(fixture.workdirRoot, "job-99")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		job  Job
	}{
		{
			name: "parent ref",
			job: fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url":   "https://115.com/s/x",
				"cookie_file": "ref:../cookie.txt",
				"keep_temp":   true,
			}),
		},
		{
			name: "absolute ref",
			job: fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url":   "https://115.com/s/x",
				"cookie_file": "ref:/etc/passwd",
				"keep_temp":   true,
			}),
		},
		{
			name: "symlink escaped ref",
			job: fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url":   "https://115.com/s/x",
				"cookie_file": "ref:link/passwd",
				"keep_temp":   true,
			}),
		},
		{
			name: "workdir symlink fixture",
			job: Job{
				ID:            99,
				LibraryID:     fixture.library.ID,
				TargetAccount: fixture.account.ID,
				TargetSubdir:  "movies",
				Tool:          producerTool115Share2CAS,
				Args: map[string]any{
					"share_url":   "https://115.com/s/x",
					"cookie_file": "ref:cookie.txt",
					"keep_temp":   true,
				},
				OwnerID: "admin",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RunProducer(ctx, tt.job, fixture.deps(&recordingRunner{}))
			if !errors.Is(err, ErrProducerUnauthorized) {
				t.Fatalf("RunProducer error = %v, want ErrProducerUnauthorized", err)
			}
		})
	}
}

func TestRunProducerRejectsProducerReplacedOutputs(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		runFunc func(ingestexec.Command) error
	}{
		{
			name: "cas symlink",
			runFunc: func(cmd ingestexec.Command) error {
				outputDir := argValue(cmd.Args, "--out")
				manifestPath := argValue(cmd.Args, "--manifest")
				outsideDir := filepath.Join(filepath.Dir(filepath.Dir(outputDir)), "outside")
				if err := os.MkdirAll(outsideDir, 0o755); err != nil {
					return err
				}
				if err := os.Remove(outputDir); err != nil {
					return err
				}
				if err := os.Symlink(outsideDir, outputDir); err != nil {
					return err
				}
				return writeManifestOnly(manifestPath)
			},
		},
		{
			name: "manifest symlink",
			runFunc: func(cmd ingestexec.Command) error {
				manifestPath := argValue(cmd.Args, "--manifest")
				outsidePath := filepath.Join(filepath.Dir(filepath.Dir(manifestPath)), "outside-manifest.jsonl")
				if err := writeManifestOnly(outsidePath); err != nil {
					return err
				}
				return os.Symlink(outsidePath, manifestPath)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newProducerFixture(t)
			runner := &recordingRunner{runFunc: tt.runFunc}
			err := RunProducer(ctx, fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
				"share_url":   "https://115.com/s/x",
				"cookie_file": "ref:cookie.txt",
				"keep_temp":   true,
			}), fixture.deps(runner))
			if !errors.Is(err, ErrProducerUnauthorized) {
				t.Fatalf("RunProducer error = %v, want ErrProducerUnauthorized", err)
			}
			if fixture.sidecar.count() != 0 {
				t.Fatalf("PutCAS calls = %d, want 0", fixture.sidecar.count())
			}
		})
	}
}

func TestPrepareProducerRunBuildsExpectedArgv(t *testing.T) {
	fixture := newProducerFixture(t)

	tests := []struct {
		name    string
		args    map[string]any
		wantMid []string
	}{
		{
			name: "transfer batch share url full args",
			args: map[string]any{
				"share_url":             "https://115.com/s/abc?password=secret",
				"cookie_file":           "ref:cookie.txt",
				"mode":                  "transfer-batch",
				"batch_size":            "1.5TiB",
				"temp_parent_cid":       "0",
				"recycle_password_file": "ref:recycle.txt",
				"limit":                 10,
			},
			wantMid: []string{
				"--share-url", "https://115.com/s/abc?password=secret",
				"--cookie-file", filepath.Join(fixture.secretsRoot, "cookie.txt"),
				"--mode", "transfer-batch",
				"--batch-size", "1.5TiB",
				"--temp-parent-cid", "0",
				"--recycle-password-file", filepath.Join(fixture.secretsRoot, "recycle.txt"),
				"--limit", "10",
			},
		},
		{
			name: "share code keep temp",
			args: map[string]any{
				"share_code":   "sw68x",
				"receive_code": "abc123",
				"cookie_file":  "ref:cookie.txt",
				"keep_temp":    true,
			},
			wantMid: []string{
				"--share-code", "sw68x",
				"--receive-code", "abc123",
				"--cookie-file", filepath.Join(fixture.secretsRoot, "cookie.txt"),
				"--keep-temp",
			},
		},
		{
			name: "direct mode",
			args: map[string]any{
				"share_url": "https://115.com/s/abc",
				"mode":      "direct",
			},
			wantMid: []string{
				"--share-url", "https://115.com/s/abc",
				"--mode", "direct",
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := fixture.jobWithArgs(producerTool115Share2CAS, tt.args)
			job.ID = int64(i + 1)
			spec, err := prepareProducerRun(job, fixture.config.Producer)
			if err != nil {
				t.Fatal(err)
			}
			wantTail := []string{"--out", filepath.Join(spec.Workdir, "cas"), "--manifest", filepath.Join(spec.Workdir, "manifest.jsonl")}
			want := append(append([]string{}, tt.wantMid...), wantTail...)
			if !reflect.DeepEqual(spec.Args, want) {
				t.Fatalf("args = %#v\nwant %#v", spec.Args, want)
			}
		})
	}
}

func TestRunProducerUsesArgSliceAndDoesNotInvokeShell(t *testing.T) {
	ctx := context.Background()
	fixture := newProducerFixture(t)
	runner := &recordingRunner{runFunc: writeOneItemProducerOutput}
	shareURL := "https://115.com/s/x?password=abc;rm -rf /"

	err := RunProducer(ctx, fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
		"share_url":   shareURL,
		"cookie_file": "ref:cookie.txt",
		"keep_temp":   true,
	}), fixture.deps(runner))
	if err != nil {
		t.Fatal(err)
	}

	if runner.calls[0].Path != fixture.binary {
		t.Fatalf("exec path = %q, want configured binary", runner.calls[0].Path)
	}
	if runner.calls[0].Path == "sh" || runner.calls[0].Path == "/bin/sh" {
		t.Fatalf("producer invoked shell: %#v", runner.calls[0])
	}
	if countArg(runner.calls[0].Args, shareURL) != 1 {
		t.Fatalf("share URL with metacharacters not preserved as one argv element: %#v", runner.calls[0].Args)
	}
	if strings.Contains(strings.Join(runner.calls[0].Args, " "), "'"+shareURL+"'") {
		t.Fatalf("argv appears shell-quoted: %#v", runner.calls[0].Args)
	}
}

func TestRunProducerRedactsSecretsInDB(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		args      map[string]any
		forbidden []string
		required  []string
	}{
		{
			name: "url query and secret refs",
			args: map[string]any{
				"share_url":             "https://115.com/s/sw68x?password=abc",
				"cookie_file":           "ref:cookie.txt",
				"mode":                  "transfer-batch",
				"recycle_password_file": "ref:recycle.txt",
			},
			forbidden: []string{"password=abc"},
			required:  []string{"password=<redacted>", "<redacted-secret-path>"},
		},
		{
			name: "receive code",
			args: map[string]any{
				"share_code":            "sw68x",
				"receive_code":          "abc123",
				"cookie_file":           "ref:cookie.txt",
				"mode":                  "transfer-batch",
				"recycle_password_file": "ref:recycle.txt",
			},
			forbidden: []string{"abc123", "--receive-code\",\"abc123"},
			required:  []string{"<redacted>", "<redacted-secret-path>", "sw68x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newProducerFixture(t)
			runner := &recordingRunner{runFunc: writeOneItemProducerOutput}
			if err := RunProducer(ctx, fixture.jobWithArgs(producerTool115Share2CAS, tt.args), fixture.deps(runner)); err != nil {
				t.Fatal(err)
			}

			runs, err := fixture.store.ListProducerRunsByJob(ctx, queries.ListProducerRunsByJobParams{JobID: fixture.job.ID})
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 {
				t.Fatalf("producer run count = %d, want 1", len(runs))
			}
			if !runs[0].ToolVersion.Valid || !strings.HasPrefix(runs[0].ToolVersion.String, "binary_sha256:") {
				t.Fatalf("tool_version = %#v, want binary_sha256", runs[0].ToolVersion)
			}
			cmdline := runs[0].Cmdline
			forbidden := append([]string{
				filepath.Join(fixture.secretsRoot, "cookie.txt"),
				filepath.Join(fixture.secretsRoot, "recycle.txt"),
			}, tt.forbidden...)
			for _, value := range forbidden {
				if strings.Contains(cmdline, value) {
					t.Fatalf("cmdline %q contains forbidden secret %q", cmdline, value)
				}
			}
			for _, value := range tt.required {
				if !strings.Contains(cmdline, value) {
					t.Fatalf("cmdline %q missing %q", cmdline, value)
				}
			}
		})
	}
}

func TestRedactProducerArgvRedactsURLQueryAndReceiveCode(t *testing.T) {
	got := RedactProducerArgv([]string{
		"/bin/115share2cas",
		"--share-url", "https://115.com/s/x?password=abc&sign=s&token=t&signature=sig&receive_code=rc&ok=1#frag",
		"--share-code", "sw68x",
		"--receive-code", "abc123",
		"--cookie-file", "/data/secrets/cookie.txt",
		"--recycle-password-file", "/data/secrets/recycle.txt",
	})
	want := []string{
		"/bin/115share2cas",
		"--share-url", "https://115.com/s/x?password=<redacted>&sign=<redacted>&token=<redacted>&signature=<redacted>&receive_code=<redacted>&ok=1#frag",
		"--share-code", "sw68x",
		"--receive-code", "<redacted>",
		"--cookie-file", "<redacted-secret-path>",
		"--recycle-password-file", "<redacted-secret-path>",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RedactProducerArgv = %#v\nwant %#v", got, want)
	}
}

func TestRunProducerExitFailureDoesNotRunManual(t *testing.T) {
	ctx := context.Background()
	fixture := newProducerFixture(t)
	runner := &recordingRunner{result: ingestexec.Result{ExitCode: 7}, err: fmt.Errorf("exit status 7")}

	err := RunProducer(ctx, fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
		"share_url":   "https://115.com/s/x",
		"cookie_file": "ref:cookie.txt",
		"keep_temp":   true,
	}), fixture.deps(runner))
	if !errors.Is(err, ErrProducerExitFailed) {
		t.Fatalf("RunProducer error = %v, want ErrProducerExitFailed", err)
	}
	if fixture.sidecar.count() != 0 {
		t.Fatalf("PutCAS calls = %d, want 0", fixture.sidecar.count())
	}
	run := mustSingleProducerRun(t, ctx, fixture, fixture.job.ID)
	if !run.ExitCode.Valid || run.ExitCode.Int64 != 7 {
		t.Fatalf("exit_code = %#v, want 7", run.ExitCode)
	}
}

func TestRunProducerTimeoutMarksExitFailed(t *testing.T) {
	ctx := context.Background()
	fixture := newProducerFixture(t)
	runner := &recordingRunner{result: ingestexec.Result{ExitCode: -1, TimedOut: true}, err: context.DeadlineExceeded}

	err := RunProducer(ctx, fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
		"share_url":   "https://115.com/s/x",
		"cookie_file": "ref:cookie.txt",
		"keep_temp":   true,
	}), fixture.deps(runner))
	if !errors.Is(err, ErrProducerExitFailed) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("RunProducer error = %v, want timeout ErrProducerExitFailed", err)
	}
	run := mustSingleProducerRun(t, ctx, fixture, fixture.job.ID)
	if !run.ExitCode.Valid || run.ExitCode.Int64 != -1 || !run.FinishedAt.Valid {
		t.Fatalf("producer run = %#v, want failed run with exit_code -1 and finished_at", run)
	}
}

type producerFixture struct {
	store       *store.Store
	library     queries.Library
	account     queries.Account
	job         Job
	sidecar     *fakeSidecar
	config      Config
	binary      string
	workdirRoot string
	secretsRoot string
}

func newProducerFixture(t *testing.T) *producerFixture {
	t.Helper()
	ctx := context.Background()
	st := openTestStore(t)
	outputRoot := filepath.Join(t.TempDir(), "out")
	workdirRoot := filepath.Join(t.TempDir(), "work")
	secretsRoot := filepath.Join(t.TempDir(), "secrets")
	for _, dir := range []string{outputRoot, workdirRoot, secretsRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(secretsRoot, "cookie.txt"), []byte("cookie"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsRoot, "recycle.txt"), []byte("recycle"), 0o600); err != nil {
		t.Fatal(err)
	}

	account := createAccount(t, ctx, st)
	library := createLibrary(t, ctx, st, outputRoot)
	jobRow, err := st.CreateJob(ctx, queries.CreateJobParams{
		Kind:      "ingest_producer",
		Status:    "running",
		Payload:   `{}`,
		OwnerID:   "admin",
		CreatedAt: fixedNow().Unix(),
		StartedAt: sql.NullInt64{Int64: fixedNow().Unix(), Valid: true},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "115share2cas")
	if err := os.WriteFile(binary, []byte("fake 115share2cas"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := Config{
		WorkerPerJob:     1,
		ProgressInterval: time.Nanosecond,
		Producer: ProducerConfig{
			WorkdirRoot:    workdirRoot,
			SecretsRoot:    secretsRoot,
			DefaultTimeout: time.Minute,
			Tools: map[string]ProducerToolConfig{
				producerTool115Share2CAS: {
					Binary: binary,
					APIArgsAllowlist: []string{
						"share_url",
						"share_code",
						"receive_code",
						"cookie_file",
						"mode",
						"batch_size",
						"temp_parent_cid",
						"recycle_password_file",
						"keep_temp",
						"limit",
					},
				},
			},
		},
	}
	fixture := &producerFixture{
		store:       st,
		library:     library,
		account:     account,
		sidecar:     &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}},
		config:      config,
		binary:      binary,
		workdirRoot: workdirRoot,
		secretsRoot: secretsRoot,
	}
	fixture.job = fixture.jobWithArgs(producerTool115Share2CAS, map[string]any{
		"share_url":   "https://115.com/s/default",
		"cookie_file": "ref:cookie.txt",
		"keep_temp":   true,
	})
	fixture.job.ID = jobRow.ID
	return fixture
}

func (f *producerFixture) jobWithArgs(tool string, args map[string]any) Job {
	return Job{
		ID:            f.job.ID,
		LibraryID:     f.library.ID,
		TargetAccount: f.account.ID,
		TargetSubdir:  "movies",
		OwnerID:       "admin",
		Tool:          tool,
		Args:          args,
	}
}

func (f *producerFixture) deps(runner producerExecRunner) Deps {
	return Deps{
		Store:      f.store,
		Sidecar:    f.sidecar,
		Config:     f.config,
		Now:        fixedNow,
		ExecRunner: runner,
	}
}

type recordingRunner struct {
	calls   []ingestexec.Command
	result  ingestexec.Result
	err     error
	runFunc func(ingestexec.Command) error
}

func (r *recordingRunner) Run(ctx context.Context, cmd ingestexec.Command) (ingestexec.Result, error) {
	r.calls = append(r.calls, cmd)
	if r.runFunc != nil {
		if err := r.runFunc(cmd); err != nil {
			return ingestexec.Result{ExitCode: -1}, err
		}
	}
	return r.result, r.err
}

func writeOneItemProducerOutput(cmd ingestexec.Command) error {
	outputDir := argValue(cmd.Args, "--out")
	manifestPath := argValue(cmd.Args, "--manifest")
	if outputDir == "" || manifestPath == "" {
		return fmt.Errorf("missing --out or --manifest in %#v", cmd.Args)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeManifestOnly(manifestPath); err != nil {
		return err
	}
	casPath := filepath.Join(outputDir, "episode.mkv.cas")
	return os.WriteFile(casPath, []byte("cas-payload"), 0o600)
}

func writeManifestOnly(manifestPath string) error {
	record := map[string]any{
		"rel_path": "episode.mkv",
		"name":     "episode.mkv",
		"size":     int64(12),
		"sha1":     "AABB",
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(line, '\n'), 0o600)
}

func argValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func countArg(args []string, value string) int {
	var count int
	for _, arg := range args {
		if arg == value {
			count++
		}
	}
	return count
}

func mustSingleProducerRun(t *testing.T, ctx context.Context, fixture *producerFixture, jobID int64) queries.ProducerRun {
	t.Helper()
	runs, err := fixture.store.ListProducerRunsByJob(ctx, queries.ListProducerRunsByJobParams{JobID: jobID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("producer run count = %d, want 1", len(runs))
	}
	return runs[0]
}
