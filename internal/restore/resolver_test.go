package restore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "echo.db"))
	st, err := store.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}

type seededEntry struct {
	fileID    int64
	blobID    int64
	copy115ID int64
	copy189ID int64
}

// seedTwoCopies inserts a library entry backed by one live copy on 115 (older
// last_seen) and one on 189pc (newer last_seen).
func seedTwoCopies(t *testing.T, ctx context.Context, st *store.Store) seededEntry {
	t.Helper()
	library, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "media",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/media",
		OwnerID:        "admin",
		CreatedAt:      1,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	blob, err := st.CreateBlob(ctx, queries.CreateBlobParams{
		Size:      1024,
		OwnerID:   "admin",
		CreatedAt: 1,
		UpdatedAt: 1,
	})
	if err != nil {
		t.Fatalf("create blob: %v", err)
	}
	for _, a := range []struct{ id, provider, mount string }{
		{"115-main", "115", "/115-main"},
		{"189-main", "189pc", "/189-main"},
	} {
		if err := st.CreateAccount(ctx, queries.CreateAccountParams{
			ID:           a.id,
			Provider:     a.provider,
			SidecarID:    "default",
			StorageMount: a.mount,
			Status:       "ok",
			OwnerID:      "admin",
			CreatedAt:    1,
			UpdatedAt:    1,
		}); err != nil {
			t.Fatalf("create account %s: %v", a.id, err)
		}
	}
	entry, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     "season/episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 1,
		CreatedAt:   1,
		UpdatedAt:   1,
	})
	if err != nil {
		t.Fatalf("upsert library entry: %v", err)
	}
	copy115, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       blob.ID,
		Provider:     "115",
		AccountID:    "115-main",
		SidecarID:    "default",
		StorageMount: "/115-main",
		RemotePath:   "/media/115/episode.mkv",
		Status:       "live",
		LastSeen:     10,
	})
	if err != nil {
		t.Fatalf("insert 115 copy: %v", err)
	}
	copy189, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       blob.ID,
		Provider:     "189pc",
		AccountID:    "189-main",
		SidecarID:    "default",
		StorageMount: "/189-main",
		RemotePath:   "/media/189/episode.mkv",
		Status:       "live",
		LastSeen:     20,
	})
	if err != nil {
		t.Fatalf("insert 189 copy: %v", err)
	}
	return seededEntry{
		fileID:    entry.ID,
		blobID:    blob.ID,
		copy115ID: copy115.ID,
		copy189ID: copy189.ID,
	}
}

func TestLiveCopiesPreferOrdersPreferredProviderFirst(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	seed := seedTwoCopies(t, ctx, st)
	r := NewResolver(st.Queries, func() time.Time { return time.Unix(100, 0) })

	copies, err := r.LiveCopies(ctx, seed.fileID, "115")
	if err != nil {
		t.Fatalf("live copies: %v", err)
	}
	if len(copies) != 2 {
		t.Fatalf("copy count = %d, want 2", len(copies))
	}
	if copies[0].Provider != "115" {
		t.Fatalf("first provider = %q, want preferred provider 115", copies[0].Provider)
	}
}

func TestLiveCopiesEmptyPreferOrdersByLastSeen(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	seed := seedTwoCopies(t, ctx, st)
	r := NewResolver(st.Queries, func() time.Time { return time.Unix(100, 0) })

	copies, err := r.LiveCopies(ctx, seed.fileID, "")
	if err != nil {
		t.Fatalf("live copies: %v", err)
	}
	if len(copies) != 2 {
		t.Fatalf("copy count = %d, want 2", len(copies))
	}
	// 189pc has the newer last_seen, so with no preference it ranks first.
	if copies[0].Provider != "189pc" {
		t.Fatalf("first provider = %q, want 189pc (newest last_seen)", copies[0].Provider)
	}
}

func TestLiveCopiesMissingFileReturnsErrNoRows(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	r := NewResolver(st.Queries, nil)

	if _, err := r.LiveCopies(ctx, 999, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing file error = %v, want sql.ErrNoRows", err)
	}
}

func TestLiveCopiesNoLiveCopyReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	seed := seedTwoCopies(t, ctx, st)
	r := NewResolver(st.Queries, func() time.Time { return time.Unix(100, 0) })

	if err := r.MarkDead(ctx, seed.copy115ID); err != nil {
		t.Fatalf("mark 115 dead: %v", err)
	}
	if err := r.MarkDead(ctx, seed.copy189ID); err != nil {
		t.Fatalf("mark 189 dead: %v", err)
	}

	copies, err := r.LiveCopies(ctx, seed.fileID, "")
	if err != nil {
		t.Fatalf("live copies: %v", err)
	}
	if len(copies) != 0 {
		t.Fatalf("copy count = %d, want 0 after both dead", len(copies))
	}
}

func TestMarkDeadRemovesCopyFromLive(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	seed := seedTwoCopies(t, ctx, st)
	r := NewResolver(st.Queries, func() time.Time { return time.Unix(777, 0) })

	if err := r.MarkDead(ctx, seed.copy115ID); err != nil {
		t.Fatalf("mark dead: %v", err)
	}

	copies, err := r.LiveCopies(ctx, seed.fileID, "115")
	if err != nil {
		t.Fatalf("live copies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("copy count = %d, want 1 after one dead", len(copies))
	}
	if copies[0].ID != seed.copy189ID {
		t.Fatalf("remaining copy id = %d, want %d (189pc)", copies[0].ID, seed.copy189ID)
	}
}
