package embyproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/playback"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestRewritePlaybackInfoMappedSourceUsesEchoURLOnly(t *testing.T) {
	raw := readFixture(t, "single_source.json")
	rewrite := newRewriteHarness(t)
	out, result, err := rewrite.Rewrite(raw, RewriteContext{
		PublicBaseURL: "https://echo.example.com",
		ProxyPrefix:   "/emby",
		ItemID:        "item1",
		EmbyServerID:  "default",
		EmbyUserID:    "emby-u1",
		EchoUserID:    "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if strings.Contains(body, "/api/restore") || strings.Contains(body, "/api/stream") {
		t.Fatalf("rewrite leaked /api route: %s", body)
	}
	if strings.Contains(body, "api_key") || strings.Contains(body, "Authorization") || strings.Contains(body, "Cookie") || strings.Contains(body, "X-Emby-Token") {
		t.Fatalf("rewrite leaked auth material: %s", body)
	}
	if !strings.Contains(body, "https://echo.example.com/emby/stream/") {
		t.Fatalf("rewrite missing Echo stream URL: %s", body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	source := decoded["MediaSources"].([]any)[0].(map[string]any)
	for _, field := range []string{"DirectStreamUrl", "StreamUrl"} {
		value, _ := source[field].(string)
		if !strings.HasPrefix(value, "https://echo.example.com/emby/stream/") {
			t.Fatalf("%s = %q, want Echo stream URL", field, value)
		}
	}
	if value, _ := source["TranscodingUrl"].(string); !strings.HasPrefix(value, "https://echo.example.com/emby/error/") {
		t.Fatalf("TranscodingUrl = %q, want Echo error URL for mapped source", value)
	}
	if pathValue, _ := source["Path"].(string); !strings.HasPrefix(pathValue, "echo://mapped/") {
		t.Fatalf("Path = %q, want non-playable echo placeholder", pathValue)
	}
	if headers, ok := source["RequiredHttpHeaders"].(map[string]any); ok && len(headers) != 0 {
		t.Fatalf("RequiredHttpHeaders = %#v, want empty object for mapped source", headers)
	}
	assertNoUpstreamPlayableLocator(t, decoded)
	if result.SessionsCreated != 1 || result.ErrorTokensCreated != 0 {
		t.Fatalf("rewrite result = %#v", result)
	}
}

func TestRewritePlaybackInfoTranscodeOnlyBecomesErrorURL(t *testing.T) {
	raw := readFixture(t, "transcode_only.json")
	rewrite := newRewriteHarness(t)
	out, result, err := rewrite.Rewrite(raw, validRewriteContext())
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if strings.Contains(body, "master.m3u8") || strings.Contains(body, "api_key") {
		t.Fatalf("transcode URL leaked: %s", body)
	}
	if !strings.Contains(body, "/emby/error/") || result.ErrorReason != "unsupported_transcode" {
		t.Fatalf("transcode rewrite body/result = %s %#v", body, result)
	}
}

func TestRewritePlaybackInfoUnknownURLFailsClosed(t *testing.T) {
	raw := readFixture(t, "unknown_url.json")
	rewrite := newRewriteHarness(t)
	out, result, err := rewrite.Rewrite(raw, validRewriteContext())
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if strings.Contains(body, "api_key") || strings.Contains(body, "http://emby") || strings.Contains(body, "/Videos/") {
		t.Fatalf("unknown URL leaked: %s", out)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	source := decoded["MediaSources"].([]any)[0].(map[string]any)
	if value, ok := source["FooUrl"].(string); ok && !strings.Contains(value, "/emby/error/") {
		t.Fatalf("FooUrl = %q, want removed or Echo error URL", value)
	}
	if result.ErrorReason != "unknown_playable_url" {
		t.Fatalf("reason = %q, want unknown_playable_url", result.ErrorReason)
	}
}

func assertNoUpstreamPlayableLocator(t *testing.T, decoded map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(decoded)
	body := string(raw)
	for _, forbidden := range []string{
		"/api/restore", "/api/stream", "http://emby", "/Videos/", "api_key",
		"Authorization", "Cookie", "X-Emby-Token",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("rewritten PlaybackInfo contains forbidden %q: %s", forbidden, body)
		}
	}
}

// readFixture loads a golden PlaybackInfo body from testdata/playbackinfo/<name>.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "playbackinfo", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// validRewriteContext is the canonical context the single_source.json harness maps for:
// emby server "default", emby user "emby-u1" linked to Echo user "admin", item "item1".
func validRewriteContext() RewriteContext {
	return RewriteContext{
		PublicBaseURL: "https://echo.example.com",
		ProxyPrefix:   "/emby",
		ItemID:        "item1",
		EmbyServerID:  "default",
		EmbyUserID:    "emby-u1",
		EchoUserID:    "admin",
	}
}

// newRewriteHarness wires a Rewriter over a real (DB-backed) SourceMapper, SessionManager,
// Quota and Resolver. The seeded fixtures make the fixture Path "/media/movies/Film.mkv"
// resolve, via emby_library_mappings (prefix "/media", echo_rel_prefix "") to a library
// entry with rel_path "movies/Film.mkv" that is owned by "admin" and has a LIVE file copy,
// so an authorized, in-quota source produces a real playback session token.
func newRewriteHarness(t *testing.T) *Rewriter {
	t.Helper()
	st := newEmbyProxyTestStore(t)
	_, rw := seedRewriteHarnessStore(t, st)
	return rw
}

// rewriteHarnessSeed carries the ids produced by seedRewriteHarnessStore so callers (such
// as the mapped-evidence handler harness) can insert a consistent emby_item_mappings row.
type rewriteHarnessSeed struct {
	mappingID      int64
	libraryID      int64
	libraryEntryID int64
	blobID         int64
	entryUpdatedAt int64
}

// seedRewriteHarnessStore performs the canonical newRewriteHarness seeding against st and
// returns both the seeded ids and a Rewriter wired over real collaborators. It is factored
// out so handler harnesses can reuse the exact same seeding (and the ids) without
// duplicating it.
func seedRewriteHarnessStore(t *testing.T, st *store.Store) (rewriteHarnessSeed, *Rewriter) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1000, 0)

	seedEmbyServerAndUserLink(t, ctx, st)

	blob, err := st.CreateBlob(ctx, queries.CreateBlobParams{
		Size:          1024,
		CanonicalName: nullString("Film.mkv"),
		OwnerID:       "admin",
		CreatedAt:     1000,
		UpdatedAt:     1000,
	})
	if err != nil {
		t.Fatalf("create blob: %v", err)
	}
	library, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "movies",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/movies",
		OwnerID:        "admin",
		CreatedAt:      1000,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	entry, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
		LibraryID:   library.ID,
		RelPath:     "movies/Film.mkv",
		Name:        "Film.mkv",
		BlobID:      blob.ID,
		EchoWritten: 0,
		CreatedAt:   1000,
		UpdatedAt:   1000,
	})
	if err != nil {
		t.Fatalf("upsert library entry: %v", err)
	}

	account := seedStreamAccount(t, ctx, st, "acct1", "115")
	seedStreamPoolAssignment(t, ctx, st, "admin", account.ID, "115")
	seedLiveFileCopy(t, ctx, st, entry.BlobID, account.ID, "115", now.Unix())

	mapping, err := st.CreateEmbyLibraryMapping(ctx, queries.CreateEmbyLibraryMappingParams{
		EmbyServerID:       "default",
		LibraryID:          library.ID,
		EmbyPathPrefix:     "/media",
		EmbyPathPrefixNorm: "/media",
		EchoRelPrefix:      "",
		CaseSensitive:      1,
		Enabled:            1,
		CreatedAt:          1000,
		UpdatedAt:          1000,
	})
	if err != nil {
		t.Fatalf("create emby library mapping: %v", err)
	}

	mapper := NewDBSourceMapper(st.Queries, nowFunc(now))
	sessions := NewSessionManager(st.Queries, SessionConfig{TTL: time.Hour, ErrorTTL: 5 * time.Minute}, nowFunc(now))
	quota := playback.NewQuota(st.Queries, nowFunc(now), time.Hour)
	resolver := playback.NewResolver(st.Queries, nowFunc(now))
	return rewriteHarnessSeed{
		mappingID:      mapping.ID,
		libraryID:      library.ID,
		libraryEntryID: entry.ID,
		blobID:         entry.BlobID,
		entryUpdatedAt: entry.UpdatedAt,
	}, NewRewriter(mapper, sessions, quota, resolver)
}

// newPlaybackInfoHandlerHarness mounts ONLY the PlaybackInfo route over a store seeded
// exactly like newRewriteHarness, with upstreamURL as the upstream Emby origin.
func newPlaybackInfoHandlerHarness(t *testing.T, upstreamURL string) http.Handler {
	t.Helper()
	st := newEmbyProxyTestStore(t)
	_, rw := seedRewriteHarnessStore(t, st)
	return mountPlaybackInfoOnly(t, st, rw, upstreamURL)
}

// newPlaybackInfoHandlerHarnessWithMappedEvidence is like newPlaybackInfoHandlerHarness but
// additionally inserts a valid emby_item_mappings row for itemID, so ListItemMappingsByItem
// returns >=1 row (the "mapped evidence" signal that forces fail-closed on bad upstream).
func newPlaybackInfoHandlerHarnessWithMappedEvidence(t *testing.T, upstreamURL, itemID string) http.Handler {
	t.Helper()
	st := newEmbyProxyTestStore(t)
	seed, rw := seedRewriteHarnessStore(t, st)
	if err := st.UpsertEmbyItemMapping(context.Background(), queries.UpsertEmbyItemMappingParams{
		EmbyServerID:          "default",
		EmbyItemID:            itemID,
		MediaSourceID:         "ms1",
		MappingID:             seed.mappingID,
		MediaSourcePathRaw:    "/media/movies/Film.mkv",
		MediaSourcePathNorm:   "/media/movies/Film.mkv",
		PathNormVersion:       PathNormVersion,
		LibraryID:             seed.libraryID,
		RelPath:               "movies/Film.mkv",
		LibraryEntryID:        seed.libraryEntryID,
		BlobID:                seed.blobID,
		LibraryEntryUpdatedAt: seed.entryUpdatedAt,
		EmbyItemEtag:          sql.NullString{},
		LastSeenAt:            1000,
		CreatedAt:             1000,
		UpdatedAt:             1000,
	}); err != nil {
		t.Fatalf("upsert emby item mapping: %v", err)
	}
	return mountPlaybackInfoOnly(t, st, rw, upstreamURL)
}

// newPlaybackInfoHandlerHarnessWithoutMappedEvidence seeds only the emby server + user link
// (no library mapping, no item mapping), so ListItemMappingsByItem returns empty for any
// item and the handler may transparently pass through a non-playable upstream error.
func newPlaybackInfoHandlerHarnessWithoutMappedEvidence(t *testing.T, upstreamURL string) http.Handler {
	t.Helper()
	st := newEmbyProxyTestStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)
	seedEmbyServerAndUserLink(t, ctx, st)
	mapper := NewDBSourceMapper(st.Queries, nowFunc(now))
	sessions := NewSessionManager(st.Queries, SessionConfig{TTL: time.Hour, ErrorTTL: 5 * time.Minute}, nowFunc(now))
	quota := playback.NewQuota(st.Queries, nowFunc(now), time.Hour)
	resolver := playback.NewResolver(st.Queries, nowFunc(now))
	rw := NewRewriter(mapper, sessions, quota, resolver)
	return mountPlaybackInfoOnly(t, st, rw, upstreamURL)
}

// mountPlaybackInfoOnly builds a chi router with ONLY the PlaybackInfo route mounted,
// pointing at upstreamURL via the default transport.
func mountPlaybackInfoOnly(t *testing.T, st *store.Store, rw *Rewriter, upstreamURL string) http.Handler {
	t.Helper()
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	cfg := PlaybackInfoConfig{
		PublicBaseURL: "https://echo.example.com",
		ProxyPrefix:   "/emby",
		EmbyServerID:  "default",
		UpstreamBase:  parsed,
		Querier:       st.Queries,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := chi.NewRouter()
	r.Handle("/emby/Items/{item_id}/PlaybackInfo", PlaybackInfoHandler(cfg, rw, http.DefaultTransport, logger))
	return r
}

func TestPlaybackInfoHandlerForcesIdentityAndNoStore(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "identity" {
			t.Fatalf("Accept-Encoding = %q, want identity", got)
		}
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write(readFixture(t, "single_source.json"))
		_ = gz.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		_, _ = w.Write(buf.Bytes())
	}))
	defer upstream.Close()

	h := newPlaybackInfoHandlerHarness(t, upstream.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/emby/Items/item1/PlaybackInfo?UserId=emby-u1&DeviceId=dev1", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding leaked: %q", rec.Header().Get("Content-Encoding"))
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("Content-Length = %q, want rewritten body length %d", got, rec.Body.Len())
	}
	if strings.Contains(rec.Body.String(), "/api/restore") || strings.Contains(rec.Body.String(), "/api/stream") || strings.Contains(rec.Body.String(), "api_key") {
		t.Fatalf("rewritten body leaked forbidden content: %s", rec.Body.String())
	}
}

func TestPlaybackInfoHandlerFailClosedOnMappedEvidenceNonJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html>upstream error with /Videos/item1/stream</html>"))
	}))
	defer upstream.Close()

	h := newPlaybackInfoHandlerHarnessWithMappedEvidence(t, upstream.URL, "item1")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/emby/Items/item1/PlaybackInfo?UserId=emby-u1", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 fail closed", rec.Code)
	}
}

func TestPlaybackInfoHandlerPassesThroughUnmappedNonJSONWithoutPlayableLocation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	defer upstream.Close()

	h := newPlaybackInfoHandlerHarnessWithoutMappedEvidence(t, upstream.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/emby/Items/unmapped/PlaybackInfo?UserId=emby-u1", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || rec.Body.String() != "upstream unavailable" {
		t.Fatalf("unmapped passthrough status/body = %d %q", rec.Code, rec.Body.String())
	}
}

func TestPlaybackInfoHandlerFailClosedOnPlayableLocation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://emby:8096/Videos/item1/stream?api_key=secret")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	h := newPlaybackInfoHandlerHarnessWithoutMappedEvidence(t, upstream.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/emby/Items/item1/PlaybackInfo?UserId=emby-u1", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for playable Location", rec.Code)
	}
	if strings.Contains(rec.Header().Get("Location"), "/Videos/") {
		t.Fatalf("playable Location leaked: %q", rec.Header().Get("Location"))
	}
}
