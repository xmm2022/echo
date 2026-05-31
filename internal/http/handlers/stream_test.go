package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
)

func (f *fakeSidecar) streamCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.streamCalls)
}

func newStreamDeps(st *store.Store, sc Sidecar) StreamDeps {
	return StreamDeps{
		Resolver: restore.NewResolver(st.Queries, testClock()),
		Sidecar:  sc,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func doStream(t *testing.T, deps StreamDeps, fileID, prefer string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/stream/{file_id}", Stream(deps))
	target := "/api/stream/" + fileID
	if prefer != "" {
		target += "?prefer=" + prefer
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func body(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

func TestStreamProxies200WithFullBody(t *testing.T) {
	seed := seedTwoCopies(t)
	payload := "0123456789"
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {result: &sidecarclient.StreamResult{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Length": []string{"10"},
				"Content-Type":   []string{"video/mp4"},
				"Accept-Ranges":  []string{"bytes"},
			},
			Body: body(payload),
		}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "115", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != payload {
		t.Fatalf("body = %q, want %q", rec.Body.String(), payload)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
}

func TestStreamProxies206PartialWithByteCount(t *testing.T) {
	seed := seedTwoCopies(t)
	part := "0123"
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {result: &sidecarclient.StreamResult{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Range":  []string{"bytes 0-3/10"},
				"Content-Length": []string{"4"},
			},
			Body: body(part),
		}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "115", http.Header{"Range": []string{"bytes=0-3"}})

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 4 {
		t.Fatalf("body length = %d, want 4 (byte count must match)", rec.Body.Len())
	}
	if rec.Body.String() != part {
		t.Fatalf("body = %q, want %q", rec.Body.String(), part)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-3/10" {
		t.Fatalf("Content-Range = %q, want bytes 0-3/10", got)
	}
}

func TestStreamProxies304WithoutBody(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {result: &sidecarclient.StreamResult{
			StatusCode: http.StatusNotModified,
			Header:     http.Header{"ETag": []string{`"etag"`}},
			Body:       nil,
		}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "115", http.Header{"If-None-Match": []string{`"etag"`}})

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 body length = %d, want 0", rec.Body.Len())
	}
	if got := rec.Header().Get("ETag"); got != `"etag"` {
		t.Fatalf("ETag = %q, want \"etag\"", got)
	}
}

func TestStreamProxies416RangeNotSatisfiable(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {result: &sidecarclient.StreamResult{
			StatusCode: http.StatusRequestedRangeNotSatisfiable,
			Header:     http.Header{"Content-Range": []string{"bytes */10"}},
			Body:       body(""),
		}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "115", http.Header{"Range": []string{"bytes=100-200"}})

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", rec.Code)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes */10" {
		t.Fatalf("Content-Range = %q, want bytes */10", got)
	}
}

func TestStreamFallsBackWhenFirstCopyGone(t *testing.T) {
	seed := seedTwoCopies(t)
	// Real hardware has no 404/410 dead signal. A stream call cannot itself confirm
	// object-missing (that needs the link JSON envelope), so a confirmed dead copy
	// is established the only way it can be on real hardware: via a link-operation
	// confirmed object-missing typed error.
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {err: &sidecarclient.SidecarTypedError{
			Kind:          sidecarclient.SidecarErrObjectMissing,
			Operation:     "link",
			HTTPStatus:    http.StatusOK,
			OpenListCode:  500,
			SafeMessage:   "object not found",
			EvidenceClass: "json_envelope",
			Confidence:    "confirmed",
		}},
		path189: {result: &sidecarclient.StreamResult{StatusCode: http.StatusOK, Header: http.Header{}, Body: body("ok")}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want ok (from 189pc)", rec.Body.String())
	}
	if sc.streamCallCount() != 2 {
		t.Fatalf("stream calls = %d, want 2 (fallback)", sc.streamCallCount())
	}
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "dead" {
		t.Fatalf("copy 115 status = %q, want dead", got)
	}
}

func TestStreamFallsBackOn5xxWithoutMarkingDead(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusBadGateway, Method: http.MethodGet, URL: path115}},
		path189: {result: &sidecarclient.StreamResult{StatusCode: http.StatusOK, Header: http.Header{}, Body: body("ok")}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live (5xx must not mark dead)", got)
	}
}

func TestStreamReturns503OnUnreachable(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {err: sidecarclient.ErrSidecarUnreachable},
		path189: {result: &sidecarclient.StreamResult{StatusCode: http.StatusOK, Header: http.Header{}, Body: body("ok")}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "", nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if sc.streamCallCount() != 1 {
		t.Fatalf("stream calls = %d, want 1 (abort on unreachable)", sc.streamCallCount())
	}
}

func TestStreamReturns404WhenAllCopiesDead(t *testing.T) {
	seed := seedTwoCopies(t)
	// "All copies dead → 404" is reason-aware: 404 is reserved for CONFIRMED-dead
	// copies. A stream call cannot itself confirm object-missing (that needs the link
	// JSON envelope), so — exactly as TestStreamFallsBackWhenFirstCopyGone does — the
	// only way to put both copies into confirmed-dead on real hardware is a link
	// operation confirmed object-missing typed error. A raw 404/410 SidecarHTTPError
	// is now a transient "rejected" (→ 503), not a dead signal.
	confirmedGone := streamOutcome{err: &sidecarclient.SidecarTypedError{
		Kind:          sidecarclient.SidecarErrObjectMissing,
		Operation:     "link",
		HTTPStatus:    http.StatusOK,
		OpenListCode:  500,
		SafeMessage:   "object not found",
		EvidenceClass: "json_envelope",
		Confidence:    "confirmed",
	}}
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: confirmedGone,
		path189: confirmedGone,
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("X-Echo-Reason"); got == "" {
		t.Fatal("missing X-Echo-Reason header")
	}
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "dead" {
		t.Fatalf("copy 115 status = %q, want dead", got)
	}
	if got := copyStatus(t, seed.store, seed.copy189ID); got != "dead" {
		t.Fatalf("copy 189 status = %q, want dead", got)
	}
}

func TestStreamReturns400ForInvalidFileID(t *testing.T) {
	seed := seedTwoCopies(t)
	deps := newStreamDeps(seed.store, &fakeSidecar{})

	rec := doStream(t, deps, "not-a-number", "", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestStreamFallsBackWhenFirstCopySuspect is the regression test for the abort-early
// bug: a TRANSIENT/suspect failure on the first copy (a /d/ HTML 500, classified as
// SidecarErrTransient, confidence suspect) must NOT abort the request with 503 — it
// must fall back to the healthy copy B and serve its bytes (200). Copy A stays live
// (scheduler_state suspect_dead), copy B is served. Before the fix handleCopyFailure
// returned abort=true for the suspect case, so this returned 503 with copy B never
// tried.
func TestStreamFallsBackWhenFirstCopySuspect(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {err: &sidecarclient.SidecarTypedError{
			Kind:          sidecarclient.SidecarErrTransient,
			Operation:     "stream",
			HTTPStatus:    http.StatusInternalServerError,
			SafeMessage:   "failed to get file",
			EvidenceClass: "html_snippet",
			Confidence:    "suspect",
		}},
		path189: {result: &sidecarclient.StreamResult{StatusCode: http.StatusOK, Header: http.Header{}, Body: body("copyB")}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fall back to copy B on suspect); body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "copyB" {
		t.Fatalf("body = %q, want copyB (served from 189pc after fallback)", rec.Body.String())
	}
	if sc.streamCallCount() != 2 {
		t.Fatalf("stream calls = %d, want 2 (first suspect, fall back to second)", sc.streamCallCount())
	}
	// First copy must NOT be confirm-dead: it stays live, only flagged suspect_dead.
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live (suspect must NOT mark dead)", got)
	}
	if got := copyScheduler(t, seed.store, seed.copy115ID); got != "suspect_dead" {
		t.Fatalf("copy 115 scheduler_state = %q, want suspect_dead", got)
	}
}

// TestStreamFallsBackOnAccountFailureExercisesAccountScheduler is the account-path
// fall-back guard: an account (auth/token) failure on copy A must NOT abort the
// request — it must cool the account (RecordAccountFailure → scheduler_state
// 'token_suspect') and fall back to a healthy copy B, serving copy B's bytes (200).
// prefer=115 only REORDERS the live set (provider_rank) — it does NOT limit it, so
// LiveCopies still returns BOTH copies (115 first, then 189pc). Copy A's account
// fault never marks the copy itself dead (it stays status 'live'); only the account
// is cooled. This is a RED guard for the abort-early regression: if the account case
// returned abort=true, copy B would never be tried (1 stream call) and the request
// would 503 with an empty body instead of falling back to 200 + copyB.
func TestStreamFallsBackOnAccountFailureExercisesAccountScheduler(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {err: &sidecarclient.SidecarTypedError{
			Kind:          sidecarclient.SidecarErrAuthOrAccount,
			Operation:     "stream",
			HTTPStatus:    http.StatusOK,
			OpenListCode:  401,
			SafeMessage:   "unauthorized",
			EvidenceClass: "json_envelope",
			Confidence:    "confirmed",
		}},
		path189: {result: &sidecarclient.StreamResult{StatusCode: http.StatusOK, Header: http.Header{}, Body: body("copyB")}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "115", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (account fault on copy A falls back to copy B); body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "copyB" {
		t.Fatalf("body = %q, want copyB (served from 189pc after account fallback)", rec.Body.String())
	}
	if sc.streamCallCount() != 2 {
		t.Fatalf("stream calls = %d, want 2 (copy A account fault, fall back to copy B)", sc.streamCallCount())
	}
	if got := accountScheduler(t, seed.store, "115-main"); got != "token_suspect" {
		t.Fatalf("account 115-main scheduler_state = %q, want token_suspect", got)
	}
	// Copy A itself is not marked dead — only its account is cooled down.
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live (account fault must NOT mark copy dead)", got)
	}
}

// TestStreamReturns503WhenAllCopiesAccountFailure preserves the original account-fault
// coverage: when EVERY live copy fails with an account fault (no healthy copy to fall
// back to), exhaustion must surface 503 (temporary; an account fault is recoverable
// via re-auth/TTL), NOT 404 — and each failing copy's account is cooled into
// scheduler_state 'token_suspect' without marking the copy itself dead.
func TestStreamReturns503WhenAllCopiesAccountFailure(t *testing.T) {
	seed := seedTwoCopies(t)
	accountFault := streamOutcome{err: &sidecarclient.SidecarTypedError{
		Kind:          sidecarclient.SidecarErrAuthOrAccount,
		Operation:     "stream",
		HTTPStatus:    http.StatusOK,
		OpenListCode:  401,
		SafeMessage:   "unauthorized",
		EvidenceClass: "json_envelope",
		Confidence:    "confirmed",
	}}
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: accountFault,
		path189: accountFault,
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "115", nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for account failure on all copies (not 404)", rec.Code)
	}
	if got := accountScheduler(t, seed.store, "115-main"); got != "token_suspect" {
		t.Fatalf("account 115-main scheduler_state = %q, want token_suspect", got)
	}
	// The copy itself is not marked dead — only the account is cooled down.
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live (account fault must NOT mark copy dead)", got)
	}
}

// TestStreamDoesNotMarkDeadOnHTML500 covers the real-hardware /d/ failure shape: a
// transient HTML 500 stream error must NOT confirm-dead the copy (it stays status
// 'live'), and the request must surface 503 (try later) rather than 404 (gone).
// Harness note: the plan's deps.sidecar.err / deps.resolver.markDeadCalls do not
// exist here — the resolver is the real restore.Resolver over a real store, so the
// "mark dead untouched" assertion is read from the DB (status stays live, scheduler
// state is suspect_dead, never confirmed_dead).
func TestStreamDoesNotMarkDeadOnHTML500(t *testing.T) {
	seed := seedTwoCopies(t)
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {err: &sidecarclient.SidecarTypedError{
			Kind:          sidecarclient.SidecarErrTransient,
			Operation:     "stream",
			HTTPStatus:    http.StatusInternalServerError,
			SafeMessage:   "failed to get file",
			EvidenceClass: "html_snippet",
			Confidence:    "suspect",
		}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "115", nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for suspect stream failure", rec.Code)
	}
	if got := copyStatus(t, seed.store, seed.copy115ID); got != "live" {
		t.Fatalf("copy 115 status = %q, want live (suspect must NOT mark dead)", got)
	}
	if got := copyScheduler(t, seed.store, seed.copy115ID); got != "suspect_dead" {
		t.Fatalf("copy 115 scheduler_state = %q, want suspect_dead", got)
	}
}
