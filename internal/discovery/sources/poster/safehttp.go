package poster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout      = 15 * time.Second
	defaultMaxBodyBytes = 2 * 1024 * 1024
	defaultMaxRedirects = 3
)

type SafeConfig struct {
	AllowedDomains []string
	Timeout        time.Duration
	MaxBodyBytes   int64
	MaxRedirects   int
}

type SafeClient struct {
	client *http.Client
	cfg    SafeConfig
}

func NewSafeClient(cfg SafeConfig) *SafeClient {
	cfg = normalizeSafeConfig(cfg)
	safe := &SafeClient{cfg: cfg}
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: safe.dialContext,
	}
	safe.client = &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > cfg.MaxRedirects {
				return fmt.Errorf("poster safe http: redirect limit exceeded")
			}
			return safe.validateRequestURL(req.URL)
		},
	}
	return safe
}

func (c *SafeClient) Do(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("poster safe http: nil request")
	}
	if err := c.validateRequestURL(req.URL); err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	limitResponseBody(resp, c.cfg.MaxBodyBytes)
	return resp, nil
}

func normalizeSafeConfig(cfg SafeConfig) SafeConfig {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	if cfg.MaxRedirects <= 0 {
		cfg.MaxRedirects = defaultMaxRedirects
	}
	for i, domain := range cfg.AllowedDomains {
		cfg.AllowedDomains[i] = normalizeHost(domain)
	}
	return cfg
}

func (c *SafeClient) validateRequestURL(u *url.URL) error {
	if u == nil {
		return errors.New("poster safe http: nil URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("poster safe http: rejected scheme %q", u.Scheme)
	}
	host := normalizeHost(u.Hostname())
	if host == "" {
		return errors.New("poster safe http: empty host")
	}
	if !c.hostAllowed(host) {
		return fmt.Errorf("poster safe http: host %q is not allowed", host)
	}
	if ip, ok := parseIPHost(host); ok && isUnsafeIP(ip) {
		return fmt.Errorf("poster safe http: unsafe host IP %q", host)
	}
	return nil
}

func (c *SafeClient) hostAllowed(host string) bool {
	if len(c.cfg.AllowedDomains) == 0 {
		return false
	}
	for _, allowed := range c.cfg.AllowedDomains {
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

func (c *SafeClient) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if strings.HasPrefix(strings.ToLower(network), "unix") {
		return nil, errors.New("poster safe http: unix network rejected")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("poster safe http: parse dial address: %w", err)
	}
	if normalizeHost(host) == "" {
		return nil, errors.New("poster safe http: empty dial host")
	}
	if ip, ok := parseIPHost(host); ok {
		if isUnsafeIP(ip) {
			return nil, fmt.Errorf("poster safe http: unsafe dial IP %q", host)
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("poster safe http: resolve host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("poster safe http: host %q resolved no addresses", host)
	}
	var lastErr error
	for _, resolved := range ips {
		ip, ok := parseIPHost(resolved.IP.String())
		if !ok || isUnsafeIP(ip) {
			lastErr = fmt.Errorf("poster safe http: host %q resolved unsafe IP %q", host, resolved.IP.String())
			continue
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("poster safe http: no safe address for %q", host)
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.Trim(host, "[]")
	return strings.TrimSuffix(host, ".")
}

func parseIPHost(host string) (netip.Addr, bool) {
	host = stripIPv6Zone(normalizeHost(host))
	ip, err := netip.ParseAddr(host)
	return ip, err == nil
}

func stripIPv6Zone(host string) string {
	if percent := strings.LastIndex(host, "%"); percent >= 0 {
		return host[:percent]
	}
	return host
}

func isUnsafeIP(ip netip.Addr) bool {
	return ip.Is4In6() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

func limitResponseBody(resp *http.Response, maxBytes int64) {
	if resp == nil || resp.Body == nil || maxBytes <= 0 {
		return
	}
	resp.Body = &limitedReadCloser{
		reader:    resp.Body,
		closeFunc: resp.Body.Close,
		max:       maxBytes,
		remaining: maxBytes,
	}
}

type limitedReadCloser struct {
	reader    io.Reader
	closeFunc func() error
	max       int64
	remaining int64
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("poster safe http: response body exceeds %d bytes", r.max)
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *limitedReadCloser) Close() error {
	if r.closeFunc == nil {
		return nil
	}
	return r.closeFunc()
}
