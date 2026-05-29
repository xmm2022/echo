package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/castree"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestRunManualIngestsRestoredItemAndWritesEcho(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	item := castree.Item{
		RelPath:  "season/episode.mkv",
		Name:     "episode.mkv",
		Size:     12,
		SHA1:     "AABB",
		MD5:      "DDCC",
		Provider: "115",
	}
	fixture.writeManifestAndCAS(t, item, []byte("cas-payload"))

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored, CloudPath: "/movies/season/episode.mkv"}}
	if err := RunManual(ctx, fixture.job, Deps{
		Store:   fixture.store,
		Sidecar: fake,
		Config:  Config{WorkerPerJob: 1},
		Now:     fixedNow,
	}); err != nil {
		t.Fatal(err)
	}

	if fake.count() != 1 {
		t.Fatalf("PutCAS calls = %d, want 1", fake.count())
	}
	req := fake.requests()[0]
	if req.StorageMount != "/115-main" || req.RemoteDir != "/movies/season" || req.CASName != "episode.mkv.cas" {
		t.Fatalf("unexpected PutCAS request: %#v", req)
	}
	if req.CASSize != int64(len("cas-payload")) {
		t.Fatalf("CASSize = %d, want %d", req.CASSize, len("cas-payload"))
	}

	copy, err := fixture.store.GetFileCopyByRemotePath(ctx, queries.GetFileCopyByRemotePathParams{
		SidecarID:    "default",
		StorageMount: "/115-main",
		RemotePath:   "/movies/season/episode.mkv",
	})
	if err != nil {
		t.Fatalf("get file copy: %v", err)
	}
	if copy.Status != "live" {
		t.Fatalf("copy status = %q, want live", copy.Status)
	}
	entry, err := fixture.store.GetLibraryEntry(ctx, queries.GetLibraryEntryParams{
		LibraryID: fixture.library.ID,
		RelPath:   item.RelPath,
	})
	if err != nil {
		t.Fatalf("get library entry: %v", err)
	}
	if entry.BlobID != copy.BlobID || entry.EchoWritten != 1 {
		t.Fatalf("entry blob/echo = (%d,%d), copy blob = %d; want echo_written=1", entry.BlobID, entry.EchoWritten, copy.BlobID)
	}

	echoPath := filepath.Join(fixture.library.EchoOutputPath, "season", "episode.mkv.echo")
	body, err := os.ReadFile(echoPath)
	if err != nil {
		t.Fatalf("read echo file: %v", err)
	}
	payload, err := castree.Decode(body)
	if err != nil {
		t.Fatalf("decode echo payload: %v", err)
	}
	if payload.SHA1 != item.SHA1 || payload.MD5 != item.MD5 || payload.Size != item.Size {
		t.Fatalf("payload = %#v, want manifest hashes and size", payload)
	}

	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Current != 1 || progress.Warnings != 0 || len(progress.FailedItems) != 0 {
		t.Fatalf("progress = %#v, want one success", progress)
	}
}

func TestRunManualRejectsInvalidInputs(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	fake := &fakeSidecar{}

	tests := []struct {
		name string
		job  Job
		deps Deps
		want string
	}{
		{name: "nil store", job: fixture.job, deps: Deps{Sidecar: fake}, want: "ingest store is nil"},
		{name: "nil sidecar", job: fixture.job, deps: Deps{Store: fixture.store}, want: "ingest sidecar is nil"},
		{name: "missing library", job: Job{LibraryID: 999, TargetAccount: fixture.account.ID}, deps: Deps{Store: fixture.store, Sidecar: fake}, want: "load library"},
		{name: "missing account", job: Job{LibraryID: fixture.library.ID, TargetAccount: "missing"}, deps: Deps{Store: fixture.store, Sidecar: fake}, want: "load account"},
		{name: "bad target subdir", job: Job{LibraryID: fixture.library.ID, TargetAccount: fixture.account.ID, TargetSubdir: "../x"}, deps: Deps{Store: fixture.store, Sidecar: fake}, want: "invalid target_subdir"},
		{name: "missing manifest", job: Job{LibraryID: fixture.library.ID, TargetAccount: fixture.account.ID, TargetSubdir: "movies", ManifestPath: filepath.Join(t.TempDir(), "missing.jsonl")}, deps: Deps{Store: fixture.store, Sidecar: fake}, want: "open manifest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RunManual(ctx, tt.job, tt.deps)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RunManual error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRunManualUsesDefaultsAndHandlesManifestScanError(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	if err := os.MkdirAll(fixture.job.ManifestPath, 0o755); err != nil {
		t.Fatal(err)
	}
	err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: &fakeSidecar{}})
	if err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("RunManual error = %v, want read manifest error", err)
	}
}

func TestRunManualUsesReturnedMetadataAndUpdatesExistingCopy(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	existingBlob := createBlob(t, ctx, fixture.store, 12, "episode.mkv")
	mustInsertHash(t, ctx, fixture.store, existingBlob.ID, "sha1", "AABB", existingBlob.Size)
	if _, err := fixture.store.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       existingBlob.ID,
		Provider:     fixture.account.Provider,
		AccountID:    fixture.account.ID,
		SidecarID:    fixture.account.SidecarID,
		StorageMount: fixture.account.StorageMount,
		RemotePath:   "/returned/episode.mkv",
		Status:       "dead",
		LastSeen:     1,
	}); err != nil {
		t.Fatalf("insert existing copy: %v", err)
	}

	item := castree.Item{
		RelPath:    "./episode.mkv",
		Name:       "episode.mkv",
		Size:       12,
		SHA1:       "AABB",
		CreateTime: "2023-11-14T22:13:20Z",
	}
	fixture.writeManifestAndCAS(t, item, []byte("cas-payload"))

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{
		Status:    sidecarclient.StatusRestored,
		CloudPath: "returned/episode.mkv",
		Hashes: map[string]string{
			"cloud_file_id": "cloud-1",
			"pickcode":      "pick-1",
		},
	}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}

	copy, err := fixture.store.GetFileCopyByRemotePath(ctx, queries.GetFileCopyByRemotePathParams{
		SidecarID:    fixture.account.SidecarID,
		StorageMount: fixture.account.StorageMount,
		RemotePath:   "/returned/episode.mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if copy.Status != "live" || !copy.CloudFileID.Valid || copy.CloudFileID.String != "cloud-1" || !copy.Pickcode.Valid || copy.Pickcode.String != "pick-1" {
		t.Fatalf("copy = %#v, want live metadata update", copy)
	}
	entry, err := fixture.store.GetLibraryEntry(ctx, queries.GetLibraryEntryParams{LibraryID: fixture.library.ID, RelPath: "episode.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Name != "episode.mkv" || entry.BlobID != existingBlob.ID {
		t.Fatalf("entry = %#v, want normalized rel_path and existing blob", entry)
	}
	blob, err := fixture.store.GetBlob(ctx, queries.GetBlobParams{ID: existingBlob.ID})
	if err != nil {
		t.Fatal(err)
	}
	if blob.SourceMtime.Valid {
		t.Fatalf("source_mtime = %#v, want existing blob unchanged", blob.SourceMtime)
	}
}

func TestResolveCandidateBlobParsesSourceMtimeWhenCreatingBlob(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	tx, q, err := fixture.store.BeginImmediateTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	blobID, _, err := resolveCandidateBlob(ctx, q, castree.Payload{
		Name:       "episode.mkv",
		Size:       12,
		SHA1:       "AABB",
		CreateTime: "2023-11-14T22:13:20Z",
	}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := q.GetBlob(ctx, queries.GetBlobParams{ID: blobID})
	if err != nil {
		t.Fatal(err)
	}
	if !blob.SourceMtime.Valid || blob.SourceMtime.Int64 != 1_700_000_000 {
		t.Fatalf("source_mtime = %#v, want parsed RFC3339 timestamp", blob.SourceMtime)
	}
}

func TestRunManualSkipsDupStatusStillCommits(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	item := castree.Item{RelPath: "episode.mkv", Name: "episode.mkv", Size: 7, SHA1: "SHA1"}
	fixture.writeManifestAndCAS(t, item, []byte("payload"))

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusSkippedDup}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GetFileCopyByRemotePath(ctx, queries.GetFileCopyByRemotePathParams{
		SidecarID:    "default",
		StorageMount: "/115-main",
		RemotePath:   "/movies/episode.mkv",
	}); err != nil {
		t.Fatalf("skipped_dup did not create live copy: %v", err)
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 0 {
		t.Fatalf("warnings = %d, want skipped_dup without warning", progress.Warnings)
	}
}

func TestRunManualRestoreFailedDoesNotFallbackOrWriteDB(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	item := castree.Item{RelPath: "episode.mkv", Name: "episode.mkv", Size: 7, SHA1: "SHA1"}
	fixture.writeManifestAndCAS(t, item, []byte("payload"))

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusFailed, Error: "rapid failed"}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 1 {
		t.Fatalf("PutCAS calls = %d, want only one sidecar call", fake.count())
	}
	if got := countRows(t, fixture.store.DB, "file_copies"); got != 0 {
		t.Fatalf("file_copies rows = %d, want 0", got)
	}
	if got := countRows(t, fixture.store.DB, "library_entries"); got != 0 {
		t.Fatalf("library_entries rows = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.library.EchoOutputPath, "episode.mkv.echo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("echo stat error = %v, want not exist", err)
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 1 || len(progress.FailedItems) != 1 || !strings.Contains(progress.FailedItems[0].Reason, "rapid failed") {
		t.Fatalf("progress = %#v, want failed item", progress)
	}
}

func TestRestoreFailureReasonDefaultsOnNilResult(t *testing.T) {
	if got := restoreFailureReason(nil); got != "sidecar restore failed" {
		t.Fatalf("reason = %q", got)
	}
	if got := restoreFailureReason(&restoreResult{}); got != "sidecar restore failed" {
		t.Fatalf("reason = %q", got)
	}
}

func TestRunManualUnexpectedAndNilSidecarResultsAreItemFailures(t *testing.T) {
	tests := []struct {
		name   string
		result *sidecarclient.ItemResult
		want   string
	}{
		{name: "nil", result: nil, want: "sidecar returned empty result"},
		{name: "failed default reason", result: &sidecarclient.ItemResult{Status: sidecarclient.StatusFailed}, want: "sidecar restore failed"},
		{name: "unexpected status", result: &sidecarclient.ItemResult{Status: "queued"}, want: "unexpected sidecar restore status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newPipelineFixture(t)
			item := castree.Item{RelPath: "episode.mkv", Name: "episode.mkv", Size: 7, SHA1: "SHA1"}
			fixture.writeManifestAndCAS(t, item, []byte("payload"))
			fake := &fakeSidecar{result: tt.result, allowNilResult: tt.result == nil}

			if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
				t.Fatal(err)
			}
			progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
			if progress.Warnings != 1 || !strings.Contains(progress.FailedItems[0].Reason, tt.want) {
				t.Fatalf("progress = %#v, want reason containing %q", progress, tt.want)
			}
		})
	}
}

func TestRunManualMissingCASIsItemFailure(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	item := castree.Item{RelPath: "missing.mkv", Name: "missing.mkv", Size: 7, SHA1: "SHA1"}
	record := map[string]any{"rel_path": item.RelPath, "name": item.Name, "size": item.Size, "sha1": item.SHA1}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.job.ManifestPath, line, 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 0 {
		t.Fatalf("PutCAS calls = %d, want none when CAS missing", fake.count())
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 1 || !strings.Contains(progress.FailedItems[0].Reason, "cas file not found") {
		t.Fatalf("progress = %#v, want missing CAS failed item", progress)
	}
}

func TestRunManualSidecarUnreachableReturnsFatal(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	item := castree.Item{RelPath: "episode.mkv", Name: "episode.mkv", Size: 7, SHA1: "SHA1"}
	fixture.writeManifestAndCAS(t, item, []byte("payload"))

	fake := &fakeSidecar{err: sidecarclient.ErrSidecarUnreachable}
	err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow})
	if !errors.Is(err, sidecarclient.ErrSidecarUnreachable) {
		t.Fatalf("RunManual error = %v, want ErrSidecarUnreachable", err)
	}
}

func TestRunManualCASRestoreHTTPErrorIsItemFailure(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	item := castree.Item{RelPath: "episode.mkv", Name: "episode.mkv", Size: 7, SHA1: "SHA1"}
	fixture.writeManifestAndCAS(t, item, []byte("payload"))

	fake := &fakeSidecar{err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusBadGateway, Method: "PUT", URL: "/api/fs/put"}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 1 || len(progress.FailedItems) != 1 {
		t.Fatalf("progress = %#v, want item failed not fatal", progress)
	}
	if got := countRows(t, fixture.store.DB, "file_copies"); got != 0 {
		t.Fatalf("file_copies rows = %d, want 0", got)
	}
}

func TestRestoreWithSidecarRejectsInvalidTargetSubdir(t *testing.T) {
	result, terminated, err := restoreWithSidecar(context.Background(), &fakeSidecar{}, t.TempDir(), queries.Account{}, castree.Item{RelPath: "episode.mkv"}, "../bad")
	if err != nil || !terminated || result == nil || !strings.Contains(result.Reason, "invalid target_subdir") {
		t.Fatalf("result=%#v terminated=%v err=%v, want invalid target_subdir termination", result, terminated, err)
	}
}

func TestRunManualCopyBlobMismatchStopsBeforeLibraryEntryAndEcho(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	existingBlob := createBlob(t, ctx, fixture.store, 99, "existing.mkv")
	if _, err := fixture.store.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       existingBlob.ID,
		Provider:     "115",
		AccountID:    fixture.account.ID,
		SidecarID:    fixture.account.SidecarID,
		StorageMount: fixture.account.StorageMount,
		RemotePath:   "/movies/episode.mkv",
		Status:       "live",
		LastSeen:     1,
	}); err != nil {
		t.Fatalf("insert existing copy: %v", err)
	}
	item := castree.Item{RelPath: "episode.mkv", Name: "episode.mkv", Size: 7, SHA1: "NEW"}
	fixture.writeManifestAndCAS(t, item, []byte("payload"))

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusSkippedDup, CloudPath: "/movies/episode.mkv"}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, fixture.store.DB, "library_entries"); got != 0 {
		t.Fatalf("library_entries rows = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.library.EchoOutputPath, "episode.mkv.echo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("echo stat error = %v, want not exist", err)
	}
	if !hasConflict(t, fixture.store.DB, "copy_blob_mismatch") {
		t.Fatal("copy_blob_mismatch conflict not recorded")
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 1 || progress.FailedItems[0].Reason != "copy_blob_mismatch" {
		t.Fatalf("progress = %#v, want copy_blob_mismatch failed item", progress)
	}
}

func TestResolveCandidateBlobRecordsMultiBlobConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	oldBlob := createBlob(t, ctx, fixture.store, 10, "old")
	newBlob := createBlobAt(t, ctx, fixture.store, 10, "new", fixedNow().Add(time.Hour).Unix())
	mustInsertHash(t, ctx, fixture.store, oldBlob.ID, "sha1", "AAA", 10)
	mustInsertHash(t, ctx, fixture.store, newBlob.ID, "md5", "BBB", 10)

	tx, q, err := fixture.store.BeginImmediateTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	blobID, conflicts, err := resolveCandidateBlob(ctx, q, castree.Payload{Name: "both", Size: 10, SHA1: "AAA", MD5: "BBB"}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if blobID != oldBlob.ID {
		t.Fatalf("candidate blob = %d, want oldest %d", blobID, oldBlob.ID)
	}
	if len(conflicts) != 1 || conflicts[0].Reason != "hash_multi_blob" {
		t.Fatalf("conflicts = %#v, want hash_multi_blob", conflicts)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !hasConflict(t, fixture.store.DB, "hash_multi_blob") {
		t.Fatal("hash_multi_blob conflict not persisted")
	}
}

func TestWriteBlobHashesRecordsOwnedByOtherConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	candidate := createBlob(t, ctx, fixture.store, 10, "candidate")
	owner := createBlob(t, ctx, fixture.store, 10, "owner")
	mustInsertHash(t, ctx, fixture.store, owner.ID, "sha1", "AAA", 10)

	tx, q, err := fixture.store.BeginImmediateTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	conflicts, err := writeBlobHashes(ctx, q, candidate.ID, castree.Payload{Name: "x", Size: 10, SHA1: "AAA"}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].Reason != "hash_owned_by_other_blob" {
		t.Fatalf("conflicts = %#v, want hash_owned_by_other_blob", conflicts)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !hasConflict(t, fixture.store.DB, "hash_owned_by_other_blob") {
		t.Fatal("hash_owned_by_other_blob conflict not persisted")
	}
}

func TestWriteBlobHashesIsIdempotentForCandidateOwner(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	candidate := createBlob(t, ctx, fixture.store, 10, "candidate")
	mustInsertHash(t, ctx, fixture.store, candidate.ID, "sha1", "AAA", 10)

	tx, q, err := fixture.store.BeginImmediateTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	conflicts, err := writeBlobHashes(ctx, q, candidate.ID, castree.Payload{Name: "x", Size: 10, SHA1: "AAA"}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none for idempotent owner", conflicts)
	}
}

func TestRunManualConcurrentSameHashDoesNotLeakUniqueError(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	items := make([]castree.Item, 50)
	for i := range items {
		items[i] = castree.Item{
			RelPath: fmt.Sprintf("same/hash-%02d.mkv", i),
			Name:    fmt.Sprintf("hash-%02d.mkv", i),
			Size:    5,
			SHA1:    "SAMEHASH",
		}
	}
	fixture.writeManifestAndCAS(t, items, []byte("payload"))

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 4}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 0 || progress.Current != len(items) {
		t.Fatalf("progress = %#v, want all successes", progress)
	}
	if got := countRows(t, fixture.store.DB, "blob_hashes"); got != 1 {
		t.Fatalf("blob_hashes rows = %d, want 1 canonical hash", got)
	}
	if got := countRows(t, fixture.store.DB, "blobs"); got != 1 {
		t.Fatalf("blobs rows = %d, want one deduped blob", got)
	}
}

func TestRunManualConcurrentRemoteMismatchRecordsOneLiveCopy(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	items := []castree.Item{
		{RelPath: "a/episode.mkv", Name: "episode.mkv", Size: 5, SHA1: "HASH-A"},
		{RelPath: "b/episode.mkv", Name: "episode.mkv", Size: 6, SHA1: "HASH-B"},
	}
	fixture.writeManifestAndCAS(t, items, []byte("payload"))

	fake := &fakeSidecar{resultFunc: func(req sidecarclient.PutCASRequest) (*sidecarclient.ItemResult, error) {
		return &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored, CloudPath: "/movies/shared/episode.mkv"}, nil
	}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 2}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, fixture.store.DB, "file_copies"); got != 1 {
		t.Fatalf("file_copies rows = %d, want one live copy", got)
	}
	if !hasConflict(t, fixture.store.DB, "copy_blob_mismatch") {
		t.Fatal("copy_blob_mismatch conflict not recorded")
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 1 {
		t.Fatalf("warnings = %d, want one mismatch warning", progress.Warnings)
	}
}

func TestRunManualInvalidRelPathFailsWithoutSidecarOrEchoEscape(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	lines := []string{
		`{"rel_path":"../escape.mkv","name":"escape.mkv","size":1,"sha1":"A"}`,
		`{"rel_path":"/abs.mkv","name":"abs.mkv","size":1,"sha1":"B"}`,
		`{"rel_path":"bad\path.mkv","name":"bad.mkv","size":1,"sha1":"C"}`,
	}
	if err := os.WriteFile(fixture.job.ManifestPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 4}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 0 {
		t.Fatalf("PutCAS calls = %d, want 0", fake.count())
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != len(lines) {
		t.Fatalf("warnings = %d, want %d", progress.Warnings, len(lines))
	}
	if got := countEchoFiles(t, fixture.library.EchoOutputPath); got != 0 {
		t.Fatalf("echo files = %d, want none", got)
	}
}

func TestRunManualMaliciousRelPathFuzzStaysInsideLibrary(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	outside := filepath.Join(filepath.Dir(fixture.library.EchoOutputPath), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	badValues := []string{
		"../escape.mkv",
		"/abs.mkv",
		`bad\path.mkv`,
		"C:/windows.mkv",
		"a/../../escape.mkv",
		"nul\x00byte.mkv",
		"control\nbyte.mkv",
		".",
	}
	var lines []string
	for i := 0; i < 100; i++ {
		rel := badValues[i%len(badValues)]
		record := map[string]any{
			"rel_path": rel,
			"name":     fmt.Sprintf("bad-%03d.mkv", i),
			"size":     1,
			"sha1":     fmt.Sprintf("BAD%03d", i),
		}
		body, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(body))
	}
	if err := os.WriteFile(fixture.job.ManifestPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 4}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 0 {
		t.Fatalf("PutCAS calls = %d, want no sidecar calls for invalid paths", fake.count())
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 100 || len(progress.FailedItems) != 100 {
		t.Fatalf("progress warnings/items = %d/%d, want 100/100", progress.Warnings, len(progress.FailedItems))
	}
	if got := countEchoFiles(t, fixture.library.EchoOutputPath); got != 0 {
		t.Fatalf("echo files under library = %d, want 0", got)
	}
	if got := countEchoFiles(t, outside); got != 0 {
		t.Fatalf("echo files outside library = %d, want 0", got)
	}
}

func TestRunManualSegmentTwoFailureLeavesEchoWrittenZero(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	item := castree.Item{RelPath: "blocked/episode.mkv", Name: "episode.mkv", Size: 7, SHA1: "SHA1"}
	fixture.writeManifestAndCAS(t, item, []byte("payload"))
	if err := os.WriteFile(filepath.Join(fixture.library.EchoOutputPath, "blocked"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	entry, err := fixture.store.GetLibraryEntry(ctx, queries.GetLibraryEntryParams{LibraryID: fixture.library.ID, RelPath: item.RelPath})
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.EchoWritten != 0 {
		t.Fatalf("echo_written = %d, want 0 after fs failure", entry.EchoWritten)
	}
	if got := countRows(t, fixture.store.DB, "file_copies"); got != 1 {
		t.Fatalf("file_copies rows = %d, want DB phase retained", got)
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 1 {
		t.Fatalf("warnings = %d, want segment 2 warning", progress.Warnings)
	}
}

func TestRunManualDedupSkipsSameJobDuplicate(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	item := castree.Item{RelPath: "episode.mkv", Name: "episode.mkv", Size: 7, SHA1: "SHA1"}
	fixture.writeManifestAndCAS(t, []castree.Item{item, item}, []byte("payload"))

	fake := &fakeSidecar{result: &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}}
	if err := RunManual(ctx, fixture.job, Deps{Store: fixture.store, Sidecar: fake, Config: Config{WorkerPerJob: 1}, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 1 {
		t.Fatalf("PutCAS calls = %d, want duplicate skipped before sidecar", fake.count())
	}
	progress := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if progress.Warnings != 0 || progress.Current != 2 {
		t.Fatalf("progress = %#v, want success + skipped duplicate", progress)
	}
}

func TestSmallHelpersCoverDefaults(t *testing.T) {
	if got := payloadFromItem(castree.Item{RelPath: "dir/name.mkv", Size: 1}).Name; got != "name.mkv" {
		t.Fatalf("payload name = %q", got)
	}
	if got := parseSourceMtime("not-a-time"); got.Valid {
		t.Fatalf("invalid source mtime parsed as %#v", got)
	}
	if got := progressInterval(Config{ProgressInterval: 250 * time.Millisecond}); got != 250*time.Millisecond {
		t.Fatalf("progress interval = %v", got)
	}
	if got := mustJSON(func() {}); got != "{}" {
		t.Fatalf("mustJSON unsupported value = %q", got)
	}
	if conflict, err := insertHashConflict(context.Background(), nil, 1, 1, "same", "{}", fixedNow); err != nil || conflict.ID != 0 {
		t.Fatalf("same blob conflict = %#v err=%v, want no-op", conflict, err)
	}
}

func TestProgressTrackerSkipsWhenUnboundAndThrottles(t *testing.T) {
	ctx := context.Background()
	if err := newProgressTracker(nil, 0, 1, time.Hour, fixedNow).Done(ctx, "noop"); err != nil {
		t.Fatalf("unbound progress tracker returned error: %v", err)
	}

	fixture := newPipelineFixture(t)
	tick := fixedNow()
	now := func() time.Time { return tick }
	progress := newProgressTracker(fixture.store, fixture.job.ID, 2, time.Hour, now)
	if err := progress.Done(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	first := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if first.Current != 1 {
		t.Fatalf("first progress = %#v", first)
	}
	if err := progress.Done(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	stillFirst := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if stillFirst.Current != 1 {
		t.Fatalf("progress should be throttled, got %#v", stillFirst)
	}
	tick = tick.Add(2 * time.Hour)
	if err := progress.Flush(ctx, false); err != nil {
		t.Fatal(err)
	}
	second := readProgress(t, ctx, fixture.store, fixture.job.ID)
	if second.Current != 2 {
		t.Fatalf("second progress = %#v", second)
	}
}

type pipelineFixture struct {
	store   *store.Store
	library queries.Library
	account queries.Account
	job     Job
}

func newPipelineFixture(t *testing.T) *pipelineFixture {
	t.Helper()
	ctx := context.Background()
	st := openTestStore(t)
	outputRoot := filepath.Join(t.TempDir(), "out")
	casRoot := filepath.Join(t.TempDir(), "cas")
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(casRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	account := createAccount(t, ctx, st)
	library := createLibrary(t, ctx, st, outputRoot)
	jobRow, err := st.CreateJob(ctx, queries.CreateJobParams{
		Kind:      "ingest_manual",
		Status:    "running",
		Payload:   `{}`,
		OwnerID:   "admin",
		CreatedAt: fixedNow().Unix(),
		StartedAt: sql.NullInt64{Int64: fixedNow().Unix(), Valid: true},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return &pipelineFixture{
		store:   st,
		library: library,
		account: account,
		job: Job{
			ID:            jobRow.ID,
			LibraryID:     library.ID,
			TargetAccount: account.ID,
			TargetSubdir:  "movies",
			CASTreePath:   casRoot,
			ManifestPath:  filepath.Join(casRoot, "manifest.jsonl"),
			OwnerID:       "admin",
		},
	}
}

func (f *pipelineFixture) writeManifestAndCAS(t *testing.T, items any, casPayload []byte) {
	t.Helper()
	var list []castree.Item
	switch v := items.(type) {
	case castree.Item:
		list = []castree.Item{v}
	case []castree.Item:
		list = v
	default:
		t.Fatalf("unsupported manifest item type %T", items)
	}
	var lines []string
	for _, item := range list {
		record := map[string]any{
			"rel_path":    item.RelPath,
			"name":        item.Name,
			"size":        item.Size,
			"sha1":        item.SHA1,
			"preID":       item.PreID,
			"md5":         item.MD5,
			"sliceMd5":    item.SliceMD5,
			"sha256":      item.SHA256,
			"create_time": item.CreateTime,
			"provider":    item.Provider,
		}
		body, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(body))
		casPath := filepath.Join(f.job.CASTreePath, filepath.FromSlash(item.RelPath+".cas"))
		if err := os.MkdirAll(filepath.Dir(casPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(casPath, casPayload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.job.ManifestPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

type fakeSidecar struct {
	mu             sync.Mutex
	result         *sidecarclient.ItemResult
	err            error
	resultFunc     func(sidecarclient.PutCASRequest) (*sidecarclient.ItemResult, error)
	calls          []sidecarclient.PutCASRequest
	allowNilResult bool
}

func (f *fakeSidecar) PutCAS(ctx context.Context, req sidecarclient.PutCASRequest) (*sidecarclient.ItemResult, error) {
	body, err := io.ReadAll(req.CASBody)
	if err != nil {
		return nil, err
	}
	req.CASBody = strings.NewReader(string(body))
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.resultFunc != nil {
		return f.resultFunc(req)
	}
	if f.err != nil {
		return nil, f.err
	}
	result := f.result
	if result == nil && f.allowNilResult {
		return nil, nil
	}
	if result == nil {
		result = &sidecarclient.ItemResult{Status: sidecarclient.StatusRestored}
	}
	return result, nil
}

func (f *fakeSidecar) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSidecar) requests() []sidecarclient.PutCASRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sidecarclient.PutCASRequest, len(f.calls))
	copy(out, f.calls)
	return out
}
