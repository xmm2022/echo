package embyproxy

import (
	"encoding/json"
	"net/http"
)

// PlaybackInfoFailClosedHandler is the Phase 3 stand-in for the PlaybackInfo rewrite
// that lands in a later task. Until Echo can safely rewrite MediaSources to its own
// stream tokens, it MUST NOT proxy PlaybackInfo to upstream Emby (which would hand the
// client a direct, untokenized source URL). It fails closed with 503 so the player
// retries rather than bypassing Echo.
func PlaybackInfoFailClosedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Echo-Reason", "temporary_unavailable")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "temporary_unavailable"})
	})
}

// BadReservedHandler answers malformed requests to Echo's reserved /stream and /error
// namespaces (missing or multi-segment token). It returns 404 rather than falling
// through to the upstream proxy, so a probe of the reserved namespace can never reach
// Emby. It leaks no detail beyond a generic not_found.
func BadReservedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Echo-Reason", "temporary_unavailable")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not_found"})
	})
}
