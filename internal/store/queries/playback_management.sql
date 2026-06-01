-- name: ListPlaybackSessionsForAdmin :many
SELECT * FROM playback_sessions
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: ListPlaybackSessionsForUser :many
SELECT * FROM playback_sessions
WHERE echo_user_id = ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: ListPlaybackEventsForAdmin :many
SELECT * FROM playback_events
WHERE (? = '' OR echo_user_id = ?)
ORDER BY started_at DESC, id DESC
LIMIT ? OFFSET ?;

-- name: ListPlaybackEventsForUser :many
SELECT * FROM playback_events
WHERE echo_user_id = ?
ORDER BY started_at DESC, id DESC
LIMIT ? OFFSET ?;

-- name: GetQuotaUsageFromEvents :one
SELECT
  COALESCE(SUM(bytes_sent), 0) AS bytes_used,
  COUNT(*) AS stream_count
FROM playback_events
WHERE echo_user_id = ?
  AND operation = 'stream'
  AND status IN ('ok','interrupted','failed','upstream_read_error')
  AND bytes_sent > 0
  AND started_at >= ?
  AND started_at < ?;

-- name: CountActivePlaybackSessions :one
SELECT COUNT(*)
FROM playback_sessions
WHERE state = 'active'
  AND expires_at > unixepoch();

-- name: ListAccountPoolAssignments :many
SELECT * FROM account_pool_assignments
WHERE (? = '' OR echo_user_id = ?)
ORDER BY echo_user_id, provider, priority, account_id
LIMIT ? OFFSET ?;

-- name: UpsertAccountPoolAssignment :one
INSERT INTO account_pool_assignments (
  echo_user_id, account_id, provider, priority, weight, max_concurrent_streams,
  daily_bytes_limit, enabled, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(echo_user_id, account_id) DO UPDATE SET
  provider = excluded.provider,
  priority = excluded.priority,
  weight = excluded.weight,
  max_concurrent_streams = excluded.max_concurrent_streams,
  daily_bytes_limit = excluded.daily_bytes_limit,
  enabled = excluded.enabled,
  updated_at = excluded.updated_at
RETURNING *;

-- name: UpdateQuotaPolicy :one
UPDATE quota_policies
SET
  name = ?,
  period = ?,
  max_bytes = ?,
  max_streams = ?,
  max_playback_sessions = ?,
  version = version + 1,
  updated_at = ?
WHERE id = ?
RETURNING *;
