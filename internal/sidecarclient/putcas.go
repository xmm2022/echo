package sidecarclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	StatusRestored   = "restored"
	StatusSkippedDup = "skipped_dup"
	StatusFailed     = "failed"
)

type PutCASRequest struct {
	StorageMount string
	RemoteDir    string
	CASName      string
	CASBody      io.Reader
	CASSize      int64
}

type ItemResult struct {
	Status    string            `json:"status"`
	Error     string            `json:"error"`
	CloudPath string            `json:"cloud_path"`
	SizeBytes int64             `json:"size_bytes"`
	Hashes    map[string]string `json:"hashes"`
}

func (c *Client) PutCAS(ctx context.Context, req PutCASRequest) (*ItemResult, error) {
	if !strings.HasSuffix(req.CASName, ".cas") {
		return nil, fmt.Errorf("%w: CASName must end with .cas", ErrCASRestoreFailed)
	}

	header := http.Header{}
	// FsStream (PUT /api/fs/put) reads File-Path and url.PathUnescape's it, so
	// each segment is percent-escaped (slashes preserved) to survive file names
	// containing %, #, spaces, CJK, etc.
	header.Set("File-Path", escapeFilePathHeader(sidecarPath(req.StorageMount, req.RemoteDir, req.CASName)))
	if req.CASSize >= 0 {
		header.Set("Content-Length", fmt.Sprintf("%d", req.CASSize))
	}

	resp, err := c.doWithContentLength(ctx, http.MethodPut, "/api/fs/put", req.CASBody, header, c.requestTimeout, req.CASSize)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		var result ItemResult
		if err := decodeData(resp.Body, &result); err != nil {
			return nil, err
		}
		// Real FsStream returns {code,message,data:null} on a synchronous put;
		// the sidecar does not echo a status/cloud_path, so Echo treats a clean
		// 2xx envelope as restored and derives the cloud path itself.
		if result.Status == "" {
			result.Status = StatusRestored
		}
		result.CloudPath = normalizePutCASCloudPath(req, result.CloudPath)
		return &result, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return nil, fmt.Errorf("%w: status %d", ErrCASRestoreFailed, resp.StatusCode)
	default:
		return nil, c.httpError(resp)
	}
}

// escapeFilePathHeader percent-escapes each path segment so the value survives
// transport in the File-Path header; FsStream url.PathUnescape's it back.
func escapeFilePathHeader(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func normalizePutCASCloudPath(req PutCASRequest, cloudPath string) string {
	if cloudPath == "" {
		cloudPath = path.Join(req.RemoteDir, strings.TrimSuffix(req.CASName, ".cas"))
	}
	cleaned := sidecarPath("", cloudPath)
	mount := sidecarPath("", req.StorageMount)
	if mount != "/" {
		switch {
		case cleaned == mount:
			cleaned = "/"
		case strings.HasPrefix(cleaned, mount+"/"):
			cleaned = strings.TrimPrefix(cleaned, mount)
		}
	}
	return sidecarPath("", cleaned)
}
