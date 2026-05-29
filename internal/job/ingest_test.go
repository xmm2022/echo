package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmm2022/echo/internal/castree"
	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

// fakeSidecar implements ingest.Sidecar and accepts every CAS restore.
type fakeSidecar struct {
	calls chan sidecarclient.PutCASRequest
}

func newFakeSidecar() *fakeSidecar {
	return &fakeSidecar{calls: make(chan sidecarclient.PutCASRequest, 64)}
}

func (f *fakeSidecar) PutCAS(_ context.Context, req sidecarclient.PutCASRequest) (*sidecarclient.ItemResult, error) {
	f.calls <- req
	return &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}, nil
}

func createTestAccount(t *testing.T, ctx context.Context, st *store.Store) queries.Account {
	t.Helper()
	account := queries.Account{
		ID:           "115-main",
		Provider:     "115",
		SidecarID:    "default",
		StorageMount: "/115-main",
		Status:       "ok",
		OwnerID:      "admin",
		CreatedAt:    fixedNow().Unix(),
		UpdatedAt:    fixedNow().Unix(),
	}
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           account.ID,
		Provider:     account.Provider,
		SidecarID:    account.SidecarID,
		StorageMount: account.StorageMount,
		Status:       account.Status,
		OwnerID:      account.OwnerID,
		CreatedAt:    account.CreatedAt,
		UpdatedAt:    account.UpdatedAt,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return account
}

func createTestLibrary(t *testing.T, ctx context.Context, st *store.Store) queries.Library {
	t.Helper()
	outputRoot := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	library, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "media",
		EchoOutputKind: "local",
		EchoOutputPath: outputRoot,
		OwnerID:        "admin",
		CreatedAt:      fixedNow().Unix(),
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	return library
}

// buildManualTree writes a one-item manifest plus its .cas file in a fresh
// directory and returns the cas root and manifest path.
func buildManualTree(t *testing.T, item castree.Item) (string, string) {
	t.Helper()
	root := t.TempDir()
	record := map[string]any{
		"rel_path": item.RelPath,
		"name":     item.Name,
		"size":     item.Size,
		"sha1":     item.SHA1,
		"md5":      item.MD5,
		"provider": item.Provider,
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.jsonl")
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	casPath := filepath.Join(root, filepath.FromSlash(item.RelPath+".cas"))
	if err := os.MkdirAll(filepath.Dir(casPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(casPath, []byte("cas-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, manifestPath
}

func TestPayloadToIngestJob(t *testing.T) {
	payload := IngestPayload{
		LibraryID:     7,
		TargetAccount: "115-main",
		TargetSubdir:  "movies",
		CASTreePath:   "/work/cas",
		ManifestPath:  "/work/cas/manifest.jsonl",
		Tool:          "115share2cas",
		Args:          map[string]any{"share_url": "https://example/x"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	job := queries.Job{ID: 42, Kind: KindIngestProducer, Payload: string(body), OwnerID: "admin"}

	got, err := payloadToIngestJob(job)
	if err != nil {
		t.Fatalf("payloadToIngestJob: %v", err)
	}
	if got.ID != 42 {
		t.Errorf("ID = %d, want 42", got.ID)
	}
	if got.LibraryID != 7 || got.TargetAccount != "115-main" || got.TargetSubdir != "movies" {
		t.Errorf("ingest job = %#v", got)
	}
	if got.CASTreePath != "/work/cas" || got.ManifestPath != "/work/cas/manifest.jsonl" {
		t.Errorf("paths = %q / %q", got.CASTreePath, got.ManifestPath)
	}
	if got.Tool != "115share2cas" || got.Args["share_url"] != "https://example/x" {
		t.Errorf("tool/args = %q / %#v", got.Tool, got.Args)
	}
	if got.OwnerID != "admin" {
		t.Errorf("owner = %q, want admin", got.OwnerID)
	}
}

func TestIngestHandlersBadPayloadFailsJob(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	deps := ingest.Deps{
		Store:   st,
		Sidecar: newFakeSidecar(),
		Config:  ingest.Config{WorkerPerJob: 1},
		Now:     fixedNow,
	}
	r, err := New(Config{Store: st, MaxConcurrent: 1, Now: fixedNow, Handlers: IngestHandlers(deps)})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.Stop()

	jobID, err := r.Enqueue(ctx, KindIngestManual, "{not valid json")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	failed := waitForJobStatus(t, ctx, st, jobID, "failed")
	if !failed.Error.Valid || !strings.Contains(failed.Error.String, "payload") {
		t.Errorf("failed job error = %+v, want a payload decode error", failed.Error)
	}
}

func TestIngestProducerHandlerFailsForUnconfiguredTool(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	account := createTestAccount(t, ctx, st)
	library := createTestLibrary(t, ctx, st)

	// No producer tools configured, so RunProducer rejects the job before any
	// subprocess is spawned. This exercises the producer handler wiring.
	deps := ingest.Deps{
		Store:   st,
		Sidecar: newFakeSidecar(),
		Config:  ingest.Config{WorkerPerJob: 1},
		Now:     fixedNow,
	}
	r, err := New(Config{Store: st, MaxConcurrent: 1, Now: fixedNow, Handlers: IngestHandlers(deps)})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.Stop()

	jobID, err := r.Enqueue(ctx, KindIngestProducer, IngestPayload{
		LibraryID: library.ID, TargetAccount: account.ID, TargetSubdir: "movies",
		Tool: "115share2cas", Args: map[string]any{"share_url": "https://example/x"},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	failed := waitForJobStatus(t, ctx, st, jobID, "failed")
	if !failed.Error.Valid || !strings.Contains(failed.Error.String, "115share2cas") {
		t.Errorf("failed job error = %+v, want it to mention the tool", failed.Error)
	}
}

func TestRunnerRunsManualIngestEndToEnd(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	account := createTestAccount(t, ctx, st)
	library := createTestLibrary(t, ctx, st)

	root1, manifest1 := buildManualTree(t, castree.Item{RelPath: "season/ep1.mkv", Name: "ep1.mkv", Size: 11, SHA1: "AA11", Provider: "115"})
	root2, manifest2 := buildManualTree(t, castree.Item{RelPath: "season/ep2.mkv", Name: "ep2.mkv", Size: 11, SHA1: "BB22", Provider: "115"})

	fake := newFakeSidecar()
	deps := ingest.Deps{
		Store:   st,
		Sidecar: fake,
		Config:  ingest.Config{WorkerPerJob: 2},
		Now:     fixedNow,
	}
	r, err := New(Config{Store: st, MaxConcurrent: 2, Now: fixedNow, Handlers: IngestHandlers(deps)})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.Stop()

	id1, err := r.Enqueue(ctx, KindIngestManual, IngestPayload{
		LibraryID: library.ID, TargetAccount: account.ID, TargetSubdir: "movies",
		CASTreePath: root1, ManifestPath: manifest1,
	})
	if err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	id2, err := r.Enqueue(ctx, KindIngestManual, IngestPayload{
		LibraryID: library.ID, TargetAccount: account.ID, TargetSubdir: "movies",
		CASTreePath: root2, ManifestPath: manifest2,
	})
	if err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}

	for _, id := range []int64{id1, id2} {
		job := waitForJobStatus(t, ctx, st, id, "done")
		progress, ok, err := ParseProgress(job.Progress)
		if err != nil || !ok {
			t.Fatalf("job %d progress: ok=%v err=%v", id, ok, err)
		}
		if progress.Current != 1 || progress.Warnings != 0 || len(progress.FailedItems) != 0 {
			t.Errorf("job %d progress = %#v, want one success", id, progress)
		}
	}

	// Each job restored exactly one CAS file.
	if got := len(fake.calls); got != 2 {
		t.Errorf("PutCAS calls = %d, want 2", got)
	}

	for _, name := range []string{"ep1.mkv", "ep2.mkv"} {
		echoPath := filepath.Join(library.EchoOutputPath, "season", name+".echo")
		if _, err := os.Stat(echoPath); err != nil {
			t.Errorf("expected echo file %s: %v", echoPath, err)
		}
	}
}
