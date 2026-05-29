package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmm2022/echo/internal/pathsafe"
	"github.com/xmm2022/echo/internal/store/queries"
)

type libraryResponse struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	EchoOutputKind string `json:"echo_output_kind"`
	EchoOutputPath string `json:"echo_output_path"`
	CreatedAt      int64  `json:"created_at"`
}

func toLibraryResponse(l queries.Library) libraryResponse {
	return libraryResponse{
		ID:             l.ID,
		Name:           l.Name,
		EchoOutputKind: l.EchoOutputKind,
		EchoOutputPath: l.EchoOutputPath,
		CreatedAt:      l.CreatedAt,
	}
}

// ListLibraries serves GET /api/libraries.
func (d APIDeps) ListLibraries(w http.ResponseWriter, r *http.Request) {
	libraries, err := d.Store.ListLibraries(r.Context())
	if err != nil {
		d.logger().Error("libraries: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	out := make([]libraryResponse, 0, len(libraries))
	for _, l := range libraries {
		out = append(out, toLibraryResponse(l))
	}
	writeJSON(w, http.StatusOK, out)
}

type createLibraryRequest struct {
	Name           string `json:"name"`
	EchoOutputPath string `json:"echo_output_path"`
}

// CreateLibrary serves POST /api/libraries. echo_output_path must be an absolute
// path that resolves (existing, no symlink, is a directory) under the configured
// echo_output_defaults.base_path (plan §10 / spec §3). v0.1 output kind is local.
func (d APIDeps) CreateLibrary(w http.ResponseWriter, r *http.Request) {
	var req createLibraryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	out := strings.TrimSpace(req.EchoOutputPath)
	if name == "" || out == "" {
		writeAPIError(w, http.StatusBadRequest, "name and echo_output_path are required")
		return
	}
	if !filepath.IsAbs(out) {
		writeAPIError(w, http.StatusBadRequest, "echo_output_path must be an absolute path")
		return
	}
	evaluated, err := pathsafe.ResolveExistingUnderRoot(d.Config.EchoOutputBasePath, out)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "echo_output_path must be an existing path under the configured base_path")
		return
	}
	info, err := os.Stat(evaluated)
	if err != nil || !info.IsDir() {
		writeAPIError(w, http.StatusBadRequest, "echo_output_path must be a directory")
		return
	}

	lib, err := d.Store.CreateLibrary(r.Context(), queries.CreateLibraryParams{
		Name:           name,
		EchoOutputKind: "local",
		EchoOutputPath: evaluated,
		OwnerID:        "admin",
		CreatedAt:      d.now().Unix(),
	})
	if err != nil {
		d.logger().Error("libraries: create", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusCreated, toLibraryResponse(lib))
}

// DeleteLibrary serves DELETE /api/libraries/{id}. library_entries cascade with
// the library (schema ON DELETE CASCADE).
func (d APIDeps) DeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInt64Param(w, r, "id")
	if !ok {
		return
	}
	if _, err := d.Store.GetLibrary(r.Context(), queries.GetLibraryParams{ID: id}); errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "library not found")
		return
	} else if err != nil {
		d.logger().Error("libraries: lookup for delete", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if err := d.Store.DeleteLibrary(r.Context(), queries.DeleteLibraryParams{ID: id}); err != nil {
		d.logger().Error("libraries: delete", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
