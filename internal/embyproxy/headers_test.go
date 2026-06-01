package embyproxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRewriteLocationAndSetCookie(t *testing.T) {
	upstream, _ := url.Parse("http://emby:8096")
	public, _ := url.Parse("https://echo.example.com")
	h := http.Header{}
	h.Set("Location", "http://emby:8096/web/index.html")
	h.Add("Set-Cookie", "EmbyAuth=abc; Domain=emby; Path=/; HttpOnly")

	got := RewriteResponseHeaders(h, HeaderConfig{UpstreamBase: upstream, PublicBase: public, ProxyPrefix: "/emby"})
	if got.Get("Location") != "https://echo.example.com/emby/web/index.html" {
		t.Fatalf("Location = %q", got.Get("Location"))
	}
	if cookie := got.Values("Set-Cookie")[0]; strings.Contains(cookie, "Domain=") || !strings.Contains(cookie, "Path=/emby") {
		t.Fatalf("cookie = %q, want Domain stripped and Path=/emby", cookie)
	}
}

func TestStripHopByHopHeaders(t *testing.T) {
	h := http.Header{"Connection": []string{"keep-alive"}, "X-Test": []string{"ok"}}
	got := StripHopByHop(h)
	if got.Get("Connection") != "" || got.Get("X-Test") != "ok" {
		t.Fatalf("headers = %#v", got)
	}
}

// TestRewriteLocationLeavesForeignAndRelativeUnchanged asserts RewriteResponseHeaders
// passes through a relative Location and a third-party absolute Location untouched (only
// upstream-origin absolute Locations are retargeted).
func TestRewriteLocationLeavesForeignAndRelativeUnchanged(t *testing.T) {
	upstream, _ := url.Parse("http://emby:8096")
	public, _ := url.Parse("https://echo.example.com")
	cfg := HeaderConfig{UpstreamBase: upstream, PublicBase: public, ProxyPrefix: "/emby"}

	// Relative Location: left alone.
	if got := RewriteLocation("/web/index.html", cfg); got != "/web/index.html" {
		t.Fatalf("relative Location = %q, want unchanged", got)
	}
	// Third-party absolute (non-upstream host): left alone.
	const foreign = "https://cdn.example.org/asset.js"
	if got := RewriteLocation(foreign, cfg); got != foreign {
		t.Fatalf("foreign Location = %q, want unchanged", got)
	}
	// Same via RewriteResponseHeaders (header-map path).
	h := http.Header{}
	h.Set("Location", foreign)
	out := RewriteResponseHeaders(h, cfg)
	if got := out.Get("Location"); got != foreign {
		t.Fatalf("RewriteResponseHeaders Location = %q, want unchanged", got)
	}
}

// TestRewriteSetCookieNoPathAppendsPath asserts a Set-Cookie with no Path attribute gets
// Path=<prefix> appended (and other attributes preserved).
func TestRewriteSetCookieNoPathAppendsPath(t *testing.T) {
	got := RewriteSetCookie("EmbyAuth=abc; HttpOnly", "/emby")
	if !strings.Contains(got, "Path=/emby") {
		t.Fatalf("cookie = %q, want Path=/emby appended", got)
	}
	if !strings.Contains(got, "HttpOnly") || !strings.Contains(got, "EmbyAuth=abc") {
		t.Fatalf("cookie = %q, want name=value and HttpOnly preserved", got)
	}
}

// TestRewriteResponseHeadersMultipleSetCookie asserts every Set-Cookie value is rewritten
// (Domain dropped, Path pinned), not just the first.
func TestRewriteResponseHeadersMultipleSetCookie(t *testing.T) {
	upstream, _ := url.Parse("http://emby:8096")
	public, _ := url.Parse("https://echo.example.com")
	h := http.Header{}
	h.Add("Set-Cookie", "A=1; Domain=emby; Path=/; HttpOnly")
	h.Add("Set-Cookie", "B=2; Domain=emby; Path=/web")

	out := RewriteResponseHeaders(h, HeaderConfig{UpstreamBase: upstream, PublicBase: public, ProxyPrefix: "/emby"})
	cookies := out.Values("Set-Cookie")
	if len(cookies) != 2 {
		t.Fatalf("got %d Set-Cookie values, want 2: %#v", len(cookies), cookies)
	}
	for _, c := range cookies {
		if strings.Contains(c, "Domain=") || !strings.Contains(c, "Path=/emby") {
			t.Fatalf("Set-Cookie = %q, want Domain stripped and Path=/emby", c)
		}
	}
}
