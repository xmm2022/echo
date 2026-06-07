package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	defaultSessionCookieName = "echo_session"
	defaultSessionTTL        = 7 * 24 * time.Hour
	dummyPasswordHash        = "argon2id$v=19$m=65536,t=3,p=1$MDEyMzQ1Njc4OWFiY2RlZg$TlSv8OgAsb0JTRrO7aFk/PPVafXgJMh8znf0ilxw5oE"
)

type SessionHTTPConfig struct {
	CookieName        string
	TTL               time.Duration
	SecureCookies     string // auto|always|never
	TrustProxyHeaders bool
}

type loginSessionRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionUserDTO struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Role     string   `json:"role"`
	Scopes   []string `json:"scopes"`
}

type sessionMeResponse struct {
	Authenticated bool            `json:"authenticated"`
	User          *sessionUserDTO `json:"user,omitempty"`
	CSRFToken     string          `json:"csrf_token,omitempty"`
}

type bootstrapAdminPasswordRequest struct {
	Password string `json:"password"`
}

func (d APIDeps) LoginSession(cfg SessionHTTPConfig) http.HandlerFunc {
	cfg = normalizeSessionHTTPConfig(cfg)
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginWrite(r, cfg) {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}

		var req loginSessionRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		user, scopes, ok, err := d.authenticateLogin(r, strings.TrimSpace(req.Username), req.Password)
		if err != nil {
			d.logger().Error("session login: get user", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}
		if !ok {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		token, selector, secret, err := auth.GenerateSessionToken()
		if err != nil {
			d.logger().Error("session login: generate token", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}
		csrfToken, err := auth.GenerateCSRFToken()
		if err != nil {
			d.logger().Error("session login: generate csrf", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}
		scopesJSON, err := json.Marshal(scopes)
		if err != nil {
			d.logger().Error("session login: marshal scopes", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}

		now := d.now().Unix()
		if err := d.Store.CreateWebSession(r.Context(), queries.CreateWebSessionParams{
			Selector:   selector,
			UserID:     user.ID,
			SecretHash: auth.HashToken(secret),
			CsrfHash:   auth.HashToken(csrfToken),
			Scopes:     string(scopesJSON),
			UserAgent:  nullString(strings.TrimSpace(r.UserAgent())),
			IpHint:     nullString(ipHint(r)),
			CreatedAt:  now,
			LastSeenAt: now,
			ExpiresAt:  now + int64(cfg.TTL/time.Second),
		}); err != nil {
			d.logger().Error("session login: create", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}
		if err := d.Store.TouchUserLogin(r.Context(), queries.TouchUserLoginParams{
			LastLoginAt: sql.NullInt64{Int64: now, Valid: true},
			UpdatedAt:   now,
			ID:          user.ID,
		}); err != nil {
			d.logger().Error("session login: touch user", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     cfg.CookieName,
			Value:    token,
			Path:     "/",
			MaxAge:   int(cfg.TTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   secureCookie(r, cfg),
		})
		writeJSON(w, http.StatusOK, sessionMeResponse{
			Authenticated: true,
			User:          userDTO(user, scopes),
			CSRFToken:     csrfToken,
		})
	}
}

func (d APIDeps) GetSessionMe(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.FromContext(r.Context())
	if userCtx.UserID == "" {
		writeJSON(w, http.StatusOK, sessionMeResponse{Authenticated: false})
		return
	}

	user, err := d.Store.GetUser(r.Context(), queries.GetUserParams{ID: userCtx.UserID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		d.logger().Error("session me: get user", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if user.Status != "active" {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	resp := sessionMeResponse{
		Authenticated: true,
		User:          userDTO(user, responseScopes(userCtx, user.Role)),
	}
	if userCtx.CredentialSource == auth.CredentialSession && userCtx.SessionSelector != "" {
		csrfToken, err := auth.GenerateCSRFToken()
		if err != nil {
			d.logger().Error("session me: generate csrf", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}
		if err := d.Store.UpdateWebSessionCSRF(r.Context(), queries.UpdateWebSessionCSRFParams{
			CsrfHash: auth.HashToken(csrfToken),
			Selector: userCtx.SessionSelector,
		}); err != nil {
			d.logger().Error("session me: rotate csrf", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal-error")
			return
		}
		resp.CSRFToken = csrfToken
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d APIDeps) LogoutSession(cfg SessionHTTPConfig) http.HandlerFunc {
	cfg = normalizeSessionHTTPConfig(cfg)
	return func(w http.ResponseWriter, r *http.Request) {
		userCtx := auth.FromContext(r.Context())
		if userCtx.CredentialSource == auth.CredentialSession && userCtx.SessionSelector != "" {
			if err := d.Store.RevokeWebSession(r.Context(), queries.RevokeWebSessionParams{
				RevokedAt: sql.NullInt64{Int64: d.now().Unix(), Valid: true},
				Selector:  userCtx.SessionSelector,
			}); err != nil {
				d.logger().Error("session logout: revoke", "err", err)
				writeAPIError(w, http.StatusInternalServerError, "internal-error")
				return
			}
		}
		http.SetCookie(w, &http.Cookie{
			Name:     cfg.CookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   secureCookie(r, cfg),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d APIDeps) SetBootstrapAdminPassword(w http.ResponseWriter, r *http.Request) {
	if d.BootstrapAdminToken == "" || !validBearer(r.Header.Get("Authorization"), d.BootstrapAdminToken) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req bootstrapAdminPasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Password) < 12 || req.Password != strings.TrimSpace(req.Password) {
		writeAPIError(w, http.StatusBadRequest, "invalid password")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		d.logger().Error("bootstrap password: hash", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	now := d.now().Unix()
	tx, q, err := d.Store.BeginImmediateTx(r.Context())
	if err != nil {
		d.logger().Error("bootstrap password: begin tx", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(r.Context())
		}
	}()

	if err := q.UpdateUserPasswordHash(r.Context(), queries.UpdateUserPasswordHashParams{
		PasswordHash: sql.NullString{String: hash, Valid: true},
		UpdatedAt:    now,
		ID:           "admin",
	}); err != nil {
		d.logger().Error("bootstrap password: update", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if err := q.RevokeWebSessionsForUser(r.Context(), queries.RevokeWebSessionsForUserParams{
		RevokedAt: sql.NullInt64{Int64: now, Valid: true},
		UserID:    "admin",
	}); err != nil {
		d.logger().Error("bootstrap password: revoke sessions", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		d.logger().Error("bootstrap password: commit", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal-error")
		return
	}
	committed = true
	w.WriteHeader(http.StatusNoContent)
}

func secureCookie(r *http.Request, cfg SessionHTTPConfig) bool {
	switch strings.ToLower(strings.TrimSpace(cfg.SecureCookies)) {
	case "always":
		return true
	case "never":
		return false
	}
	if r.TLS != nil {
		return true
	}
	return cfg.TrustProxyHeaders && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func sameOriginWrite(r *http.Request, cfg SessionHTTPConfig) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	reqScheme := effectiveRequestScheme(r, cfg)
	return strings.EqualFold(u.Scheme, reqScheme) &&
		strings.EqualFold(normalizeOriginHost(u.Scheme, u.Host), normalizeOriginHost(reqScheme, requestHost(r)))
}

func (d APIDeps) authenticateLogin(r *http.Request, username, password string) (queries.User, []string, bool, error) {
	if username == "" || password == "" {
		_ = auth.VerifyPassword(password, dummyPasswordHash)
		return queries.User{}, nil, false, nil
	}
	user, err := d.Store.GetUserByUsername(r.Context(), queries.GetUserByUsernameParams{Username: username})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_ = auth.VerifyPassword(password, dummyPasswordHash)
			return queries.User{}, nil, false, nil
		}
		return queries.User{}, nil, false, err
	}

	passwordHash := dummyPasswordHash
	if user.PasswordHash.Valid {
		passwordHash = user.PasswordHash.String
	}
	passwordOK := auth.VerifyPassword(password, passwordHash)
	if user.Status != "active" || !user.PasswordHash.Valid || !passwordOK {
		return queries.User{}, nil, false, nil
	}
	scopes, ok := auth.ScopesForRole(user.Role)
	if !ok {
		return queries.User{}, nil, false, nil
	}
	return user, scopes, true, nil
}

func normalizeSessionHTTPConfig(cfg SessionHTTPConfig) SessionHTTPConfig {
	if strings.TrimSpace(cfg.CookieName) == "" {
		cfg.CookieName = defaultSessionCookieName
	}
	if cfg.TTL <= 0 {
		cfg.TTL = defaultSessionTTL
	}
	if strings.TrimSpace(cfg.SecureCookies) == "" {
		cfg.SecureCookies = "auto"
	}
	return cfg
}

func userDTO(user queries.User, scopes []string) *sessionUserDTO {
	return &sessionUserDTO{
		ID:       user.ID,
		Username: user.Username,
		Role:     user.Role,
		Scopes:   scopes,
	}
}

func responseScopes(userCtx auth.UserContext, role string) []string {
	if len(userCtx.Scopes) > 0 {
		return userCtx.Scopes
	}
	scopes, _ := auth.ScopesForRole(role)
	return scopes
}

func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func ipHint(r *http.Request) string {
	if r.RemoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func effectiveRequestScheme(r *http.Request, cfg SessionHTTPConfig) string {
	if cfg.TrustProxyHeaders {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))) {
		case "https":
			return "https"
		case "http":
			return "http"
		}
	}
	if r.TLS != nil || strings.EqualFold(r.URL.Scheme, "https") {
		return "https"
	}
	return "http"
}

func requestHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

func normalizeOriginHost(scheme, host string) string {
	hostname, port, err := net.SplitHostPort(host)
	if err != nil {
		hostname = host
		port = ""
	}
	if port == defaultPortForScheme(scheme) {
		port = ""
	}
	hostname = strings.ToLower(hostname)
	if port == "" {
		return hostname
	}
	return hostname + ":" + port
}

func defaultPortForScheme(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}
