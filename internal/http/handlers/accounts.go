package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store/queries"
)

type accountResponse struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	SidecarID    string `json:"sidecar_id"`
	StorageMount string `json:"storage_mount"`
	Status       string `json:"status"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

func toAccountResponse(a queries.Account) accountResponse {
	return accountResponse{
		ID:           a.ID,
		Provider:     a.Provider,
		SidecarID:    a.SidecarID,
		StorageMount: a.StorageMount,
		Status:       a.Status,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// ListAccounts serves GET /api/accounts.
func (d APIDeps) ListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := d.Store.ListAccounts(r.Context())
	if err != nil {
		d.logger().Error("accounts: list", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	out := make([]accountResponse, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, toAccountResponse(a))
	}
	writeJSON(w, http.StatusOK, out)
}

type createAccountRequest struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	SidecarID    string `json:"sidecar_id"`
	StorageMount string `json:"storage_mount"`
}

// CreateAccount serves POST /api/accounts. It binds an Echo account to a storage
// that must already exist on the sidecar (spec §6 — Echo holds only a reference;
// real credentials live on the sidecar).
func (d APIDeps) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Provider = strings.TrimSpace(req.Provider)
	req.SidecarID = strings.TrimSpace(req.SidecarID)
	req.StorageMount = strings.TrimSpace(req.StorageMount)
	if req.ID == "" || req.Provider == "" || req.SidecarID == "" || req.StorageMount == "" {
		writeAPIError(w, http.StatusBadRequest, "id, provider, sidecar_id, storage_mount are required")
		return
	}

	storages, err := d.Sidecar.ListStorages(r.Context())
	if err != nil {
		if errors.Is(err, sidecarclient.ErrSidecarUnreachable) {
			writeAPIError(w, http.StatusServiceUnavailable, "sidecar-unreachable")
			return
		}
		d.logger().Error("accounts: list storages", "err", err)
		writeAPIError(w, http.StatusBadGateway, "sidecar-error")
		return
	}
	if !storageExists(storages, req.StorageMount) {
		writeAPIError(w, http.StatusBadRequest, "storage_mount not found on sidecar")
		return
	}

	// Reject a duplicate id up front for a clear 409; the UNIQUE(sidecar_id,
	// storage_mount) constraint is enforced by the insert below.
	if _, err := d.Store.GetAccount(r.Context(), queries.GetAccountParams{ID: req.ID}); err == nil {
		writeAPIError(w, http.StatusConflict, "account already exists")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		d.logger().Error("accounts: lookup", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}

	now := d.now().Unix()
	if err := d.Store.CreateAccount(r.Context(), queries.CreateAccountParams{
		ID:           req.ID,
		Provider:     req.Provider,
		SidecarID:    req.SidecarID,
		StorageMount: req.StorageMount,
		Status:       "ok",
		LastCheck:    sql.NullInt64{Int64: now, Valid: true},
		OwnerID:      "admin",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		if isUniqueViolation(err) {
			writeAPIError(w, http.StatusConflict, "account id or storage binding already exists")
			return
		}
		d.logger().Error("accounts: create", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}

	created, err := d.Store.GetAccount(r.Context(), queries.GetAccountParams{ID: req.ID})
	if err != nil {
		d.logger().Error("accounts: reload", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	writeJSON(w, http.StatusCreated, toAccountResponse(created))
}

// DeleteAccount serves DELETE /api/accounts/{id}. An account still referenced by
// file_copies cannot be removed (409) — those copies must be reconciled first.
func (d APIDeps) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := d.Store.GetAccount(r.Context(), queries.GetAccountParams{ID: id}); errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "account not found")
		return
	} else if err != nil {
		d.logger().Error("accounts: lookup for delete", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if err := d.Store.DeleteAccount(r.Context(), queries.DeleteAccountParams{ID: id}); err != nil {
		if isForeignKeyViolation(err) {
			writeAPIError(w, http.StatusConflict, "account still has file copies")
			return
		}
		d.logger().Error("accounts: delete", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func storageExists(storages []sidecarclient.Storage, mount string) bool {
	for _, s := range storages {
		if s.MountPath == mount {
			return true
		}
	}
	return false
}

// isUniqueViolation / isForeignKeyViolation classify modernc.org/sqlite constraint
// errors by message (the driver wraps them as "... UNIQUE constraint failed ..." /
// "... FOREIGN KEY constraint failed ..."). Used to map DB constraints to 409.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key constraint")
}
