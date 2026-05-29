package job

import (
	"context"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

func TestStartMarksRunningJobsInterrupted(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Simulate a job left running by a crashed previous process.
	stale := createJobRow(t, ctx, st, "ingest_manual", "running", 10)
	other := createJobRow(t, ctx, st, "ingest_manual", "done", 20)

	r, err := New(Config{Store: st, MaxConcurrent: 2, Now: fixedNow})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.Stop()

	got, err := st.GetJob(ctx, queries.GetJobParams{ID: stale.ID})
	if err != nil {
		t.Fatalf("get stale job: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("stale running job status = %q, want failed", got.Status)
	}
	if !got.Error.Valid || got.Error.String != interruptedError {
		t.Errorf("stale running job error = %+v, want %q", got.Error, interruptedError)
	}
	if !got.FinishedAt.Valid {
		t.Error("stale running job missing finished_at")
	}

	doneGot, err := st.GetJob(ctx, queries.GetJobParams{ID: other.ID})
	if err != nil {
		t.Fatalf("get done job: %v", err)
	}
	if doneGot.Status != "done" {
		t.Errorf("done job status = %q, want done (untouched)", doneGot.Status)
	}
}

func TestStartRunsPendingJobsFromDB(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// A job enqueued by a previous process but never dispatched.
	pending := createJobRow(t, ctx, st, "unit_test", "pending", 10)

	ran := make(chan int64, 1)
	r, err := New(Config{
		Store:         st,
		MaxConcurrent: 2,
		Now:           fixedNow,
		Handlers: map[string]Handler{
			"unit_test": func(_ context.Context, job queries.Job) error {
				ran <- job.ID
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.Stop()

	select {
	case id := <-ran:
		if id != pending.ID {
			t.Errorf("ran job %d, want %d", id, pending.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending job was not picked up at startup")
	}

	waitForJobStatus(t, ctx, st, pending.ID, "done")
}
