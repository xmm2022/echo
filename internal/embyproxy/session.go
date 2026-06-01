// Package embyproxy mints and verifies the split tokens (selector.secret) that
// back playback sessions and error tokens for the Emby reverse proxy. Tokens are
// stored as a plaintext selector plus a hash of only the secret half, so a leaked
// database row cannot reconstruct a usable token, and lookups never log the
// selector, secret, or full token.
package embyproxy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store/queries"
)

// Default token lifetimes used when SessionConfig leaves a TTL at zero. Production
// callers can override either; the tests pass explicit values.
const (
	defaultSessionTTL = 12 * time.Hour
	defaultErrorTTL   = 5 * time.Minute
)

var (
	// ErrInvalidToken is returned when a token is malformed, references no row, or
	// fails secret verification. It is deliberately indistinguishable across those
	// cases so callers cannot probe which selectors exist.
	ErrInvalidToken = errors.New("invalid playback token")
	// ErrExpiredToken is returned when a token's row exists and verifies but its
	// expires_at has passed relative to the injected clock.
	ErrExpiredToken = errors.New("expired playback token")
	// ErrRevokedToken is returned when a playback session row is in the 'revoked' state.
	ErrRevokedToken = errors.New("revoked playback token")
)

// SessionQuerier is the subset of *queries.Queries the SessionManager needs.
// *queries.Queries satisfies it directly, so callers pass store.Queries.
type SessionQuerier interface {
	CreatePlaybackSession(context.Context, queries.CreatePlaybackSessionParams) error
	GetPlaybackSessionBySelector(context.Context, queries.GetPlaybackSessionBySelectorParams) (queries.PlaybackSession, error)
	CreatePlaybackErrorToken(context.Context, queries.CreatePlaybackErrorTokenParams) error
	GetPlaybackErrorTokenBySelector(context.Context, queries.GetPlaybackErrorTokenBySelectorParams) (queries.PlaybackErrorToken, error)
}

// SessionConfig sets token lifetimes. A zero TTL/ErrorTTL falls back to the package
// defaults.
type SessionConfig struct {
	TTL      time.Duration
	ErrorTTL time.Duration
}

// SessionManager mints and verifies playback-session and error tokens. now is
// injected so expiry is deterministic in tests; nil means time.Now.
type SessionManager struct {
	q   SessionQuerier
	cfg SessionConfig
	now func() time.Time
}

// NewSessionManager builds a SessionManager, applying default TTLs for any field
// left at zero so production callers need not set them explicitly.
func NewSessionManager(q SessionQuerier, cfg SessionConfig, now func() time.Time) *SessionManager {
	if cfg.TTL == 0 {
		cfg.TTL = defaultSessionTTL
	}
	if cfg.ErrorTTL == 0 {
		cfg.ErrorTTL = defaultErrorTTL
	}
	return &SessionManager{q: q, cfg: cfg, now: now}
}

func (m *SessionManager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// CreatePlaybackSessionInput describes a new active playback session. LibraryEntryID
// and BlobID reference real rows (foreign_keys is ON), so the caller resolves them
// before minting the session.
type CreatePlaybackSessionInput struct {
	EchoUserID     string
	EmbyServerID   string
	EmbyUserID     string
	DeviceID       string
	ItemID         string
	MediaSourceID  string
	LibraryEntryID int64
	BlobID         int64
}

// CreatePlaybackSession mints a selector.secret token, stores the selector plus the
// hash of only the secret, and returns the full token alongside the canonical stored
// row (re-read by selector). The full token is the only place the secret is ever
// surfaced; it is never logged.
func (m *SessionManager) CreatePlaybackSession(ctx context.Context, in CreatePlaybackSessionInput) (string, queries.PlaybackSession, error) {
	full, err := auth.GenerateToken()
	if err != nil {
		return "", queries.PlaybackSession{}, err
	}
	selector, secret, ok := splitToken(full)
	if !ok {
		return "", queries.PlaybackSession{}, ErrInvalidToken
	}
	id, err := newID("sess")
	if err != nil {
		return "", queries.PlaybackSession{}, err
	}
	now := m.clock()
	if err := m.q.CreatePlaybackSession(ctx, queries.CreatePlaybackSessionParams{
		ID:                id,
		Selector:          selector,
		TokenHash:         auth.HashToken(secret),
		EchoUserID:        in.EchoUserID,
		EmbyServerID:      in.EmbyServerID,
		EmbyUserID:        in.EmbyUserID,
		DeviceID:          nullString(in.DeviceID),
		ItemID:            in.ItemID,
		MediaSourceID:     in.MediaSourceID,
		EmbyPlaySessionID: sql.NullString{},
		LibraryEntryID:    sql.NullInt64{Int64: in.LibraryEntryID, Valid: true},
		BlobID:            sql.NullInt64{Int64: in.BlobID, Valid: true},
		PreferProvider:    sql.NullString{},
		State:             "active",
		FailureReason:     sql.NullString{},
		CreatedAt:         now.Unix(),
		LastSeenAt:        now.Unix(),
		ExpiresAt:         now.Add(m.cfg.TTL).Unix(),
	}); err != nil {
		return "", queries.PlaybackSession{}, err
	}
	row, err := m.q.GetPlaybackSessionBySelector(ctx, queries.GetPlaybackSessionBySelectorParams{Selector: selector})
	if err != nil {
		return "", queries.PlaybackSession{}, err
	}
	return full, row, nil
}

// LookupPlaybackSession resolves token to its active session row, returning
// ErrInvalidToken for malformed/unknown/mismatched tokens, ErrRevokedToken for
// revoked sessions, and ErrExpiredToken once expires_at has passed.
func (m *SessionManager) LookupPlaybackSession(ctx context.Context, token string) (queries.PlaybackSession, error) {
	selector, secret, ok := splitToken(token)
	if !ok {
		return queries.PlaybackSession{}, ErrInvalidToken
	}
	row, err := m.q.GetPlaybackSessionBySelector(ctx, queries.GetPlaybackSessionBySelectorParams{Selector: selector})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.PlaybackSession{}, ErrInvalidToken
	}
	if err != nil {
		return queries.PlaybackSession{}, err
	}
	if !auth.VerifyTokenHash(secret, row.TokenHash) {
		return queries.PlaybackSession{}, ErrInvalidToken
	}
	if row.State == "revoked" {
		return queries.PlaybackSession{}, ErrRevokedToken
	}
	if m.clock().Unix() >= row.ExpiresAt {
		return queries.PlaybackSession{}, ErrExpiredToken
	}
	if row.State != "active" {
		return queries.PlaybackSession{}, ErrInvalidToken
	}
	return row, nil
}

// CreateErrorTokenInput describes a short-lived error token surfaced to the player.
// EchoUserID/EmbyServerID are nullable because some failures occur before the
// request is mapped to an identity.
type CreateErrorTokenInput struct {
	EchoUserID    sql.NullString
	EmbyServerID  sql.NullString
	EmbyUserID    string
	ItemID        string
	MediaSourceID string
	Reason        string
	HTTPStatus    int
}

// CreateErrorToken mints an error token recording only a safe reason and the HTTP
// status. It refuses to create a token for a non-error status (outside 4xx/5xx) so
// the error path can never masquerade as success.
func (m *SessionManager) CreateErrorToken(ctx context.Context, in CreateErrorTokenInput) (string, queries.PlaybackErrorToken, error) {
	if in.HTTPStatus < 400 || in.HTTPStatus > 599 {
		return "", queries.PlaybackErrorToken{}, errors.New("error token http status must be 4xx or 5xx")
	}
	full, err := auth.GenerateToken()
	if err != nil {
		return "", queries.PlaybackErrorToken{}, err
	}
	selector, secret, ok := splitToken(full)
	if !ok {
		return "", queries.PlaybackErrorToken{}, ErrInvalidToken
	}
	id, err := newID("err")
	if err != nil {
		return "", queries.PlaybackErrorToken{}, err
	}
	now := m.clock()
	if err := m.q.CreatePlaybackErrorToken(ctx, queries.CreatePlaybackErrorTokenParams{
		ID:            id,
		Selector:      selector,
		TokenHash:     auth.HashToken(secret),
		EchoUserID:    in.EchoUserID,
		EmbyServerID:  in.EmbyServerID,
		EmbyUserID:    nullString(in.EmbyUserID),
		DeviceID:      sql.NullString{},
		ItemID:        nullString(in.ItemID),
		MediaSourceID: nullString(in.MediaSourceID),
		Reason:        in.Reason,
		HttpStatus:    int64(in.HTTPStatus),
		CreatedAt:     now.Unix(),
		ExpiresAt:     now.Add(m.cfg.ErrorTTL).Unix(),
	}); err != nil {
		return "", queries.PlaybackErrorToken{}, err
	}
	row, err := m.q.GetPlaybackErrorTokenBySelector(ctx, queries.GetPlaybackErrorTokenBySelectorParams{Selector: selector})
	if err != nil {
		return "", queries.PlaybackErrorToken{}, err
	}
	return full, row, nil
}

// LookupErrorToken resolves token to its error-token row, returning ErrInvalidToken
// for malformed/unknown/mismatched tokens and ErrExpiredToken once expires_at has
// passed. Error tokens have no revocable state.
func (m *SessionManager) LookupErrorToken(ctx context.Context, token string) (queries.PlaybackErrorToken, error) {
	selector, secret, ok := splitToken(token)
	if !ok {
		return queries.PlaybackErrorToken{}, ErrInvalidToken
	}
	row, err := m.q.GetPlaybackErrorTokenBySelector(ctx, queries.GetPlaybackErrorTokenBySelectorParams{Selector: selector})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.PlaybackErrorToken{}, ErrInvalidToken
	}
	if err != nil {
		return queries.PlaybackErrorToken{}, err
	}
	if !auth.VerifyTokenHash(secret, row.TokenHash) {
		return queries.PlaybackErrorToken{}, ErrInvalidToken
	}
	if m.clock().Unix() >= row.ExpiresAt {
		return queries.PlaybackErrorToken{}, ErrExpiredToken
	}
	return row, nil
}

// splitToken divides a selector.secret token on its single separator. base64url
// (RawURLEncoding) never contains a '.', so SplitN on the first '.' is exact; ok is
// false unless both halves are non-empty.
func splitToken(token string) (selector, secret string, ok bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// nullString wraps a value, treating empty as NULL.
func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// newID returns a prefixed random ID (prefix_<base64url(16 bytes)>), matching the
// convention in internal/http/handlers. It is a local copy by design: the handler's
// newID is unexported and this package must not depend on it.
func newID(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}
