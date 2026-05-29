package sidecarclient

import (
	"context"
	"net/http"
)

type Storage struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	MountPath string `json:"mount_path"`
	Status    string `json:"status"`
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

	var storages []Storage
	if err := decodeData(resp.Body, &storages); err != nil {
		return nil, err
	}
	return storages, nil
}
