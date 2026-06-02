-- name: CreateDiscoveryRun :one
INSERT INTO discovery_runs (
  kind, source_id, subscription_id, job_id, status, counters_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: FinishDiscoveryRun :exec
UPDATE discovery_runs
SET status = ?, counters_json = ?, error_kind = ?, error_message = ?,
    started_at = COALESCE(started_at, ?), finished_at = ?
WHERE id = ?;

-- name: CreateDiscoveryRawAccessEvent :exec
INSERT INTO discovery_raw_access_events (
  resource_id, actor_user_id, request_id, response_bytes, redacted, accessed_at
) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListDiscoveryRuns :many
SELECT * FROM discovery_runs ORDER BY created_at DESC LIMIT ? OFFSET ?;
