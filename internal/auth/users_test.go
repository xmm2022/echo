package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

func TestGenerateTokenHashDoesNotExposeSecret(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := HashToken(token)
	if token == "" || !strings.Contains(token, ".") {
		t.Fatalf("token shape = %q, want selector.secret", token)
	}
	if strings.Contains(hash, token) {
		t.Fatalf("hash contains token: %q", hash)
	}
	if !VerifyTokenHash(token, hash) {
		t.Fatal("hash did not verify generated token")
	}
	if VerifyTokenHash(token+"x", hash) {
		t.Fatal("modified token verified")
	}
}

func TestTokenScopes(t *testing.T) {
	scopes, err := decodeScopes(`["read","playback","discovery"]`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := UserContext{
		UserID: "user1",
		Role:   "user",
		Scopes: scopes,
		Now:    time.Unix(1000, 0),
	}
	if !ctx.HasScope("playback") {
		t.Fatal("playback scope missing")
	}
	if !ctx.HasScope("discovery") {
		t.Fatal("discovery scope missing")
	}
	if ctx.HasScope("admin") {
		t.Fatal("non-admin token has admin scope")
	}
	admin := UserContext{UserID: "admin", Role: "admin", Scopes: []string{"admin"}}
	if !admin.HasScope("read") || !admin.HasScope("playback") || !admin.HasScope("admin") || !admin.HasScope("discovery") {
		t.Fatal("admin scope should imply admin/read/playback/discovery")
	}
}

func TestDecodeScopesRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{
		``,
		`null`,
		`[]`,
		`["admin", 1]`,
		`["unknown"]`,
		`{"scope":"admin"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if scopes, err := decodeScopes(raw); err == nil {
				t.Fatalf("decodeScopes(%s) = %#v, nil error; want denial", raw, scopes)
			}
		})
	}
}

type fakeTokenStore struct {
	token    queries.ApiToken
	tokenErr error
	user     queries.User
	userErr  error
	touchErr error
	touched  bool
}

func (f *fakeTokenStore) GetAPITokenByHash(ctx context.Context, _ queries.GetAPITokenByHashParams) (queries.ApiToken, error) {
	return f.token, f.tokenErr
}

func (f *fakeTokenStore) GetUser(ctx context.Context, _ queries.GetUserParams) (queries.User, error) {
	return f.user, f.userErr
}

func (f *fakeTokenStore) TouchAPIToken(ctx context.Context, _ queries.TouchAPITokenParams) error {
	f.touched = true
	return f.touchErr
}

type fakeSessionStore struct {
	session        queries.WebSession
	sessErr        error
	user           queries.User
	userErr        error
	touchErr       error
	sessionLookups int
	userLookups    int
	touched        bool
}

func (f *fakeSessionStore) GetWebSession(ctx context.Context, _ queries.GetWebSessionParams) (queries.WebSession, error) {
	f.sessionLookups++
	return f.session, f.sessErr
}

func (f *fakeSessionStore) TouchWebSession(ctx context.Context, _ queries.TouchWebSessionParams) error {
	f.touched = true
	return f.touchErr
}

func (f *fakeSessionStore) GetUser(ctx context.Context, _ queries.GetUserParams) (queries.User, error) {
	f.userLookups++
	return f.user, f.userErr
}

func TestAuthenticatorAuthenticateDenialMatrix(t *testing.T) {
	now := time.Unix(1000, 0)
	authReq := func(header string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		return req
	}

	var nilAuth *Authenticator
	if _, ok := nilAuth.Authenticate(authReq("Bearer secret")); ok {
		t.Fatal("nil authenticator Authenticate ok = true, want false")
	}
	if _, ok := (&Authenticator{Now: func() time.Time { return now }}).Authenticate(authReq("Bearer secret")); ok {
		t.Fatal("nil store Authenticate ok = true, want false")
	}

	validToken := queries.ApiToken{
		ID:        "tok_admin",
		UserID:    "admin",
		Name:      "admin token",
		TokenHash: HashToken("secret"),
		Scopes:    `["admin","read"]`,
		CreatedAt: 900,
	}
	validUser := queries.User{
		ID:        "admin",
		Username:  "admin",
		Role:      "admin",
		Status:    "active",
		CreatedAt: 1,
		UpdatedAt: 1,
	}

	tests := []struct {
		name        string
		header      string
		store       *fakeTokenStore
		want        UserContext
		wantOK      bool
		wantTouched bool
	}{
		{
			name:   "missing Authorization header",
			header: "",
			store:  &fakeTokenStore{token: validToken, user: validUser},
		},
		{
			name:   "malformed header",
			header: "Basic x",
			store:  &fakeTokenStore{token: validToken, user: validUser},
		},
		{
			name:   "token lookup error",
			header: "Bearer secret",
			store:  &fakeTokenStore{tokenErr: sql.ErrNoRows},
		},
		{
			name:   "revoked token",
			header: "Bearer secret",
			store: &fakeTokenStore{
				token: withToken(validToken, func(token *queries.ApiToken) {
					token.RevokedAt = sql.NullInt64{Int64: 1, Valid: true}
				}),
				user: validUser,
			},
		},
		{
			name:   "expired token",
			header: "Bearer secret",
			store: &fakeTokenStore{
				token: withToken(validToken, func(token *queries.ApiToken) {
					token.ExpiresAt = sql.NullInt64{Int64: 999, Valid: true}
				}),
				user: validUser,
			},
		},
		{
			name:   "expires exactly now",
			header: "Bearer secret",
			store: &fakeTokenStore{
				token: withToken(validToken, func(token *queries.ApiToken) {
					token.ExpiresAt = sql.NullInt64{Int64: 1000, Valid: true}
				}),
				user: validUser,
			},
		},
		{
			name:   "inactive user",
			header: "Bearer secret",
			store: &fakeTokenStore{
				token: validToken,
				user: withUser(validUser, func(user *queries.User) {
					user.Status = "disabled"
				}),
			},
		},
		{
			name:   "invalid scopes",
			header: "Bearer secret",
			store: &fakeTokenStore{
				token: withToken(validToken, func(token *queries.ApiToken) {
					token.Scopes = `["bogus"]`
				}),
				user: validUser,
			},
		},
		{
			name:   "success",
			header: "Bearer secret",
			store:  &fakeTokenStore{token: validToken, user: validUser},
			want: UserContext{
				UserID:           "admin",
				Role:             "admin",
				Scopes:           []string{"admin", "read"},
				Now:              now,
				CredentialSource: CredentialBearer,
			},
			wantOK:      true,
			wantTouched: true,
		},
		{
			name:   "touch error ignored",
			header: "Bearer secret",
			store: &fakeTokenStore{
				token:    validToken,
				user:     validUser,
				touchErr: errors.New("boom"),
			},
			want: UserContext{
				UserID:           "admin",
				Role:             "admin",
				Scopes:           []string{"admin", "read"},
				Now:              now,
				CredentialSource: CredentialBearer,
			},
			wantOK:      true,
			wantTouched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authenticator{
				Store: tt.store,
				Now:   func() time.Time { return now },
			}
			got, ok := a.Authenticate(authReq(tt.header))
			if ok != tt.wantOK {
				t.Fatalf("Authenticate ok = %v, want %v", ok, tt.wantOK)
			}
			if got.UserID != tt.want.UserID ||
				got.Role != tt.want.Role ||
				!slices.Equal(got.Scopes, tt.want.Scopes) ||
				!got.Now.Equal(tt.want.Now) ||
				got.CredentialSource != tt.want.CredentialSource ||
				got.SessionSelector != tt.want.SessionSelector ||
				got.CSRFHash != tt.want.CSRFHash {
				t.Fatalf("Authenticate user = %#v, want %#v", got, tt.want)
			}
			if tt.store.touched != tt.wantTouched {
				t.Fatalf("TouchAPIToken called = %v, want %v", tt.store.touched, tt.wantTouched)
			}
		})
	}
}

func TestAuthenticatorAuthenticateSessionDenialMatrix(t *testing.T) {
	now := time.Unix(2000, 0)
	sessionReq := func(cookieValue string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/app", nil)
		if cookieValue != "" {
			req.AddCookie(&http.Cookie{Name: "echo_session", Value: cookieValue})
		}
		return req
	}
	_, selector, secret, err := GenerateSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	validCookie := selector + "." + secret
	wrongSecretCookie := selector + "." + differentBase64URLString(secret)

	validUser := queries.User{
		ID:        "u1",
		Username:  "user",
		Role:      "user",
		Status:    "active",
		CreatedAt: 1,
		UpdatedAt: 1,
	}
	validSession := queries.WebSession{
		Selector:   selector,
		UserID:     "u1",
		SecretHash: HashToken(secret),
		CsrfHash:   HashToken("csrf"),
		Scopes:     `["discovery","read","playback"]`,
		CreatedAt:  1000,
		LastSeenAt: 1000,
		ExpiresAt:  3000,
	}

	var nilAuth *Authenticator
	if _, ok := nilAuth.Authenticate(sessionReq(validCookie)); ok {
		t.Fatal("nil authenticator Authenticate ok = true, want false")
	}
	if _, ok := (&Authenticator{
		SessionConfig: SessionConfig{CookieName: "echo_session"},
		Now:           func() time.Time { return now },
	}).Authenticate(sessionReq(validCookie)); ok {
		t.Fatal("nil session store Authenticate ok = true, want false")
	}
	if _, ok := (&Authenticator{
		SessionStore:  &fakeSessionStore{session: validSession, user: validUser},
		SessionConfig: SessionConfig{},
		Now:           func() time.Time { return now },
	}).Authenticate(sessionReq(validCookie)); ok {
		t.Fatal("empty session cookie name Authenticate ok = true, want false")
	}

	tests := []struct {
		name               string
		cookie             string
		store              *fakeSessionStore
		touchInterval      time.Duration
		want               UserContext
		wantOK             bool
		wantTouched        bool
		wantSessionLookups int
		wantUserLookups    int
	}{
		{
			name:  "missing cookie",
			store: &fakeSessionStore{session: validSession, user: validUser},
		},
		{
			name:   "malformed cookie",
			cookie: ".secret",
			store:  &fakeSessionStore{session: validSession, user: validUser},
		},
		{
			name:               "lookup error",
			cookie:             validCookie,
			store:              &fakeSessionStore{sessErr: sql.ErrNoRows},
			wantSessionLookups: 1,
		},
		{
			name:               "wrong secret",
			cookie:             wrongSecretCookie,
			store:              &fakeSessionStore{session: validSession, user: validUser},
			wantSessionLookups: 1,
		},
		{
			name:   "expired session",
			cookie: validCookie,
			store: &fakeSessionStore{
				session: withWebSession(validSession, func(s *queries.WebSession) {
					s.ExpiresAt = 2000
				}),
				user: validUser,
			},
			wantSessionLookups: 1,
		},
		{
			name:   "revoked session",
			cookie: validCookie,
			store: &fakeSessionStore{
				session: withWebSession(validSession, func(s *queries.WebSession) {
					s.RevokedAt = sql.NullInt64{Int64: 1500, Valid: true}
				}),
				user: validUser,
			},
			wantSessionLookups: 1,
		},
		{
			name:   "disabled user",
			cookie: validCookie,
			store: &fakeSessionStore{
				session: validSession,
				user: withUser(validUser, func(u *queries.User) {
					u.Status = "disabled"
				}),
			},
			wantSessionLookups: 1,
			wantUserLookups:    1,
		},
		{
			name:               "user lookup error",
			cookie:             validCookie,
			store:              &fakeSessionStore{session: validSession, userErr: sql.ErrNoRows},
			wantSessionLookups: 1,
			wantUserLookups:    1,
		},
		{
			name:   "invalid scopes",
			cookie: validCookie,
			store: &fakeSessionStore{
				session: withWebSession(validSession, func(s *queries.WebSession) {
					s.Scopes = `["bogus"]`
				}),
				user: validUser,
			},
			wantSessionLookups: 1,
			wantUserLookups:    1,
		},
		{
			name:          "success touches stale session",
			cookie:        validCookie,
			store:         &fakeSessionStore{session: validSession, user: validUser},
			touchInterval: time.Minute,
			want: UserContext{
				UserID:           "u1",
				Role:             "user",
				Scopes:           []string{"discovery", "read", "playback"},
				Now:              now,
				CredentialSource: CredentialSession,
				SessionSelector:  selector,
				CSRFHash:         HashToken("csrf"),
			},
			wantOK:             true,
			wantTouched:        true,
			wantSessionLookups: 1,
			wantUserLookups:    1,
		},
		{
			name:   "fresh last seen skips touch",
			cookie: validCookie,
			store: &fakeSessionStore{
				session: withWebSession(validSession, func(s *queries.WebSession) {
					s.LastSeenAt = 1990
				}),
				user: validUser,
			},
			touchInterval: time.Minute,
			want: UserContext{
				UserID:           "u1",
				Role:             "user",
				Scopes:           []string{"discovery", "read", "playback"},
				Now:              now,
				CredentialSource: CredentialSession,
				SessionSelector:  selector,
				CSRFHash:         HashToken("csrf"),
			},
			wantOK:             true,
			wantSessionLookups: 1,
			wantUserLookups:    1,
		},
		{
			name:   "zero touch interval touches fresh session",
			cookie: validCookie,
			store: &fakeSessionStore{
				session: withWebSession(validSession, func(s *queries.WebSession) {
					s.LastSeenAt = 1999
				}),
				user: validUser,
			},
			want: UserContext{
				UserID:           "u1",
				Role:             "user",
				Scopes:           []string{"discovery", "read", "playback"},
				Now:              now,
				CredentialSource: CredentialSession,
				SessionSelector:  selector,
				CSRFHash:         HashToken("csrf"),
			},
			wantOK:             true,
			wantTouched:        true,
			wantSessionLookups: 1,
			wantUserLookups:    1,
		},
		{
			name:   "touch error ignored",
			cookie: validCookie,
			store: &fakeSessionStore{
				session:  validSession,
				user:     validUser,
				touchErr: errors.New("boom"),
			},
			touchInterval: time.Minute,
			want: UserContext{
				UserID:           "u1",
				Role:             "user",
				Scopes:           []string{"discovery", "read", "playback"},
				Now:              now,
				CredentialSource: CredentialSession,
				SessionSelector:  selector,
				CSRFHash:         HashToken("csrf"),
			},
			wantOK:             true,
			wantTouched:        true,
			wantSessionLookups: 1,
			wantUserLookups:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authenticator{
				SessionStore: tt.store,
				SessionConfig: SessionConfig{
					CookieName:    "echo_session",
					TouchInterval: tt.touchInterval,
				},
				Now: func() time.Time { return now },
			}
			got, ok := a.Authenticate(sessionReq(tt.cookie))
			if ok != tt.wantOK {
				t.Fatalf("Authenticate ok = %v, want %v", ok, tt.wantOK)
			}
			if got.UserID != tt.want.UserID ||
				got.Role != tt.want.Role ||
				!slices.Equal(got.Scopes, tt.want.Scopes) ||
				!got.Now.Equal(tt.want.Now) ||
				got.CredentialSource != tt.want.CredentialSource ||
				got.SessionSelector != tt.want.SessionSelector ||
				got.CSRFHash != tt.want.CSRFHash {
				t.Fatalf("Authenticate user = %#v, want %#v", got, tt.want)
			}
			if tt.store.touched != tt.wantTouched {
				t.Fatalf("TouchWebSession called = %v, want %v", tt.store.touched, tt.wantTouched)
			}
			if tt.store.sessionLookups != tt.wantSessionLookups {
				t.Fatalf("GetWebSession calls = %v, want %v", tt.store.sessionLookups, tt.wantSessionLookups)
			}
			if tt.store.userLookups != tt.wantUserLookups {
				t.Fatalf("GetUser calls = %v, want %v", tt.store.userLookups, tt.wantUserLookups)
			}
		})
	}
}

func withToken(token queries.ApiToken, fn func(*queries.ApiToken)) queries.ApiToken {
	fn(&token)
	return token
}

func withUser(user queries.User, fn func(*queries.User)) queries.User {
	fn(&user)
	return user
}

func withWebSession(s queries.WebSession, fn func(*queries.WebSession)) queries.WebSession {
	fn(&s)
	return s
}

func differentBase64URLString(s string) string {
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}
