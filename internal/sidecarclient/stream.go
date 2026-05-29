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
		return nil, c.httpError(resp)
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
