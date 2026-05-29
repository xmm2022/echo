package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/xmm2022/echo/internal/store/queries"
)

// FailedItem mirrors a single failed manifest item recorded in job progress.
type FailedItem struct {
	RelPath string `json:"rel_path"`
	Reason  string `json:"reason"`
}

// Progress is the canonical shape persisted in jobs.progress.
//
// Per-item progress during an ingest run is written by the ingest pipeline's
// own throttled tracker (1s, spec §4). The runner does not duplicate that;
// it only seeds an initial snapshot when a job starts running so the column is
// never NULL while a job is in flight — notably during a long producer
// subprocess, before ingest begins reporting per item.
type Progress struct {
	Current     int          `json:"current"`
	Total       int          `json:"total"`
	Msg         string       `json:"msg,omitempty"`
	Warnings    int          `json:"warnings"`
	FailedItems []FailedItem `json:"failed_items,omitempty"`
}

func marshalProgress(p Progress) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal job progress: %w", err)
	}
	return string(body), nil
}

// ParseProgress decodes a jobs.progress column value. A NULL or empty column
// yields the zero Progress and ok=false.
func ParseProgress(col sql.NullString) (Progress, bool, error) {
	if !col.Valid || col.String == "" {
		return Progress{}, false, nil
	}
	var p Progress
	if err := json.Unmarshal([]byte(col.String), &p); err != nil {
		return Progress{}, false, fmt.Errorf("parse job progress: %w", err)
	}
	return p, true, nil
}

// writeInitialProgress seeds jobs.progress with a running snapshot.
func (r *Runner) writeInitialProgress(ctx context.Context, jobID int64) error {
	body, err := marshalProgress(Progress{Msg: "running"})
	if err != nil {
		return err
	}
	return r.store.UpdateJobProgress(ctx, queries.UpdateJobProgressParams{
		ID:       jobID,
		Progress: sql.NullString{String: body, Valid: true},
	})
}
