package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

func TestMigrationUpDownClean(t *testing.T) {
	db, err := sql.Open("sqlite", testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := MigrateUp(db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if !tableExists(t, db, "accounts") {
		t.Fatal("accounts table missing after migrate up")
	}

	if err := MigrateDown(db); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	for _, name := range []string{
		"accounts", "libraries", "blobs", "library_entries", "blob_hashes",
		"file_copies", "hash_conflicts", "jobs", "producer_runs",
	} {
		if tableExists(t, db, name) {
			t.Fatalf("%s table still exists after migrate down", name)
		}
	}

	if err := MigrateUp(db); err != nil {
		t.Fatalf("second migrate up after down: %v", err)
	}
}

func TestBlobHashesUniqueKey(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	blob := createBlob(t, ctx, st.Queries, 1024)
	arg := queries.InsertBlobHashParams{
		BlobID:        blob.ID,
		HashType:      "sha1",
		HashValue:     "ABCDEF",
		HashValueNorm: "abcdef",
		Size:          blob.Size,
	}
	if _, err := st.InsertBlobHash(ctx, arg); err != nil {
		t.Fatalf("insert blob hash: %v", err)
	}
	if _, err := st.InsertBlobHash(ctx, arg); err == nil {
		t.Fatal("expected duplicate blob_hashes key to fail")
	}
}

func TestFileCopiesUniqueRemoteKey(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	blob := createBlob(t, ctx, st.Queries, 1024)
	createAccount(t, ctx, st.Queries)

	arg := fileCopyParams(blob.ID, 1)
	if _, err := st.InsertFileCopy(ctx, arg); err != nil {
		t.Fatalf("insert file copy: %v", err)
	}
	if _, err := st.InsertFileCopy(ctx, arg); err == nil {
		t.Fatal("expected duplicate file_copies remote key to fail")
	}
}

func TestListLiveCopiesByBlobPreferProviderBindsProviderAndLimit(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	blob := createBlob(t, ctx, st.Queries, 1024)
	createAccount(t, ctx, st.Queries)
	createAccountWith(t, ctx, st.Queries, "189-main", "189pc", "/189-main")

	if _, err := st.InsertFileCopy(ctx, fileCopyParams(blob.ID, 10)); err != nil {
		t.Fatalf("insert 115 file copy: %v", err)
	}
	if _, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       blob.ID,
		Provider:     "189pc",
		AccountID:    "189-main",
		SidecarID:    "default",
		StorageMount: "/189-main",
		RemotePath:   "/media/episode.mkv",
		CloudFileID:  sql.NullString{String: "cloud-189", Valid: true},
		Status:       "live",
		LastSeen:     20,
	}); err != nil {
		t.Fatalf("insert 189 file copy: %v", err)
	}

	copies, err := st.ListLiveCopiesByBlobPreferProvider(ctx, queries.ListLiveCopiesByBlobPreferProviderParams{
		BlobID:            blob.ID,
		PreferredProvider: "115",
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("list preferred copies: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("copy count = %d, want 1", len(copies))
	}
	if copies[0].Provider != "115" {
		t.Fatalf("first provider = %q, want preferred provider 115", copies[0].Provider)
	}
}

func TestLibraryEntriesUniquePath(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	blob := createBlob(t, ctx, st.Queries, 1024)
	library := createLibrary(t, ctx, st.Queries)

	insert := func() error {
		_, err := st.DB.ExecContext(ctx, `
INSERT INTO library_entries (
  library_id, rel_path, name, blob_id, echo_written, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			library.ID, "season/episode.mkv", "episode.mkv", blob.ID, 0, int64(1), int64(1))
		return err
	}
	if err := insert(); err != nil {
		t.Fatalf("insert library entry: %v", err)
	}
	if err := insert(); err == nil {
		t.Fatal("expected duplicate library_entries path to fail")
	}
}

func TestUpdateAccountChangesBindingFields(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	createAccount(t, ctx, st.Queries)

	if err := st.UpdateAccount(ctx, queries.UpdateAccountParams{
		Provider:     "189pc",
		SidecarID:    "secondary",
		StorageMount: "/189-main",
		Status:       "ok",
		LastCheck:    sql.NullInt64{Int64: 2, Valid: true},
		OwnerID:      "admin",
		UpdatedAt:    2,
		ID:           "115-main",
	}); err != nil {
		t.Fatalf("update account: %v", err)
	}

	account, err := st.GetAccount(ctx, queries.GetAccountParams{ID: "115-main"})
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.Provider != "189pc" || account.SidecarID != "secondary" || account.StorageMount != "/189-main" {
		t.Fatalf("account binding = (%q, %q, %q), want updated values", account.Provider, account.SidecarID, account.StorageMount)
	}
}

func TestUpdateLibraryChangesEditableFields(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	library := createLibrary(t, ctx, st.Queries)

	if err := st.UpdateLibrary(ctx, queries.UpdateLibraryParams{
		Name:           "renamed",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/renamed",
		OwnerID:        "admin",
		ID:             library.ID,
	}); err != nil {
		t.Fatalf("update library: %v", err)
	}

	got, err := st.GetLibrary(ctx, queries.GetLibraryParams{ID: library.ID})
	if err != nil {
		t.Fatalf("get library: %v", err)
	}
	if got.Name != "renamed" || got.EchoOutputPath != "/tmp/renamed" {
		t.Fatalf("library = (%q, %q), want updated fields", got.Name, got.EchoOutputPath)
	}
}

func TestDeleteJobRemovesJob(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	job, err := st.CreateJob(ctx, queries.CreateJobParams{
		Kind:      "ingest_manual",
		Status:    "pending",
		Payload:   `{"library_id":1}`,
		OwnerID:   "admin",
		CreatedAt: 1,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := st.DeleteJob(ctx, queries.DeleteJobParams{ID: job.ID}); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	if _, err := st.GetJob(ctx, queries.GetJobParams{ID: job.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get deleted job error = %v, want sql.ErrNoRows", err)
	}
}

func TestDeletingBlobCascadesDependentRows(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	blob := createBlob(t, ctx, st.Queries, 1024)
	library := createLibrary(t, ctx, st.Queries)
	createAccount(t, ctx, st.Queries)

	if _, err := st.InsertBlobHash(ctx, queries.InsertBlobHashParams{
		BlobID:        blob.ID,
		HashType:      "sha1",
		HashValue:     "ABCDEF",
		HashValueNorm: "abcdef",
		Size:          blob.Size,
	}); err != nil {
		t.Fatalf("insert blob hash: %v", err)
	}
	if _, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     "season/episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1,
		UpdatedAt:   1,
	}); err != nil {
		t.Fatalf("insert library entry: %v", err)
	}
	if _, err := st.InsertFileCopy(ctx, fileCopyParams(blob.ID, 1)); err != nil {
		t.Fatalf("insert file copy: %v", err)
	}

	if err := st.DeleteBlob(ctx, queries.DeleteBlobParams{ID: blob.ID}); err != nil {
		t.Fatalf("delete blob should cascade dependents: %v", err)
	}

	for _, tc := range []struct {
		table string
		want  int64
	}{
		{table: "blob_hashes", want: 0},
		{table: "library_entries", want: 0},
		{table: "file_copies", want: 0},
	} {
		if got := countRows(t, st.DB, tc.table); got != tc.want {
			t.Fatalf("%s rows = %d, want %d", tc.table, got, tc.want)
		}
	}
}

func TestBeginImmediateTxSerializesConcurrentFileCopyUpserts(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	blob := createBlob(t, ctx, st.Queries, 1024)
	createAccount(t, ctx, st.Queries)

	tx1, q1, err := st.BeginImmediateTx(ctx)
	if err != nil {
		t.Fatalf("begin first immediate tx: %v", err)
	}

	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(attempted)
		tx2, q2, err := st.BeginImmediateTx(ctx)
		if err != nil {
			done <- fmt.Errorf("begin second immediate tx: %w", err)
			return
		}
		defer tx2.Rollback(ctx)

		copy, err := q2.UpsertFileCopyLive(ctx, upsertFileCopyParams(blob.ID, 2))
		if err != nil {
			done <- fmt.Errorf("second upsert: %w", err)
			return
		}
		if copy.BlobID != blob.ID {
			done <- fmt.Errorf("second upsert changed blob_id to %d", copy.BlobID)
			return
		}
		if err := tx2.Commit(ctx); err != nil {
			done <- fmt.Errorf("second commit: %w", err)
			return
		}
		done <- nil
	}()

	<-attempted
	select {
	case err := <-done:
		t.Fatalf("second transaction finished before first committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := q1.UpsertFileCopyLive(ctx, upsertFileCopyParams(blob.ID, 1)); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second transaction did not complete after first commit")
	}

	copy, err := st.GetFileCopyByRemotePath(ctx, queries.GetFileCopyByRemotePathParams{
		SidecarID:    "default",
		StorageMount: "/115-main",
		RemotePath:   "/media/episode.mkv",
	})
	if err != nil {
		t.Fatalf("get upserted copy: %v", err)
	}
	if copy.LastSeen != 2 {
		t.Fatalf("last_seen = %d, want second serialized upsert timestamp 2", copy.LastSeen)
	}
}

func TestOpenEnforcesConnectionPragmasOnNewConnections(t *testing.T) {
	ctx := context.Background()
	st, err := Open(plainTestDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	st.DB.SetMaxIdleConns(0)
	conn, err := st.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, tc := range []struct {
		name string
		want int64
	}{
		{name: "foreign_keys", want: 1},
		{name: "busy_timeout", want: 5000},
	} {
		var got int64
		if err := conn.QueryRowContext(ctx, "PRAGMA "+tc.name).Scan(&got); err != nil {
			t.Fatalf("read PRAGMA %s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("PRAGMA %s = %d, want %d on a fresh pooled connection", tc.name, got, tc.want)
		}
	}

	var journalMode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read PRAGMA journal_mode: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("PRAGMA journal_mode = %q, want wal on a fresh pooled connection", journalMode)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(testDSN(t))
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

func testDSN(t *testing.T) string {
	t.Helper()
	dbPath := plainTestDBPath(t)
	return "file:" + dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

func plainTestDSN(t *testing.T) string {
	t.Helper()
	return "file:" + plainTestDBPath(t)
}

func plainTestDBPath(t *testing.T) string {
	t.Helper()
	return filepath.ToSlash(filepath.Join(t.TempDir(), "echo.db"))
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite_master for %s: %v", name, err)
	}
	return true
}

func countRows(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var got int64
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return got
}

func createBlob(t *testing.T, ctx context.Context, q *queries.Queries, size int64) queries.Blob {
	t.Helper()
	blob, err := q.CreateBlob(ctx, queries.CreateBlobParams{
		Size:          size,
		CanonicalName: sql.NullString{String: "episode.mkv", Valid: true},
		OwnerID:       "admin",
		CreatedAt:     1,
		UpdatedAt:     1,
	})
	if err != nil {
		t.Fatalf("create blob: %v", err)
	}
	return blob
}

func createAccount(t *testing.T, ctx context.Context, q *queries.Queries) {
	t.Helper()
	createAccountWith(t, ctx, q, "115-main", "115", "/115-main")
}

func createAccountWith(t *testing.T, ctx context.Context, q *queries.Queries, id, provider, storageMount string) {
	t.Helper()
	if err := q.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           id,
		Provider:     provider,
		SidecarID:    "default",
		StorageMount: storageMount,
		Status:       "ok",
		OwnerID:      "admin",
		CreatedAt:    1,
		UpdatedAt:    1,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
}

func createLibrary(t *testing.T, ctx context.Context, q *queries.Queries) queries.Library {
	t.Helper()
	library, err := q.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "media",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/media",
		OwnerID:        "admin",
		CreatedAt:      1,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	return library
}

func fileCopyParams(blobID int64, lastSeen int64) queries.InsertFileCopyParams {
	return queries.InsertFileCopyParams{
		BlobID:       blobID,
		Provider:     "115",
		AccountID:    "115-main",
		SidecarID:    "default",
		StorageMount: "/115-main",
		RemotePath:   "/media/episode.mkv",
		CloudFileID:  sql.NullString{String: "cloud-1", Valid: true},
		Pickcode:     sql.NullString{String: "pick-1", Valid: true},
		Status:       "live",
		LastSeen:     lastSeen,
	}
}

func upsertFileCopyParams(blobID int64, lastSeen int64) queries.UpsertFileCopyLiveParams {
	return queries.UpsertFileCopyLiveParams{
		BlobID:       blobID,
		Provider:     "115",
		AccountID:    "115-main",
		SidecarID:    "default",
		StorageMount: "/115-main",
		RemotePath:   "/media/episode.mkv",
		CloudFileID:  sql.NullString{String: fmt.Sprintf("cloud-%d", lastSeen), Valid: true},
		Pickcode:     sql.NullString{String: fmt.Sprintf("pick-%d", lastSeen), Valid: true},
		LastSeen:     lastSeen,
	}
}
