package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func libraryDeps(st *store.Store, base string) APIDeps {
	return APIDeps{Store: st, Config: APIConfig{EchoOutputBasePath: base}, Logger: apiLogger(), Now: apiClock()}
}

type libraryJSON struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	EchoOutputKind string `json:"echo_output_kind"`
	EchoOutputPath string `json:"echo_output_path"`
}

func TestCreateLibraryHappyPath(t *testing.T) {
	base := t.TempDir()
	libDir := filepath.Join(base, "media")
	if err := os.Mkdir(libDir, 0o750); err != nil {
		t.Fatal(err)
	}
	st := newAPIStore(t)
	deps := libraryDeps(st, base)

	body := `{"name":"media","echo_output_path":"` + libDir + `"}`
	rec := doReq(t, http.MethodPost, "/api/libraries", body, func(r chi.Router) {
		r.Post("/api/libraries", deps.CreateLibrary)
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got libraryJSON
	decodeBody(t, rec, &got)
	if got.EchoOutputKind != "local" || got.Name != "media" {
		t.Fatalf("resp = %+v, want kind=local name=media", got)
	}
	lib, err := st.GetLibrary(context.Background(), queries.GetLibraryParams{ID: got.ID})
	if err != nil {
		t.Fatalf("library not persisted: %v", err)
	}
	if !sameDir(t, lib.EchoOutputPath, libDir) {
		t.Fatalf("stored echo_output_path %q != %q", lib.EchoOutputPath, libDir)
	}
}

func TestCreateLibraryRejectsBadPaths(t *testing.T) {
	base := t.TempDir()
	existingDir := filepath.Join(base, "ok")
	if err := os.Mkdir(existingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	regularFile := filepath.Join(base, "file")
	if err := os.WriteFile(regularFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newAPIStore(t)
	deps := libraryDeps(st, base)
	reg := func(r chi.Router) { r.Post("/api/libraries", deps.CreateLibrary) }

	cases := map[string]string{
		"relative path":      `{"name":"x","echo_output_path":"relative/dir"}`,
		"outside base":       `{"name":"x","echo_output_path":"/etc"}`,
		"nonexistent dir":    `{"name":"x","echo_output_path":"` + filepath.Join(base, "missing") + `"}`,
		"path is a file":     `{"name":"x","echo_output_path":"` + regularFile + `"}`,
		"missing name":       `{"echo_output_path":"` + existingDir + `"}`,
		"missing out path":   `{"name":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := doReq(t, http.MethodPost, "/api/libraries", body, reg)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestListLibraries(t *testing.T) {
	st := newAPIStore(t)
	deps := libraryDeps(st, t.TempDir())
	createLibraryRow(t, st)
	rec := doReq(t, http.MethodGet, "/api/libraries", "", func(r chi.Router) {
		r.Get("/api/libraries", deps.ListLibraries)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []libraryJSON
	decodeBody(t, rec, &got)
	if len(got) != 1 {
		t.Fatalf("libraries = %+v, want 1", got)
	}
}

func TestDeleteLibraryHappyAndMissing(t *testing.T) {
	st := newAPIStore(t)
	deps := libraryDeps(st, t.TempDir())
	lib := createLibraryRow(t, st)
	reg := func(r chi.Router) { r.Delete("/api/libraries/{id}", deps.DeleteLibrary) }

	if rec := doReq(t, http.MethodDelete, "/api/libraries/"+itoa(lib.ID), "", reg); rec.Code != http.StatusNoContent {
		t.Fatalf("delete existing = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, http.MethodDelete, "/api/libraries/99999", "", reg); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", rec.Code)
	}
	if rec := doReq(t, http.MethodDelete, "/api/libraries/abc", "", reg); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete bad id = %d, want 400", rec.Code)
	}
}

func createLibraryRow(t *testing.T, st *store.Store) queries.Library {
	t.Helper()
	lib, err := st.CreateLibrary(context.Background(), queries.CreateLibraryParams{
		Name: "media", EchoOutputKind: "local", EchoOutputPath: "/tmp/x", OwnerID: "admin", CreatedAt: 1,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	return lib
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}
