-- name: CreateJob :one
INSERT INTO jobs (
  kind, status, payload, progress, error, owner_id, created_at, started_at, finished_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetJob :one
SELECT * FROM jobs
WHERE id = ?;

-- name: DeleteJob :exec
DELETE FROM jobs
WHERE id = ?;

-- name: ListJobsByStatus :many
SELECT * FROM jobs
WHERE status = ?
ORDER BY created_at, id
LIMIT ?;

-- name: MarkJobRunning :exec
UPDATE jobs
SET status = 'running',
    started_at = ?
WHERE id = ?;

-- name: UpdateJobProgress :exec
UPDATE jobs
SET progress = ?
WHERE id = ?;

-- name: FinishJob :exec
UPDATE jobs
SET status = ?,
    error = ?,
    finished_at = ?
WHERE id = ?;
