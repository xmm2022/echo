package sidecarclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type DirectLink struct {
	URL       string
	Headers   http.Header
	ExpiresAt time.Time
}

type linkHeaderValues map[string][]string

func (h *linkHeaderValues) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*h = nil
		return nil
	}

	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	values := make(linkHeaderValues, len(raw))
	for key, item := range raw {
		var single string
		if err := json.Unmarshal(item, &single); err == nil {
			values[key] = []string{single}
			continue
		}

		var multi []string
		if err := json.Unmarshal(item, &multi); err == nil {
			values[key] = multi
			continue
		}

		return fmt.Errorf("decode link header %q: want string or []string", key)
	}

	*h = values
	return nil
}

func (c *Client) Link(ctx context.Context, storageMount, remotePath string) (*DirectLink, error) {
	fullPath := sidecarPath(storageMount, remotePath)
	payload := map[string]string{
		"storage_mount": storageMount,
		"mount":         storageMount,
		"path":          fullPath,
		"remote_path":   remotePath,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json")

	resp, err := c.do(ctx, http.MethodPost, "/api/fs/link", bytes.NewReader(body), header, c.requestTimeout)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.httpError(resp)
	}

	var raw struct {
		URL       string           `json:"url"`
		RawURL    string           `json:"raw_url"`
		Headers   linkHeaderValues `json:"headers"`
		Header    linkHeaderValues `json:"header"`
		ExpiresAt string           `json:"expires_at"`
	}
	if err := decodeData(resp.Body, &raw); err != nil {
		// A non-success OpenList envelope (code != 200, incl. code == 0) on a
		// 200 response is the real sidecar's way of signalling an fs/link
		// failure. Classify it so callers can do correct multi-copy fallback
		// and bad-copy eviction.
		var env *sidecarEnvelopeError
		if errors.As(err, &env) {
			return nil, classifySidecarMessage("link", resp.StatusCode, env.Code, env.Message)
		}
		return nil, err
	}
	linkURL := raw.URL
	if linkURL == "" {
		linkURL = raw.RawURL
	}
	if linkURL == "" {
		return nil, ErrLinkNotAvailable
	}
	headers := http.Header{}
	mergeLinkHeaders(headers, raw.Header)
	mergeLinkHeaders(headers, raw.Headers)
	expiresAt := time.Time{}
	if raw.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, raw.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("parse direct link expires_at: %w", err)
		}
		expiresAt = parsed
	}
	return &DirectLink{
		URL:       linkURL,
		Headers:   headers,
		ExpiresAt: expiresAt,
	}, nil
}

func mergeLinkHeaders(dst http.Header, src linkHeaderValues) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
