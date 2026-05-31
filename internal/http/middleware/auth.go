// Package middleware holds Echo's HTTP middleware. v0.1 has a single static admin
// bearer token (spec §6); finer-grained auth is a later version.
package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/xmm2022/echo/internal/auth"
)

type CheckFunc func(*http.Request) (auth.UserContext, bool)

// Auth returns middleware enforcing the static admin bearer token. Requests must
// carry `Authorization: Bearer <token>` (scheme is case-insensitive per RFC 7235).
// A missing, malformed, or mismatched token yields 401 with a WWW-Authenticate
// challenge. An empty configured token fails closed — every request is rejected —
// so a misconfigured deployment never silently runs without auth.
func Auth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" || !validBearer(r.Header.Get("Authorization"), token) {
				writeUnauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func AuthFunc(check CheckFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := check(r)
			if !ok {
				writeUnauthorized(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.NewContext(r.Context(), user)))
		})
	}
}

func validBearer(header, token string) bool {
	got := auth.BearerToken(header)
	if got == "" || token == "" {
		return false
	}
	// Compare fixed-length digests so a timing side-channel cannot leak the
	// configured token's length (ConstantTimeCompare returns early on a length
	// mismatch). Mirrors the bootstrap-token check in the handlers package.
	gotSum := sha256.Sum256([]byte(got))
	wantSum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="echo"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}
