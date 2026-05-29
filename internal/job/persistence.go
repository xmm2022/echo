package job

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/store/queries"
)

const interruptedError = "interrupted"

// recoverInterrupted marks every job left in the running state by a previous
// process as failed (spec §5: process crash). v0.1 does not auto-resume a job;
// the admin re-submits it.
func (r *Runner) recoverInterrupted(ctx context.Context) error {
	return r.store.FailRunningJobs(ctx, queries.FailRunningJobsParams{
		Error:      sql.NullString{String: interruptedError, Valid: true},
		FinishedAt: sql.NullInt64{Int64: r.now().Unix(), Valid: true},
	})
}

// reconcile re-drives the .echo write-back for committed library entries whose
// filesystem write did not complete, and clears stale temp files (spec §3
// write timing). Run once at startup, after recoverInterrupted.
func (r *Runner) reconcile(ctx context.Context) error {
	return ingest.Reconcile(ctx, r.store, r.logger)
}

// loadPending re-enqueues jobs that were already pending in the DB (enqueued
// but not yet dispatched before a restart) so they run after startup.
func (r *Runner) loadPending(ctx context.Context) error {
	ids, err := r.store.ListJobIDsByStatus(ctx, queries.ListJobIDsByStatusParams{Status: statusPending})
	if err != nil {
		return fmt.Errorf("list pending jobs: %w", err)
	}
	for _, id := range ids {
		r.enqueueID(id)
	}
	return nil
}
