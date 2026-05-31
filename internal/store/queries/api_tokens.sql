-- name: CreateAPIToken :exec
INSERT INTO api_tokens (
  id, user_id, name, token_hash, scopes, expires_at, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?
);

-- name: GetAPITokenByHash :one
SELECT * FROM api_tokens
WHERE token_hash = ?;

-- name: ListAPITokensByUser :many
SELECT * FROM api_tokens
WHERE user_id = ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: RevokeAPIToken :exec
UPDATE api_tokens
SET revoked_at = ?
WHERE id = ? AND user_id = ?;

-- name: TouchAPIToken :exec
UPDATE api_tokens
SET last_used_at = ?
WHERE id = ?;
