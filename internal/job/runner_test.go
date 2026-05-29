package job

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without a store should return an error")
	}
}

func TestMarshalPayload(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    string
		wantErr bool
	}{
		{name: "nil", in: nil, want: "{}"},
		{name: "empty string", in: "", want: "{}"},
		{name: "json string", in: `{"a":1}`, want: `{"a":1}`},
		{name: "empty bytes", in: []byte(nil), want: "{}"},
		{name: "bytes", in: []byte(`{"b":2}`), want: `{"b":2}`},
		{name: "empty raw", in: json.RawMessage(nil), want: "{}"},
		{name: "raw", in: json.RawMessage(`{"c":3}`), want: `{"c":3}`},
		{name: "struct", in: struct {
			X int `json:"x"`
		}{X: 5}, want: `{"x":5}`},
		{name: "unmarshalable", in: make(chan int), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := marshalPayload(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("marshalPayload(%v) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("marshalPayload(%v): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("marshalPayload(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEnqueueRunsHandlerAndMarksDone(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	seen := make(chan queries.Job, 1)
	r, err := New(Config{
		Store:         st,
		MaxConcurrent: 2,
		Now:           fixedNow,
		Handlers: map[string]Handler{
			"unit_test": func(_ context.Context, job queries.Job) error {
				seen <- job
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

	jobID, err := r.Enqueue(ctx, "unit_test", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if jobID <= 0 {
		t.Fatalf("enqueue returned non-positive job id %d", jobID)
	}

	select {
	case got := <-seen:
		if got.ID != jobID {
			t.Errorf("handler job id = %d, want %d", got.ID, jobID)
		}
		if got.Kind != "unit_test" {
			t.Errorf("handler job kind = %q, want unit_test", got.Kind)
		}
		if !strings.Contains(got.Payload, `"hello":"world"`) {
			t.Errorf("handler job payload = %q, want it to contain hello:world", got.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}

	done := waitForJobStatus(t, ctx, st, jobID, "done")
	if done.Error.Valid {
		t.Errorf("done job has error %q, want none", done.Error.String)
	}
	if !done.StartedAt.Valid {
		t.Error("done job missing started_at")
	}
	if !done.FinishedAt.Valid {
		t.Error("done job missing finished_at")
	}
}

func TestEnqueuePersistsPendingRowBeforeRun(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	release := make(chan struct{})
	r, err := New(Config{
		Store:         st,
		MaxConcurrent: 1,
		Now:           fixedNow,
		Handlers: map[string]Handler{
			"unit_test": func(ctx context.Context, _ queries.Job) error {
				<-release
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
	defer func() {
		close(release)
		r.Stop()
	}()

	jobID, err := r.Enqueue(ctx, "unit_test", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	row, err := st.GetJob(ctx, queries.GetJobParams{ID: jobID})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if row.Kind != "unit_test" {
		t.Errorf("row kind = %q, want unit_test", row.Kind)
	}
	if row.Payload != "{}" {
		t.Errorf("row payload = %q, want {}", row.Payload)
	}
	if row.CreatedAt != fixedNow().Unix() {
		t.Errorf("row created_at = %d, want %d", row.CreatedAt, fixedNow().Unix())
	}
}

func TestHandlerErrorMarksJobFailed(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	r, err := New(Config{
		Store:         st,
		MaxConcurrent: 2,
		Now:           fixedNow,
		Handlers: map[string]Handler{
			"unit_test": func(context.Context, queries.Job) error {
				return errors.New("boom")
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

	jobID, err := r.Enqueue(ctx, "unit_test", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	failed := waitForJobStatus(t, ctx, st, jobID, "failed")
	if !failed.Error.Valid || !strings.Contains(failed.Error.String, "boom") {
		t.Errorf("failed job error = %+v, want it to contain boom", failed.Error)
	}
	if !failed.FinishedAt.Valid {
		t.Error("failed job missing finished_at")
	}
}

func TestUnknownKindMarksJobFailed(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	r, err := New(Config{Store: st, MaxConcurrent: 1, Now: fixedNow})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.Stop()

	jobID, err := r.Enqueue(ctx, "mystery", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	failed := waitForJobStatus(t, ctx, st, jobID, "failed")
	if !failed.Error.Valid || !strings.Contains(failed.Error.String, "mystery") {
		t.Errorf("failed job error = %+v, want it to mention the kind", failed.Error)
	}
}

func TestConcurrencyCappedByMaxConcurrent(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	const maxConcurrent = 2
	const total = 8
	h := newRecordingHandler()
	r, err := New(Config{
		Store:         st,
		MaxConcurrent: maxConcurrent,
		Now:           fixedNow,
		Handlers:      map[string]Handler{"unit_test": h.handle},
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	for i := 0; i < total; i++ {
		if _, err := r.Enqueue(ctx, "unit_test", nil); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// Exactly maxConcurrent handlers should start; the rest must wait.
	for i := 0; i < maxConcurrent; i++ {
		select {
		case <-h.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d handlers started, want %d", i, maxConcurrent)
		}
	}
	select {
	case <-h.started:
		t.Fatalf("a %dth handler started; concurrency cap not enforced", maxConcurrent+1)
	case <-time.After(150 * time.Millisecond):
	}

	// Release everything and let the remaining jobs drain.
	close(h.release)
	for i := maxConcurrent; i < total; i++ {
		select {
		case <-h.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("handler %d never started after release", i)
		}
	}
	r.Stop()

	if got := h.maxObserved(); got != maxConcurrent {
		t.Errorf("max concurrent handlers = %d, want %d", got, maxConcurrent)
	}
	if got := h.callCount(); got != total {
		t.Errorf("handler call count = %d, want %d", got, total)
	}
}

func TestStopCancelsRunningHandler(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	h := newRecordingHandler()
	r, err := New(Config{
		Store:         st,
		MaxConcurrent: 2,
		Now:           fixedNow,
		Handlers:      map[string]Handler{"unit_test": h.handle},
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	jobID, err := r.Enqueue(ctx, "unit_test", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}

	// Stop cancels the runner context; the blocked handler must observe
	// ctx.Done() and return, allowing Stop to complete.
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return; worker did not exit on cancel")
	}

	failed := waitForJobStatus(t, ctx, st, jobID, "failed")
	if !failed.FinishedAt.Valid {
		t.Error("canceled job missing finished_at")
	}
}

func TestCancelStopsSpecificJob(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	h := newRecordingHandler()
	r, err := New(Config{
		Store:         st,
		MaxConcurrent: 2,
		Now:           fixedNow,
		Handlers:      map[string]Handler{"unit_test": h.handle},
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.Stop()

	jobID, err := r.Enqueue(ctx, "unit_test", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}

	if !r.Cancel(jobID) {
		t.Fatal("Cancel returned false for a running job")
	}

	failed := waitForJobStatus(t, ctx, st, jobID, "failed")
	if !failed.FinishedAt.Valid {
		t.Error("canceled job missing finished_at")
	}
}
