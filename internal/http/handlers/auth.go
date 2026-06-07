package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store/queries"
)

func (d APIDeps) MountBootstrap(r chi.Router) {
	r.Post("/api/bootstrap/admin-token", d.CreateBootstrapAdminToken)
	r.Post("/api/bootstrap/admin-password", d.SetBootstrapAdminPassword)
}

type createBootstrapAdminTokenRequest struct {
	Name      string `json:"name"`
	ExpiresAt *int64 `json:"expires_at"`
}

func (d APIDeps) CreateBootstrapAdminToken(w http.ResponseWriter, r *http.Request) {
	if d.BootstrapAdminToken == "" || !validBearer(r.Header.Get("Authorization"), d.BootstrapAdminToken) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req createBootstrapAdminTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid name")
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		d.logger().Error("bootstrap token: generate", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	id, err := newID("tok")
	if err != nil {
		d.logger().Error("bootstrap token: id", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	expires := sql.NullInt64{}
	if req.ExpiresAt != nil {
		expires = sql.NullInt64{Int64: *req.ExpiresAt, Valid: true}
	}

	if err := d.Store.CreateAPIToken(r.Context(), queries.CreateAPITokenParams{
		ID:        id,
		UserID:    "admin",
		Name:      name,
		TokenHash: auth.HashToken(token),
		Scopes:    `["admin"]`,
		ExpiresAt: expires,
		CreatedAt: d.now().Unix(),
	}); err != nil {
		d.logger().Error("bootstrap token: create", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "token": token})
}

func validBearer(header, expected string) bool {
	got := auth.BearerToken(header)
	if got == "" || expected == "" {
		return false
	}
	g := sha256.Sum256([]byte(got))
	e := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(g[:], e[:]) == 1
}

func newID(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}
