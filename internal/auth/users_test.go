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
	ctx := UserContext{
		UserID: "user1",
		Role:   "user",
		Scopes: []string{"read", "playback"},
		Now:    time.Unix(1000, 0),
	}
	if !ctx.HasScope("playback") {
		t.Fatal("playback scope missing")
	}
	if ctx.HasScope("admin") {
		t.Fatal("non-admin token has admin scope")
	}
	admin := UserContext{UserID: "admin", Role: "admin", Scopes: []string{"admin"}}
	if !admin.HasScope("read") || !admin.HasScope("playback") || !admin.HasScope("admin") {
		t.Fatal("admin scope should imply admin/read/playback")
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
				UserID: "admin",
				Role:   "admin",
				Scopes: []string{"admin", "read"},
				Now:    now,
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
				UserID: "admin",
				Role:   "admin",
				Scopes: []string{"admin", "read"},
				Now:    now,
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
			if got.UserID != tt.want.UserID || got.Role != tt.want.Role || !slices.Equal(got.Scopes, tt.want.Scopes) || !got.Now.Equal(tt.want.Now) {
				t.Fatalf("Authenticate user = %#v, want %#v", got, tt.want)
			}
			if tt.store.touched != tt.wantTouched {
				t.Fatalf("TouchAPIToken called = %v, want %v", tt.store.touched, tt.wantTouched)
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
