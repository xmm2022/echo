package embyproxy

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ErrorHandler serves Echo's reserved /error/{token} route: it resolves a short-lived
// error token to the safe failure reason + HTTP status the player should see.
//
// Deviation from the plan's ErrorHandler(mgr, events *playback.Events): there is no
// playback.Events type in this codebase, and the plan makes the playback_events audit
// write explicitly optional ("If writing playback_events…"). Phase 3 therefore takes
// only *SessionManager and writes NO audit event; the audit hook can be added later
// without changing this route's wire contract.
//
// The handler never consults the resolver, sidecar, or quota and never requires an
// upstream Emby cookie/token: an error token is self-contained.
func ErrorHandler(mgr *SessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		row, err := mgr.LookupErrorToken(r.Context(), token)
		if err != nil {
			// Do NOT distinguish invalid/expired/missing to the client: all collapse
			// to a generic 404 so the error namespace cannot be probed.
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Echo-Reason", "temporary_unavailable")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "temporary_unavailable"})
			return
		}
		// row.Reason is a safe enum chosen at mint time; row.HttpStatus is constrained
		// to 4xx/5xx by CreateErrorToken, so echoing both is safe.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Echo-Reason", row.Reason)
		w.WriteHeader(int(row.HttpStatus))
		_ = json.NewEncoder(w).Encode(map[string]string{"error": row.Reason})
	})
}
