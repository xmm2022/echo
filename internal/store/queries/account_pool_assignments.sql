-- name: CreateAccountPoolAssignment :one
INSERT INTO account_pool_assignments (
  echo_user_id, account_id, provider, priority, weight, max_concurrent_streams,
  daily_bytes_limit, enabled, created_at, updated_at
)
SELECT
  sqlc.arg(echo_user_id), accounts.id, accounts.provider, sqlc.arg(priority),
  sqlc.arg(weight), sqlc.arg(max_concurrent_streams), sqlc.arg(daily_bytes_limit),
  sqlc.arg(enabled), sqlc.arg(created_at), sqlc.arg(updated_at)
FROM accounts
WHERE accounts.id = sqlc.arg(account_id)
  AND accounts.provider = sqlc.arg(provider)
RETURNING *;

-- name: CountActiveStreamsForAccount :one
SELECT COUNT(*)
FROM playback_events
WHERE account_id = ?
  AND operation = 'stream'
  AND finished_at IS NULL
  AND started_at >= ?;

-- name: SumAccountPlaybackBytesInWindow :one
SELECT CAST(COALESCE(SUM(bytes_sent), 0) AS INTEGER) AS bytes
FROM playback_events
WHERE account_id = ?
  AND operation = 'stream'
  AND started_at >= ?
  AND started_at < ?
  AND bytes_sent > 0
  AND status IN ('ok','interrupted','failed','upstream_read_error');

-- name: ListPlayableCopiesForUser :many
SELECT
  fc.id, fc.blob_id, fc.provider, fc.account_id, fc.sidecar_id, fc.storage_mount,
  fc.remote_path, fc.cloud_file_id, fc.pickcode, fc.status, fc.last_seen,
  fc.scheduler_state, fc.cooldown_until, fc.verify_after, fc.failure_count,
  fc.last_failure_at, fc.last_failure_kind, fc.last_failure_confidence,
  fc.last_failure_code, fc.last_failure_message, fc.dead_reason, fc.dead_at,
  apa.priority, apa.weight, apa.max_concurrent_streams, apa.daily_bytes_limit,
  CAST(CASE WHEN fc.provider = sqlc.arg(prefer_provider) THEN 0 ELSE 1 END AS INTEGER) AS provider_rank
FROM file_copies fc
JOIN account_pool_assignments apa ON apa.account_id = fc.account_id
JOIN accounts a ON a.id = fc.account_id
WHERE fc.blob_id = sqlc.arg(blob_id)
  AND apa.echo_user_id = sqlc.arg(echo_user_id)
  AND apa.provider = a.provider
  AND apa.provider = fc.provider
  AND apa.enabled = 1
  AND fc.status = 'live'
  AND fc.scheduler_state NOT IN ('confirmed_dead','suspect_dead')
  AND (fc.cooldown_until IS NULL OR fc.cooldown_until <= sqlc.arg(now))
  AND a.status NOT IN ('disabled','deleted')
  AND a.scheduler_state NOT IN ('cooldown','unhealthy','token_suspect','disabled')
  AND (a.cooldown_until IS NULL OR a.cooldown_until <= sqlc.arg(now))
  AND (
    apa.max_concurrent_streams IS NULL OR
    (
      SELECT COUNT(*) FROM playback_events pe
      WHERE pe.account_id = fc.account_id
        AND pe.operation = 'stream'
        AND pe.finished_at IS NULL
        AND pe.started_at >= sqlc.arg(active_since)
    ) < apa.max_concurrent_streams
  )
  AND (
    apa.daily_bytes_limit IS NULL OR
    (
      SELECT COALESCE(SUM(pe.bytes_sent), 0) FROM playback_events pe
      WHERE pe.account_id = fc.account_id
        AND pe.operation = 'stream'
        AND pe.started_at >= sqlc.arg(day_start)
        AND pe.started_at < sqlc.arg(day_end)
        AND pe.bytes_sent > 0
        AND pe.status IN ('ok','interrupted','failed','upstream_read_error')
    ) < apa.daily_bytes_limit
  )
ORDER BY provider_rank ASC, apa.priority ASC, fc.last_seen DESC
LIMIT sqlc.arg(limit);
