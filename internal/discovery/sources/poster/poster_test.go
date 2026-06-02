package poster

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xmm2022/echo/internal/discovery"
)

func TestSafeClientRejectsLocalhost(t *testing.T) {
	client := NewSafeClient(SafeConfig{AllowedDomains: []string{"127.0.0.1"}})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1/private", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected localhost rejection")
	}
}

func TestSafeClientRejectsDisallowedHost(t *testing.T) {
	client := NewSafeClient(SafeConfig{AllowedDomains: []string{"poster.example"}})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://other.example/list", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected disallowed host rejection")
	}
}

func TestSafeClientRejectsRedirectToLocalhost(t *testing.T) {
	client := NewSafeClient(SafeConfig{AllowedDomains: []string{"poster.example", "127.0.0.1"}})
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/private", nil)
	err := client.client.CheckRedirect(req, []*http.Request{
		{URL: mustParseURL(t, "https://poster.example/list")},
	})
	if err == nil {
		t.Fatal("expected redirect localhost rejection")
	}
}

func TestSafeClientRejectsUnsafeDialTargets(t *testing.T) {
	client := NewSafeClient(SafeConfig{AllowedDomains: []string{"poster.example"}})
	cases := []struct {
		network string
		addr    string
	}{
		{network: "tcp", addr: "[::1]:80"},
		{network: "tcp", addr: "[::ffff:127.0.0.1]:80"},
		{network: "tcp", addr: "169.254.169.254:80"},
		{network: "tcp", addr: "224.0.0.1:80"},
		{network: "tcp", addr: "0.0.0.0:80"},
		{network: "tcp", addr: "[::]:80"},
		{network: "unix", addr: "/tmp/poster.sock"},
	}
	for _, tt := range cases {
		t.Run(tt.network+"_"+tt.addr, func(t *testing.T) {
			conn, err := client.dialContext(context.Background(), tt.network, tt.addr)
			if err == nil {
				if conn != nil {
					conn.Close()
				}
				t.Fatal("expected unsafe dial target rejection")
			}
		})
	}
}

func TestSafeClientDisablesProxyEnvironmentUse(t *testing.T) {
	client := NewSafeClient(SafeConfig{AllowedDomains: []string{"poster.example"}})
	transport, ok := client.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected proxy environment use disabled")
	}
}

func TestSafeClientRejectsHTTPRedirectToLocalhost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
	}))
	defer server.Close()

	client := NewSafeClient(SafeConfig{AllowedDomains: []string{"poster.example", "127.0.0.1"}})
	client.client.Transport = rewriteHostTransport(t, server.URL)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://poster.example/start", nil)
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected redirected localhost rejection")
	}
}

func TestSafeClientAllowsConfiguredRedirectCount(t *testing.T) {
	client := NewSafeClient(SafeConfig{AllowedDomains: []string{"poster.example"}, MaxRedirects: 1})
	req, _ := http.NewRequest(http.MethodGet, "https://poster.example/page/2", nil)
	err := client.client.CheckRedirect(req, []*http.Request{
		{URL: mustParseURL(t, "https://poster.example/page/1")},
	})
	if err != nil {
		t.Fatalf("first redirect should be allowed: %v", err)
	}
	err = client.client.CheckRedirect(req, []*http.Request{
		{URL: mustParseURL(t, "https://poster.example/page/1")},
		{URL: mustParseURL(t, "https://poster.example/page/2")},
	})
	if err == nil {
		t.Fatal("expected second redirect rejection")
	}
}

func TestSafeClientLimitsGzipResponseAfterDecompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		if _, err := gz.Write([]byte("abcdef")); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	client := NewSafeClient(SafeConfig{
		AllowedDomains: []string{"poster.example"},
		MaxBodyBytes:   3,
	})
	client.client.Transport = rewriteHostTransport(t, server.URL)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://poster.example/gzip", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatal("expected decompressed body limit error")
	}
	if !bytes.Equal(body, []byte("abc")) {
		t.Fatalf("body = %q, want abc", body)
	}
}

func TestSafeClientLimitsResponseBody(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("abcdef")),
	}
	limitResponseBody(resp, 3)
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatal("expected body limit error")
	}
	if string(body) != "abc" {
		t.Fatalf("body = %q, want abc", body)
	}
}

func TestParse115ShareLinks(t *testing.T) {
	html := `<a href="https://115.com/s/abc?password=pass">Movie 2024 2160p</a>`
	items, err := ParseHTML("https://poster.example/item/1", []byte(html))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Provider != "115" {
		t.Fatalf("items = %#v", items)
	}
}

func TestParseHTMLExtracts115Details(t *testing.T) {
	html := `<html><body>
		<a href="/go?target=https%3A%2F%2Fignored.example%2Fs%2Fnope">ignored</a>
		<a href="https://115.com/s/abc123?password=passcode">Movie 2024 2160p</a>
	</body></html>`
	items, err := ParseHTML("https://poster.example/item/1", []byte(html))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1: %#v", len(items), items)
	}
	item := items[0]
	if item.Provider != "115" || item.ShareCode != "abc123" || item.ReceiveCode != "passcode" {
		t.Fatalf("item = %#v", item)
	}
	if item.Title != "Movie 2024 2160p" || item.RawText != "Movie 2024 2160p" {
		t.Fatalf("text fields not extracted: %#v", item)
	}
	if strings.Contains(item.ShareURL, "passcode") {
		t.Fatalf("share url leaked receive code: %q", item.ShareURL)
	}
}

func TestCrawlRedactsSecretAnchorText(t *testing.T) {
	adapter := NewAdapter(AdapterConfig{AllowedDomains: []string{"poster.example"}})
	adapter.client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(`<a href="https://115.com/s/abc?password=pass">https://115.com/s/abc?password=pass</a>`)),
			Request:    req,
		}, nil
	})

	result, err := adapter.Crawl(context.Background(), discovery.Source{
		ConfigJson: `{"urls":["https://poster.example/list"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %#v", result.Items)
	}
	item := result.Items[0]
	for _, forbidden := range []string{"pass", "password=", "receive_code"} {
		if strings.Contains(item.ShareURLRedacted, forbidden) ||
			strings.Contains(item.RawTextRedacted, forbidden) ||
			strings.Contains(item.ParsedJSON, forbidden) {
			t.Fatalf("redacted fields leaked %q: %#v", forbidden, item)
		}
	}
	if err := discovery.ValidateParsedJSONForStorage([]byte(item.ParsedJSON)); err != nil {
		t.Fatalf("parsed json contract failed: %v, json=%s", err, item.ParsedJSON)
	}
}

func TestCrawlRedactsExactReceiveCodeFromPlainAnchorText(t *testing.T) {
	adapter := NewAdapter(AdapterConfig{AllowedDomains: []string{"poster.example"}})
	adapter.client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(`<a href="https://115.com/s/abc?password=Pass">Pass</a>`)),
			Request:    req,
		}, nil
	})

	result, err := adapter.Crawl(context.Background(), discovery.Source{
		ConfigJson: `{"urls":["https://poster.example/list"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %#v", result.Items)
	}
	item := result.Items[0]
	if item.ReceiveCode != "Pass" {
		t.Fatalf("ReceiveCode = %q, want Pass", item.ReceiveCode)
	}
	for field, value := range map[string]string{
		"Title":           item.Title,
		"RawTextRedacted": item.RawTextRedacted,
		"ParsedJSON":      item.ParsedJSON,
	} {
		if strings.Contains(value, item.ReceiveCode) {
			t.Fatalf("%s leaked receive code %q: %#v", field, item.ReceiveCode, item)
		}
	}
}

func TestCrawlParsesConfiguredURLsToDiscoveryResources(t *testing.T) {
	adapter := NewAdapter(AdapterConfig{AllowedDomains: []string{"poster.example"}})
	adapter.client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://poster.example/list" {
			t.Fatalf("unexpected URL: %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(`<a href="https://115.com/s/abc?password=pass">Movie 2024 2160p</a>`)),
			Request:    req,
		}, nil
	})

	result, err := adapter.Crawl(context.Background(), discovery.Source{
		ConfigJson: `{"urls":["https://poster.example/list"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %#v", result.Items)
	}
	item := result.Items[0]
	if item.Provider != discovery.Provider115 ||
		item.LinkKind != discovery.Link115Share ||
		item.ShareCode != "abc" ||
		item.ReceiveCode != "pass" ||
		item.Title != "Movie 2024 2160p" {
		t.Fatalf("item = %#v", item)
	}
	if strings.Contains(item.ShareURLRedacted, "pass") ||
		strings.Contains(item.RawTextRedacted, "pass") ||
		strings.Contains(item.ParsedJSON, "pass") ||
		strings.Contains(item.ParsedJSON, "receive_code") {
		t.Fatalf("redacted fields leaked secret: %#v", item)
	}
	if err := discovery.ValidateParsedJSONForStorage([]byte(item.ParsedJSON)); err != nil {
		t.Fatalf("parsed json contract failed: %v, json=%s", err, item.ParsedJSON)
	}
}

func TestCrawlWrapsURLOnCrawlURLFailure(t *testing.T) {
	adapter := NewAdapter(AdapterConfig{AllowedDomains: []string{"poster.example"}})
	adapter.client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/bad" {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("error")),
				Request:    req,
			}, nil
		}
		return nil, errors.New("unexpected URL")
	})

	_, err := adapter.Crawl(context.Background(), discovery.Source{
		ConfigJson: `{"urls":["https://poster.example/bad"]}`,
	})
	if err == nil {
		t.Fatal("expected crawl failure")
	}
	if !strings.Contains(err.Error(), "https://poster.example/bad") {
		t.Fatalf("error did not include URL: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func rewriteHostTransport(t *testing.T, target string) http.RoundTripper {
	t.Helper()
	targetURL := mustParseURL(t, target)
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rewritten := req.Clone(req.Context())
		rewritten.URL = cloneURL(req.URL)
		rewritten.URL.Scheme = targetURL.Scheme
		rewritten.URL.Host = targetURL.Host
		rewritten.Host = targetURL.Host
		return http.DefaultTransport.RoundTrip(rewritten)
	})
}

func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
