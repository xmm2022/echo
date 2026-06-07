-- name: CreateWebSession :exec
INSERT INTO web_sessions (
  selector, user_id, secret_hash, csrf_hash, scopes, user_agent, ip_hint,
  created_at, last_seen_at, expires_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: GetWebSession :one
SELECT * FROM web_sessions
WHERE selector = ?;

-- name: TouchWebSession :exec
UPDATE web_sessions
SET last_seen_at = ?
WHERE selector = ? AND revoked_at IS NULL;

-- name: UpdateWebSessionCSRF :exec
UPDATE web_sessions
SET csrf_hash = ?
WHERE selector = ? AND revoked_at IS NULL;

-- name: RevokeWebSession :exec
UPDATE web_sessions
SET revoked_at = ?
WHERE selector = ? AND revoked_at IS NULL;

-- name: RevokeWebSessionsForUser :exec
UPDATE web_sessions
SET revoked_at = ?
WHERE user_id = ? AND revoked_at IS NULL;

-- name: DeleteExpiredWebSessions :exec
DELETE FROM web_sessions
WHERE expires_at <= ? OR (revoked_at IS NOT NULL AND revoked_at <= ?);
