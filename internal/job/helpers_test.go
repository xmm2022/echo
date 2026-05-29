package job

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func fixedNow() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "echo.db"))
	st, err := store.Open("file:" + dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}

func createJobRow(t *testing.T, ctx context.Context, st *store.Store, kind, status string, createdAt int64) queries.Job {
	t.Helper()
	job, err := st.CreateJob(ctx, queries.CreateJobParams{
		Kind:      kind,
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

// waitForJobStatus polls the job row until its status matches want or the
// deadline elapses. Job execution is asynchronous, so the terminal status is
// written after the handler returns; polling avoids racing that write.
func waitForJobStatus(t *testing.T, ctx context.Context, st *store.Store, jobID int64, want string) queries.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		job, err := st.GetJob(ctx, queries.GetJobParams{ID: jobID})
		if err != nil {
			t.Fatalf("get job %d: %v", jobID, err)
		}
		if job.Status == want {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %d status = %q, want %q (timed out)", jobID, job.Status, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// recordingHandler is a test Handler that records concurrency and blocks each
// invocation until released, so tests can observe the runner's scheduling.
type recordingHandler struct {
	mu        sync.Mutex
	active    int
	maxActive int
	calls     []int64

	started chan int64
	release chan struct{}
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{
		started: make(chan int64, 64),
		release: make(chan struct{}),
	}
}

func (h *recordingHandler) handle(ctx context.Context, job queries.Job) error {
	h.mu.Lock()
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.calls = append(h.calls, job.ID)
	h.mu.Unlock()

	h.started <- job.ID

	select {
	case <-h.release:
	case <-ctx.Done():
	}

	h.mu.Lock()
	h.active--
	h.mu.Unlock()
	return ctx.Err()
}

func (h *recordingHandler) maxObserved() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.maxActive
}

func (h *recordingHandler) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}
