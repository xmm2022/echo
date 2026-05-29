-- name: CreateProducerRun :one
INSERT INTO producer_runs (
  job_id, tool, tool_version, cmdline, workdir, output_dir, manifest_path,
  stdout_path, stderr_path, exit_code, started_at, finished_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetProducerRun :one
SELECT * FROM producer_runs
WHERE id = ?;

-- name: ListProducerRunsByJob :many
SELECT * FROM producer_runs
WHERE job_id = ?
ORDER BY id;

-- name: FinishProducerRun :exec
UPDATE producer_runs
SET exit_code = ?,
    finished_at = ?,
    manifest_path = COALESCE(?, manifest_path),
    stdout_path = COALESCE(?, stdout_path),
    stderr_path = COALESCE(?, stderr_path)
WHERE id = ?;
