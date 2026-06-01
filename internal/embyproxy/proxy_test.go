package embyproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/playback"
	"github.com/xmm2022/echo/internal/sidecarclient"
)

func discardProxyLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestReverseProxyStripsPrefixAndRewritesHeaders stands up a fake upstream and asserts
// the transparent fallback strips the /emby prefix, preserves query + ordinary headers,
// drops hop-by-hop request headers, rewrites Location/Set-Cookie on the way back, and
// streams the body through unchanged.
func TestReverseProxyStripsPrefixAndRewritesHeaders(t *testing.T) {
	const body = "upstream-body-bytes"

	var (
		gotPath     string
		gotQuery    string
		gotConn     string
		gotXTest    string
		locationOut string // set once the server URL is known, read inside the handler
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotConn = r.Header.Get("Connection")
		gotXTest = r.Header.Get("X-Test")
		// Emit an upstream-origin Location plus a Domain-scoped cookie to verify rewrite.
		w.Header().Set("Location", locationOut)
		w.Header().Add("Set-Cookie", "EmbyAuth=abc; Domain=upstream; Path=/; HttpOnly")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()
	locationOut = upstream.URL + "/web/x"

	upstreamBase, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	public, _ := url.Parse("https://echo.example.com")

	h := NewReverseProxy(ProxyConfig{
		UpstreamBase: upstreamBase,
		PublicBase:   public,
		ProxyPrefix:  "/emby",
	}, upstream.Client(), discardProxyLogger())

	req := httptest.NewRequest(http.MethodGet, "/emby/Users/x?api_key=k&foo=bar", nil)
	req.Header.Set("X-Test", "ok")
	req.Header.Set("Connection", "keep-alive")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Prefix stripped, query preserved.
	if gotPath != "/Users/x" {
		t.Fatalf("upstream path = %q, want /Users/x", gotPath)
	}
	if gotQuery != "api_key=k&foo=bar" {
		t.Fatalf("upstream query = %q, want api_key=k&foo=bar", gotQuery)
	}
	// Hop-by-hop dropped, ordinary header forwarded.
	if gotConn != "" {
		t.Fatalf("upstream saw Connection = %q, want empty", gotConn)
	}
	if gotXTest != "ok" {
		t.Fatalf("upstream X-Test = %q, want ok", gotXTest)
	}

	// Location rewritten to public origin + prefix.
	if loc := rec.Header().Get("Location"); loc != "https://echo.example.com/emby/web/x" {
		t.Fatalf("Location = %q, want https://echo.example.com/emby/web/x", loc)
	}
	// Set-Cookie: Domain stripped, Path pinned to /emby.
	cookie := rec.Header().Get("Set-Cookie")
	if strings.Contains(cookie, "Domain=") || !strings.Contains(cookie, "Path=/emby") {
		t.Fatalf("Set-Cookie = %q, want Domain stripped and Path=/emby", cookie)
	}
	// Body streamed through unchanged.
	if rec.Body.String() != body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestReverseProxyBarePrefixMapsToRoot asserts a request to the bare prefix forwards to
// upstream "/".
func TestReverseProxyBarePrefixMapsToRoot(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	upstreamBase, _ := url.Parse(upstream.URL)
	public, _ := url.Parse("https://echo.example.com")
	h := NewReverseProxy(ProxyConfig{UpstreamBase: upstreamBase, PublicBase: public, ProxyPrefix: "/emby"}, upstream.Client(), discardProxyLogger())

	req := httptest.NewRequest(http.MethodGet, "/emby", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if gotPath != "/" {
		t.Fatalf("upstream path = %q, want / for bare prefix", gotPath)
	}
}

// TestReverseProxyPreservesWebSocketUpgrade is the regression test for the upgrade bug:
// the Rewrite hook must not rebuild pr.Out.Header from the (never-stripped) inbound
// request, because that drops the Connection: Upgrade / Upgrade headers that
// httputil.ReverseProxy re-adds to pr.Out for protocol upgrades. Emby's web client opens
// /embywebsocket through this fallback proxy, so upstream MUST still receive the upgrade
// headers. We do not perform a real WS handshake; we only assert the forwarded headers.
func TestReverseProxyPreservesWebSocketUpgrade(t *testing.T) {
	var (
		gotConnection string
		gotUpgrade    string
		gotPath       string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConnection = r.Header.Get("Connection")
		gotUpgrade = r.Header.Get("Upgrade")
		gotPath = r.URL.Path
		// Return a plain 200; the test only inspects the forwarded request headers.
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	upstreamBase, _ := url.Parse(upstream.URL)
	public, _ := url.Parse("https://echo.example.com")
	h := NewReverseProxy(ProxyConfig{UpstreamBase: upstreamBase, PublicBase: public, ProxyPrefix: "/emby"}, upstream.Client(), discardProxyLogger())

	req := httptest.NewRequest(http.MethodGet, "/emby/embywebsocket?api_key=k", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if gotPath != "/embywebsocket" {
		t.Fatalf("upstream path = %q, want /embywebsocket", gotPath)
	}
	if !strings.EqualFold(gotUpgrade, "websocket") {
		t.Fatalf("upstream Upgrade = %q, want websocket", gotUpgrade)
	}
	// Connection must still announce the upgrade token (case-insensitive contains).
	if !connectionContainsToken(gotConnection, "Upgrade") {
		t.Fatalf("upstream Connection = %q, want it to contain the Upgrade token", gotConnection)
	}
}

// connectionContainsToken reports whether the comma-separated Connection header value
// contains tok, comparing case-insensitively (RFC 7230 tokens are case-insensitive).
func connectionContainsToken(connection, tok string) bool {
	for _, part := range strings.Split(connection, ",") {
		if strings.EqualFold(strings.TrimSpace(part), tok) {
			return true
		}
	}
	return false
}

// TestReverseProxyStripsConnectionNamedHeader locks down that removing the manual
// StripHopByHop call did NOT regress hop-by-hop handling: a header NAMED by the request's
// Connection token list (here X-Secret) must not reach upstream. ReverseProxy strips these
// natively (RFC 7230 §6.1) before the Rewrite hook runs.
func TestReverseProxyStripsConnectionNamedHeader(t *testing.T) {
	var (
		gotConn   string
		gotSecret string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConn = r.Header.Get("Connection")
		gotSecret = r.Header.Get("X-Secret")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	upstreamBase, _ := url.Parse(upstream.URL)
	public, _ := url.Parse("https://echo.example.com")
	h := NewReverseProxy(ProxyConfig{UpstreamBase: upstreamBase, PublicBase: public, ProxyPrefix: "/emby"}, upstream.Client(), discardProxyLogger())

	req := httptest.NewRequest(http.MethodGet, "/emby/Users/x", nil)
	req.Header.Set("Connection", "X-Secret")
	req.Header.Set("X-Secret", "leak")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if gotConn != "" {
		t.Fatalf("upstream Connection = %q, want empty", gotConn)
	}
	if gotSecret != "" {
		t.Fatalf("upstream X-Secret = %q, want empty (named by Connection, must be stripped)", gotSecret)
	}
}

// TestReverseProxyPreservesEncodedSlashInPath asserts that an encoded slash (%2F) inside a
// path segment round-trips to upstream as %2F rather than collapsing to a literal "/",
// because the Rewrite hook sets pr.Out.URL.RawPath to the prefix-stripped escaped path.
func TestReverseProxyPreservesEncodedSlashInPath(t *testing.T) {
	var (
		gotEscaped string
		gotPath    string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscaped = r.URL.EscapedPath()
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	upstreamBase, _ := url.Parse(upstream.URL)
	public, _ := url.Parse("https://echo.example.com")
	h := NewReverseProxy(ProxyConfig{UpstreamBase: upstreamBase, PublicBase: public, ProxyPrefix: "/emby"}, upstream.Client(), discardProxyLogger())

	// /emby/Items/a%2Fb -> upstream should see escaped /Items/a%2Fb (decoded /Items/a/b).
	req := httptest.NewRequest(http.MethodGet, "/emby/Items/a%2Fb", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if gotEscaped != "/Items/a%2Fb" {
		t.Fatalf("upstream escaped path = %q, want /Items/a%%2Fb", gotEscaped)
	}
	if gotPath != "/Items/a/b" {
		t.Fatalf("upstream decoded path = %q, want /Items/a/b", gotPath)
	}
}

// TestReverseProxyGuardDeniesStreamBeforeUpstream proves the Phase 3 fail-closed playback
// guard is wired ahead of the upstream proxy: a suspicious stream path is denied with a
// controlled 503 and never reaches upstream. An ordinary API path is still proxied through
// to prove the guard does not over-block the fallback.
func TestReverseProxyGuardDeniesStreamBeforeUpstream(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Videos/123/stream" {
			t.Fatalf("upstream was called for a guarded stream path: %s", r.URL.Path)
		}
		upstreamHits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	upstreamBase, _ := url.Parse(upstream.URL)
	public, _ := url.Parse("https://echo.example.com")
	h := NewReverseProxy(ProxyConfig{UpstreamBase: upstreamBase, PublicBase: public, ProxyPrefix: "/emby"}, upstream.Client(), discardProxyLogger())

	// Guarded stream path: denied with 503 + X-Echo-Reason, upstream untouched.
	req := httptest.NewRequest(http.MethodGet, "/emby/Videos/123/stream", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("guarded stream status = %d, want 503", rec.Code)
	}
	if reason := rec.Header().Get("X-Echo-Reason"); reason != "temporary_unavailable" {
		t.Fatalf("X-Echo-Reason = %q, want temporary_unavailable", reason)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream hits = %d after guarded request, want 0", upstreamHits)
	}

	// Ordinary API path: proxied through (upstream sees it, guard does not over-block).
	req2 := httptest.NewRequest(http.MethodGet, "/emby/Users/x/Items", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("ordinary API status = %d, want 204 (proxied through)", rec2.Code)
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d after ordinary request, want 1", upstreamHits)
	}
}

// TestPlaybackInfoToStreamEndToEndWithFakes is the Phase-4 e2e golden gate: a PlaybackInfo
// request flows through the mounted PlaybackInfoHandler (which forwards to a fake upstream
// Emby returning single_source.json), gets rewritten so the source's playable URL becomes a
// real Echo /emby/stream/{token} URL backed by a freshly minted playback session, and that
// stream URL — routed back through the SAME mounted router and the SAME store — serves real
// 206 partial bytes from a Range-honoring fake sidecar. It proves the session minted during
// rewrite is visible to the StreamHandler lookup and the byte path is wired end to end.
func TestPlaybackInfoToStreamEndToEndWithFakes(t *testing.T) {
	fakeEmby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items/item1/PlaybackInfo" {
			t.Fatalf("upstream path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readFixture(t, "single_source.json"))
	}))
	defer fakeEmby.Close()

	fakeSidecar := newFakeStreamingSidecar(t, []byte("0123456789abcdef"))
	echo := newFullEmbyProxyHarness(t, fakeEmby.URL, fakeSidecar)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/emby/Items/item1/PlaybackInfo?UserId=emby-u1&DeviceId=dev1", nil)
	echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo status=%d body=%s", rec.Code, rec.Body.String())
	}
	streamURL := extractFirstEchoStreamURL(t, rec.Body.Bytes())
	if strings.Contains(streamURL, "/api/stream") || !strings.Contains(streamURL, "/emby/stream/") {
		t.Fatalf("stream URL = %q", streamURL)
	}

	streamReq := httptest.NewRequest(http.MethodGet, streamURL, nil)
	streamReq.Header.Set("Range", "bytes=0-3")
	streamRec := httptest.NewRecorder()
	echo.ServeHTTP(streamRec, streamReq)
	if streamRec.Code != http.StatusPartialContent {
		t.Fatalf("stream status=%d body=%q", streamRec.Code, streamRec.Body.String())
	}
	if streamRec.Body.String() != "0123" {
		t.Fatalf("stream body=%q, want 0123", streamRec.Body.String())
	}
}

// streamingSidecar satisfies the local Sidecar interface and honors a closed-range
// Range header (bytes=A-B) by returning a 206 with the inclusive byte slice; any request
// without a parseable closed range falls back to a 200 with the full data.
type streamingSidecar struct {
	data []byte
}

func (s *streamingSidecar) Stream(ctx context.Context, req sidecarclient.StreamRequest) (*sidecarclient.StreamResult, error) {
	if start, end, ok := parseClosedRange(req.Headers.Get("Range"), len(s.data)); ok {
		return &sidecarclient.StreamResult{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Range": []string{"bytes " + strconv.Itoa(start) + "-" + strconv.Itoa(end) + "/" + strconv.Itoa(len(s.data))},
				"Accept-Ranges": []string{"bytes"},
			},
			Body: io.NopCloser(bytes.NewReader(s.data[start : end+1])),
		}, nil
	}
	return &sidecarclient.StreamResult{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Accept-Ranges": []string{"bytes"}},
		Body:       io.NopCloser(bytes.NewReader(s.data)),
	}, nil
}

// parseClosedRange parses the closed "bytes=A-B" form (the only form the e2e test uses),
// returning the inclusive [start, end] bounds clamped to the data length. It reports ok
// only for a well-formed closed range; anything else (no header, suffix/open ranges,
// garbage) returns ok=false so the caller can fall back to a full 200 response.
func parseClosedRange(header string, length int) (start, end int, ok bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(header, prefix)
	dash := strings.IndexByte(spec, '-')
	if dash <= 0 || dash == len(spec)-1 {
		return 0, 0, false
	}
	start, err := strconv.Atoi(spec[:dash])
	if err != nil {
		return 0, 0, false
	}
	end, err = strconv.Atoi(spec[dash+1:])
	if err != nil {
		return 0, 0, false
	}
	if start < 0 || end < start || start >= length {
		return 0, 0, false
	}
	if end >= length {
		end = length - 1
	}
	return start, end, true
}

// newFakeStreamingSidecar returns a Sidecar whose Stream honors a closed Range header,
// serving 206 partial content for "bytes=A-B" (e.g. bytes=0-3 over "0123456789abcdef"
// yields "0123") and a full 200 otherwise.
func newFakeStreamingSidecar(t *testing.T, data []byte) Sidecar {
	t.Helper()
	return &streamingSidecar{data: data}
}

// newFullEmbyProxyHarness seeds a real store EXACTLY like newRewriteHarness (so
// /media/movies/Film.mkv maps to the admin-owned entry movies/Film.mkv with a live file
// copy via an enabled pool) and mounts the FULL Deps router — Stream, Error, PlaybackInfo,
// and the upstream fallback — over collaborators that all share the SAME store and clock.
// Sharing st is the crux: the playback session minted while rewriting PlaybackInfo must be
// visible to the StreamHandler lookup on the subsequent stream request.
func newFullEmbyProxyHarness(t *testing.T, embyURL string, sidecar Sidecar) http.Handler {
	t.Helper()
	st := newEmbyProxyTestStore(t)
	seedRewriteHarnessStore(t, st)

	now := nowFunc(time.Unix(1000, 0))
	mgr := NewSessionManager(st.Queries, SessionConfig{TTL: time.Hour, ErrorTTL: 5 * time.Minute}, now)
	resolver := playback.NewResolver(st.Queries, now)
	quota := playback.NewQuota(st.Queries, now, time.Hour)
	failures := playback.NewFailureRecorder(st.Queries, now)
	mapper := NewDBSourceMapper(st.Queries, now)
	rewriter := NewRewriter(mapper, mgr, quota, resolver)

	upstreamBase, err := url.Parse(embyURL)
	if err != nil {
		t.Fatalf("parse emby url: %v", err)
	}
	publicBase, _ := url.Parse("https://echo.example.com")

	playbackInfo := PlaybackInfoHandler(PlaybackInfoConfig{
		PublicBaseURL: "https://echo.example.com",
		ProxyPrefix:   "/emby",
		EmbyServerID:  "default",
		UpstreamBase:  upstreamBase,
		Querier:       st.Queries,
	}, rewriter, http.DefaultTransport, discardStreamLogger())

	deps := &Deps{
		ProxyPrefix:  "/emby",
		Stream:       StreamHandler(mgr, resolver, quota, sidecar, failures, nil, discardStreamLogger()),
		Error:        ErrorHandler(mgr),
		PlaybackInfo: playbackInfo,
		Upstream:     NewReverseProxy(ProxyConfig{UpstreamBase: upstreamBase, PublicBase: publicBase, ProxyPrefix: "/emby"}, http.DefaultClient, discardProxyLogger()),
	}
	r := chi.NewRouter()
	deps.Mount(r)
	return r
}

// extractFirstEchoStreamURL unmarshals a (rewritten) PlaybackInfo body and returns
// MediaSources[0].DirectStreamUrl — the Echo /emby/stream/{token} URL.
func extractFirstEchoStreamURL(t *testing.T, body []byte) string {
	t.Helper()
	var decoded struct {
		MediaSources []struct {
			DirectStreamUrl string `json:"DirectStreamUrl"`
		} `json:"MediaSources"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal playbackinfo body: %v", err)
	}
	if len(decoded.MediaSources) == 0 {
		t.Fatalf("no MediaSources in body: %s", body)
	}
	return decoded.MediaSources[0].DirectStreamUrl
}
