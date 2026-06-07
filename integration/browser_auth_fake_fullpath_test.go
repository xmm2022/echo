package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	httpserver "github.com/xmm2022/echo/internal/http"
	"github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/media"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestBrowserAuthFakeFullPathCreatesDiscoveryRequestAndRejectsMissingCSRF(t *testing.T) {
	ctx := context.Background()
	h := newDiscoveryHarness(t)
	seedMediaRequestUser(t, h.store, "requester", "user")
	password := "correct horse battery staple"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := h.store.UpdateUserPasswordHash(ctx, queries.UpdateUserPasswordHashParams{
		PasswordHash: sql.NullString{String: hash, Valid: true},
		UpdatedAt:    h.now.Unix(),
		ID:           "requester",
	}); err != nil {
		t.Fatalf("update user password hash: %v", err)
	}
	target := seedMediaRequestTarget(t, h)
	fake := &fakeMediaRequestTMDB{
		movies: map[string]tmdb.Media{
			"321": {
				TMDBID:      "321",
				MediaType:   "movie",
				Title:       "Browser Auth Movie",
				ReleaseYear: 2026,
				RawJSON:     `{}`,
			},
			"322": {
				TMDBID:      "322",
				MediaType:   "movie",
				Title:       "CSRF Blocked Movie",
				ReleaseYear: 2026,
				RawJSON:     `{}`,
			},
		},
	}

	server := newBrowserAuthFullPathServer(t, h, fake)
	login := loginBrowserAuthUser(t, server.URL, "requester", password)

	successResp := postBrowserAuthJSON(t, server.URL, "/api/me/discovery/requests", login.Cookie, login.CSRFToken, map[string]any{
		"tmdb_id":       "321",
		"media_type":    "movie",
		"tmdb_language": "zh-CN",
		"target_id":     target.ID,
		"user_note":     "small browser auth gate note",
	})
	successBody := readBrowserAuthBody(t, successResp)
	if successResp.StatusCode != http.StatusCreated {
		t.Fatalf("create request status = %d, want %d (body: %s)", successResp.StatusCode, http.StatusCreated, successBody)
	}

	forbiddenResp := postBrowserAuthJSON(t, server.URL, "/api/me/discovery/requests", login.Cookie, "", map[string]any{
		"tmdb_id":       "322",
		"media_type":    "movie",
		"tmdb_language": "zh-CN",
		"target_id":     target.ID,
		"user_note":     "this should fail before media handling",
	})
	forbiddenBody := readBrowserAuthBody(t, forbiddenResp)
	if forbiddenResp.StatusCode != http.StatusForbidden {
		t.Fatalf("create request without csrf status = %d, want %d (body: %s)", forbiddenResp.StatusCode, http.StatusForbidden, forbiddenBody)
	}
}

type browserAuthLogin struct {
	Cookie    *http.Cookie
	CSRFToken string
}

func newBrowserAuthFullPathServer(t *testing.T, h *discoveryHarness, fake *fakeMediaRequestTMDB) *httptest.Server {
	t.Helper()
	now := func() time.Time { return h.now }
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	authenticator := &auth.Authenticator{
		Store:        h.store,
		SessionStore: h.store,
		SessionConfig: auth.SessionConfig{
			CookieName:    "echo_session",
			TouchInterval: time.Minute,
		},
		Now: now,
	}
	mediaSvc := &media.Service{Store: h.store, TMDB: fake, Now: now}
	server := httptest.NewServer(httpserver.HandlerWithDeps(httpserver.Deps{
		Logger:    logger,
		AuthCheck: authenticator.Authenticate,
		API: &handlers.APIDeps{
			Store:  h.store,
			Media:  mediaSvc,
			Logger: logger,
			Now:    now,
		},
		Session: handlers.SessionHTTPConfig{
			CookieName:    "echo_session",
			TTL:           time.Hour,
			SecureCookies: "never",
		},
	}))
	t.Cleanup(server.Close)
	return server
}

func loginBrowserAuthUser(t *testing.T, baseURL, username, password string) browserAuthLogin {
	t.Helper()
	resp := doBrowserAuthJSON(t, baseURL, "/api/session/login", nil, "", map[string]string{
		"username": username,
		"password": password,
	})
	body := readBrowserAuthBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want %d (body: %s)", resp.StatusCode, http.StatusOK, body)
	}
	var parsed struct {
		Authenticated bool   `json:"authenticated"`
		CSRFToken     string `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode login body %q: %v", body, err)
	}
	if !parsed.Authenticated {
		t.Fatalf("login authenticated = false, want true (body: %s)", body)
	}
	if parsed.CSRFToken == "" {
		t.Fatalf("login csrf token is empty (body: %s)", body)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "echo_session" {
			copy := *cookie
			sessionCookie = &copy
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("login did not set echo_session cookie")
	}
	return browserAuthLogin{Cookie: sessionCookie, CSRFToken: parsed.CSRFToken}
}

func postBrowserAuthJSON(t *testing.T, baseURL, path string, cookie *http.Cookie, csrfToken string, body any) *http.Response {
	t.Helper()
	return doBrowserAuthJSON(t, baseURL, path, cookie, csrfToken, body)
}

func doBrowserAuthJSON(t *testing.T, baseURL, path string, cookie *http.Cookie, csrfToken string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request POST %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrfToken != "" {
		req.Header.Set("X-CSRF-Token", csrfToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do POST %s: %v", path, err)
	}
	return resp
}

func readBrowserAuthBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return raw
}
