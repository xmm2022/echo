package sidecarclient

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type StreamRequest struct {
	StorageMount string
	RemotePath   string
	Headers      http.Header
}

type StreamResult struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

var streamRequestHeaders = []string{
	"Range",
	"If-Range",
	"If-Modified-Since",
	"If-None-Match",
	"User-Agent",
}

var streamResponseHeaders = []string{
	"Content-Length",
	"Content-Range",
	"Content-Type",
	"Accept-Ranges",
	"Last-Modified",
	"ETag",
}

func (c *Client) Stream(ctx context.Context, req StreamRequest) (*StreamResult, error) {
	pathValue := sidecarPath(req.StorageMount, req.RemotePath)
	endpoint := "/d/" + strings.TrimLeft(pathValue, "/")
	header := http.Header{}
	for _, key := range streamRequestHeaders {
		for _, value := range req.Headers.Values(key) {
			header.Add(key, value)
		}
	}
	header.Set("X-Echo-Storage-Mount", req.StorageMount)

	resp, err := c.do(ctx, http.MethodGet, endpoint, nil, header, c.streamTimeout)
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified, http.StatusRequestedRangeNotSatisfiable:
		result := &StreamResult{
			StatusCode: resp.StatusCode,
			Header:     copyHeaders(resp.Header, streamResponseHeaders),
			Body:       resp.Body,
		}
		return result, nil
	default:
		defer resp.Body.Close()
		// A real OpenList /d/ download failure arrives as a non-2xx HTTP status
		// with an HTML body (e.g. 500 + "<html>...failed to get file...</html>"),
		// NOT a JSON {code,message,data} envelope. The HTML may literally say
		// "object not found", but on real hardware that is low-confidence
		// evidence, so we classify it as a transient/suspect html_snippet failure
		// and deliberately do NOT route it through classifySidecarMessage (which
		// would mark it object-missing/dead). JSON-envelope failures keep their
		// existing SidecarHTTPError behavior.
		if isHTMLResponse(resp) {
			return nil, streamHTMLError(resp)
		}
		return nil, c.httpError(resp)
	}
}

// streamHTMLDownloadBodyLimit bounds how much of a failed /d/ HTML body we read
// before classifying it, so a hostile or oversized error page cannot exhaust
// memory. safeSidecarMessage caps the resulting SafeMessage at 512 bytes.
const streamHTMLDownloadBodyLimit = 4 << 10 // 4 KiB

// isHTMLResponse reports whether the response advertises an HTML body via its
// Content-Type header (the shape real OpenList /d/ failures take).
func isHTMLResponse(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html")
}

// streamHTMLError builds a transient/suspect typed error from a non-2xx /d/ HTML
// response. It reads at most streamHTMLDownloadBodyLimit of the body, redacts and
// strips tags for the SafeMessage, and keeps the raw snippet in RawMessage (which
// must never be logged or serialized to clients).
func streamHTMLError(resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, streamHTMLDownloadBodyLimit))
	raw := string(snippet)
	return &SidecarTypedError{
		Kind:          SidecarErrTransient,
		Operation:     "stream",
		HTTPStatus:    resp.StatusCode,
		SafeMessage:   safeSidecarMessage(raw),
		RawMessage:    raw,
		EvidenceClass: "html_snippet",
		Confidence:    "suspect",
	}
}

func copyHeaders(src http.Header, keys []string) http.Header {
	dst := http.Header{}
	for _, key := range keys {
		for _, value := range src.Values(key) {
			dst.Add(key, value)
		}
	}
	return dst
}

func escapeDPath(rawPath string) string {
	parts := strings.Split(rawPath, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
