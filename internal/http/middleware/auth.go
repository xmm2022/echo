// Package middleware holds Echo's HTTP middleware. v0.1 has a single static admin
// bearer token (spec §6); finer-grained auth is a later version.
package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// Auth returns middleware enforcing the static admin bearer token. Requests must
// carry `Authorization: Bearer <token>` (scheme is case-insensitive per RFC 7235).
// A missing, malformed, or mismatched token yields 401 with a WWW-Authenticate
// challenge. An empty configured token fails closed — every request is rejected —
// so a misconfigured deployment never silently runs without auth.
func Auth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" || !validBearer(r.Header.Get("Authorization"), token) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="echo"`)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func validBearer(header, token string) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
