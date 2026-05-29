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
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusNotFound, Method: http.MethodGet, URL: path115}},
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
	sc := &fakeSidecar{streamByPath: map[string]streamOutcome{
		path115: {err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusNotFound, Method: http.MethodGet, URL: path115}},
		path189: {err: &sidecarclient.SidecarHTTPError{StatusCode: http.StatusGone, Method: http.MethodGet, URL: path189}},
	}}
	deps := newStreamDeps(seed.store, sc)

	rec := doStream(t, deps, intToStr(seed.fileID), "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("X-Echo-Reason"); got == "" {
		t.Fatal("missing X-Echo-Reason header")
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
