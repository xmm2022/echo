-- name: InsertHashConflict :one
INSERT INTO hash_conflicts (
  blob_id_a, blob_id_b, reason, detail, observed_at, status
) VALUES (
  ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: ListOpenHashConflicts :many
SELECT * FROM hash_conflicts
WHERE status = 'open'
ORDER BY observed_at DESC
LIMIT ? OFFSET ?;

-- name: UpdateHashConflictStatus :exec
UPDATE hash_conflicts
SET status = ?
WHERE id = ?;
