package handlers

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestBootstrapAdminPasswordSetsHashAndRevokesSessions(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Now: apiClock(), Logger: apiLogger(), BootstrapAdminToken: "boot"}
	if err := st.CreateWebSession(context.Background(), queries.CreateWebSessionParams{
		Selector:   "admin-session",
		UserID:     "admin",
		SecretHash: auth.HashToken("secret"),
		CsrfHash:   auth.HashToken("csrf"),
		Scopes:     `["admin"]`,
		CreatedAt:  1,
		LastSeenAt: 1,
		ExpiresAt:  9999,
	}); err != nil {
		t.Fatalf("create web session: %v", err)
	}
	register := func(r chi.Router) { deps.MountBootstrap(r) }

	if rec := doReq(t, http.MethodPost, "/api/bootstrap/admin-password", `{"password":"ValidPassword123"}`, register); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without bootstrap header = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/bootstrap/admin-password", strings.NewReader(`{"password":"ValidPassword123"}`))
	req.Header.Set("Authorization", "Bearer boot")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	register(r)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "argon2id") {
		t.Fatalf("response leaked password hash: %q", rec.Body.String())
	}

	admin, err := st.GetUser(context.Background(), queries.GetUserParams{ID: "admin"})
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	if !admin.PasswordHash.Valid || !auth.VerifyPassword("ValidPassword123", admin.PasswordHash.String) {
		t.Fatalf("admin password hash was not stored or does not verify")
	}
	if admin.UpdatedAt != 1000 {
		t.Fatalf("admin updated_at=%d, want 1000", admin.UpdatedAt)
	}
	session, err := st.GetWebSession(context.Background(), queries.GetWebSessionParams{Selector: "admin-session"})
	if err != nil {
		t.Fatalf("get web session: %v", err)
	}
	if !session.RevokedAt.Valid || session.RevokedAt.Int64 != 1000 {
		t.Fatalf("revoked_at=%v, want 1000", session.RevokedAt)
	}
}

func TestBootstrapAdminPasswordRejectsWeakOrTrimmedPassword(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Now: apiClock(), Logger: apiLogger(), BootstrapAdminToken: "boot"}
	register := func(r chi.Router) { deps.MountBootstrap(r) }

	tests := []struct {
		name string
		body string
	}{
		{name: "short", body: `{"password":"too-short"}`},
		{name: "trimmed", body: `{"password":" ValidPassword123 "}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/bootstrap/admin-password", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer boot")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r := chi.NewRouter()
			register(r)
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSessionLoginMeLogout(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Now: apiClock(), Logger: apiLogger()}
	setUserPassword(t, st, "admin", "ValidPassword123")
	cfg := SessionHTTPConfig{CookieName: "echo_session", TTL: time.Hour, SecureCookies: "never"}

	loginReq := httptest.NewRequest(http.MethodPost, "/api/session/login", strings.NewReader(`{"username":" admin ","password":"ValidPassword123"}`))
	loginReq.Host = "app.example"
	loginReq.Header.Set("Origin", "https://app.example")
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("User-Agent", "EchoBrowser/1.0")
	loginReq.RemoteAddr = "203.0.113.9:1234"
	loginRec := httptest.NewRecorder()
	loginRouter := chi.NewRouter()
	loginRouter.Post("/api/session/login", deps.LoginSession(cfg))
	loginRouter.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200; body=%s", loginRec.Code, loginRec.Body.String())
	}

	cookie := singleCookie(t, loginRec, "echo_session")
	if cookie.Value == "" || cookie.Path != "/" || cookie.MaxAge != int(time.Hour.Seconds()) || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure {
		t.Fatalf("login cookie = %#v, want HttpOnly Lax non-secure session cookie", cookie)
	}
	selector, secret, ok := auth.ParseSessionCookie(cookie.Value)
	if !ok {
		t.Fatalf("login cookie did not contain a valid session token")
	}
	session, err := st.GetWebSession(context.Background(), queries.GetWebSessionParams{Selector: selector})
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.UserID != "admin" || !auth.VerifySessionSecret(secret, session.SecretHash) || session.UserAgent.String != "EchoBrowser/1.0" || !session.IpHint.Valid || session.IpHint.String == "" {
		t.Fatalf("stored session = %#v, secret verifies=%v", session, auth.VerifySessionSecret(secret, session.SecretHash))
	}
	var loginBody sessionMeResponse
	decodeBody(t, loginRec, &loginBody)
	if !loginBody.Authenticated || loginBody.User == nil || loginBody.User.ID != "admin" || loginBody.User.Username != "admin" || loginBody.User.Role != "admin" || loginBody.CSRFToken == "" {
		t.Fatalf("login response = %#v, want authenticated admin with csrf", loginBody)
	}
	if strings.Contains(loginRec.Body.String(), "password_hash") || strings.Contains(loginRec.Body.String(), "secret_hash") || strings.Contains(loginRec.Body.String(), "csrf_hash") {
		t.Fatalf("login response leaked sensitive fields: %s", loginRec.Body.String())
	}

	sessionUser := auth.UserContext{
		UserID:           "admin",
		Role:             "admin",
		Scopes:           []string{"admin"},
		CredentialSource: auth.CredentialSession,
		SessionSelector:  selector,
	}
	meRec := doSessionReqAs(t, http.MethodGet, "/api/session/me", "", sessionUser, func(r chi.Router) {
		r.Get("/api/session/me", deps.GetSessionMe)
	})
	if meRec.Code != http.StatusOK {
		t.Fatalf("me status = %d, want 200; body=%s", meRec.Code, meRec.Body.String())
	}
	var meBody sessionMeResponse
	decodeBody(t, meRec, &meBody)
	if !meBody.Authenticated || meBody.User == nil || meBody.User.ID != "admin" || meBody.CSRFToken == "" || meBody.CSRFToken == loginBody.CSRFToken {
		t.Fatalf("me response = %#v, want authenticated admin with rotated csrf", meBody)
	}
	rotated, err := st.GetWebSession(context.Background(), queries.GetWebSessionParams{Selector: selector})
	if err != nil {
		t.Fatalf("get rotated session: %v", err)
	}
	if rotated.CsrfHash != auth.HashToken(meBody.CSRFToken) || rotated.CsrfHash == session.CsrfHash {
		t.Fatalf("csrf hash was not rotated")
	}
	if strings.Contains(meRec.Body.String(), "password_hash") || strings.Contains(meRec.Body.String(), "secret_hash") || strings.Contains(meRec.Body.String(), "csrf_hash") {
		t.Fatalf("me response leaked sensitive fields: %s", meRec.Body.String())
	}

	logoutRec := doSessionReqAs(t, http.MethodPost, "/api/session/logout", "", sessionUser, func(r chi.Router) {
		r.Post("/api/session/logout", deps.LogoutSession(cfg))
	})
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204; body=%s", logoutRec.Code, logoutRec.Body.String())
	}
	clearCookie := singleCookie(t, logoutRec, "echo_session")
	if clearCookie.MaxAge != -1 || clearCookie.Value != "" {
		t.Fatalf("clear cookie = %#v, want MaxAge=-1 empty value", clearCookie)
	}
	revoked, err := st.GetWebSession(context.Background(), queries.GetWebSessionParams{Selector: selector})
	if err != nil {
		t.Fatalf("get revoked session: %v", err)
	}
	if !revoked.RevokedAt.Valid || revoked.RevokedAt.Int64 != 1000 {
		t.Fatalf("revoked_at=%v, want 1000", revoked.RevokedAt)
	}
}

func TestSessionLoginDenialMatrix(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, st *store.Store)
		body    string
		want    int
		origin  string
		host    string
	}{
		{
			name: "unknown user",
			body: `{"username":"missing","password":"ValidPassword123"}`,
			want: http.StatusUnauthorized,
		},
		{
			name: "disabled user",
			prepare: func(t *testing.T, st *store.Store) {
				hash, err := auth.HashPassword("ValidPassword123")
				if err != nil {
					t.Fatalf("hash password: %v", err)
				}
				if err := st.CreateUser(context.Background(), queries.CreateUserParams{
					ID:            "disabled",
					Username:      "disabled",
					Role:          "user",
					Status:        "disabled",
					QuotaPolicyID: 1,
					PasswordHash:  sql.NullString{String: hash, Valid: true},
					CreatedAt:     1,
					UpdatedAt:     1,
				}); err != nil {
					t.Fatalf("create disabled user: %v", err)
				}
			},
			body: `{"username":"disabled","password":"ValidPassword123"}`,
			want: http.StatusUnauthorized,
		},
		{
			name: "missing hash",
			body: `{"username":"admin","password":"ValidPassword123"}`,
			want: http.StatusUnauthorized,
		},
		{
			name: "wrong password",
			prepare: func(t *testing.T, st *store.Store) {
				setUserPassword(t, st, "admin", "ValidPassword123")
			},
			body: `{"username":"admin","password":"WrongPassword123"}`,
			want: http.StatusUnauthorized,
		},
		{
			name: "cross origin",
			prepare: func(t *testing.T, st *store.Store) {
				setUserPassword(t, st, "admin", "ValidPassword123")
			},
			body:   `{"username":"admin","password":"ValidPassword123"}`,
			want:   http.StatusForbidden,
			origin: "https://evil.example",
			host:   "app.example",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newAPIStore(t)
			if tc.prepare != nil {
				tc.prepare(t, st)
			}
			deps := APIDeps{Store: st, Now: apiClock(), Logger: apiLogger()}
			req := httptest.NewRequest(http.MethodPost, "/api/session/login", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			rec := httptest.NewRecorder()
			r := chi.NewRouter()
			r.Post("/api/session/login", deps.LoginSession(SessionHTTPConfig{}))
			r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if got := singleCookieOrNil(rec, "echo_session"); got != nil {
				t.Fatalf("denied login set cookie: %#v", got)
			}
			if tc.want == http.StatusUnauthorized && !strings.Contains(rec.Body.String(), "unauthorized") {
				t.Fatalf("401 body = %q, want generic unauthorized", rec.Body.String())
			}
		})
	}
}

func TestSessionMeBearerHasNoCSRF(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Now: apiClock(), Logger: apiLogger()}
	bearerUser := auth.UserContext{
		UserID:           "admin",
		Role:             "admin",
		Scopes:           []string{"admin"},
		CredentialSource: auth.CredentialBearer,
	}
	rec := doSessionReqAs(t, http.MethodGet, "/api/session/me", "", bearerUser, func(r chi.Router) {
		r.Get("/api/session/me", deps.GetSessionMe)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body sessionMeResponse
	decodeBody(t, rec, &body)
	if !body.Authenticated || body.User == nil || body.User.ID != "admin" || body.CSRFToken != "" {
		t.Fatalf("body = %#v, want authenticated bearer user without csrf", body)
	}
	if strings.Contains(rec.Body.String(), "csrf_token") {
		t.Fatalf("bearer response included csrf_token field: %s", rec.Body.String())
	}
}

func TestSessionMeFailClosedForMissingOrDisabledUser(t *testing.T) {
	tests := []struct {
		name    string
		userID  string
		prepare func(t *testing.T, st *store.Store)
	}{
		{name: "missing", userID: "missing"},
		{
			name:   "disabled",
			userID: "admin",
			prepare: func(t *testing.T, st *store.Store) {
				if err := st.UpdateUserStatus(context.Background(), queries.UpdateUserStatusParams{
					Status:    "disabled",
					UpdatedAt: 1000,
					ID:        "admin",
				}); err != nil {
					t.Fatalf("disable admin: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newAPIStore(t)
			if tc.prepare != nil {
				tc.prepare(t, st)
			}
			deps := APIDeps{Store: st, Now: apiClock(), Logger: apiLogger()}
			rec := doSessionReqAs(t, http.MethodGet, "/api/session/me", "", auth.UserContext{
				UserID:           tc.userID,
				Role:             "admin",
				Scopes:           []string{"admin"},
				CredentialSource: auth.CredentialSession,
				SessionSelector:  "selector",
			}, func(r chi.Router) {
				r.Get("/api/session/me", deps.GetSessionMe)
			})
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want fail-closed 401; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSecureCookiePolicy(t *testing.T) {
	if !secureCookie(httptest.NewRequest(http.MethodGet, "/", nil), SessionHTTPConfig{SecureCookies: "always"}) {
		t.Fatal("always policy = false, want true")
	}
	if secureCookie(httptest.NewRequest(http.MethodGet, "/", nil), SessionHTTPConfig{SecureCookies: "never"}) {
		t.Fatal("never policy = true, want false")
	}
	tlsReq := httptest.NewRequest(http.MethodGet, "/", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	if !secureCookie(tlsReq, SessionHTTPConfig{SecureCookies: "auto"}) {
		t.Fatal("auto TLS policy = false, want true")
	}
	proxyReq := httptest.NewRequest(http.MethodGet, "/", nil)
	proxyReq.Header.Set("X-Forwarded-Proto", "HTTPS")
	if !secureCookie(proxyReq, SessionHTTPConfig{SecureCookies: "auto", TrustProxyHeaders: true}) {
		t.Fatal("auto trusted X-Forwarded-Proto:https policy = false, want true")
	}
	if secureCookie(proxyReq, SessionHTTPConfig{SecureCookies: "auto"}) {
		t.Fatal("auto untrusted X-Forwarded-Proto:https policy = true, want false")
	}
	if secureCookie(httptest.NewRequest(http.MethodGet, "/", nil), SessionHTTPConfig{}) {
		t.Fatal("auto/default plain HTTP policy = true, want false")
	}
}

func setUserPassword(t *testing.T, st *store.Store, userID, password string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := st.UpdateUserPasswordHash(context.Background(), queries.UpdateUserPasswordHashParams{
		PasswordHash: sql.NullString{String: hash, Valid: true},
		UpdatedAt:    1,
		ID:           userID,
	}); err != nil {
		t.Fatalf("update user password: %v", err)
	}
}

func doSessionReqAs(t *testing.T, method, target, body string, user auth.UserContext, register func(chi.Router)) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *strings.Reader
	if body == "" {
		bodyReader = strings.NewReader("")
	} else {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(auth.NewContext(req.Context(), user))
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	register(r)
	r.ServeHTTP(rec, req)
	return rec
}

func singleCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	cookie := singleCookieOrNil(rec, name)
	if cookie == nil {
		t.Fatalf("cookie %q not found in %v", name, rec.Result().Cookies())
	}
	return cookie
}

func singleCookieOrNil(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func TestGetSessionMeUnauthenticated(t *testing.T) {
	st := newAPIStore(t)
	deps := APIDeps{Store: st, Now: apiClock(), Logger: apiLogger()}
	rec := doReq(t, http.MethodGet, "/api/session/me", "", func(r chi.Router) {
		r.Get("/api/session/me", deps.GetSessionMe)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["authenticated"] != false {
		t.Fatalf("body=%v, want authenticated false", body)
	}
}
