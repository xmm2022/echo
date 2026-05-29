-- name: InsertHashConflict :one
INSERT INTO hash_conflicts (
  blob_id_a, blob_id_b, reason, detail, observed_at, status
) VALUES (
  ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetHashConflict :one
SELECT * FROM hash_conflicts
WHERE id = ?;

-- name: ListOpenHashConflicts :many
SELECT * FROM hash_conflicts
WHERE status = 'open'
ORDER BY observed_at DESC
LIMIT ? OFFSET ?;

-- name: CountOpenHashConflicts :one
SELECT COUNT(*) FROM hash_conflicts
WHERE status = 'open';

-- name: UpdateHashConflictStatus :exec
UPDATE hash_conflicts
SET status = ?
WHERE id = ?;
