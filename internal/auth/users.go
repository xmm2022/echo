package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

// HashToken returns the stored hash form of an API token plaintext.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// validScopes is the closed set of scope strings an API token may carry.
// HasScope (admin-implies-all) and decodeScopes both derive from it so the
// scope vocabulary lives in exactly one place.
var validScopes = map[string]struct{}{
	"admin":     {},
	"discovery": {},
	"read":      {},
	"playback":  {},
}

// UserContext carries the authenticated identity and scopes for a request;
// Now is the evaluation time used for token expiry checks in later phases.
type UserContext struct {
	UserID string
	Role   string
	Scopes []string
	Now    time.Time
}

type contextKey struct{}

func NewContext(ctx context.Context, user UserContext) context.Context {
	return context.WithValue(ctx, contextKey{}, user)
}

func FromContext(ctx context.Context) UserContext {
	user, _ := ctx.Value(contextKey{}).(UserContext)
	return user
}

type TokenStore interface {
	GetAPITokenByHash(context.Context, queries.GetAPITokenByHashParams) (queries.ApiToken, error)
	GetUser(context.Context, queries.GetUserParams) (queries.User, error)
	TouchAPIToken(context.Context, queries.TouchAPITokenParams) error
}

type Authenticator struct {
	Store TokenStore
	Now   func() time.Time
}

func GenerateToken() (string, error) {
	selector, err := randomBase64URL(12)
	if err != nil {
		return "", err
	}
	secret, err := randomBase64URL(32)
	if err != nil {
		return "", err
	}
	return selector + "." + secret, nil
}

func VerifyTokenHash(token, stored string) bool {
	got := HashToken(token)
	return subtle.ConstantTimeCompare([]byte(got), []byte(stored)) == 1
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Authenticator) Authenticate(r *http.Request) (UserContext, bool) {
	if a == nil || a.Store == nil {
		return UserContext{}, false
	}
	raw := BearerToken(r.Header.Get("Authorization"))
	if raw == "" {
		return UserContext{}, false
	}
	token, err := a.Store.GetAPITokenByHash(r.Context(), queries.GetAPITokenByHashParams{TokenHash: HashToken(raw)})
	if err != nil || token.RevokedAt.Valid {
		return UserContext{}, false
	}
	now := a.now()
	nowUnix := now.Unix()
	if token.ExpiresAt.Valid && token.ExpiresAt.Int64 <= nowUnix {
		return UserContext{}, false
	}
	user, err := a.Store.GetUser(r.Context(), queries.GetUserParams{ID: token.UserID})
	if err != nil || user.Status != "active" {
		return UserContext{}, false
	}
	scopes, err := decodeScopes(token.Scopes)
	if err != nil {
		return UserContext{}, false
	}
	_ = a.Store.TouchAPIToken(r.Context(), queries.TouchAPITokenParams{
		LastUsedAt: sql.NullInt64{Int64: nowUnix, Valid: true},
		ID:         token.ID,
	})
	return UserContext{UserID: user.ID, Role: user.Role, Scopes: scopes, Now: now}, true
}

func (u UserContext) HasScope(scope string) bool {
	if u.Role == "admin" && contains(u.Scopes, "admin") {
		_, ok := validScopes[scope]
		return ok
	}
	return contains(u.Scopes, scope)
}

func randomBase64URL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func contains(s []string, v string) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

func BearerToken(header string) string {
	const prefix = "Bearer "
	header = strings.TrimSpace(header)
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return ""
	}
	return token
}

func decodeScopes(raw string) ([]string, error) {
	var scopes []string
	if err := json.Unmarshal([]byte(raw), &scopes); err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		return nil, errors.New("scopes must not be empty")
	}
	for _, scope := range scopes {
		if _, ok := validScopes[scope]; !ok {
			return nil, fmt.Errorf("invalid scope %q", scope)
		}
	}
	return scopes, nil
}
