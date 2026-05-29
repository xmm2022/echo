//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildMockProducer compiles testdata/mock115share2cas into a temp binary and
// returns its path. The mock has no build tag and only imports the stdlib, so the
// build is fast and offline.
func buildMockProducer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mock115share2cas")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/mock115share2cas")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mock producer: %v\n%s", err, out)
	}
	return bin
}

// TestProducerIngestEndToEnd runs a producer ingest: the mock 115share2cas emits a
// CAS tree + manifest, exits 0, and the job funnels into the same manual chain
// (PutCAS -> commit -> .echo). It also confirms the share_url secret is redacted
// in both the recorded cmdline and the exposed job payload (spec §6/§8).
func TestProducerIngestEndToEnd(t *testing.T) {
	bin := buildMockProducer(t)
	env := newEnv(t, envConfig{producerBinary: bin})

	account := env.createAccount("acc-115")
	lib := env.createLibrary("producer-lib")

	var out struct {
		JobID int64 `json:"job_id"`
	}
	env.doJSON(http.MethodPost, "/api/ingest/producer", map[string]any{
		"library_id":     lib.ID,
		"target_account": account.ID,
		"target_subdir":  "imports",
		"tool":           "115share2cas",
		"args": map[string]any{
			"share_url": "https://115.com/s/abcdef?password=topsecret",
			"mode":      "direct",
		},
	}, http.StatusAccepted, &out)

	jr := env.waitForJob(out.JobID, defaultJobTimeout)
	if jr.Status != "done" {
		t.Fatalf("producer job status = %q (error %q), want done", jr.Status, jr.Error)
	}

	// Producer run recorded, successful exit, secret redacted in the cmdline.
	if len(jr.ProducerRuns) != 1 {
		t.Fatalf("got %d producer runs, want 1: %+v", len(jr.ProducerRuns), jr.ProducerRuns)
	}
	pr := jr.ProducerRuns[0]
	if pr.Tool != "115share2cas" {
		t.Errorf("producer run tool = %q, want 115share2cas", pr.Tool)
	}
	if pr.ExitCode == nil || *pr.ExitCode != 0 {
		t.Errorf("producer run exit_code = %v, want 0", pr.ExitCode)
	}
	if strings.Contains(pr.Cmdline, "topsecret") {
		t.Errorf("producer cmdline leaked share_url secret: %q", pr.Cmdline)
	}
	if strings.Contains(string(jr.Payload), "topsecret") {
		t.Errorf("job payload leaked share_url secret: %s", jr.Payload)
	}

	// Producer reuses the manual chain: the mock's single item is fully ingested.
	entries := env.listEntries(lib.ID)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.RelPath != "producer-movie.mkv" {
		t.Errorf("entry rel_path = %q, want producer-movie.mkv", entry.RelPath)
	}
	if !entry.EchoWritten || entry.LiveCopies != 1 {
		t.Errorf("entry echo_written=%v live_copies=%d, want true/1", entry.EchoWritten, entry.LiveCopies)
	}

	echoPath := filepath.Join(lib.EchoOutputPath, "producer-movie.mkv.echo")
	if _, err := os.Stat(echoPath); err != nil {
		t.Errorf("expected .echo at %s: %v", echoPath, err)
	}

	var link restoreResp
	env.doJSON(http.MethodGet, fmt.Sprintf("/api/restore/%d", entry.ID), nil, http.StatusOK, &link)
	if link.URL == "" {
		t.Error("producer restore url is empty")
	}
}
