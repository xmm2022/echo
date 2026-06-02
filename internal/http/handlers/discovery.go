package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (d APIDeps) MountDiscovery(r chi.Router) {
	r.Get("/api/discovery/subscriptions", d.ListDiscoverySubscriptions)
}

func (d APIDeps) ListDiscoverySubscriptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}
