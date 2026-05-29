//go:build integration

package integration

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient/fakesidecar"
)

// TestMultiJobConcurrency submits many ingest jobs at once and asserts the spec §7
// concurrency guarantees: the runner never exceeds max_concurrent × worker_per_job
// simultaneous PutCAS calls at the sidecar, each job's output is isolated to its
// own library, and every job completes. The fake sidecar holds each PutCAS briefly
// (PutCASDelay) so concurrent calls genuinely overlap and the high-water mark is
// observable.
func TestMultiJobConcurrency(t *testing.T) {
	const (
		numJobs      = 10
		itemsPerJob  = 3
		maxConcurrent = 2
		workerPerJob  = 3
	)
	limit := maxConcurrent * workerPerJob // 6

	env := newEnv(t, envConfig{
		maxConcurrent: maxConcurrent,
		workerPerJob:  workerPerJob,
		fakeOpts:      fakesidecar.Options{PutCASDelay: 40 * time.Millisecond},
	})
	account := env.createAccount("acc-115")

	type jobInfo struct {
		libID    int64
		relPaths []string
	}
	infos := make([]jobInfo, numJobs)
	jobIDs := make([]int64, numJobs)

	// Submit all jobs up front so they contend for the runner's slots.
	for j := 0; j < numJobs; j++ {
		lib := env.createLibrary(fmt.Sprintf("lib-%02d", j))
		items := make([]casFixture, itemsPerJob)
		rels := make([]string, itemsPerJob)
		for i := 0; i < itemsPerJob; i++ {
			rel := fmt.Sprintf("media/clip-%02d-%02d.mkv", j, i)
			rels[i] = rel
			// Globally-unique content => unique sha1 => distinct blobs, so jobs do
			// not collide on hashes and isolation is unambiguous.
			items[i] = casFixture{relPath: rel, content: []byte(fmt.Sprintf("payload-job-%02d-item-%02d", j, i))}
		}
		casTree, manifest := env.writeCASTree(fmt.Sprintf("batch-%02d", j), items)
		infos[j] = jobInfo{libID: lib.ID, relPaths: rels}
		jobIDs[j] = env.submitManualIngest(lib.ID, account.ID, "imports", casTree, manifest)
	}

	// Sample the running-job count while the batch drains so we can assert the
	// runner's job-level cap (plan §12: 任意时刻 running ≤ max_concurrent). The
	// PutCAS cap alone does not prove this — 6 single-worker jobs would also reach
	// 6 — so this independently exercises the runner semaphore, observed over HTTP.
	var maxRunning int64
	sampleStop := make(chan struct{})
	var samplerDone sync.WaitGroup
	var stopOnce sync.Once
	stopSampler := func() {
		stopOnce.Do(func() { close(sampleStop) })
		samplerDone.Wait()
	}
	defer stopSampler()
	samplerDone.Add(1)
	go func() {
		defer samplerDone.Done()
		for {
			select {
			case <-sampleStop:
				return
			default:
			}
			if n := env.runningJobCount(); n >= 0 {
				for {
					cur := atomic.LoadInt64(&maxRunning)
					if int64(n) <= cur || atomic.CompareAndSwapInt64(&maxRunning, cur, int64(n)) {
						break
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// All jobs reach done.
	for j, id := range jobIDs {
		jr := env.waitForJob(id, defaultJobTimeout)
		if jr.Status != "done" {
			t.Fatalf("job %d (lib-%02d) status = %q (error %q), want done", id, j, jr.Status, jr.Error)
		}
	}
	stopSampler()

	// Job-level cap: never more than max_concurrent running at once, and the cap
	// is actually reached (no accidental serialization).
	maxRun := int(atomic.LoadInt64(&maxRunning))
	if maxRun > maxConcurrent {
		t.Errorf("max concurrent running jobs = %d, want <= %d (max_concurrent)", maxRun, maxConcurrent)
	}
	if maxRun < maxConcurrent {
		t.Errorf("max concurrent running jobs = %d, want to reach %d (cap not saturated / sampling missed)", maxRun, maxConcurrent)
	}
	t.Logf("observed max concurrent running jobs = %d (max_concurrent %d)", maxRun, maxConcurrent)

	// Sidecar-level cap: concurrent PutCAS ≤ max_concurrent × worker_per_job, with
	// parallelism beyond the job cap (proving in-job workers overlap on top of
	// cross-job concurrency).
	maxObserved := env.fake.MaxConcurrentPutCAS()
	if maxObserved > limit {
		t.Errorf("max concurrent PutCAS = %d, want <= %d (max_concurrent×worker_per_job)", maxObserved, limit)
	}
	if maxObserved < maxConcurrent+1 {
		t.Errorf("max concurrent PutCAS = %d, want >= %d (insufficient parallelism observed)", maxObserved, maxConcurrent+1)
	}
	if got, want := env.fake.PutCASCount(), numJobs*itemsPerJob; got != want {
		t.Errorf("total PutCAS calls = %d, want %d", got, want)
	}
	t.Logf("observed max concurrent PutCAS = %d (cap %d)", maxObserved, limit)

	// Data isolation: each library holds exactly its own job's entries, all live.
	for j, info := range infos {
		entries := env.listEntries(info.libID)
		if len(entries) != itemsPerJob {
			t.Errorf("lib-%02d: %d entries, want %d", j, len(entries), itemsPerJob)
			continue
		}
		want := make(map[string]bool, len(info.relPaths))
		for _, rel := range info.relPaths {
			want[rel] = true
		}
		for _, e := range entries {
			if !want[e.RelPath] {
				t.Errorf("lib-%02d: unexpected entry %q (cross-job contamination)", j, e.RelPath)
			}
			if !e.EchoWritten || e.LiveCopies != 1 {
				t.Errorf("lib-%02d entry %q: echo_written=%v live_copies=%d, want true/1", j, e.RelPath, e.EchoWritten, e.LiveCopies)
			}
		}
	}
}
