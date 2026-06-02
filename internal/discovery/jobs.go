package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	KindSourceCrawl       = "discovery_source_crawl"
	KindSubscriptionCheck = "discovery_subscription_check"
	KindDispatch          = "discovery_dispatch"
	KindReconcile         = "discovery_reconcile"
	KindTMDBRefresh       = "discovery_tmdb_refresh"
)

func JobHandlers(deps Deps) map[string]job.Handler {
	orchestrator := NewOrchestrator(deps)
	return map[string]job.Handler{
		KindSourceCrawl: func(ctx context.Context, row queries.Job) error {
			payload, err := decodePayload[SourceCrawlPayload](row)
			if err != nil {
				return err
			}
			return orchestrator.RunSourceCrawl(ctx, payload)
		},
		KindSubscriptionCheck: func(ctx context.Context, row queries.Job) error {
			payload, err := decodePayload[SubscriptionCheckPayload](row)
			if err != nil {
				return err
			}
			return orchestrator.RunSubscriptionCheck(ctx, payload)
		},
		KindDispatch: func(ctx context.Context, row queries.Job) error {
			payload, err := decodePayload[DispatchPayload](row)
			if err != nil {
				return err
			}
			return orchestrator.RunDispatch(ctx, payload)
		},
		KindReconcile: func(ctx context.Context, row queries.Job) error {
			payload, err := decodePayload[ReconcilePayload](row)
			if err != nil {
				return err
			}
			return orchestrator.RunReconcile(ctx, payload)
		},
		KindTMDBRefresh: func(ctx context.Context, row queries.Job) error {
			if _, err := decodePayload[struct{}](row); err != nil {
				return err
			}
			return orchestrator.RunTMDBRefresh(ctx)
		},
	}
}

func decodePayload[T any](row queries.Job) (T, error) {
	var payload T
	body := row.Payload
	if strings.TrimSpace(body) == "" {
		body = "{}"
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return payload, fmt.Errorf("decode discovery payload for job %d: %w", row.ID, err)
	}
	return payload, nil
}
