package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func accountsDeps(st *store.Store, sc StorageLister) APIDeps {
	return APIDeps{Store: st, Sidecar: sc, Logger: apiLogger(), Now: apiClock()}
}

func okStorageLister() fakeStorageLister {
	return fakeStorageLister{storages: []sidecarclient.Storage{
		{ID: "default", Provider: "115", MountPath: "/115-main", Status: "ok"},
	}}
}

type accountJSON struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	SidecarID    string `json:"sidecar_id"`
	StorageMount string `json:"storage_mount"`
	Status       string `json:"status"`
}

func TestCreateAccountHappyPath(t *testing.T) {
	st := newAPIStore(t)
	deps := accountsDeps(st, okStorageLister())

	body := `{"id":"115-main","provider":"115","sidecar_id":"default","storage_mount":"/115-main"}`
	rec := doReq(t, http.MethodPost, "/api/accounts", body, func(r chi.Router) {
		r.Post("/api/accounts", deps.CreateAccount)
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got accountJSON
	decodeBody(t, rec, &got)
	if got.ID != "115-main" || got.Status != "ok" {
		t.Fatalf("resp = %+v, want id=115-main status=ok", got)
	}
	acc, err := st.GetAccount(context.Background(), queries.GetAccountParams{ID: "115-main"})
	if err != nil {
		t.Fatalf("account not persisted: %v", err)
	}
	if acc.Provider != "115" || acc.StorageMount != "/115-main" {
		t.Fatalf("persisted = %+v", acc)
	}
}

func TestCreateAccountStorageNotOnSidecar(t *testing.T) {
	st := newAPIStore(t)
	deps := accountsDeps(st, okStorageLister())

	body := `{"id":"x","provider":"115","sidecar_id":"default","storage_mount":"/nope"}`
	rec := doReq(t, http.MethodPost, "/api/accounts", body, func(r chi.Router) {
		r.Post("/api/accounts", deps.CreateAccount)
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateAccountSidecarUnreachable(t *testing.T) {
	st := newAPIStore(t)
	deps := accountsDeps(st, fakeStorageLister{err: sidecarclient.ErrSidecarUnreachable})

	body := `{"id":"x","provider":"115","sidecar_id":"default","storage_mount":"/115-main"}`
	rec := doReq(t, http.MethodPost, "/api/accounts", body, func(r chi.Router) {
		r.Post("/api/accounts", deps.CreateAccount)
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateAccountMissingFields(t *testing.T) {
	st := newAPIStore(t)
	deps := accountsDeps(st, okStorageLister())

	rec := doReq(t, http.MethodPost, "/api/accounts", `{"id":"x"}`, func(r chi.Router) {
		r.Post("/api/accounts", deps.CreateAccount)
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateAccountInvalidJSON(t *testing.T) {
	st := newAPIStore(t)
	deps := accountsDeps(st, okStorageLister())

	rec := doReq(t, http.MethodPost, "/api/accounts", `{not json`, func(r chi.Router) {
		r.Post("/api/accounts", deps.CreateAccount)
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateAccountDuplicate(t *testing.T) {
	st := newAPIStore(t)
	deps := accountsDeps(st, okStorageLister())
	body := `{"id":"115-main","provider":"115","sidecar_id":"default","storage_mount":"/115-main"}`
	reg := func(r chi.Router) { r.Post("/api/accounts", deps.CreateAccount) }

	if rec := doReq(t, http.MethodPost, "/api/accounts", body, reg); rec.Code != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", rec.Code)
	}
	rec := doReq(t, http.MethodPost, "/api/accounts", body, reg)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListAccounts(t *testing.T) {
	st := newAPIStore(t)
	deps := accountsDeps(st, okStorageLister())
	if err := st.CreateAccount(context.Background(), queries.CreateAccountParams{
		ID: "a1", Provider: "115", SidecarID: "default", StorageMount: "/115-main",
		Status: "ok", OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, http.MethodGet, "/api/accounts", "", func(r chi.Router) {
		r.Get("/api/accounts", deps.ListAccounts)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []accountJSON
	decodeBody(t, rec, &got)
	if len(got) != 1 || got[0].ID != "a1" {
		t.Fatalf("accounts = %+v, want [a1]", got)
	}
}

func TestDeleteAccountHappyAndMissing(t *testing.T) {
	st := newAPIStore(t)
	deps := accountsDeps(st, okStorageLister())
	if err := st.CreateAccount(context.Background(), queries.CreateAccountParams{
		ID: "a1", Provider: "115", SidecarID: "default", StorageMount: "/115-main",
		Status: "ok", OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	reg := func(r chi.Router) { r.Delete("/api/accounts/{id}", deps.DeleteAccount) }

	if rec := doReq(t, http.MethodDelete, "/api/accounts/a1", "", reg); rec.Code != http.StatusNoContent {
		t.Fatalf("delete existing = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, http.MethodDelete, "/api/accounts/missing", "", reg); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", rec.Code)
	}
}

func TestDeleteAccountWithCopiesConflicts(t *testing.T) {
	ctx := context.Background()
	st := newAPIStore(t)
	deps := accountsDeps(st, okStorageLister())
	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID: "a1", Provider: "115", SidecarID: "default", StorageMount: "/115-main",
		Status: "ok", OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	blob, err := st.CreateBlob(ctx, queries.CreateBlobParams{Size: 1, OwnerID: "admin", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertFileCopy(ctx, queries.InsertFileCopyParams{
		BlobID: blob.ID, Provider: "115", AccountID: "a1", SidecarID: "default",
		StorageMount: "/115-main", RemotePath: "/x.mkv", Status: "live", LastSeen: 1,
	}); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, http.MethodDelete, "/api/accounts/a1", "", func(r chi.Router) {
		r.Delete("/api/accounts/{id}", deps.DeleteAccount)
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete account with copies = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}
