package sidecarclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
	header.Set("File-Path", path.Join(req.RemoteDir, req.CASName))
	header.Set("X-Echo-Storage-Mount", req.StorageMount)
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
		return &result, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return nil, fmt.Errorf("%w: status %d", ErrCASRestoreFailed, resp.StatusCode)
	default:
		return nil, c.httpError(resp)
	}
}
