package embyproxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Deps wires the Emby reverse-proxy routes. ProxyPrefix defaults to "/emby". Stream,
// Error, and Upstream are supplied by the caller; PlaybackInfo and BadReserved default
// to the package's fail-closed handlers when nil so a partial wiring never silently
// proxies a reserved path to upstream Emby.
type Deps struct {
	ProxyPrefix  string
	Stream       http.Handler
	Error        http.Handler
	PlaybackInfo http.Handler
	BadReserved  http.Handler
	Upstream     http.Handler
}

// Mount registers Echo's reserved Emby routes and the transparent upstream fallback on
// r. Route ORDER is part of the contract: the exact reserved paths
// (/stream/{token}, /error/{token}, /Items/{item_id}/PlaybackInfo) are claimed by
// Echo's own handlers, the malformed/sub-path variants of the reserved namespaces fall
// to the fail-closed BadReserved handler, and only everything else reaches Upstream.
// Because chi's {token} matches a single path segment, /stream/foo/bar does NOT match
// /stream/{token} and correctly falls to the /stream/* BadReserved route.
func (d *Deps) Mount(r chi.Router) {
	prefix := d.ProxyPrefix
	if prefix == "" {
		prefix = "/emby"
	}
	badReserved := d.BadReserved
	if badReserved == nil {
		badReserved = BadReservedHandler()
	}
	playbackInfo := d.PlaybackInfo
	if playbackInfo == nil {
		playbackInfo = PlaybackInfoFailClosedHandler()
	}
	r.Handle(prefix+"/stream/{token}", d.Stream)
	r.Handle(prefix+"/stream", badReserved)
	r.Handle(prefix+"/stream/*", badReserved)
	r.Handle(prefix+"/error/{token}", d.Error)
	r.Handle(prefix+"/error", badReserved)
	r.Handle(prefix+"/error/*", badReserved)
	r.Handle(prefix+"/Items/{item_id}/PlaybackInfo", playbackInfo)
	r.Handle(prefix+"/*", d.Upstream)
}

// ProxyConfig carries the origins and mount prefix for the transparent reverse-proxy
// fallback that forwards every non-reserved Emby path to the upstream server.
type ProxyConfig struct {
	UpstreamBase *url.URL
	PublicBase   *url.URL
	ProxyPrefix  string
}

// NewReverseProxy builds the transparent upstream fallback handler. It strips ProxyPrefix
// from the request path, retargets the request to cfg.UpstreamBase (preserving method,
// query, and body), drops hop-by-hop request headers, and rewrites response Location and
// Set-Cookie headers back through Echo via the headers.go helpers (the single source of
// truth for those rules). The response body is streamed through unchanged. A nil client
// falls back to http.DefaultClient. On an upstream transport failure it logs (without
// secrets) and writes a 502.
//
// It does NOT add Accept-Encoding: identity, leaving content-encoding negotiation to the
// client and upstream untouched.
func NewReverseProxy(cfg ProxyConfig, client *http.Client, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if client == nil {
		client = http.DefaultClient
	}
	headerCfg := HeaderConfig{
		UpstreamBase: cfg.UpstreamBase,
		PublicBase:   cfg.PublicBase,
		ProxyPrefix:  cfg.ProxyPrefix,
	}

	rp := &httputil.ReverseProxy{
		Transport: client.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			// Retarget scheme/host/base path to upstream, dropping the proxy prefix so
			// /emby/web/index.html -> <upstream>/web/index.html and a bare /emby -> /.
			pr.Out.URL.Scheme = cfg.UpstreamBase.Scheme
			pr.Out.URL.Host = cfg.UpstreamBase.Host
			pr.Out.Host = cfg.UpstreamBase.Host
			pr.Out.URL.Path = stripPrefixPath(pr.In.URL.Path, cfg.ProxyPrefix)
			// Preserve the prefix-stripped *escaped* path so encoded segments (e.g. an
			// item id carrying %2F) round-trip to upstream instead of collapsing to a
			// literal slash; keep Path (decoded) consistent.
			pr.Out.URL.RawPath = stripPrefixPath(pr.In.URL.EscapedPath(), cfg.ProxyPrefix)
			// RawQuery is carried over from pr.In by SetURL-style copying; ReverseProxy
			// already clones the URL, so the query string is preserved as-is.

			// NOTE: We deliberately do NOT touch pr.Out.Header here. httputil.ReverseProxy
			// has already (a) stripped hop-by-hop headers from pr.Out (including any named
			// by the Connection token list) and (b) for protocol-upgrade requests re-added
			// Connection: Upgrade + Upgrade: <type> to pr.Out before calling this hook.
			// Rebuilding the header from pr.In (never stripped, and with the upgrade
			// headers removed by StripHopByHop) would break WebSocket upgrades such as
			// Emby's /embywebsocket. Let ReverseProxy own request hop-by-hop handling.
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header = RewriteResponseHeaders(resp.Header, headerCfg)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// Log the failure without any header/cookie/token content.
			logger.Error("emby proxy: upstream request failed",
				"method", r.Method, "path", r.URL.Path, "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	// Fail-closed playback guard runs BEFORE the upstream proxy on the fallback path. Echo's
	// reserved stream/error/PlaybackInfo routes never reach here (Mount's route precedence
	// claims them first), but the guard still defends the fallback so a media stream/download
	// request is never transparently proxied to upstream Emby untokenized. Phase 4 replaces
	// this Phase-3 guard with a mapping-aware one.
	guard := NewPlaybackGuard(GuardConfig{Phase: 3})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !guard.Allow(r, w) {
			// guard.Allow already wrote the controlled 503.
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// stripPrefixPath removes a leading proxy prefix (e.g. "/emby") from path and guarantees
// a result that starts with "/": "/emby/web" -> "/web", a bare "/emby" -> "/", and a
// path that does not carry the prefix is returned with a leading slash unchanged.
func stripPrefixPath(path, prefix string) string {
	prefix = strings.TrimRight(prefix, "/")
	if prefix != "" {
		if path == prefix {
			return "/"
		}
		if rest := strings.TrimPrefix(path, prefix+"/"); rest != path {
			path = "/" + rest
		}
	}
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}
