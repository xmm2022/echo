-- name: CreateBlob :one
INSERT INTO blobs (
  size, canonical_name, source_mtime, owner_id, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetBlob :one
SELECT * FROM blobs
WHERE id = ?;

-- name: ListBlobs :many
SELECT * FROM blobs
ORDER BY id
LIMIT ? OFFSET ?;

-- name: DeleteBlob :exec
DELETE FROM blobs
WHERE id = ?;
