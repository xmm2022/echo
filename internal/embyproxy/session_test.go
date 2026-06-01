package embyproxy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestPlaybackTokenLookupVerifiesSecretAndExpiry(t *testing.T) {
	st := newEmbyProxyTestStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)
	seedEmbyServerAndUserLink(t, ctx, st)
	entry := seedMappedLibraryEntry(t, ctx, st, "admin")

	mgr := NewSessionManager(st.Queries, SessionConfig{TTL: time.Hour}, nowFunc(now))
	token, session, err := mgr.CreatePlaybackSession(ctx, CreatePlaybackSessionInput{
		EchoUserID: "admin", EmbyServerID: "default", EmbyUserID: "emby-u1",
		DeviceID: "dev1", ItemID: "item1", MediaSourceID: "ms1",
		LibraryEntryID: entry.ID, BlobID: entry.BlobID,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.LookupPlaybackSession(ctx, token)
	if err != nil {
		t.Fatalf("lookup token: %v", err)
	}
	if got.ID != session.ID {
		t.Fatalf("session id = %q, want %q", got.ID, session.ID)
	}
	if _, err := mgr.LookupPlaybackSession(ctx, token+"x"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("modified token error = %v, want ErrInvalidToken", err)
	}
	expiredMgr := NewSessionManager(st.Queries, SessionConfig{TTL: time.Hour}, nowFunc(now.Add(2*time.Hour)))
	if _, err := expiredMgr.LookupPlaybackSession(ctx, token); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expired lookup error = %v, want ErrExpiredToken", err)
	}
}

func TestErrorTokenLookupReturnsSafeReasonOnly(t *testing.T) {
	st := newEmbyProxyTestStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)
	seedEmbyServerAndUserLink(t, ctx, st)
	mgr := NewSessionManager(st.Queries, SessionConfig{ErrorTTL: 5 * time.Minute}, nowFunc(now))

	token, created, err := mgr.CreateErrorToken(ctx, CreateErrorTokenInput{
		EchoUserID: sql.NullString{String: "admin", Valid: true}, EmbyServerID: sql.NullString{String: "default", Valid: true},
		EmbyUserID: "emby-u1", ItemID: "item1", MediaSourceID: "ms1",
		Reason: "quota_exceeded", HTTPStatus: http.StatusTooManyRequests,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.LookupErrorToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.Reason != "quota_exceeded" || got.HttpStatus != http.StatusTooManyRequests {
		t.Fatalf("error token = %#v", got)
	}
}

func TestPlaybackTokenLookupRejectsRevokedSession(t *testing.T) {
	st := newEmbyProxyTestStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)
	seedEmbyServerAndUserLink(t, ctx, st)
	entry := seedMappedLibraryEntry(t, ctx, st, "admin")

	mgr := NewSessionManager(st.Queries, SessionConfig{TTL: time.Hour}, nowFunc(now))
	token, session, err := mgr.CreatePlaybackSession(ctx, CreatePlaybackSessionInput{
		EchoUserID: "admin", EmbyServerID: "default", EmbyUserID: "emby-u1",
		DeviceID: "dev1", ItemID: "item1", MediaSourceID: "ms1",
		LibraryEntryID: entry.ID, BlobID: entry.BlobID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RevokePlaybackSession(ctx, queries.RevokePlaybackSessionParams{
		FailureReason: sql.NullString{String: "admin_revoked", Valid: true},
		LastSeenAt:    now.Unix(),
		ID:            session.ID,
	}); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := mgr.LookupPlaybackSession(ctx, token); !errors.Is(err, ErrRevokedToken) {
		t.Fatalf("revoked lookup error = %v, want ErrRevokedToken", err)
	}
}

func TestCreateErrorTokenRejectsNonErrorStatus(t *testing.T) {
	st := newEmbyProxyTestStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)
	seedEmbyServerAndUserLink(t, ctx, st)
	mgr := NewSessionManager(st.Queries, SessionConfig{}, nowFunc(now))

	if _, _, err := mgr.CreateErrorToken(ctx, CreateErrorTokenInput{
		EchoUserID:   sql.NullString{String: "admin", Valid: true},
		EmbyServerID: sql.NullString{String: "default", Valid: true},
		EmbyUserID:   "emby-u1", ItemID: "item1", MediaSourceID: "ms1",
		Reason: "quota_exceeded", HTTPStatus: http.StatusOK,
	}); err == nil {
		t.Fatal("CreateErrorToken with 200 status returned nil error, want rejection")
	}
}

func newEmbyProxyTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("file:" + filepath.Join(t.TempDir(), "echo.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func nowFunc(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func seedEmbyServerAndUserLink(t *testing.T, ctx context.Context, st *store.Store) {
	t.Helper()
	if err := st.CreateEmbyServer(ctx, queries.CreateEmbyServerParams{
		ID:            "default",
		Name:          "main-emby",
		BaseUrl:       "http://emby:8096",
		PublicBaseUrl: "https://echo.example.com",
		ProxyPrefix:   "/emby",
		Enabled:       1,
		CreatedAt:     1000,
		UpdatedAt:     1000,
	}); err != nil {
		t.Fatalf("create emby server: %v", err)
	}
	if err := st.CreateEmbyUserLink(ctx, queries.CreateEmbyUserLinkParams{
		EmbyServerID: "default",
		EmbyUserID:   "emby-u1",
		EmbyUsername: sql.NullString{String: "alice", Valid: true},
		EchoUserID:   "admin",
		Enabled:      1,
		CreatedAt:    1000,
		UpdatedAt:    1000,
	}); err != nil {
		t.Fatalf("create emby user link: %v", err)
	}
}

func seedMappedLibraryEntry(t *testing.T, ctx context.Context, st *store.Store, echoUser string) queries.LibraryEntry {
	t.Helper()
	blob, err := st.CreateBlob(ctx, queries.CreateBlobParams{
		Size:          1024,
		CanonicalName: sql.NullString{String: "episode.mkv", Valid: true},
		OwnerID:       echoUser,
		CreatedAt:     1000,
		UpdatedAt:     1000,
	})
	if err != nil {
		t.Fatalf("create blob: %v", err)
	}
	library, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "media",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/media",
		OwnerID:        echoUser,
		CreatedAt:      1000,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	entry, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     "season/episode.mkv",
		Name:        "episode.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1000,
		UpdatedAt:   1000,
	})
	if err != nil {
		t.Fatalf("upsert library entry: %v", err)
	}
	return entry
}
