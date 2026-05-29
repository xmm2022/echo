package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/pathsafe"
	"github.com/xmm2022/echo/internal/store/queries"
)

type ingestManualRequest struct {
	LibraryID     int64  `json:"library_id"`
	TargetAccount string `json:"target_account"`
	TargetSubdir  string `json:"target_subdir,omitempty"`
	CASTreePath   string `json:"cas_tree_path"`
	ManifestPath  string `json:"manifest_path"`
}

// IngestManual serves POST /api/ingest/manual. It validates the library/account
// references and confines cas_tree_path + manifest_path with pathsafe before
// enqueuing an ingest_manual job (spec §3 / plan R2). It never reads the manifest
// itself — that happens in the job (RunManual).
func (d APIDeps) IngestManual(w http.ResponseWriter, r *http.Request) {
	var req ingestManualRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !d.requireLibrary(w, r, req.LibraryID) {
		return
	}
	if !d.requireAccount(w, r, strings.TrimSpace(req.TargetAccount)) {
		return
	}
	casTreeIn := strings.TrimSpace(req.CASTreePath)
	manifestIn := strings.TrimSpace(req.ManifestPath)
	if casTreeIn == "" || manifestIn == "" {
		writeAPIError(w, http.StatusBadRequest, "cas_tree_path and manifest_path are required")
		return
	}

	casTree, ok := d.resolveCASTree(casTreeIn)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "cas_tree_path must be an existing directory under a configured import root")
		return
	}
	// manifest_path must resolve (existing, no symlink) under the evaluated cas tree
	// and be a regular file. ResolveExistingUnderRoot rejects symlinks and escapes;
	// the IsRegular check rejects directories.
	manifest, err := pathsafe.ResolveExistingUnderRoot(casTree, manifestIn)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "manifest_path must be an existing file under cas_tree_path")
		return
	}
	if info, err := os.Lstat(manifest); err != nil || !info.Mode().IsRegular() {
		writeAPIError(w, http.StatusBadRequest, "manifest_path must be a regular file")
		return
	}

	payload := job.IngestPayload{
		LibraryID:     req.LibraryID,
		TargetAccount: strings.TrimSpace(req.TargetAccount),
		TargetSubdir:  req.TargetSubdir,
		CASTreePath:   casTree,
		ManifestPath:  manifest,
	}
	d.enqueueIngest(w, r, job.KindIngestManual, payload)
}

type ingestProducerRequest struct {
	LibraryID     int64          `json:"library_id"`
	TargetAccount string         `json:"target_account"`
	TargetSubdir  string         `json:"target_subdir,omitempty"`
	Tool          string         `json:"tool"`
	Args          map[string]any `json:"args"`
}

// IngestProducer serves POST /api/ingest/producer. It validates library/account
// and runs the producer arg pre-flight (allowlist + combination + ref: form) so a
// bad request fails synchronously with 400. The secret files behind ref: are
// resolved later in the job (spec §6 / Phase 6).
func (d APIDeps) IngestProducer(w http.ResponseWriter, r *http.Request) {
	var req ingestProducerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !d.requireLibrary(w, r, req.LibraryID) {
		return
	}
	if !d.requireAccount(w, r, strings.TrimSpace(req.TargetAccount)) {
		return
	}
	if err := ingest.ValidateProducerRequest(strings.TrimSpace(req.Tool), req.Args, d.Config.Producer); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := job.IngestPayload{
		LibraryID:     req.LibraryID,
		TargetAccount: strings.TrimSpace(req.TargetAccount),
		TargetSubdir:  req.TargetSubdir,
		Tool:          strings.TrimSpace(req.Tool),
		Args:          req.Args,
	}
	d.enqueueIngest(w, r, job.KindIngestProducer, payload)
}

// enqueueIngest enqueues the job and writes 202 {job_id}.
func (d APIDeps) enqueueIngest(w http.ResponseWriter, r *http.Request, kind string, payload job.IngestPayload) {
	id, err := d.Jobs.Enqueue(r.Context(), kind, payload)
	if err != nil {
		d.logger().Error("ingest: enqueue", "kind", kind, "err", err)
		writeAPIError(w, http.StatusInternalServerError, "enqueue-failed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"job_id": id})
}

// requireLibrary writes 400 and returns false when the referenced library is
// absent. A missing body reference is a client error, not a 404 on a URL resource.
func (d APIDeps) requireLibrary(w http.ResponseWriter, r *http.Request, id int64) bool {
	_, err := d.Store.GetLibrary(r.Context(), queries.GetLibraryParams{ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusBadRequest, "library not found")
		return false
	}
	if err != nil {
		d.logger().Error("ingest: load library", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return false
	}
	return true
}

func (d APIDeps) requireAccount(w http.ResponseWriter, r *http.Request, id string) bool {
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "target_account is required")
		return false
	}
	_, err := d.Store.GetAccount(r.Context(), queries.GetAccountParams{ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusBadRequest, "target_account not found")
		return false
	}
	if err != nil {
		d.logger().Error("ingest: load account", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return false
	}
	return true
}

// resolveCASTree confines candidate under the producer workdir root or any manual
// import root (first match wins) and requires it to be an existing directory. It
// returns the symlink-evaluated absolute path.
func (d APIDeps) resolveCASTree(candidate string) (string, bool) {
	roots := make([]string, 0, 1+len(d.Config.ManualImportRoots))
	if d.Config.ProducerWorkdirRoot != "" {
		roots = append(roots, d.Config.ProducerWorkdirRoot)
	}
	roots = append(roots, d.Config.ManualImportRoots...)
	for _, root := range roots {
		evaluated, err := pathsafe.ResolveExistingUnderRoot(root, candidate)
		if err != nil {
			continue
		}
		if info, err := os.Lstat(evaluated); err != nil || !info.IsDir() {
			continue
		}
		return evaluated, true
	}
	return "", false
}
