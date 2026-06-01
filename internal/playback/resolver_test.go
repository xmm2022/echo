package playback

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestResolveCopiesRequiresOwnerOrGrantAndPool(t *testing.T) {
	st := newPlaybackTestStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)

	user := createPlaybackUser(t, ctx, st, "u1", "alice")
	library, entry := createPlaybackLibraryEntry(t, ctx, st, "admin")
	account := createPlaybackAccount(t, ctx, st, "acct1", "115")
	copy := createPlaybackCopy(t, ctx, st, entry.BlobID, account.ID, "115", 10)
	createPoolAssignment(t, ctx, st, user.ID, account.ID, "115", 100, 1)

	resolver := NewResolver(st.Queries, nowFunc(now))
	if _, err := resolver.ResolveCopies(ctx, user.ID, entry.ID, "", 5); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("without grant error = %v, want ErrUnauthorized", err)
	}

	grantPlayback(t, ctx, st, library.ID, user.ID)
	copies, err := resolver.ResolveCopies(ctx, user.ID, entry.ID, "", 5)
	if err != nil {
		t.Fatalf("resolve copies: %v", err)
	}
	if len(copies) != 1 || copies[0].ID != copy.ID {
		t.Fatalf("copies = %#v, want copy %d", copies, copy.ID)
	}
}

func TestResolveCopiesFiltersSchedulerAndAccountCooldown(t *testing.T) {
	st := newPlaybackTestStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)
	user, entry, account := createPlaybackUserEntryAndPool(t, ctx, st, now)
	copy := createPlaybackCopy(t, ctx, st, entry.BlobID, account.ID, "115", 10)
	resolver := NewResolver(st.Queries, nowFunc(now))

	if err := st.MarkFileCopySuspectDead(ctx, queries.MarkFileCopySuspectDeadParams{
		LastFailureAt:      sql.NullInt64{Int64: now.Unix(), Valid: true},
		LastFailureKind:    sql.NullString{String: "transient", Valid: true},
		LastFailureCode:    sql.NullInt64{Int64: 500, Valid: true},
		LastFailureMessage: sql.NullString{String: "html failure", Valid: true},
		VerifyAfter:        sql.NullInt64{Int64: now.Add(time.Hour).Unix(), Valid: true},
		ID:                 copy.ID,
	}); err != nil {
		t.Fatal(err)
	}
	copies, err := resolver.ResolveCopies(ctx, user.ID, entry.ID, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 0 {
		t.Fatalf("suspect copy returned: %#v", copies)
	}
}

// --- helpers: real store + sqlc, mirroring internal/store/store_test.go patterns ---

func nowFunc(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// newPlaybackTestStore opens a real on-disk SQLite store in a temp dir. store.Open
// runs every migration and forces foreign_keys/busy_timeout/WAL itself, so a fresh
// store already has working FKs plus the seeded admin user and quota_policies id=1.
func newPlaybackTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "echo.db"))
	st, err := store.Open(dsn)
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

func createPlaybackUser(t *testing.T, ctx context.Context, st *store.Store, id, username string) queries.User {
	t.Helper()
	if err := st.CreateUser(ctx, queries.CreateUserParams{
		ID:            id,
		Username:      username,
		Role:          "user",
		Status:        "active",
		QuotaPolicyID: 1, // seeded 'unlimited' policy
		CreatedAt:     1,
		UpdatedAt:     1,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, err := st.GetUser(ctx, queries.GetUserParams{ID: id})
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	return user
}

// createPlaybackLibraryEntry creates an owner-owned library plus a fresh blob and a
// library entry over that blob, returning both.
func createPlaybackLibraryEntry(t *testing.T, ctx context.Context, st *store.Store, owner string) (queries.Library, queries.LibraryEntry) {
	t.Helper()
	library, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "media",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/media",
		OwnerID:        owner,
		CreatedAt:      1,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	blob, err := st.CreateBlob(ctx, queries.CreateBlobParams{
		Size:          1024,
		CanonicalName: sql.NullString{String: "episode.mkv", Valid: true},
		OwnerID:       owner,
		CreatedAt:     1,
		UpdatedAt:     1,
	})
	if err != nil {
		t.Fatalf("create blob: %v", err)
	}
	entry, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     "show/episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1,
		UpdatedAt:   1,
	})
	if err != nil {
		t.Fatalf("upsert library entry: %v", err)
	}
	return library, entry
}

func createPlaybackAccount(t *testing.T, ctx context.Context, st *store.Store, id, provider string) queries.Account {
	t.Helper()
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           id,
		Provider:     provider,
		SidecarID:    "default",
		StorageMount: "/" + id,
		Status:       "ok",
		OwnerID:      "admin",
		CreatedAt:    1,
		UpdatedAt:    1,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return queries.Account{
		ID:           id,
		Provider:     provider,
		SidecarID:    "default",
		StorageMount: "/" + id,
		Status:       "ok",
		OwnerID:      "admin",
	}
}

// createPlaybackCopy inserts a live file copy. A distinct remote path per call keeps
// the UNIQUE(sidecar_id, storage_mount, remote_path) constraint happy across copies
// sharing an account/mount. A fresh InsertFileCopy gets scheduler_state 'healthy', so
// the copy is eligible for ListPlayableCopiesForUser until something marks it dead.
func createPlaybackCopy(t *testing.T, ctx context.Context, st *store.Store, blobID int64, accountID, provider string, lastSeen int64) queries.FileCopy {
	t.Helper()
	remotePath := fmt.Sprintf("/media/%s/episode-%d.mkv", accountID, lastSeen)
	copy, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       blobID,
		Provider:     provider,
		AccountID:    accountID,
		SidecarID:    "default",
		StorageMount: "/" + accountID,
		RemotePath:   remotePath,
		CloudFileID:  sql.NullString{String: fmt.Sprintf("cloud-%d", lastSeen), Valid: true},
		Pickcode:     sql.NullString{String: fmt.Sprintf("pick-%d", lastSeen), Valid: true},
		Status:       "live",
		LastSeen:     lastSeen,
	})
	if err != nil {
		t.Fatalf("insert file copy: %v", err)
	}
	return copy
}

func createPoolAssignment(t *testing.T, ctx context.Context, st *store.Store, echoUserID, accountID, provider string, priority, weight int64) queries.AccountPoolAssignment {
	t.Helper()
	apa, err := st.CreateAccountPoolAssignment(ctx, queries.CreateAccountPoolAssignmentParams{
		EchoUserID:           echoUserID,
		Priority:             priority,
		Weight:               weight,
		MaxConcurrentStreams: sql.NullInt64{},
		DailyBytesLimit:      sql.NullInt64{},
		Enabled:              1,
		CreatedAt:            1,
		UpdatedAt:            1,
		AccountID:            accountID,
		Provider:             provider,
	})
	if err != nil {
		t.Fatalf("create pool assignment: %v", err)
	}
	return apa
}

func grantPlayback(t *testing.T, ctx context.Context, st *store.Store, libraryID int64, echoUserID string) {
	t.Helper()
	if err := st.GrantLibraryPlayback(ctx, queries.GrantLibraryPlaybackParams{
		LibraryID:  libraryID,
		EchoUserID: echoUserID,
		CreatedBy:  sql.NullString{String: "admin", Valid: true},
		CreatedAt:  1,
		UpdatedAt:  1,
	}); err != nil {
		t.Fatalf("grant library playback: %v", err)
	}
}

// createPlaybackUserEntryAndPool wires a user that already has playback access (via a
// grant) to an account-backed pool over a fresh library entry, so callers only need to
// add file copies. Returns the user, the entry, and the account.
func createPlaybackUserEntryAndPool(t *testing.T, ctx context.Context, st *store.Store, now time.Time) (queries.User, queries.LibraryEntry, queries.Account) {
	t.Helper()
	user := createPlaybackUser(t, ctx, st, "u1", "alice")
	library, entry := createPlaybackLibraryEntry(t, ctx, st, "admin")
	account := createPlaybackAccount(t, ctx, st, "acct1", "115")
	createPoolAssignment(t, ctx, st, user.ID, account.ID, "115", 100, 1)
	grantPlayback(t, ctx, st, library.ID, user.ID)
	return user, entry, account
}
