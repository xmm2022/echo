-- name: InsertPlaybackEvent :one
INSERT INTO playback_events (
  request_id, session_id, error_token_id, echo_user_id, library_entry_id, blob_id,
  copy_id, provider, account_id, operation, status, bytes_sent, range_header,
  http_status, failure_kind, failure_message, started_at, finished_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id;

-- name: SumPlaybackBytesInWindow :one
SELECT CAST(COALESCE(SUM(bytes_sent), 0) AS INTEGER) AS bytes
FROM playback_events
WHERE echo_user_id = ?
  AND operation = 'stream'
  AND started_at >= ?
  AND started_at < ?
  AND bytes_sent > 0
  AND status IN ('ok','interrupted','failed','upstream_read_error');

-- name: ListActiveStreamEventIDs :many
SELECT id
FROM playback_events
WHERE echo_user_id = ?
  AND operation = 'stream'
  AND finished_at IS NULL
  AND started_at >= ?
ORDER BY started_at DESC;

-- name: MarkStalePlaybackEventsInterrupted :exec
UPDATE playback_events
SET status = 'interrupted',
    failure_kind = 'process_reconcile',
    finished_at = ?
WHERE operation = 'stream'
  AND finished_at IS NULL
  AND started_at < ?;

-- name: FinishPlaybackEvent :exec
UPDATE playback_events
SET status = ?, bytes_sent = ?, http_status = ?, failure_kind = ?, failure_message = ?, finished_at = ?
WHERE id = ? AND operation = 'stream' AND finished_at IS NULL;
