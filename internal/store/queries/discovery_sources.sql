-- name: CreateDiscoverySource :one
INSERT INTO discovery_sources (
  kind, name, enabled, config_json, secret_ref, rate_limit_json,
  scheduler_state, next_run_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 'healthy', ?, ?, ?)
RETURNING *;

-- name: GetDiscoverySource :one
SELECT * FROM discovery_sources WHERE id = ?;

-- name: ListDiscoverySources :many
SELECT * FROM discovery_sources ORDER BY id DESC LIMIT ? OFFSET ?;

-- name: UpdateDiscoverySource :one
UPDATE discovery_sources
SET name = ?, enabled = ?, config_json = ?, secret_ref = ?, rate_limit_json = ?,
    updated_at = ?
WHERE id = ?
RETURNING *;

-- name: UpdateDiscoverySourceState :exec
UPDATE discovery_sources
SET scheduler_state = ?, backoff_until = ?, failure_count = ?, last_error_kind = ?,
    last_error_message = ?, updated_at = ?
WHERE id = ?;

-- name: ClearDiscoverySourceLease :exec
UPDATE discovery_sources
SET locked_until = NULL, scheduler_state = 'healthy', backoff_until = NULL, failure_count = 0,
    last_success_at = ?, last_error_kind = NULL, last_error_message = NULL,
    next_run_at = ?, updated_at = ?
WHERE id = ?;

-- name: BackoffDiscoverySource :exec
UPDATE discovery_sources
SET locked_until = NULL, scheduler_state = 'backoff', backoff_until = ?,
    failure_count = failure_count + 1, last_error_kind = ?,
    last_error_message = ?, updated_at = ?
WHERE id = ?;

-- name: LeaseDueDiscoverySources :many
UPDATE discovery_sources
SET locked_until = ?, updated_at = ?
WHERE id IN (
  SELECT ds.id FROM discovery_sources AS ds
  WHERE ds.enabled = 1
    AND ds.scheduler_state IN ('healthy','backoff')
    AND (ds.next_run_at IS NULL OR ds.next_run_at <= ?)
    AND (ds.locked_until IS NULL OR ds.locked_until <= ?)
    AND (ds.backoff_until IS NULL OR ds.backoff_until <= ?)
  ORDER BY COALESCE(ds.next_run_at, 0), ds.id
  LIMIT ?
)
RETURNING *;
