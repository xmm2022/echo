package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	path115 = "/media/115/episode.mkv"
	path189 = "/media/189/episode.mkv"
)

// --- scripted sidecar ---

type linkOutcome struct {
	link *sidecarclient.DirectLink
	err  error
}

type streamOutcome struct {
	result *sidecarclient.StreamResult
	err    error
}

// fakeSidecar returns per-remote-path scripted outcomes and records call order so
// tests can assert chained fallback across copies.
type fakeSidecar struct {
	mu           sync.Mutex
	linkByPath   map[string]linkOutcome
	streamByPath map[string]streamOutcome
	linkCalls    []string
	streamCalls  []string
}

func (f *fakeSidecar) Link(_ context.Context, _ string, remotePath string) (*sidecarclient.DirectLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkCalls = append(f.linkCalls, remotePath)
	o, ok := f.linkByPath[remotePath]
	if !ok {
		return nil, &sidecarclient.SidecarHTTPError{StatusCode: http.StatusNotFound, Method: http.MethodPost, URL: remotePath}
	}
	return o.link, o.err
}

func (f *fakeSidecar) Stream(_ context.Context, req sidecarclient.StreamRequest) (*sidecarclient.StreamResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamCalls = append(f.streamCalls, req.RemotePath)
	o, ok := f.streamByPath[req.RemotePath]
	if !ok {
		return nil, &sidecarclient.SidecarHTTPError{StatusCode: http.StatusNotFound, Method: http.MethodGet, URL: req.RemotePath}
	}
	return o.result, o.err
}

func (f *fakeSidecar) linkCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.linkCalls)
}

// --- seed ---

type seeded struct {
	store     *store.Store
	fileID    int64
	blobID    int64
	copy115ID int64
	copy189ID int64
}

// seedTwoCopies builds a library entry with a live copy on 115 (newer last_seen)
// and on 189pc (older), so the default ordering tries 115 then 189pc.
func seedTwoCopies(t *testing.T) seeded {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "echo.db"))
	st, err := store.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

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
	blob, err := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 1024, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
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
	c115, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       blob.ID,
		Provider:     "115",
		AccountID:    "115-main",
		SidecarID:    "default",
		StorageMount: "/115-main",
		RemotePath:   path115,
		Status:       "live",
		LastSeen:     20,
	})
	if err != nil {
		t.Fatalf("insert 115 copy: %v", err)
	}
	c189, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID:       blob.ID,
		Provider:     "189pc",
		AccountID:    "189-main",
		SidecarID:    "default",
		StorageMount: "/189-main",
		RemotePath:   path189,
		Status:       "live",
		LastSeen:     10,
	})
	if err != nil {
		t.Fatalf("insert 189 copy: %v", err)
	}
	return seeded{store: st, fileID: entry.ID, blobID: blob.ID, copy115ID: c115.ID, copy189ID: c189.ID}
}

func testClock() func() time.Time { return func() time.Time { return time.Unix(1000, 0) } }

func intToStr(n int64) string { return strconv.FormatInt(n, 10) }

func newRestoreDeps(st *store.Store, sc Sidecar) RestoreDeps {
	return RestoreDeps{
		Resolver: restore.NewResolver(st.Queries, testClock()),
		Sidecar:  sc,
		Cache:    restore.NewLinkCache(testClock()),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func doRestore(t *testing.T, deps RestoreDeps, fileID, prefer string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/restore/{file_id}", Restore(deps))
	target := "/api/restore/" + fileID
	if prefer != "" {
		target += "?prefer=" + prefer
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func copyStatus(t *testing.T, st *store.Store, copyID int64) string {
	t.Helper()
	var status string
	if err := st.DB.QueryRow("SELECT status FROM file_copies WHERE id = ?", copyID).Scan(&status); err != nil {
		t.Fatalf("query copy %d status: %v", copyID, err)
	}
	return status
}

func copyScheduler(t *testing.T, st *store.Store, copyID int64) string {
	t.Helper()
	var schedulerState string
	if err := st.DB.QueryRow("SELECT scheduler_state FROM file_copies WHERE id = ?", copyID).Scan(&schedulerState); err != nil {
		t.Fatalf("query copy %d scheduler_state: %v", copyID, err)
	}
	return schedulerState
}

func accountScheduler(t *testing.T, st *store.Store, accountID string) string {
	t.Helper()
	var schedulerState string
	if err := st.DB.QueryRow("SELECT scheduler_state FROM accounts WHERE id = ?", accountID).Scan(&schedulerState); err != nil {
		t.Fatalf("query account %s scheduler_state: %v", accountID, err)
	}
	return schedulerState
}

// --- tests ---

func TestRestoreReturnsLinkJSONOnSuccess(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: {link: &sidecarclient.DirectLink{
			URL:       "https://dl.example/115/episode.mkv",
			Headers:   http.Header{"X-Download-Token": []string{"tok"}},
			ExpiresAt: time.Unix(5000, 0),
		}},
	}}
	deps := newRestoreDeps(seed.store, sc)

	rec := doRestore(t, deps, intToStr(seed.fileID), "115")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		URL      string            `json:"url"`
		Headers  map[string]string `json:"headers"`
		Provider string            `json:"provider"`
		CopyID   int64             `json:"copy_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.URL != "https://dl.example/115/episode.mkv" {
		t.Fatalf("url = %q", resp.URL)
	}
	if resp.Provider != "115" {
		t.Fatalf("provider = %q, want 115", resp.Provider)
	}
	if resp.CopyID != seed.copy115ID {
		t.Fatalf("copy_id = %d, want %d", resp.CopyID, seed.copy115ID)
	}
	if resp.Headers["X-Download-Token"] != "tok" {
		t.Fatalf("headers = %v, want X-Download-Token=tok", resp.Headers)
	}
}

func TestRestoreFallsBackWhenFirstCopyGone(t *testing.T) {
	seed := seedTwoCopies(t)
	// Real hardware has no 404/410 dead signal: a confirmed object-missing link
	// envelope (HTTP 200 + code != 200) is the only confirm-dead signal, and it
	// retires the copy then falls back to the next.
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: {err: &sidecarclient.SidecarTypedError{
			Kind:          sidecarclient.SidecarErrObjectMissing,
			Operation:     "link",
			HTTPStatus:    http.StatusOK,
			OpenListCode:  500,
			SafeMessage:   "object not found",
			EvidenceClass: "json_envelope",
			Confidence:    "confirmed",
		}},
		path189: {link: &sidecarclient.DirectLink{URL: "https://dl.example/189/episode.mkv"}},
	}}
	deps := newRestoreDeps(seed.store, sc)

	rec := doRestore(t, deps, intToStr(seed.fileID), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Provider string `json:"provider"`
		CopyID   int64  `json:"copy_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Provider != "189pc" {
		t.Fatalf("provider = %q, want 189pc after fallback", resp.Provider)
	}
	if resp.CopyID != seed.copy189ID {
		t.Fatalf("copy_id = %d, want %d", resp.CopyID, seed.copy189ID)
	}
	if sc.linkCallCount() != 2 {
		t.Fatalf("link calls = %d, want 2 (fallback)", sc.linkCallCount())
	}
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "dead" {
		t.Fatalf("copy 115 status = %q, want dead", got)
	}
}

func TestRestoreFallsBackOn5xxWithoutMarkingDead(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: {err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusBadGateway, Method: http.MethodPost, URL: path115}},
		path189: {link: &sidecarclient.DirectLink{URL: "https://dl.example/189/episode.mkv"}},
	}}
	deps := newRestoreDeps(seed.store, sc)

	rec := doRestore(t, deps, intToStr(seed.fileID), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live (5xx must not mark dead)", got)
	}
}

func TestRestoreFallsBackOnOther4xxWithoutMarkingDead(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: {err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusForbidden, Method: http.MethodPost, URL: path115}},
		path189: {link: &sidecarclient.DirectLink{URL: "https://dl.example/189/episode.mkv"}},
	}}
	deps := newRestoreDeps(seed.store, sc)

	rec := doRestore(t, deps, intToStr(seed.fileID), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fallback past 403); body=%s", rec.Code, rec.Body.String())
	}
	// A non-404/410 4xx (e.g. auth) is treated as this-copy-unavailable: try the
	// next copy but do NOT mark it dead — it may be a transient auth problem.
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live (403 must not mark dead)", got)
	}
}

// TestRestoreFallsBackWhenFirstCopySuspect is the restore-path regression test for
// the abort-early bug: an unconfirmed object-missing (suspect) failure on the first
// copy must fall back to a healthy copy B and return its link (200), NOT abort with
// 503. Copy A stays live (scheduler_state suspect_dead) so a transient fault never
// causes a permanent loss; copy B serves the link. Before the fix the suspect case
// returned abort=true → 503 with copy B never tried.
func TestRestoreFallsBackWhenFirstCopySuspect(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: {err: &sidecarclient.SidecarTypedError{
			Kind:          sidecarclient.SidecarErrObjectMissing,
			Operation:     "link",
			HTTPStatus:    http.StatusOK,
			OpenListCode:  500,
			SafeMessage:   "temporarily unavailable",
			EvidenceClass: "html_snippet",
			Confidence:    "suspect", // not "confirmed" → suspect, not gone
		}},
		path189: {link: &sidecarclient.DirectLink{URL: "https://dl.example/189/episode.mkv"}},
	}}
	deps := newRestoreDeps(seed.store, sc)

	rec := doRestore(t, deps, intToStr(seed.fileID), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fall back to copy B on suspect); body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Provider string `json:"provider"`
		CopyID   int64  `json:"copy_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.CopyID != seed.copy189ID {
		t.Fatalf("copy_id = %d, want %d (copy B after fallback)", resp.CopyID, seed.copy189ID)
	}
	if sc.linkCallCount() != 2 {
		t.Fatalf("link calls = %d, want 2 (first suspect, fall back to second)", sc.linkCallCount())
	}
	// First copy must NOT be confirm-dead: it stays live, only flagged suspect_dead.
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live (suspect must NOT mark dead)", got)
	}
	if got := copyScheduler(t, seed.store, seed.copy115ID); got != "suspect_dead" {
		t.Fatalf("copy 115 scheduler_state = %q, want suspect_dead", got)
	}
}

func TestRestoreReturns503OnUnreachable(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: {err: sidecarclient.ErrSidecarUnreachable},
		path189: {link: &sidecarclient.DirectLink{URL: "https://dl.example/189/episode.mkv"}},
	}}
	deps := newRestoreDeps(seed.store, sc)

	rec := doRestore(t, deps, intToStr(seed.fileID), "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	// Unreachable aborts immediately, the second copy is never tried.
	if sc.linkCallCount() != 1 {
		t.Fatalf("link calls = %d, want 1 (abort on unreachable)", sc.linkCallCount())
	}
}

func TestRestoreReturns404WhenAllCopiesDead(t *testing.T) {
	seed := seedTwoCopies(t)
	// "All copies dead → 404" is now reason-aware: 404 is reserved for copies that
	// are CONFIRMED dead. On real hardware the only confirm-dead signal is a link
	// operation that returns HTTP 200 with an OpenList envelope code != 200 and a
	// confirmed object-missing message, so both copies must fail that way for the
	// request to legitimately collapse to 404 (a transient/suspect failure would be
	// 503 instead — see TestRestoreReturns503WhenAllCopiesTransient).
	confirmedGone := linkOutcome{err: &sidecarclient.SidecarTypedError{
		Kind:          sidecarclient.SidecarErrObjectMissing,
		Operation:     "link",
		HTTPStatus:    http.StatusOK,
		OpenListCode:  500,
		SafeMessage:   "object not found",
		EvidenceClass: "json_envelope",
		Confidence:    "confirmed",
	}}
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: confirmedGone,
		path189: confirmedGone,
	}}
	deps := newRestoreDeps(seed.store, sc)

	rec := doRestore(t, deps, intToStr(seed.fileID), "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Echo-Reason"); got == "" {
		t.Fatal("missing X-Echo-Reason header")
	}
	var resp struct {
		DeadCopies []struct {
			CopyID   int64  `json:"copy_id"`
			Provider string `json:"provider"`
			Reason   string `json:"reason"`
		} `json:"dead_copies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(resp.DeadCopies) != 2 {
		t.Fatalf("dead_copies count = %d, want 2", len(resp.DeadCopies))
	}
	// Both copies are now confirmed dead.
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "dead" {
		t.Fatalf("copy 115 status = %q, want dead", got)
	}
	if got := copyStatus(t, seed.store, seed.copy189ID); got != "dead" {
		t.Fatalf("copy 189 status = %q, want dead", got)
	}
}

// TestRestoreReturns503WhenAllCopiesTransient is the counterpart to the all-dead
// 404 case: when every live copy fails TRANSIENTLY (here a raw 5xx SidecarHTTPError,
// which never marks dead), exhausting the copies must surface 503 "try later", NOT a
// permanent 404 — a transient backend blip across all copies is not a dead file.
func TestRestoreReturns503WhenAllCopiesTransient(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: {err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusBadGateway, Method: http.MethodPost, URL: path115}},
		path189: {err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusBadGateway, Method: http.MethodPost, URL: path189}},
	}}
	deps := newRestoreDeps(seed.store, sc)

	rec := doRestore(t, deps, intToStr(seed.fileID), "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (all copies transient); body=%s", rec.Code, rec.Body.String())
	}
	if sc.linkCallCount() != 2 {
		t.Fatalf("link calls = %d, want 2 (both copies tried before 503)", sc.linkCallCount())
	}
	// Neither copy is marked dead — a 5xx is not a confirm-dead signal.
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live", got)
	}
	if got := copyStatus(t, seed.store, seed.copy189ID); got != "live" {
		t.Fatalf("copy 189 status = %q, want live", got)
	}
}

func TestRestoreReturns404ForMissingFile(t *testing.T) {
	seed := seedTwoCopies(t)
	deps := newRestoreDeps(seed.store, &fakeSidecar{})

	rec := doRestore(t, deps, "99999", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRestoreReturns400ForInvalidFileID(t *testing.T) {
	seed := seedTwoCopies(t)
	deps := newRestoreDeps(seed.store, &fakeSidecar{})

	rec := doRestore(t, deps, "not-a-number", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRestoreMarksConfirmedDeadOnlyForObjectMissingLinkEnvelope covers the only
// real-hardware confirm-dead signal: a /api/fs/link call that returns HTTP 200 with
// an OpenList envelope whose code != 200 and a confirmed object-missing message.
// Harness note: the plan's deps.sidecar.linkErr / deps.resolver.markConfirmedDeadCalls
// do not exist here — the resolver is the real restore.Resolver over a real store, so
// the "confirmed dead once" assertion is read from the DB (the linked copy becomes
// status 'dead' + scheduler_state 'confirmed_dead').
func TestRestoreMarksConfirmedDeadOnlyForObjectMissingLinkEnvelope(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{linkByPath: map[string]linkOutcome{
		path115: {err: &sidecarclient.SidecarTypedError{
			Kind:          sidecarclient.SidecarErrObjectMissing,
			Operation:     "link",
			HTTPStatus:    http.StatusOK,
			OpenListCode:  500,
			SafeMessage:   "object not found",
			EvidenceClass: "json_envelope",
			Confidence:    "confirmed",
		}},
	}}
	deps := newRestoreDeps(seed.store, sc)

	doRestore(t, deps, intToStr(seed.fileID), "115")

	if got := copyStatus(t, seed.store, seed.copy115ID); got != "dead" {
		t.Fatalf("copy 115 status = %q, want dead (confirmed object-missing)", got)
	}
	if got := copyScheduler(t, seed.store, seed.copy115ID); got != "confirmed_dead" {
		t.Fatalf("copy 115 scheduler_state = %q, want confirmed_dead", got)
	}
}

func TestRestoreServesFromCacheWithoutSidecarCall(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{} // empty: any Link call would 404
	deps := newRestoreDeps(seed.store, sc)
	deps.Cache.Put(seed.blobID, seed.copy115ID, &sidecarclient.DirectLink{URL: "https://dl.example/cached"})

	rec := doRestore(t, deps, intToStr(seed.fileID), "115")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.URL != "https://dl.example/cached" {
		t.Fatalf("url = %q, want cached link", resp.URL)
	}
	if sc.linkCallCount() != 0 {
		t.Fatalf("link calls = %d, want 0 (served from cache)", sc.linkCallCount())
	}
}
