//go:build integration

package integration

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmm2022/echo/internal/castree"
)

// TestManualImportEndToEnd drives a manual ingest through the real HTTP API
// against a fake sidecar and real SQLite, then exercises both restore endpoints:
// manifest -> PutCAS -> two-phase commit -> .echo write -> restore JSON + stream.
func TestManualImportEndToEnd(t *testing.T) {
	env := newEnv(t, envConfig{})

	account := env.createAccount("acc-115")
	lib := env.createLibrary("movies")

	content := []byte("the quick brown fox jumps over the lazy dog")
	const relPath = "Film/movie.mkv"
	casTree, manifest := env.writeCASTree("batch-1", []casFixture{{relPath: relPath, content: content}})

	jobID := env.submitManualIngest(lib.ID, account.ID, "imports", casTree, manifest)
	jr := env.waitForJob(jobID, defaultJobTimeout)
	if jr.Status != "done" {
		t.Fatalf("job status = %q (error: %q), want done", jr.Status, jr.Error)
	}

	// DB / API state: one entry, .echo written, exactly one live copy.
	entries := env.listEntries(lib.ID)
	if len(entries) != 1 {
		t.Fatalf("got %d library entries, want 1: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.RelPath != relPath {
		t.Errorf("entry rel_path = %q, want %q", entry.RelPath, relPath)
	}
	if !entry.EchoWritten {
		t.Errorf("entry echo_written = false, want true")
	}
	if entry.LiveCopies != 1 {
		t.Errorf("entry live_copies = %d, want 1", entry.LiveCopies)
	}
	if entry.BlobID <= 0 {
		t.Errorf("entry blob_id = %d, want > 0", entry.BlobID)
	}

	// The fake sidecar received exactly one PutCAS, for a .cas File-Path.
	if got := env.fake.PutCASCount(); got != 1 {
		t.Errorf("fake PutCAS count = %d, want 1", got)
	}
	if fp := env.fake.LastPutCASRequest().FilePath; filepath.Ext(fp) != ".cas" {
		t.Errorf("fake last PutCAS File-Path = %q, want a .cas path", fp)
	}

	// The .echo placeholder exists on disk at <echo_output_path>/<rel_path>.echo and
	// decodes to the manifest payload (base64 JSON, sha1 preserved).
	echoPath := filepath.Join(lib.EchoOutputPath, filepath.FromSlash(relPath)+".echo")
	raw, err := os.ReadFile(echoPath)
	if err != nil {
		t.Fatalf("read .echo file %s: %v", echoPath, err)
	}
	payload, err := castree.Decode(raw)
	if err != nil {
		t.Fatalf("decode .echo payload: %v", err)
	}
	wantSum := sha1.Sum(content)
	if payload.SHA1 != hex.EncodeToString(wantSum[:]) {
		t.Errorf(".echo sha1 = %q, want %q", payload.SHA1, hex.EncodeToString(wantSum[:]))
	}
	if payload.Size != int64(len(content)) {
		t.Errorf(".echo size = %d, want %d", payload.Size, len(content))
	}
	if payload.Name != "movie.mkv" {
		t.Errorf(".echo name = %q, want movie.mkv", payload.Name)
	}

	// Restore JSON: a sidecar direct link with headers.
	var link restoreResp
	env.doJSON(http.MethodGet, fmt.Sprintf("/api/restore/%d", entry.ID), nil, http.StatusOK, &link)
	if link.URL == "" {
		t.Error("restore url is empty")
	}
	if link.Headers["X-Download-Token"] == "" {
		t.Errorf("restore headers missing X-Download-Token: %+v", link.Headers)
	}
	if link.CopyID <= 0 {
		t.Errorf("restore copy_id = %d, want > 0", link.CopyID)
	}

	// Restore stream: Echo proxies the sidecar byte stream verbatim.
	t.Run("stream full", func(t *testing.T) {
		resp := env.do(http.MethodGet, fmt.Sprintf("/api/stream/%d", entry.ID), nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "0123456789" {
			t.Errorf("stream body = %q, want 0123456789", body)
		}
	})

	t.Run("stream range", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, env.baseURL+fmt.Sprintf("/api/stream/%d", entry.ID), nil)
		req.Header.Set("Authorization", "Bearer "+env.adminToken)
		req.Header.Set("Range", "bytes=2-5")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("range request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("range stream status = %d, want 206", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "2345" {
			t.Errorf("range body = %q, want 2345", body)
		}
		if cr := resp.Header.Get("Content-Range"); cr == "" {
			t.Error("range response missing Content-Range header")
		}
	})
}

// TestRestoreUnknownFileID confirms a missing file_id is a clean 404, not a panic
// or 500, through the full stack.
func TestRestoreUnknownFileID(t *testing.T) {
	env := newEnv(t, envConfig{})
	resp := env.do(http.MethodGet, "/api/restore/999999", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("restore unknown file_id status = %d, want 404", resp.StatusCode)
	}
}
