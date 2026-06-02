package discovery

import (
	"context"

	storeq "github.com/xmm2022/echo/internal/store/queries"
)

type DiscoveredResourceStore struct {
	writer *SafeDiscoveredResourceWriter
}

// NewDiscoveredResourceStore is the narrow Task-2 discovery storage facade for
// writes that must enforce parsed_json redaction before reaching sqlc.
func NewDiscoveredResourceStore(inner discoveredResourceWriter) *DiscoveredResourceStore {
	return &DiscoveredResourceStore{writer: NewSafeDiscoveredResourceWriter(inner)}
}

func (s *DiscoveredResourceStore) UpsertDiscoveredResource(ctx context.Context, arg storeq.UpsertDiscoveredResourceParams) (storeq.DiscoveredResource, error) {
	return s.writer.UpsertDiscoveredResource(ctx, arg)
}
