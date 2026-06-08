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

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestPhase1SeedsAdminUser(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	admin, err := st.GetUser(ctx, queries.GetUserParams{ID: "admin"})
	if err != nil {
		t.Fatalf("get admin user: %v", err)
	}
	if admin.Username != "admin" || admin.Role != "admin" || admin.Status != "active" {
		t.Fatalf("admin = (%q,%q,%q), want admin/admin/active", admin.Username, admin.Role, admin.Status)
	}
	if admin.PasswordHash.Valid {
		t.Fatalf("admin password_hash = %q, want NULL", admin.PasswordHash.String)
	}
	if admin.CreatedAt != 0 || admin.UpdatedAt != 0 {
		t.Fatalf("admin timestamps = (%d,%d), want 0/0", admin.CreatedAt, admin.UpdatedAt)
	}
	if admin.LastLoginAt.Valid {
		t.Fatalf("admin last_login_at = %d, want NULL", admin.LastLoginAt.Int64)
	}
}

func TestPhase1APITokenHashOnly(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := int64(1000)

	plain := "selector.secret"
	hash := auth.HashToken(plain)
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("hash = %q, want sha256 prefix", hash)
	}
	if hash == plain {
		t.Fatal("hash equals plaintext token")
	}
	if second := auth.HashToken(plain); second != hash {
		t.Fatalf("second hash = %q, want %q", second, hash)
	}
	if len(hash) != len("sha256:")+64 {
		t.Fatalf("hash length = %d, want %d", len(hash), len("sha256:")+64)
	}
	if err := st.CreateAPIToken(ctx, queries.CreateAPITokenParams{
		ID: "tok1", UserID: "admin", Name: "cli", TokenHash: hash,
		Scopes: `["admin","read","playback"]`, ExpiresAt: sql.NullInt64{},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("create token: %v", err)
	}
	got, err := st.GetAPITokenByHash(ctx, queries.GetAPITokenByHashParams{TokenHash: hash})
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if got.TokenHash != hash || strings.Contains(got.TokenHash, plain) {
		t.Fatalf("token_hash = %q, want stored hash without plaintext", got.TokenHash)
	}
	if got.UserID != "admin" || got.RevokedAt.Valid {
		t.Fatalf("token row = %#v", got)
	}
}

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
		"users", "api_tokens", "quota_policies", "library_grants", "account_pool_assignments",
		"quota_usage", "playback_events", "accounts", "libraries", "blobs", "library_entries",
		"blob_hashes", "file_copies", "hash_conflicts", "jobs", "producer_runs",
		"emby_servers", "emby_user_links", "playback_sessions", "playback_error_tokens",
		"emby_library_mappings", "emby_item_mappings", "web_sessions",
	} {
		if tableExists(t, db, name) {
			t.Fatalf("%s table still exists after migrate down", name)
		}
	}

	if err := MigrateUp(db); err != nil {
		t.Fatalf("second migrate up after down: %v", err)
	}
}

func TestPhase2QuotaPolicyBackfillsUsers(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	admin, err := st.GetUser(ctx, queries.GetUserParams{ID: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if admin.QuotaPolicyID == 0 {
		t.Fatalf("admin quota_policy_id = %d, want sentinel policy", admin.QuotaPolicyID)
	}
	policy, err := st.GetQuotaPolicy(ctx, queries.GetQuotaPolicyParams{ID: admin.QuotaPolicyID})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Period != "none" {
		t.Fatalf("sentinel period = %q, want none", policy.Period)
	}
}

func TestPhase2PlaybackEventsHaveNullableSessionSnapshots(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.InsertPlaybackEvent(ctx, queries.InsertPlaybackEventParams{
		RequestID: "req1", SessionID: sql.NullString{}, ErrorTokenID: sql.NullString{},
		EchoUserID: sql.NullString{String: "admin", Valid: true}, Operation: "stream",
		Status: "ok", BytesSent: 123, StartedAt: 1000,
	}); err != nil {
		t.Fatalf("insert playback event without session table parent: %v", err)
	}
}

func TestPhase3PlaybackSessionAndErrorTokenSchema(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := int64(1000)

	// Seed FK parents for playback_sessions.blob_id / library_entry_id (= 1).
	// The test DSN enables foreign_keys(1), and createBlob/createLibrary use
	// AUTOINCREMENT PKs, so the first rows created get id 1.
	blob := createBlob(t, ctx, st.Queries, 1024)
	library := createLibrary(t, ctx, st.Queries)
	entry, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     "season/episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("seed library entry: %v", err)
	}

	if err := st.CreateEmbyServer(ctx, queries.CreateEmbyServerParams{
		ID: "default", Name: "main-emby", BaseUrl: "http://emby:8096",
		PublicBaseUrl: "https://echo.example.com", ProxyPrefix: "/emby",
		Enabled: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create emby server: %v", err)
	}
	if err := st.CreateEmbyUserLink(ctx, queries.CreateEmbyUserLinkParams{
		EmbyServerID: "default", EmbyUserID: "emby-u1", EmbyUsername: sql.NullString{String: "alice", Valid: true},
		EchoUserID: "admin", Enabled: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create emby user link: %v", err)
	}
	if err := st.CreatePlaybackSession(ctx, queries.CreatePlaybackSessionParams{
		ID: "sess1", Selector: "sel1", TokenHash: "sha256:abc", EchoUserID: "admin",
		EmbyServerID: "default", EmbyUserID: "emby-u1", DeviceID: sql.NullString{String: "dev1", Valid: true},
		ItemID: "item1", MediaSourceID: "ms1", EmbyPlaySessionID: sql.NullString{String: "play1", Valid: true},
		LibraryEntryID: sql.NullInt64{Int64: entry.ID, Valid: true}, BlobID: sql.NullInt64{Int64: blob.ID, Valid: true},
		State: "active", CreatedAt: now, LastSeenAt: now, ExpiresAt: now + 3600,
	}); err != nil {
		t.Fatalf("create playback session: %v", err)
	}
	if err := st.CreatePlaybackErrorToken(ctx, queries.CreatePlaybackErrorTokenParams{
		ID: "err1", Selector: "errsel", TokenHash: "sha256:def", EchoUserID: sql.NullString{String: "admin", Valid: true},
		EmbyServerID: sql.NullString{String: "default", Valid: true}, EmbyUserID: sql.NullString{String: "emby-u1", Valid: true},
		ItemID: sql.NullString{String: "item1", Valid: true}, MediaSourceID: sql.NullString{String: "ms1", Valid: true},
		Reason: "quota_exceeded", HttpStatus: 429, CreatedAt: now, ExpiresAt: now + 300,
	}); err != nil {
		t.Fatalf("create playback error token: %v", err)
	}
}

func TestPhase4EmbyMappingSchema(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := int64(1000)

	if err := st.CreateEmbyServer(ctx, queries.CreateEmbyServerParams{
		ID: "default", Name: "main-emby", BaseUrl: "http://emby:8096",
		PublicBaseUrl: "https://echo.example.com", ProxyPrefix: "/emby",
		Enabled: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	library := createLibrary(t, ctx, st.Queries)
	blob := createBlob(t, ctx, st.Queries, 1024)
	entry, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID: library.ID, RelPath: "movies/Film.mkv", Name: "Film.mkv",
		BlobID: blob.ID, EchoWritten: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := st.CreateEmbyLibraryMapping(ctx, queries.CreateEmbyLibraryMappingParams{
		EmbyServerID: "default", LibraryID: library.ID,
		EmbyPathPrefix: "/media", EmbyPathPrefixNorm: "/media",
		EchoRelPrefix: "movies", CaseSensitive: 1, Enabled: 1,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	if err := st.UpsertEmbyItemMapping(ctx, queries.UpsertEmbyItemMappingParams{
		EmbyServerID: "default", EmbyItemID: "item1", MediaSourceID: "ms1",
		MappingID: mapping.ID, MediaSourcePathRaw: "/media/Film.mkv",
		MediaSourcePathNorm: "/media/Film.mkv", PathNormVersion: 1,
		LibraryID: library.ID, RelPath: "movies/Film.mkv",
		LibraryEntryID: entry.ID, BlobID: blob.ID,
		LibraryEntryUpdatedAt: entry.UpdatedAt, LastSeenAt: now,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert item mapping: %v", err)
	}
}

func TestPhase4ItemMappingCacheInvalidatesOnEntryOrMappingChange(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	seed := seedPhase4MappingCache(t, ctx, st)

	if _, err := st.GetValidEmbyItemMapping(ctx, queries.GetValidEmbyItemMappingParams{
		EmbyServerID: "default", EmbyItemID: "item1", MediaSourceID: "ms1",
		MediaSourcePathNorm: "/media/Film.mkv", PathNormVersion: 1,
	}); err != nil {
		t.Fatalf("valid cache lookup: %v", err)
	}
	if _, err := st.GetValidEmbyItemMapping(ctx, queries.GetValidEmbyItemMappingParams{
		EmbyServerID: "default", EmbyItemID: "item1", MediaSourceID: "ms1",
		MediaSourcePathNorm: "/media/Film.mkv", PathNormVersion: 2,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("path_norm_version mismatch error = %v, want sql.ErrNoRows", err)
	}

	if err := st.UpdateLibraryEntryBlobForTest(ctx, queries.UpdateLibraryEntryBlobForTestParams{
		BlobID: seed.OtherBlobID, UpdatedAt: seed.EntryUpdatedAt + 1, ID: seed.EntryID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetValidEmbyItemMapping(ctx, queries.GetValidEmbyItemMappingParams{
		EmbyServerID: "default", EmbyItemID: "item1", MediaSourceID: "ms1",
		MediaSourcePathNorm: "/media/Film.mkv", PathNormVersion: 1,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale blob/updated_at cache error = %v, want sql.ErrNoRows", err)
	}

	if err := st.SetEmbyLibraryMappingEnabled(ctx, queries.SetEmbyLibraryMappingEnabledParams{
		Enabled: 0, UpdatedAt: seed.EntryUpdatedAt + 2, ID: seed.MappingID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetValidEmbyItemMapping(ctx, queries.GetValidEmbyItemMappingParams{
		EmbyServerID: "default", EmbyItemID: "item1", MediaSourceID: "ms1",
		MediaSourcePathNorm: "/media/Film.mkv", PathNormVersion: 1,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled mapping cache error = %v, want sql.ErrNoRows", err)
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

func TestPhase0CopyFailureSchema(t *testing.T) {
	st := openTestStore(t)

	ctx := context.Background()
	now := time.Now().Unix()
	createAccount(t, ctx, st.Queries)
	blob := createBlob(t, ctx, st.Queries, 1)
	copy, err := st.InsertFileCopy(ctx, fileCopyParams(blob.ID, now))
	if err != nil {
		t.Fatal(err)
	}
	if copy.SchedulerState != "healthy" {
		t.Fatalf("scheduler_state = %q, want healthy", copy.SchedulerState)
	}
	if err := st.InsertCopyFailure(ctx, queries.InsertCopyFailureParams{
		CopyID: sql.NullInt64{Int64: copy.ID, Valid: true}, AccountID: sql.NullString{String: "115-main", Valid: true},
		SidecarID: copy.SidecarID, StorageMount: copy.StorageMount, Operation: "link", Kind: "object_missing",
		Confidence: "confirmed", EvidenceClass: "json_envelope", HttpStatus: sql.NullInt64{Int64: 200, Valid: true},
		OpenlistCode: sql.NullInt64{Int64: 500, Valid: true}, SafeMessage: sql.NullString{String: "object not found", Valid: true},
		ObservedAt: now, RequestID: sql.NullString{String: "req1", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestFileCopyLiveRevivesDeadCopy proves the FOLDED-IN invariant: a copy previously
// retired (confirmed_dead) or hidden (suspect_dead) must be fully revived —
// status='live' AND scheduler_state='healthy' with failure fields cleared — when a
// later successful re-ingest writes it live again, so it reappears in
// ListLiveCopiesByBlob. Both revival queries are covered: UpsertFileCopyLive (the
// upsert/ON CONFLICT path) and UpdateFileCopyLive (the by-id path the ingest pipeline
// actually takes for an already-known remote_path). Without resetting scheduler_state
// the 0.3 live-copy filter would silently hide the revived copy forever.
func TestFileCopyLiveRevivesDeadCopy(t *testing.T) {
	markConfirmedDead := func(st *Store, ctx context.Context, id int64) error {
		return st.MarkFileCopyConfirmedDead(ctx, queries.MarkFileCopyConfirmedDeadParams{
			LastFailureAt:      sql.NullInt64{Int64: 5, Valid: true},
			LastFailureKind:    sql.NullString{String: "object_missing", Valid: true},
			LastFailureCode:    sql.NullInt64{Int64: 500, Valid: true},
			LastFailureMessage: sql.NullString{String: "object not found", Valid: true},
			DeadReason:         sql.NullString{String: "object not found", Valid: true},
			DeadAt:             sql.NullInt64{Int64: 5, Valid: true},
			ID:                 id,
		})
	}
	markSuspectDead := func(st *Store, ctx context.Context, id int64) error {
		return st.MarkFileCopySuspectDead(ctx, queries.MarkFileCopySuspectDeadParams{
			LastFailureAt:      sql.NullInt64{Int64: 6, Valid: true},
			LastFailureKind:    sql.NullString{String: "transient", Valid: true},
			LastFailureCode:    sql.NullInt64{Int64: 500, Valid: true},
			LastFailureMessage: sql.NullString{String: "failed to get file", Valid: true},
			VerifyAfter:        sql.NullInt64{Int64: 9_999_999_999, Valid: true},
			ID:                 id,
		})
	}

	states := []struct {
		name string
		mark func(*Store, context.Context, int64) error
	}{
		{"confirmed_dead", markConfirmedDead},
		{"suspect_dead", markSuspectDead},
	}
	mechanisms := []struct {
		name   string
		revive func(*Store, context.Context, int64, int64) error
	}{
		{
			name: "upsert",
			revive: func(st *Store, ctx context.Context, blobID, _ int64) error {
				_, err := st.UpsertFileCopyLive(ctx, upsertFileCopyParams(blobID, 20))
				return err
			},
		},
		{
			name: "update_by_id",
			revive: func(st *Store, ctx context.Context, _, copyID int64) error {
				return st.UpdateFileCopyLive(ctx, queries.UpdateFileCopyLiveParams{
					LastSeen:    20,
					CloudFileID: sql.NullString{String: "cloud-20", Valid: true},
					Pickcode:    sql.NullString{String: "pick-20", Valid: true},
					ID:          copyID,
				})
			},
		},
	}

	for _, state := range states {
		for _, mech := range mechanisms {
			t.Run(state.name+"/"+mech.name, func(t *testing.T) {
				ctx := context.Background()
				st := openTestStore(t)
				blob := createBlob(t, ctx, st.Queries, 1024)
				createAccount(t, ctx, st.Queries)
				copy, err := st.InsertFileCopy(ctx, fileCopyParams(blob.ID, 10))
				if err != nil {
					t.Fatalf("insert file copy: %v", err)
				}
				if err := state.mark(st, ctx, copy.ID); err != nil {
					t.Fatalf("mark %s: %v", state.name, err)
				}
				// Sanity: the copy is hidden from the live pool while marked dead.
				if hidden, err := st.ListLiveCopiesByBlob(ctx, queries.ListLiveCopiesByBlobParams{BlobID: blob.ID, Limit: 5}); err != nil {
					t.Fatalf("list live copies (while %s): %v", state.name, err)
				} else if len(hidden) != 0 {
					t.Fatalf("live copies while %s = %d, want 0 (must be hidden)", state.name, len(hidden))
				}

				if err := mech.revive(st, ctx, blob.ID, copy.ID); err != nil {
					t.Fatalf("revive via %s: %v", mech.name, err)
				}

				revived, err := st.GetFileCopyByRemotePath(ctx, queries.GetFileCopyByRemotePathParams{
					SidecarID:    "default",
					StorageMount: "/115-main",
					RemotePath:   "/media/episode.mkv",
				})
				if err != nil {
					t.Fatalf("reload revived copy: %v", err)
				}
				if revived.ID != copy.ID {
					t.Fatalf("revival created a new copy id=%d, want same id=%d", revived.ID, copy.ID)
				}
				if revived.Status != "live" {
					t.Fatalf("status = %q, want live", revived.Status)
				}
				if revived.SchedulerState != "healthy" {
					t.Fatalf("scheduler_state = %q, want healthy after revival", revived.SchedulerState)
				}
				if revived.FailureCount != 0 {
					t.Fatalf("failure_count = %d, want 0 after revival", revived.FailureCount)
				}
				if revived.VerifyAfter.Valid || revived.CooldownUntil.Valid {
					t.Fatalf("verify_after/cooldown_until still set after revival: %+v / %+v", revived.VerifyAfter, revived.CooldownUntil)
				}
				if revived.LastFailureAt.Valid || revived.LastFailureKind.Valid || revived.LastFailureCode.Valid ||
					revived.LastFailureMessage.Valid || revived.LastFailureConfidence.Valid ||
					revived.DeadReason.Valid || revived.DeadAt.Valid {
					t.Fatalf("failure/dead fields still set after revival: %+v", revived)
				}

				live, err := st.ListLiveCopiesByBlob(ctx, queries.ListLiveCopiesByBlobParams{BlobID: blob.ID, Limit: 5})
				if err != nil {
					t.Fatalf("list live copies (after revival): %v", err)
				}
				if len(live) != 1 || live[0].ID != copy.ID {
					t.Fatalf("revived copy not in live pool: got %d rows", len(live))
				}
			})
		}
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

func TestGetLibraryEntryByID(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	blob := createBlob(t, ctx, st.Queries, 1024)
	library := createLibrary(t, ctx, st.Queries)

	entry, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     "season/episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1,
		UpdatedAt:   1,
	})
	if err != nil {
		t.Fatalf("upsert library entry: %v", err)
	}

	got, err := st.GetLibraryEntryByID(ctx, queries.GetLibraryEntryByIDParams{ID: entry.ID})
	if err != nil {
		t.Fatalf("get library entry by id: %v", err)
	}
	if got.ID != entry.ID {
		t.Fatalf("entry id = %d, want %d", got.ID, entry.ID)
	}
	if got.BlobID != blob.ID {
		t.Fatalf("entry blob_id = %d, want %d", got.BlobID, blob.ID)
	}
	if got.RelPath != "season/episode.mkv" {
		t.Fatalf("entry rel_path = %q, want season/episode.mkv", got.RelPath)
	}

	if _, err := st.GetLibraryEntryByID(ctx, queries.GetLibraryEntryByIDParams{ID: entry.ID + 999}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get missing entry error = %v, want sql.ErrNoRows", err)
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

func TestImmediateTxCommitFailureAllowsRollback(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	tx, _, err := st.BeginImmediateTx(ctx)
	if err != nil {
		t.Fatalf("begin immediate tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("force transaction inactive: %v", err)
	}

	if err := tx.Commit(ctx); err == nil {
		t.Fatal("commit unexpectedly succeeded after transaction was already rolled back")
	}
	if err := tx.Rollback(ctx); errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("rollback after failed commit returned ErrTxDone; commit marked tx done too early")
	}
	if err := tx.Rollback(ctx); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("second rollback err=%v, want ErrTxDone", err)
	}

	tx2, _, err := st.BeginImmediateTx(ctx)
	if err != nil {
		t.Fatalf("begin immediate tx after failed commit rollback: %v", err)
	}
	if err := tx2.Rollback(ctx); err != nil {
		t.Fatalf("rollback second tx: %v", err)
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

type phase4MappingSeed struct {
	OtherBlobID    int64
	EntryUpdatedAt int64
	EntryID        int64
	MappingID      int64
}

func seedPhase4MappingCache(t *testing.T, ctx context.Context, st *Store) phase4MappingSeed {
	t.Helper()
	now := int64(1000)

	if err := st.CreateEmbyServer(ctx, queries.CreateEmbyServerParams{
		ID: "default", Name: "main-emby", BaseUrl: "http://emby:8096",
		PublicBaseUrl: "https://echo.example.com", ProxyPrefix: "/emby",
		Enabled: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed emby server: %v", err)
	}
	library := createLibrary(t, ctx, st.Queries)
	blob := createBlob(t, ctx, st.Queries, 1024)
	otherBlob := createBlob(t, ctx, st.Queries, 2048)
	entry, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID: library.ID, RelPath: "movies/Film.mkv", Name: "Film.mkv",
		BlobID: blob.ID, EchoWritten: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed library entry: %v", err)
	}
	mapping, err := st.CreateEmbyLibraryMapping(ctx, queries.CreateEmbyLibraryMappingParams{
		EmbyServerID: "default", LibraryID: library.ID,
		EmbyPathPrefix: "/media", EmbyPathPrefixNorm: "/media",
		EchoRelPrefix: "movies", CaseSensitive: 1, Enabled: 1,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed library mapping: %v", err)
	}
	if err := st.UpsertEmbyItemMapping(ctx, queries.UpsertEmbyItemMappingParams{
		EmbyServerID: "default", EmbyItemID: "item1", MediaSourceID: "ms1",
		MappingID: mapping.ID, MediaSourcePathRaw: "/media/Film.mkv",
		MediaSourcePathNorm: "/media/Film.mkv", PathNormVersion: 1,
		LibraryID: library.ID, RelPath: "movies/Film.mkv",
		LibraryEntryID: entry.ID, BlobID: blob.ID,
		LibraryEntryUpdatedAt: entry.UpdatedAt, LastSeenAt: now,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed item mapping: %v", err)
	}

	return phase4MappingSeed{
		OtherBlobID:    otherBlob.ID,
		EntryUpdatedAt: entry.UpdatedAt,
		EntryID:        entry.ID,
		MappingID:      mapping.ID,
	}
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
