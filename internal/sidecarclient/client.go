package sidecarclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/config"
)

type Config struct {
	BaseURL        string
	AuthTokenEnv   string
	MinVersion     string
	RequestTimeout config.Duration
	StreamTimeout  config.Duration
}

type Sidecar interface {
	Ping(ctx context.Context) error
	Version(ctx context.Context) (string, error)
	ListStorages(ctx context.Context) ([]Storage, error)
	PutCAS(ctx context.Context, req PutCASRequest) (*ItemResult, error)
	Link(ctx context.Context, storageMount, remotePath string) (*DirectLink, error)
	Stream(ctx context.Context, req StreamRequest) (*StreamResult, error)
}

type Client struct {
	baseURL        *url.URL
	authToken      string
	minVersion     string
	requestTimeout time.Duration
	streamTimeout  time.Duration
	httpClient     *http.Client
}

func New(cfg Config) *Client {
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil {
		base = &url.URL{}
	}
	requestTimeout := cfg.RequestTimeout.Duration
	if requestTimeout <= 0 {
		requestTimeout = 30 * time.Second
	}
	streamTimeout := cfg.StreamTimeout.Duration
	if streamTimeout <= 0 {
		streamTimeout = requestTimeout
	}

	return &Client{
		baseURL:        base,
		authToken:      os.Getenv(cfg.AuthTokenEnv),
		minVersion:     cfg.MinVersion,
		requestTimeout: requestTimeout,
		streamTimeout:  streamTimeout,
		httpClient:     http.DefaultClient,
	}
}

func FromEndpointConfig(cfg config.SidecarEndpointConfig) Config {
	return Config{
		BaseURL:        cfg.BaseURL,
		AuthTokenEnv:   cfg.AuthTokenEnv,
		MinVersion:     cfg.MinVersion,
		RequestTimeout: cfg.RequestTimeout,
		StreamTimeout:  cfg.StreamTimeout,
	}
}

func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/ping", nil, nil, c.requestTimeout)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.httpError(resp)
	}
	return nil
}

func (c *Client) Version(ctx context.Context) (string, error) {
	version, err := c.versionFromSettings(ctx)
	if err != nil {
		return "", err
	}
	if version == "" {
		version, err = c.versionFromPing(ctx)
		if err != nil {
			return "", err
		}
	}
	if c.minVersion != "" && version != c.minVersion {
		return "", fmt.Errorf("%w: required %q got %q", ErrSidecarVersionTooOld, c.minVersion, version)
	}
	return version, nil
}

func (c *Client) versionFromSettings(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/public/settings", nil, nil, c.requestTimeout)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", c.httpError(resp)
	}

	var data map[string]any
	if err := decodeData(resp.Body, &data); err != nil {
		return "", err
	}
	return firstString(data, "commit", "git_commit", "tag", "build_commit", "version"), nil
}

func (c *Client) versionFromPing(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/ping", nil, nil, c.requestTimeout)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", c.httpError(resp)
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return firstString(data, "commit", "git_commit", "tag", "build_commit", "version"), nil
}

func firstString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := data[key]
		if !ok {
			continue
		}
		if s, ok := value.(string); ok {
			return s
		}
	}
	return ""
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, header http.Header, timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	resp, err := c.doNoCancel(ctx, method, path, body, header, -1)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func (c *Client) doWithContentLength(ctx context.Context, method, path string, body io.Reader, header http.Header, timeout time.Duration, contentLength int64) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	resp, err := c.doNoCancel(ctx, method, path, body, header, contentLength)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func (c *Client) doNoCancel(ctx context.Context, method, path string, body io.Reader, header http.Header, contentLength int64) (*http.Response, error) {
	u := c.endpoint(path)
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	return resp, nil
}

func (c *Client) endpoint(path string) string {
	if c.baseURL == nil {
		return path
	}
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return u.String()
}

func (c *Client) httpError(resp *http.Response) error {
	return &SidecarHTTPError{
		StatusCode: resp.StatusCode,
		Method:     resp.Request.Method,
		URL:        resp.Request.URL.String(),
	}
}

func classifyTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrSidecarUnreachable, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return fmt.Errorf("%w: %v", ErrSidecarUnreachable, err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%w: %v", ErrSidecarUnreachable, err)
	}
	return err
}

func decodeData(r io.Reader, dst any) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	var wrapped struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.Data) > 0 && !bytes.Equal(wrapped.Data, []byte("null")) {
		return json.Unmarshal(wrapped.Data, dst)
	}
	return json.Unmarshal(data, dst)
}
