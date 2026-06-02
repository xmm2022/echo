package job

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xmm2022/echo/internal/metrics"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	statusPending = "pending"
	statusDone    = "done"
	statusFailed  = "failed"

	defaultOwnerID = "admin"
)

// Handler executes a single job. The terminal status is derived from the
// returned error: nil → done, non-nil → failed (error recorded in jobs.error).
// Handlers MUST honor ctx cancellation (graceful shutdown and per-job cancel).
type Handler func(ctx context.Context, job queries.Job) error

// Config configures a Runner.
type Config struct {
	Store         *store.Store
	Handlers      map[string]Handler
	MaxConcurrent int
	Logger        *slog.Logger
	Now           func() time.Time
	// Metrics is optional; when nil, job metrics are not recorded.
	Metrics *metrics.Metrics
}

// Runner executes queued jobs with a bounded number of concurrent jobs. It is
// safe for concurrent use after Start. v0.1 assumes a single Runner per process
// (spec §5: no cross-process locking).
type Runner struct {
	store         *store.Store
	handlers      map[string]Handler
	maxConcurrent int
	logger        *slog.Logger
	now           func() time.Time
	metrics       *metrics.Metrics

	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}

	queueMu sync.Mutex
	queue   []int64
	notify  chan struct{}

	jobsWG     sync.WaitGroup
	dispatchWG sync.WaitGroup

	cancelMu sync.Mutex
	cancels  map[int64]context.CancelFunc

	startOnce sync.Once
	stopOnce  sync.Once
}

// New validates the config and returns a Runner that has not started yet.
func New(cfg Config) (*Runner, error) {
	if cfg.Store == nil {
		return nil, errors.New("job: store is required")
	}
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	handlers := make(map[string]Handler, len(cfg.Handlers))
	for kind, h := range cfg.Handlers {
		handlers[kind] = h
	}
	return &Runner{
		store:         cfg.Store,
		handlers:      handlers,
		maxConcurrent: maxConcurrent,
		logger:        logger,
		now:           now,
		metrics:       cfg.Metrics,
		sem:           make(chan struct{}, maxConcurrent),
		notify:        make(chan struct{}, 1),
		cancels:       make(map[int64]context.CancelFunc),
	}, nil
}

// Start performs crash recovery and reconcile, re-enqueues pending jobs, then
// launches the dispatcher. The provided context governs the runner lifetime:
// cancelling it (or calling Stop) drains in-flight jobs. Start is idempotent;
// if it returns an error the Runner is left unusable (the dispatcher never
// launched) — treat the error as fatal rather than retrying Start.
func (r *Runner) Start(ctx context.Context) error {
	var startErr error
	r.startOnce.Do(func() {
		r.ctx, r.cancel = context.WithCancel(ctx)
		if err := r.recoverInterrupted(r.ctx); err != nil {
			startErr = fmt.Errorf("recover interrupted jobs: %w", err)
			return
		}
		if err := r.reconcile(r.ctx); err != nil {
			startErr = fmt.Errorf("startup reconcile: %w", err)
			return
		}
		if err := r.loadPending(r.ctx); err != nil {
			startErr = err
			return
		}
		r.dispatchWG.Add(1)
		go r.dispatch()
	})
	return startErr
}

// Stop cancels the runner and blocks until the dispatcher and all in-flight
// jobs have exited. Queued-but-not-started jobs stay pending in the DB and run
// on the next Start. Stop is idempotent.
func (r *Runner) Stop() {
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
	})
	r.dispatchWG.Wait()
	r.jobsWG.Wait()
}

// Enqueue creates a pending job row and schedules it. The payload is stored as
// JSON text in jobs.payload (a struct, a pre-encoded JSON string/[]byte, or nil
// for an empty object). Returns the new job id. Enqueue never blocks on the
// dispatcher and is safe for concurrent use; if called after Stop the row is
// persisted as pending and runs on the next Start.
func (r *Runner) Enqueue(ctx context.Context, kind string, payload any) (int64, error) {
	body, err := marshalPayload(payload)
	if err != nil {
		return 0, err
	}
	job, err := r.store.CreateJob(ctx, queries.CreateJobParams{
		Kind:      kind,
		Status:    statusPending,
		Payload:   body,
		OwnerID:   defaultOwnerID,
		CreatedAt: r.now().Unix(),
	})
	if err != nil {
		return 0, fmt.Errorf("create job: %w", err)
	}
	r.enqueueID(job.ID)
	return job.ID, nil
}

func (r *Runner) EnqueueExisting(jobID int64) {
	r.enqueueID(jobID)
}

// Cancel cancels a job currently running under this runner. Returns false if
// the job is not running (unknown, pending, or already finished).
func (r *Runner) Cancel(jobID int64) bool {
	r.cancelMu.Lock()
	cancel, ok := r.cancels[jobID]
	r.cancelMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func (r *Runner) enqueueID(jobID int64) {
	r.queueMu.Lock()
	for _, queuedID := range r.queue {
		if queuedID == jobID {
			r.queueMu.Unlock()
			return
		}
	}
	r.queue = append(r.queue, jobID)
	r.queueMu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *Runner) popID() (int64, bool) {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if len(r.queue) == 0 {
		return 0, false
	}
	id := r.queue[0]
	r.queue = r.queue[1:]
	return id, true
}

// dispatch is the single goroutine that pulls queued job ids in FIFO order and
// launches a worker per job, bounded by the semaphore.
func (r *Runner) dispatch() {
	defer r.dispatchWG.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		default:
		}

		id, ok := r.popID()
		if !ok {
			select {
			case <-r.ctx.Done():
				return
			case <-r.notify:
			}
			continue
		}

		select {
		case r.sem <- struct{}{}:
		case <-r.ctx.Done():
			return
		}
		// ctx may have been canceled while we waited for a slot; if so, leave
		// the job pending rather than spinning up a worker during shutdown.
		select {
		case <-r.ctx.Done():
			<-r.sem
			return
		default:
		}

		r.jobsWG.Add(1)
		go func(jobID int64) {
			defer r.jobsWG.Done()
			defer func() { <-r.sem }()
			r.runJob(jobID)
		}(id)
	}
}

func (r *Runner) runJob(jobID int64) {
	jobCtx, cancel := context.WithCancel(r.ctx)
	defer cancel()

	// Already shutting down before this job started: leave it pending so the
	// next Start re-enqueues it.
	select {
	case <-jobCtx.Done():
		return
	default:
	}

	// Status writes must survive ctx cancellation so a canceled or shutdown
	// job is recorded as finished rather than left dangling as running.
	writeCtx := context.WithoutCancel(jobCtx)

	job, err := r.store.ClaimPendingJob(writeCtx, queries.ClaimPendingJobParams{
		StartedAt: sql.NullInt64{Int64: r.now().Unix(), Valid: true},
		ID:        jobID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		r.logger.Error("job claim failed", "job_id", jobID, "error", err)
		return
	}
	r.registerCancel(jobID, cancel)
	defer r.unregisterCancel(jobID)

	if err := r.writeInitialProgress(writeCtx, jobID); err != nil {
		r.logger.Warn("job initial progress write failed", "job_id", jobID, "error", err)
	}

	runErr := r.execute(jobCtx, job)

	status := statusDone
	var jobErr sql.NullString
	if runErr != nil {
		status = statusFailed
		jobErr = sql.NullString{String: runErr.Error(), Valid: true}
	}
	if err := r.store.FinishJob(writeCtx, queries.FinishJobParams{
		Status:     status,
		Error:      jobErr,
		FinishedAt: sql.NullInt64{Int64: r.now().Unix(), Valid: true},
		ID:         jobID,
	}); err != nil {
		r.logger.Error("job finish write failed", "job_id", jobID, "error", err)
		return
	}
	r.logger.Info("job finished", "job_id", jobID, "kind", job.Kind, "status", status)
	r.metrics.IncJob(job.Kind, status)
}

func (r *Runner) execute(ctx context.Context, job queries.Job) error {
	handler, ok := r.handlers[job.Kind]
	if !ok {
		return fmt.Errorf("no handler registered for job kind %q", job.Kind)
	}
	return handler(ctx, job)
}

func (r *Runner) registerCancel(jobID int64, cancel context.CancelFunc) {
	r.cancelMu.Lock()
	r.cancels[jobID] = cancel
	r.cancelMu.Unlock()
}

func (r *Runner) unregisterCancel(jobID int64) {
	r.cancelMu.Lock()
	delete(r.cancels, jobID)
	r.cancelMu.Unlock()
}

func marshalPayload(payload any) (string, error) {
	switch v := payload.(type) {
	case nil:
		return "{}", nil
	case string:
		if v == "" {
			return "{}", nil
		}
		return v, nil
	case []byte:
		if len(v) == 0 {
			return "{}", nil
		}
		return string(v), nil
	case json.RawMessage:
		if len(v) == 0 {
			return "{}", nil
		}
		return string(v), nil
	default:
		body, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal job payload: %w", err)
		}
		return string(body), nil
	}
}
