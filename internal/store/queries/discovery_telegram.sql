-- name: CreateTelegramChannel :one
INSERT INTO telegram_channels (
  source_id, channel_ref, stable_peer_id, username_snapshot, title_snapshot,
  level_flags, enabled, last_message_id, last_message_date, next_run_at,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListTelegramChannelsBySource :many
SELECT * FROM telegram_channels
WHERE source_id = ?
ORDER BY id;

-- name: LeaseDueTelegramChannels :many
UPDATE telegram_channels
SET locked_until = ?, updated_at = ?
WHERE id IN (
  SELECT tc.id FROM telegram_channels AS tc
  WHERE tc.source_id = ?
    AND tc.enabled = 1
    AND (tc.next_run_at IS NULL OR tc.next_run_at <= ?)
    AND (tc.locked_until IS NULL OR tc.locked_until <= ?)
    AND (tc.backoff_until IS NULL OR tc.backoff_until <= ?)
  ORDER BY COALESCE(tc.next_run_at, 0), tc.id
  LIMIT ?
)
RETURNING *;

-- name: UpdateTelegramChannelCursor :exec
UPDATE telegram_channels
SET last_message_id = ?, last_message_date = ?, failure_count = 0,
    last_error_kind = NULL, last_error_message = NULL, locked_until = NULL,
    updated_at = ?
WHERE id = ?;

-- name: BackoffTelegramChannel :exec
UPDATE telegram_channels
SET backoff_until = ?, failure_count = failure_count + 1,
    last_error_kind = ?, last_error_message = ?, locked_until = NULL,
    updated_at = ?
WHERE id = ?;
