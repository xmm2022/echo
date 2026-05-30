package sidecarclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Storage mirrors the subset of OpenList's model.Storage that Echo binds
// accounts to. Echo only needs id / driver / mount_path / status; the sidecar
// returns many more fields which are intentionally ignored.
type Storage struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	MountPath string `json:"mount_path"`
	Status    string `json:"status"`
}

// UnmarshalJSON pins the real OpenList model.Storage contract:
//   - id is a numeric primary key (uint); a string (or any non-numeric) id is
//     rejected — a string id means we are not talking to the real sidecar shape.
//   - the sidecar field is "driver" (there is no "provider" field); Echo keeps
//     its own "Provider" vocabulary but populates it from driver.
func (s *Storage) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        json.RawMessage `json:"id"`
		Driver    string          `json:"driver"`
		MountPath string          `json:"mount_path"`
		Status    string          `json:"status"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode sidecar storage: %w", err)
	}
	id := strings.TrimSpace(string(raw.ID))
	if id == "" || id == "null" {
		return fmt.Errorf("sidecar storage missing id")
	}
	if id[0] == '"' {
		return fmt.Errorf("sidecar storage id must be numeric, got string %s", id)
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return fmt.Errorf("sidecar storage id must be an integer, got %s", id)
	}
	s.ID = id
	s.Provider = raw.Driver
	s.MountPath = raw.MountPath
	s.Status = raw.Status
	return nil
}

func (c *Client) ListStorages(ctx context.Context) ([]Storage, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/admin/storage/list", nil, nil, c.requestTimeout)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.httpError(resp)
	}

	// Real OpenList always returns a paginated {content,total} envelope under
	// data; a bare array is not a shape the sidecar produces, so decoding into
	// this struct naturally rejects it.
	var page struct {
		Content []Storage `json:"content"`
		Total   int64     `json:"total"`
	}
	if err := decodeData(resp.Body, &page); err != nil {
		return nil, err
	}
	return page.Content, nil
}
