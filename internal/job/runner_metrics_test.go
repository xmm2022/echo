package job

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/xmm2022/echo/internal/metrics"
	"github.com/xmm2022/echo/internal/store/queries"
)

// TestRunnerRecordsJobMetrics proves the finishJob → metrics.IncJob wiring fires
// with the correct kind/status labels for both terminal outcomes.
func TestRunnerRecordsJobMetrics(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	r, err := New(Config{
		Store:         st,
		MaxConcurrent: 2,
		Now:           fixedNow,
		Metrics:       m,
		Handlers: map[string]Handler{
			"ok_job":  func(context.Context, queries.Job) error { return nil },
			"err_job": func(context.Context, queries.Job) error { return errors.New("boom") },
		},
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.Stop()

	okID, err := r.Enqueue(ctx, "ok_job", nil)
	if err != nil {
		t.Fatalf("enqueue ok_job: %v", err)
	}
	errID, err := r.Enqueue(ctx, "err_job", nil)
	if err != nil {
		t.Fatalf("enqueue err_job: %v", err)
	}
	waitForJobStatus(t, ctx, st, okID, "done")
	waitForJobStatus(t, ctx, st, errID, "failed")

	if got := jobCounter(t, reg, "ok_job", "done"); got != 1 {
		t.Errorf(`echo_jobs_total{kind="ok_job",status="done"} = %v, want 1`, got)
	}
	if got := jobCounter(t, reg, "err_job", "failed"); got != 1 {
		t.Errorf(`echo_jobs_total{kind="err_job",status="failed"} = %v, want 1`, got)
	}
}

func jobCounter(t *testing.T, g prometheus.Gatherer, kind, status string) float64 {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "echo_jobs_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range metric.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["kind"] == kind && labels["status"] == status {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return -1
}
