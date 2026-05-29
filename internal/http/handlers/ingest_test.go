package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

type ingestFixture struct {
	deps       APIDeps
	jobs       *fakeJobs
	store      *store.Store
	importRoot string
	casTree    string
	manifest   string
	libraryID  int64
}

func newIngestFixture(t *testing.T) ingestFixture {
	t.Helper()
	ctx := context.Background()
	st := newAPIStore(t)

	lib, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name: "media", EchoOutputKind: "local", EchoOutputPath: "/tmp/out", OwnerID: "admin", CreatedAt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID: "115-main", Provider: "115", SidecarID: "default", StorageMount: "/115-main",
		Status: "ok", OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	importRoot := t.TempDir()
	casTree := filepath.Join(importRoot, "tree")
	if err := os.Mkdir(casTree, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(casTree, "manifest.jsonl")
	if err := os.WriteFile(manifest, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	jobs := &fakeJobs{}
	deps := APIDeps{
		Store: st, Jobs: jobs, Logger: apiLogger(), Now: apiClock(),
		Config: APIConfig{
			ManualImportRoots: []string{importRoot},
			Producer:          producerHandlerConfig(),
		},
	}
	return ingestFixture{deps: deps, jobs: jobs, store: st, importRoot: importRoot, casTree: casTree, manifest: manifest, libraryID: lib.ID}
}

func producerHandlerConfig() ingest.ProducerConfig {
	return ingest.ProducerConfig{
		Tools: map[string]ingest.ProducerToolConfig{
			"115share2cas": {
				Binary: "/usr/local/bin/115share2cas",
				APIArgsAllowlist: []string{
					"share_url", "share_code", "receive_code", "cookie_file", "mode",
					"batch_size", "temp_parent_cid", "recycle_password_file", "keep_temp", "limit",
				},
			},
		},
	}
}

func (f ingestFixture) postManual(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, http.MethodPost, "/api/ingest/manual", body, func(r chi.Router) {
		r.Post("/api/ingest/manual", f.deps.IngestManual)
	})
}

func TestIngestManualHappyPath(t *testing.T) {
	f := newIngestFixture(t)
	body := `{"library_id":` + itoa(f.libraryID) + `,"target_account":"115-main","cas_tree_path":"` + f.casTree + `","manifest_path":"` + f.manifest + `"}`
	rec := f.postManual(t, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		JobID int64 `json:"job_id"`
	}
	decodeBody(t, rec, &resp)
	if resp.JobID == 0 {
		t.Fatal("job_id missing")
	}
	if len(f.jobs.enqueued) != 1 || f.jobs.enqueued[0].kind != job.KindIngestManual {
		t.Fatalf("enqueued = %+v, want one ingest_manual", f.jobs.enqueued)
	}
	payload, ok := f.jobs.enqueued[0].payload.(job.IngestPayload)
	if !ok {
		t.Fatalf("payload type = %T, want job.IngestPayload", f.jobs.enqueued[0].payload)
	}
	if !sameDir(t, payload.CASTreePath, f.casTree) {
		t.Fatalf("payload cas_tree = %q, want resolve of %q", payload.CASTreePath, f.casTree)
	}
	if !sameFile(t, payload.ManifestPath, f.manifest) {
		t.Fatalf("payload manifest = %q, want resolve of %q", payload.ManifestPath, f.manifest)
	}
}

func TestIngestManualRejectsPathTraversal(t *testing.T) {
	f := newIngestFixture(t)

	// symlink inside the import root pointing outside it
	treeSymlink := filepath.Join(f.importRoot, "evil-tree")
	if err := os.Symlink("/etc", treeSymlink); err != nil {
		t.Fatal(err)
	}
	// symlink inside the cas tree pointing at a file outside it
	manifestSymlink := filepath.Join(f.casTree, "m.link")
	if err := os.Symlink("/etc/hostname", manifestSymlink); err != nil {
		t.Fatal(err)
	}
	// a real file in the import root but OUTSIDE the cas tree
	outsideManifest := filepath.Join(f.importRoot, "outside.jsonl")
	if err := os.WriteFile(outsideManifest, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lib := itoa(f.libraryID)
	cases := map[string]string{
		"manifest etc passwd": `{"library_id":` + lib + `,"target_account":"115-main","cas_tree_path":"` + f.casTree + `","manifest_path":"/etc/passwd"}`,
		"cas tree symlink":    `{"library_id":` + lib + `,"target_account":"115-main","cas_tree_path":"` + treeSymlink + `","manifest_path":"` + f.manifest + `"}`,
		"manifest symlink":    `{"library_id":` + lib + `,"target_account":"115-main","cas_tree_path":"` + f.casTree + `","manifest_path":"` + manifestSymlink + `"}`,
		"manifest outside":    `{"library_id":` + lib + `,"target_account":"115-main","cas_tree_path":"` + f.casTree + `","manifest_path":"` + outsideManifest + `"}`,
		"cas tree not in root": `{"library_id":` + lib + `,"target_account":"115-main","cas_tree_path":"/tmp","manifest_path":"` + f.manifest + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := f.postManual(t, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if len(f.jobs.enqueued) != 0 {
		t.Fatalf("no job should be enqueued on rejection, got %+v", f.jobs.enqueued)
	}
}

func TestIngestManualValidatesReferences(t *testing.T) {
	f := newIngestFixture(t)
	lib := itoa(f.libraryID)
	cases := map[string]string{
		"missing library": `{"library_id":99999,"target_account":"115-main","cas_tree_path":"` + f.casTree + `","manifest_path":"` + f.manifest + `"}`,
		"missing account": `{"library_id":` + lib + `,"target_account":"nope","cas_tree_path":"` + f.casTree + `","manifest_path":"` + f.manifest + `"}`,
		"missing paths":   `{"library_id":` + lib + `,"target_account":"115-main"}`,
		"bad json":        `{not json`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := f.postManual(t, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIngestProducerHappyAndValidation(t *testing.T) {
	f := newIngestFixture(t)
	reg := func(r chi.Router) { r.Post("/api/ingest/producer", f.deps.IngestProducer) }
	lib := itoa(f.libraryID)

	good := `{"library_id":` + lib + `,"target_account":"115-main","tool":"115share2cas","args":{"share_url":"https://115.com/s/x","mode":"direct"}}`
	rec := doReq(t, http.MethodPost, "/api/ingest/producer", good, reg)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if len(f.jobs.enqueued) != 1 || f.jobs.enqueued[0].kind != job.KindIngestProducer {
		t.Fatalf("enqueued = %+v, want one ingest_producer", f.jobs.enqueued)
	}

	badTool := `{"library_id":` + lib + `,"target_account":"115-main","tool":"rm","args":{"share_url":"x"}}`
	if rec := doReq(t, http.MethodPost, "/api/ingest/producer", badTool, reg); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad tool = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	missingLib := `{"library_id":99999,"target_account":"115-main","tool":"115share2cas","args":{"share_url":"x","mode":"direct"}}`
	if rec := doReq(t, http.MethodPost, "/api/ingest/producer", missingLib, reg); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing library = %d, want 400", rec.Code)
	}
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}
