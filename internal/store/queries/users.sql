-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ?;

-- name: CreateUser :exec
INSERT INTO users (
  id, username, role, status, quota_policy_id, password_hash, created_at, updated_at, last_login_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: UpdateUserStatus :exec
UPDATE users
SET status = ?, updated_at = ?
WHERE id = ?;

-- name: ListUsers :many
SELECT * FROM users
ORDER BY username
LIMIT ? OFFSET ?;

-- name: UpdateUserPasswordHash :exec
UPDATE users
SET password_hash = ?, updated_at = ?
WHERE id = ?;

-- name: TouchUserLogin :exec
UPDATE users
SET last_login_at = ?, updated_at = ?
WHERE id = ?;
