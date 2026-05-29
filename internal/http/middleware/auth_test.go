package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthBearer(t *testing.T) {
	const token = "s3cret-admin-token"
	tests := []struct {
		name       string
		token      string
		authHeader string
		wantStatus int
	}{
		{name: "valid token", token: token, authHeader: "Bearer " + token, wantStatus: http.StatusOK},
		{name: "lowercase scheme", token: token, authHeader: "bearer " + token, wantStatus: http.StatusOK},
		{name: "missing header", token: token, authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "wrong token", token: token, authHeader: "Bearer nope", wantStatus: http.StatusUnauthorized},
		{name: "no bearer prefix", token: token, authHeader: token, wantStatus: http.StatusUnauthorized},
		{name: "empty bearer value", token: token, authHeader: "Bearer ", wantStatus: http.StatusUnauthorized},
		{name: "empty configured token fails closed", token: "", authHeader: "Bearer " + token, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			h := Auth(tt.token)(next)

			req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK && !called {
				t.Fatal("next handler not called on valid auth")
			}
			if tt.wantStatus == http.StatusUnauthorized {
				if called {
					t.Fatal("next handler called despite 401")
				}
				if got := rec.Header().Get("WWW-Authenticate"); got == "" {
					t.Fatal("401 missing WWW-Authenticate header")
				}
			}
		})
	}
}

func TestAuthBodyDrainedNotRequired(t *testing.T) {
	// Sanity: middleware does not read the request body, so a denied request leaves
	// the body untouched for any (none here) downstream use.
	h := Auth("tok")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/api/accounts", io.NopCloser(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
