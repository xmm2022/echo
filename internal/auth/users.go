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
	UserID           string
	Role             string
	Scopes           []string
	Now              time.Time
	CredentialSource string
	SessionSelector  string
	CSRFHash         string
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

type SessionStore interface {
	GetWebSession(context.Context, queries.GetWebSessionParams) (queries.WebSession, error)
	TouchWebSession(context.Context, queries.TouchWebSessionParams) error
	GetUser(context.Context, queries.GetUserParams) (queries.User, error)
}

type SessionConfig struct {
	CookieName    string
	TouchInterval time.Duration
}

type Authenticator struct {
	Store         TokenStore
	SessionStore  SessionStore
	SessionConfig SessionConfig
	Now           func() time.Time
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
	if a == nil {
		return UserContext{}, false
	}
	raw := BearerToken(r.Header.Get("Authorization"))
	if raw != "" {
		return a.authenticateBearer(r, raw)
	}
	return a.authenticateSession(r)
}

func (a *Authenticator) authenticateBearer(r *http.Request, raw string) (UserContext, bool) {
	if a.Store == nil {
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
	return UserContext{
		UserID:           user.ID,
		Role:             user.Role,
		Scopes:           scopes,
		Now:              now,
		CredentialSource: CredentialBearer,
	}, true
}

func (a *Authenticator) authenticateSession(r *http.Request) (UserContext, bool) {
	if a.SessionStore == nil || a.SessionConfig.CookieName == "" {
		return UserContext{}, false
	}
	cookie, err := r.Cookie(a.SessionConfig.CookieName)
	if err != nil {
		return UserContext{}, false
	}
	selector, secret, ok := ParseSessionCookie(cookie.Value)
	if !ok {
		return UserContext{}, false
	}
	session, err := a.SessionStore.GetWebSession(r.Context(), queries.GetWebSessionParams{Selector: selector})
	if err != nil || session.RevokedAt.Valid {
		return UserContext{}, false
	}
	now := a.now()
	nowUnix := now.Unix()
	if session.ExpiresAt <= nowUnix || !VerifySessionSecret(secret, session.SecretHash) {
		return UserContext{}, false
	}
	user, err := a.SessionStore.GetUser(r.Context(), queries.GetUserParams{ID: session.UserID})
	if err != nil || user.Status != "active" {
		return UserContext{}, false
	}
	scopes, err := decodeScopes(session.Scopes)
	if err != nil {
		return UserContext{}, false
	}
	if shouldTouchSession(session, a.SessionConfig.TouchInterval, nowUnix) {
		_ = a.SessionStore.TouchWebSession(r.Context(), queries.TouchWebSessionParams{
			LastSeenAt: nowUnix,
			Selector:   selector,
		})
	}
	return UserContext{
		UserID:           user.ID,
		Role:             user.Role,
		Scopes:           scopes,
		Now:              now,
		CredentialSource: CredentialSession,
		SessionSelector:  selector,
		CSRFHash:         session.CsrfHash,
	}, true
}

func shouldTouchSession(session queries.WebSession, interval time.Duration, nowUnix int64) bool {
	if interval <= 0 {
		return true
	}
	return session.LastSeenAt+int64(interval/time.Second) <= nowUnix
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
