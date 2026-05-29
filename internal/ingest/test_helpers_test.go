package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func fixedNow() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "echo.db"))
	st, err := store.Open("file:" + dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}

func createAccount(t *testing.T, ctx context.Context, st *store.Store) queries.Account {
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

func createLibrary(t *testing.T, ctx context.Context, st *store.Store, outputRoot string) queries.Library {
	t.Helper()
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

func createBlob(t *testing.T, ctx context.Context, st *store.Store, size int64, name string) queries.Blob {
	t.Helper()
	return createBlobAt(t, ctx, st, size, name, fixedNow().Unix())
}

func createBlobAt(t *testing.T, ctx context.Context, st *store.Store, size int64, name string, ts int64) queries.Blob {
	t.Helper()
	blob, err := st.CreateBlob(ctx, queries.CreateBlobParams{
		Size:          size,
		CanonicalName: sql.NullString{String: name, Valid: name != ""},
		OwnerID:       "admin",
		CreatedAt:     ts,
		UpdatedAt:     ts,
	})
	if err != nil {
		t.Fatalf("create blob: %v", err)
	}
	return blob
}

func mustInsertHash(t *testing.T, ctx context.Context, st *store.Store, blobID int64, hashType, value string, size int64) {
	t.Helper()
	if _, err := st.InsertBlobHash(ctx, queries.InsertBlobHashParams{
		BlobID:        blobID,
		HashType:      hashType,
		HashValue:     value,
		HashValueNorm: normalizeHash(value),
		Size:          size,
	}); err != nil {
		t.Fatalf("insert blob hash: %v", err)
	}
}

func readProgress(t *testing.T, ctx context.Context, st *store.Store, jobID int64) Progress {
	t.Helper()
	job, err := st.GetJob(ctx, queries.GetJobParams{ID: jobID})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if !job.Progress.Valid {
		t.Fatal("job progress is null")
	}
	var progress Progress
	if err := json.Unmarshal([]byte(job.Progress.String), &progress); err != nil {
		t.Fatalf("decode progress %q: %v", job.Progress.String, err)
	}
	return progress
}

func countRows(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var got int64
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return got
}

func hasConflict(t *testing.T, db *sql.DB, reason string) bool {
	t.Helper()
	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM hash_conflicts WHERE reason = ?", reason).Scan(&count); err != nil {
		t.Fatalf("count conflict %s: %v", reason, err)
	}
	return count > 0
}

func countEchoFiles(t *testing.T, root string) int {
	t.Helper()
	var count int
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(d.Name()) == ".echo" {
			count++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk echo files: %v", err)
	}
	return count
}
