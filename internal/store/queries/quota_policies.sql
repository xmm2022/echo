-- name: GetQuotaPolicy :one
SELECT * FROM quota_policies WHERE id = ?;

-- name: ListQuotaPolicies :many
SELECT * FROM quota_policies ORDER BY id LIMIT ? OFFSET ?;

-- name: CreateQuotaPolicy :one
INSERT INTO quota_policies (
  name, period, max_bytes, max_streams, max_playback_sessions, version, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, 1, ?, ?
)
RETURNING *;
