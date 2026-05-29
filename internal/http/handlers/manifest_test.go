package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

type entryJSON struct {
	RelPath     string `json:"rel_path"`
	EchoWritten bool   `json:"echo_written"`
	LiveCopies  int64  `json:"live_copies"`
}

func seedLibraryEntries(t *testing.T, st *store.Store) int64 {
	t.Helper()
	ctx := context.Background()
	lib, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name: "media", EchoOutputKind: "local", EchoOutputPath: "/tmp/out", OwnerID: "admin", CreatedAt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID: "115-main", Provider: "115", SidecarID: "default", StorageMount: "/115-main",
		Status: "ok", OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	blobWith, err := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 1, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	blobNo, err := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 2, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	// blobWith gets two live copies + one dead.
	for i, st2 := range []struct{ path, status string }{
		{"/a.mkv", "live"}, {"/b.mkv", "live"}, {"/c.mkv", "dead"},
	} {
		_ = i
		if _, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
			BlobID: blobWith.ID, Provider: "115", AccountID: "115-main", SidecarID: "default",
			StorageMount: "/115-main", RemotePath: st2.path, Status: st2.status, LastSeen: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	entries := []struct {
		rel    string
		blobID int64
		echo   int64
	}{
		{"season/ep1.mkv", blobWith.ID, 1},
		{"season/ep2.mkv", blobNo.ID, 0},
		{"extras/clip.mkv", blobWith.ID, 1},
		// underscore must be matched literally, not as a LIKE wildcard:
		{"a_b.mkv", blobNo.ID, 0},
		{"axb.mkv", blobNo.ID, 0},
	}
	for _, e := range entries {
		if _, err := st.UpsertLibraryEntry(ctx, queries.UpsertLibraryEntryParams{
			LibraryID: lib.ID, RelPath: e.rel, Name: e.rel, BlobID: e.blobID,
			EchoWritten: e.echo, CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return lib.ID
}

func TestListLibraryEntriesAllAndPrefix(t *testing.T) {
	st := newAPIStore(t)
	libID := seedLibraryEntries(t, st)
	deps := APIDeps{Store: st, Logger: apiLogger(), Now: apiClock()}
	reg := func(r chi.Router) { r.Get("/api/libraries/{id}/entries", deps.ListLibraryEntries) }

	// All entries.
	rec := doReq(t, http.MethodGet, "/api/libraries/"+itoa(libID)+"/entries", "", reg)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var all []entryJSON
	decodeBody(t, rec, &all)
	if len(all) != 5 {
		t.Fatalf("all entries = %d, want 5", len(all))
	}

	// Prefix season/.
	rec = doReq(t, http.MethodGet, "/api/libraries/"+itoa(libID)+"/entries?prefix=season/", "", reg)
	var seasons []entryJSON
	decodeBody(t, rec, &seasons)
	if len(seasons) != 2 {
		t.Fatalf("season entries = %d, want 2", len(seasons))
	}
	if seasons[0].RelPath != "season/ep1.mkv" || !seasons[0].EchoWritten || seasons[0].LiveCopies != 2 {
		t.Fatalf("ep1 = %+v, want echo_written=true live_copies=2", seasons[0])
	}
	if seasons[1].EchoWritten || seasons[1].LiveCopies != 0 {
		t.Fatalf("ep2 = %+v, want echo_written=false live_copies=0", seasons[1])
	}
}

func TestListLibraryEntriesUnderscoreIsLiteral(t *testing.T) {
	st := newAPIStore(t)
	libID := seedLibraryEntries(t, st)
	deps := APIDeps{Store: st, Logger: apiLogger(), Now: apiClock()}
	reg := func(r chi.Router) { r.Get("/api/libraries/{id}/entries", deps.ListLibraryEntries) }

	rec := doReq(t, http.MethodGet, "/api/libraries/"+itoa(libID)+"/entries?prefix=a_b", "", reg)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []entryJSON
	decodeBody(t, rec, &got)
	if len(got) != 1 || got[0].RelPath != "a_b.mkv" {
		t.Fatalf("prefix a_b matched %+v, want only a_b.mkv (underscore is literal, not a wildcard)", got)
	}
}

func TestListLibraryEntriesLibraryNotFound(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Logger: apiLogger(), Now: apiClock()}
	reg := func(r chi.Router) { r.Get("/api/libraries/{id}/entries", deps.ListLibraryEntries) }

	if rec := doReq(t, http.MethodGet, "/api/libraries/99999/entries", "", reg); rec.Code != http.StatusNotFound {
		t.Fatalf("missing library = %d, want 404", rec.Code)
	}
	if rec := doReq(t, http.MethodGet, "/api/libraries/abc/entries", "", reg); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d, want 400", rec.Code)
	}
}
