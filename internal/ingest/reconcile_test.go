package ingest

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmm2022/echo/internal/castree"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestReconcileWritesMissingEchoAndMarksWritten(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	blob := createBlob(t, ctx, fixture.store, 42, "episode.mkv")
	mustInsertHash(t, ctx, fixture.store, blob.ID, "sha1", "ABC", blob.Size)
	entry, err := fixture.store.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   fixture.library.ID,
		RelPath:     "season/episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1,
		UpdatedAt:   1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, fixture.store, slog.New(slog.NewTextHandler(os.Stderr, nil))); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(fixture.library.EchoOutputPath, "season", "episode.mkv.echo"))
	if err != nil {
		t.Fatalf("read rewritten echo: %v", err)
	}
	payload, err := castree.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Size != blob.Size || payload.SHA1 != "ABC" {
		t.Fatalf("payload = %#v, want DB blob/hash", payload)
	}
	got, err := fixture.store.GetLibraryEntry(ctx, queries.GetLibraryEntryParams{LibraryID: fixture.library.ID, RelPath: entry.RelPath})
	if err != nil {
		t.Fatal(err)
	}
	if got.EchoWritten != 1 {
		t.Fatalf("echo_written = %d, want 1", got.EchoWritten)
	}
}

func TestReconcileAcceptsValidExistingEcho(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	blob := createBlob(t, ctx, fixture.store, 42, "episode.mkv")
	mustInsertHash(t, ctx, fixture.store, blob.ID, "sha1", "ABC", blob.Size)
	if _, err := fixture.store.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   fixture.library.ID,
		RelPath:     "episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1,
		UpdatedAt:   1,
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := castree.Encode(castree.Payload{Name: "episode.mkv", Size: blob.Size, SHA1: "ABC"})
	if err != nil {
		t.Fatal(err)
	}
	echoPath := filepath.Join(fixture.library.EchoOutputPath, "episode.mkv.echo")
	if err := os.WriteFile(echoPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, fixture.store, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(echoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(encoded) {
		t.Fatal("valid existing echo was unexpectedly rewritten")
	}
}

func TestReconcileRewritesInvalidExistingEcho(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	blob := createBlob(t, ctx, fixture.store, 42, "episode.mkv")
	mustInsertHash(t, ctx, fixture.store, blob.ID, "sha1", "ABC", blob.Size)
	if _, err := fixture.store.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   fixture.library.ID,
		RelPath:     "episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1,
		UpdatedAt:   1,
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := castree.Encode(castree.Payload{Name: "episode.mkv", Size: 99, SHA1: "WRONG"})
	if err != nil {
		t.Fatal(err)
	}
	echoPath := filepath.Join(fixture.library.EchoOutputPath, "episode.mkv.echo")
	if err := os.WriteFile(echoPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, fixture.store, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(echoPath)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := castree.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Size != blob.Size || payload.SHA1 != "ABC" {
		t.Fatalf("payload = %#v, want rewritten DB values", payload)
	}
}

func TestReconcileRemovesStaleTmpAndLeavesOrphanEcho(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	tmpPath := filepath.Join(fixture.library.EchoOutputPath, "stale.echo.tmp")
	orphanPath := filepath.Join(fixture.library.EchoOutputPath, "orphan.echo")
	if err := os.WriteFile(tmpPath, []byte("tmp"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, fixture.store, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("tmp stat error = %v, want removed", err)
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatalf("orphan should remain: %v", err)
	}
}

func TestEchoPayloadMatchesDBRequiresKnownHashes(t *testing.T) {
	blob := queries.Blob{Size: 10}
	hashes := []queries.BlobHash{
		{HashType: "sha1", HashValueNorm: "sha", Size: 10},
		{HashType: "md5", HashValueNorm: "md5", Size: 10},
		{HashType: "sha256", HashValueNorm: "sha256", Size: 10},
		{HashType: "preid", HashValueNorm: "pre", Size: 10},
		{HashType: "slice_md5", HashValueNorm: "slice", Size: 10},
	}
	if !echoPayloadMatchesDB(castree.Payload{Size: 10, SHA1: "SHA", MD5: "MD5", SHA256: "SHA256", PreID: "PRE", SliceMD5: "SLICE"}, blob, hashes) {
		t.Fatal("expected matching payload")
	}
	if echoPayloadMatchesDB(castree.Payload{Size: 10, MD5: "ABC"}, blob, []queries.BlobHash{{HashType: "sha1", HashValueNorm: "abc", Size: 10}}) {
		t.Fatal("expected missing sha1 to fail")
	}
	if echoPayloadMatchesDB(castree.Payload{Size: 11, SHA1: "SHA", MD5: "MD5", SHA256: "SHA256", PreID: "PRE", SliceMD5: "SLICE"}, blob, hashes) {
		t.Fatal("expected size mismatch to fail")
	}
	if _, ok := payloadHashValue(castree.Payload{}, "unknown"); ok {
		t.Fatal("unknown hash type should not be present")
	}
}

func TestReconcileWarnsEchoWithNoLiveCopyWithoutDeletingIt(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	blob := createBlob(t, ctx, fixture.store, 42, "episode.mkv")
	if _, err := fixture.store.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   fixture.library.ID,
		RelPath:     "episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 1,
		CreatedAt:   1,
		UpdatedAt:   1,
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := castree.Encode(castree.Payload{Name: "episode.mkv", Size: blob.Size})
	if err != nil {
		t.Fatal(err)
	}
	echoPath := filepath.Join(fixture.library.EchoOutputPath, "episode.mkv.echo")
	if err := os.WriteFile(echoPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, fixture.store, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(echoPath); err != nil {
		t.Fatalf("echo should remain despite no live copy: %v", err)
	}
}

func TestPayloadFromDBMapsHashTypes(t *testing.T) {
	payload := payloadFromDB(
		queries.LibraryEntry{Name: "x"},
		queries.Blob{Size: 5},
		[]queries.BlobHash{
			{HashType: "sha1", HashValue: "sha"},
			{HashType: "md5", HashValue: "md5"},
			{HashType: "sha256", HashValue: "sha256"},
			{HashType: "preid", HashValue: "pre"},
			{HashType: "slice_md5", HashValue: "slice"},
		},
	)
	if payload.SHA1 != "sha" || payload.MD5 != "md5" || payload.SHA256 != "sha256" || payload.PreID != "pre" || payload.SliceMD5 != "slice" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestReconcileEntryPropagatesUnsafeLibraryPath(t *testing.T) {
	ctx := context.Background()
	fixture := newPipelineFixture(t)
	blob := createBlob(t, ctx, fixture.store, 42, "episode.mkv")
	library, err := fixture.store.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "bad",
		EchoOutputKind: "local",
		EchoOutputPath: "relative",
		OwnerID:        "admin",
		CreatedAt:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := fixture.store.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     "episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1,
		UpdatedAt:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = reconcileEntry(ctx, fixture.store, entry, fixedNow)
	if err == nil {
		t.Fatal("expected unsafe library path error")
	}
}

var _ = sql.ErrNoRows
