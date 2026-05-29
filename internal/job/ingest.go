package job

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/store/queries"
)

// Job kinds handled by the ingest wiring. HTTP handlers (Phase 9) enqueue with
// these kinds; IngestHandlers maps them to the ingest entry points.
const (
	KindIngestManual   = "ingest_manual"
	KindIngestProducer = "ingest_producer"
)

// IngestPayload is the JSON stored in jobs.payload for ingest jobs. Manual
// imports set CASTreePath/ManifestPath; producer jobs set Tool/Args (the
// producer step then fills in the cas tree and manifest itself).
type IngestPayload struct {
	LibraryID     int64          `json:"library_id"`
	TargetAccount string         `json:"target_account"`
	TargetSubdir  string         `json:"target_subdir,omitempty"`
	CASTreePath   string         `json:"cas_tree_path,omitempty"`
	ManifestPath  string         `json:"manifest_path,omitempty"`
	Tool          string         `json:"tool,omitempty"`
	Args          map[string]any `json:"args,omitempty"`
}

func payloadToIngestJob(job queries.Job) (ingest.Job, error) {
	var p IngestPayload
	if err := json.Unmarshal([]byte(job.Payload), &p); err != nil {
		return ingest.Job{}, fmt.Errorf("decode ingest payload for job %d: %w", job.ID, err)
	}
	owner := job.OwnerID
	if owner == "" {
		owner = defaultOwnerID
	}
	return ingest.Job{
		ID:            job.ID,
		LibraryID:     p.LibraryID,
		TargetAccount: p.TargetAccount,
		TargetSubdir:  p.TargetSubdir,
		CASTreePath:   p.CASTreePath,
		ManifestPath:  p.ManifestPath,
		OwnerID:       owner,
		Tool:          p.Tool,
		Args:          p.Args,
	}, nil
}

// IngestHandlers builds the runner Handler map that drives the Phase 5/6 ingest
// entry points. deps carries the store, sidecar, and ingest config (notably
// Config.WorkerPerJob, the per-job worker count).
func IngestHandlers(deps ingest.Deps) map[string]Handler {
	return map[string]Handler{
		KindIngestManual: func(ctx context.Context, job queries.Job) error {
			ingestJob, err := payloadToIngestJob(job)
			if err != nil {
				return err
			}
			return ingest.RunManual(ctx, ingestJob, deps)
		},
		KindIngestProducer: func(ctx context.Context, job queries.Job) error {
			ingestJob, err := payloadToIngestJob(job)
			if err != nil {
				return err
			}
			return ingest.RunProducer(ctx, ingestJob, deps)
		},
	}
}
