package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/xmm2022/echo/internal/store/queries"
)

func createJobWith(t *testing.T, ctx context.Context, st *Store, status string, createdAt int64) queries.Job {
	t.Helper()
	job, err := st.CreateJob(ctx, queries.CreateJobParams{
		Kind:      "ingest_manual",
		Status:    status,
		Payload:   "{}",
		OwnerID:   "admin",
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

func TestFailRunningJobsMarksOnlyRunning(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	pending := createJobWith(t, ctx, st, "pending", 10)
	running1 := createJobWith(t, ctx, st, "running", 20)
	running2 := createJobWith(t, ctx, st, "running", 30)
	done := createJobWith(t, ctx, st, "done", 40)

	if err := st.FailRunningJobs(ctx, queries.FailRunningJobsParams{
		Error:      sql.NullString{String: "interrupted", Valid: true},
		FinishedAt: sql.NullInt64{Int64: 99, Valid: true},
	}); err != nil {
		t.Fatalf("fail running jobs: %v", err)
	}

	for _, id := range []int64{running1.ID, running2.ID} {
		got, err := st.GetJob(ctx, queries.GetJobParams{ID: id})
		if err != nil {
			t.Fatalf("get job %d: %v", id, err)
		}
		if got.Status != "failed" {
			t.Errorf("job %d status = %q, want failed", id, got.Status)
		}
		if !got.Error.Valid || got.Error.String != "interrupted" {
			t.Errorf("job %d error = %+v, want interrupted", id, got.Error)
		}
		if !got.FinishedAt.Valid || got.FinishedAt.Int64 != 99 {
			t.Errorf("job %d finished_at = %+v, want 99", id, got.FinishedAt)
		}
	}

	pendingGot, err := st.GetJob(ctx, queries.GetJobParams{ID: pending.ID})
	if err != nil {
		t.Fatalf("get pending job: %v", err)
	}
	if pendingGot.Status != "pending" {
		t.Errorf("pending job status = %q, want pending", pendingGot.Status)
	}
	if pendingGot.Error.Valid || pendingGot.FinishedAt.Valid {
		t.Errorf("pending job mutated: error=%+v finished=%+v", pendingGot.Error, pendingGot.FinishedAt)
	}

	doneGot, err := st.GetJob(ctx, queries.GetJobParams{ID: done.ID})
	if err != nil {
		t.Fatalf("get done job: %v", err)
	}
	if doneGot.Status != "done" {
		t.Errorf("done job status = %q, want done", doneGot.Status)
	}
}

func TestListJobIDsByStatusOrdersByCreatedAtThenID(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	late := createJobWith(t, ctx, st, "pending", 30)
	earlyA := createJobWith(t, ctx, st, "pending", 10)
	running := createJobWith(t, ctx, st, "running", 20)
	earlyB := createJobWith(t, ctx, st, "pending", 10)

	ids, err := st.ListJobIDsByStatus(ctx, queries.ListJobIDsByStatusParams{Status: "pending"})
	if err != nil {
		t.Fatalf("list job ids: %v", err)
	}

	want := []int64{earlyA.ID, earlyB.ID, late.ID}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
	for _, id := range ids {
		if id == running.ID {
			t.Fatalf("running job %d should not be in pending list", running.ID)
		}
	}
}
