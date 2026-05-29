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

-- name: ListJobs :many
SELECT * FROM jobs
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?;

-- name: ListJobIDsByStatus :many
SELECT id FROM jobs
WHERE status = ?
ORDER BY created_at, id;

-- name: MarkJobRunning :exec
UPDATE jobs
SET status = 'running',
    started_at = ?
WHERE id = ?;

-- name: UpdateJobProgress :exec
UPDATE jobs
SET progress = ?
WHERE id = ?;

-- name: FailRunningJobs :exec
UPDATE jobs
SET status = 'failed',
    error = ?,
    finished_at = ?
WHERE status = 'running';

-- name: FinishJob :exec
UPDATE jobs
SET status = ?,
    error = ?,
    finished_at = ?
WHERE id = ?;
