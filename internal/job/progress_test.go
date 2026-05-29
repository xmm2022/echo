package job

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

func TestInitialProgressWrittenWhenJobStarts(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	h := newRecordingHandler()
	r, err := New(Config{
		Store:         st,
		MaxConcurrent: 1,
		Now:           fixedNow,
		Handlers:      map[string]Handler{"unit_test": h.handle},
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		close(h.release)
		r.Stop()
	}()

	jobID, err := r.Enqueue(ctx, "unit_test", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait until the handler is running; by then runJob has already seeded the
	// initial progress snapshot (it writes before invoking the handler).
	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}

	row, err := st.GetJob(ctx, queries.GetJobParams{ID: jobID})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	progress, ok, err := ParseProgress(row.Progress)
	if err != nil {
		t.Fatalf("parse progress: %v", err)
	}
	if !ok {
		t.Fatal("job progress is null while running; want an initial snapshot")
	}
	if progress.Current != 0 {
		t.Errorf("initial progress current = %d, want 0", progress.Current)
	}
}

func TestParseProgress(t *testing.T) {
	if _, ok, err := ParseProgress(sql.NullString{}); err != nil || ok {
		t.Errorf("null column: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	if _, ok, err := ParseProgress(sql.NullString{String: "", Valid: true}); err != nil || ok {
		t.Errorf("empty column: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	p, ok, err := ParseProgress(sql.NullString{
		String: `{"current":3,"total":5,"warnings":1,"failed_items":[{"rel_path":"a/b.mkv","reason":"boom"}]}`,
		Valid:  true,
	})
	if err != nil || !ok {
		t.Fatalf("valid column: ok=%v err=%v", ok, err)
	}
	if p.Current != 3 || p.Total != 5 || p.Warnings != 1 {
		t.Errorf("decoded progress = %+v, want current=3 total=5 warnings=1", p)
	}
	if len(p.FailedItems) != 1 || p.FailedItems[0].RelPath != "a/b.mkv" || p.FailedItems[0].Reason != "boom" {
		t.Errorf("decoded failed items = %+v", p.FailedItems)
	}

	if _, _, err := ParseProgress(sql.NullString{String: "{not json", Valid: true}); err == nil {
		t.Error("malformed column: want error, got nil")
	}
}
