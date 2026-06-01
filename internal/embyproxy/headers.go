package embyproxy

import (
	"net/http"
	"net/url"
	"strings"
)

// HeaderConfig carries the upstream/public origins and the proxy mount prefix used to
// rewrite Location and Set-Cookie headers on responses flowing back through Echo.
type HeaderConfig struct {
	UpstreamBase *url.URL
	PublicBase   *url.URL
	ProxyPrefix  string
}

// hopByHopHeaders are the connection-scoped headers that MUST NOT be forwarded across a
// proxy hop (RFC 7230 §6.1). Keys are canonicalized so lookups match http.Header keys.
var hopByHopHeaders = map[string]struct{}{
	http.CanonicalHeaderKey("Connection"):          {},
	http.CanonicalHeaderKey("Keep-Alive"):          {},
	http.CanonicalHeaderKey("Proxy-Authenticate"):  {},
	http.CanonicalHeaderKey("Proxy-Authorization"): {},
	http.CanonicalHeaderKey("TE"):                  {},
	http.CanonicalHeaderKey("Trailer"):             {},
	http.CanonicalHeaderKey("Transfer-Encoding"):   {},
	http.CanonicalHeaderKey("Upgrade"):             {},
}

// StripHopByHop returns a NEW header map with the standard hop-by-hop headers removed,
// PLUS any header named in the Connection header's comma-separated token list (also a
// hop-by-hop signal). All other headers are copied through unchanged. src is not
// mutated.
func StripHopByHop(src http.Header) http.Header {
	// Collect the extra hop-by-hop names announced via Connection before copying.
	connTokens := map[string]struct{}{}
	for _, value := range src[http.CanonicalHeaderKey("Connection")] {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token != "" {
				connTokens[http.CanonicalHeaderKey(token)] = struct{}{}
			}
		}
	}

	dst := make(http.Header, len(src))
	for key, values := range src {
		canon := http.CanonicalHeaderKey(key)
		if _, drop := hopByHopHeaders[canon]; drop {
			continue
		}
		if _, drop := connTokens[canon]; drop {
			continue
		}
		// Copy the value slice so callers can mutate dst without touching src.
		cp := make([]string, len(values))
		copy(cp, values)
		dst[canon] = cp
	}
	return dst
}

// RewriteResponseHeaders returns a NEW header map (via StripHopByHop) with every
// Location value retargeted from the upstream origin to Echo's public origin+prefix and
// every Set-Cookie value rewritten to drop Domain and pin Path to the proxy prefix. It
// does NOT force Accept-Encoding: identity and does NOT mutate src.
func RewriteResponseHeaders(src http.Header, cfg HeaderConfig) http.Header {
	dst := StripHopByHop(src)

	if locs := dst[http.CanonicalHeaderKey("Location")]; len(locs) > 0 {
		rewritten := make([]string, len(locs))
		for i, v := range locs {
			rewritten[i] = RewriteLocation(v, cfg)
		}
		dst[http.CanonicalHeaderKey("Location")] = rewritten
	}

	if cookies := dst[http.CanonicalHeaderKey("Set-Cookie")]; len(cookies) > 0 {
		rewritten := make([]string, len(cookies))
		for i, v := range cookies {
			rewritten[i] = RewriteSetCookie(v, cfg.ProxyPrefix)
		}
		dst[http.CanonicalHeaderKey("Set-Cookie")] = rewritten
	}

	return dst
}

// RewriteLocation rewrites an absolute Location that points at the upstream origin
// (scheme+host) so it instead points at Echo's public origin with the proxy prefix
// prepended to the original path; query and fragment are preserved. Anything that does
// not match the upstream origin -- a relative Location, or a third-party absolute URL --
// is returned unchanged. Relative Locations are intentionally left alone: chasing them
// through the prefix would require guessing whether they are already prefix-relative, so
// the conservative choice is to pass them through untouched.
func RewriteLocation(raw string, cfg HeaderConfig) string {
	if raw == "" || cfg.UpstreamBase == nil || cfg.PublicBase == nil {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	// Only absolute URLs whose scheme+host match upstream are rewritten.
	if u.Scheme != cfg.UpstreamBase.Scheme || u.Host != cfg.UpstreamBase.Host {
		return raw
	}
	out := *cfg.PublicBase
	out.Path = singleSlashJoin(cfg.ProxyPrefix, u.Path)
	out.RawQuery = u.RawQuery
	out.Fragment = u.Fragment
	return out.String()
}

// RewriteSetCookie strips any Domain attribute (so the cookie defaults to the echo host)
// and forces Path=<proxyPrefix> (replacing an existing Path or appending one). The
// name=value pair and all other attributes (HttpOnly, Secure, SameSite, etc.) are
// preserved. proxyPrefix == "" yields Path=/.
func RewriteSetCookie(raw string, proxyPrefix string) string {
	if raw == "" {
		return raw
	}
	path := proxyPrefix
	if path == "" {
		path = "/"
	}

	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts)+1)
	pathSet := false
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if i == 0 {
			// First segment is the name=value pair; keep it verbatim (after trim).
			out = append(out, trimmed)
			continue
		}
		if trimmed == "" {
			continue
		}
		key := trimmed
		if idx := strings.IndexByte(trimmed, '='); idx >= 0 {
			key = trimmed[:idx]
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "domain":
			// Drop entirely so the cookie binds to the echo host.
			continue
		case "path":
			out = append(out, "Path="+path)
			pathSet = true
		default:
			out = append(out, trimmed)
		}
	}
	if !pathSet {
		out = append(out, "Path="+path)
	}
	return strings.Join(out, "; ")
}

// singleSlashJoin joins prefix and path with exactly one slash between them, tolerating
// either or both carrying their own slash, and always returns a path beginning with "/".
func singleSlashJoin(prefix, path string) string {
	prefix = strings.TrimRight(prefix, "/")
	if path == "" {
		if prefix == "" {
			return "/"
		}
		return prefix
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return prefix + path
}
