package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func seedConflict(t *testing.T, st *store.Store, reason, status string) queries.HashConflict {
	t.Helper()
	ctx := context.Background()
	a, err := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 1, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 2, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.InsertHashConflict(ctx, queries.InsertHashConflictParams{
		BlobIDA: a.ID, BlobIDB: b.ID, Reason: reason, Detail: `{"k":"v"}`, ObservedAt: 5, Status: status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestListConflictsOnlyOpen(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Logger: apiLogger(), Now: apiClock()}
	seedConflict(t, st, "hash_multi_blob", "open")
	seedConflict(t, st, "copy_blob_mismatch", "dismissed")

	rec := doReq(t, http.MethodGet, "/api/conflicts", "", func(r chi.Router) { r.Get("/api/conflicts", deps.ListConflicts) })
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got []struct {
		Reason string `json:"reason"`
		Status string `json:"status"`
	}
	decodeBody(t, rec, &got)
	if len(got) != 1 || got[0].Reason != "hash_multi_blob" {
		t.Fatalf("conflicts = %+v, want only the open one", got)
	}
}

func TestDismissConflict(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Logger: apiLogger(), Now: apiClock()}
	c := seedConflict(t, st, "hash_multi_blob", "open")

	rec := doReq(t, http.MethodPost, "/api/conflicts/"+itoa(c.ID)+"/dismiss", "", func(r chi.Router) {
		r.Post("/api/conflicts/{id}/dismiss", deps.DismissConflict)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := st.GetHashConflict(context.Background(), queries.GetHashConflictParams{ID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "dismissed" {
		t.Fatalf("status = %q, want dismissed", got.Status)
	}
}

func TestDismissConflictNotFoundAndBadID(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Logger: apiLogger(), Now: apiClock()}
	reg := func(r chi.Router) { r.Post("/api/conflicts/{id}/dismiss", deps.DismissConflict) }

	if rec := doReq(t, http.MethodPost, "/api/conflicts/99999/dismiss", "", reg); rec.Code != http.StatusNotFound {
		t.Fatalf("missing conflict = %d, want 404", rec.Code)
	}
	if rec := doReq(t, http.MethodPost, "/api/conflicts/abc/dismiss", "", reg); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d, want 400", rec.Code)
	}
}
