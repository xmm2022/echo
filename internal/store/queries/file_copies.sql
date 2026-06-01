-- name: GetFileCopyByRemotePath :one
SELECT * FROM file_copies
WHERE sidecar_id = ? AND storage_mount = ? AND remote_path = ?;

-- name: GetFileCopyByID :one
SELECT * FROM file_copies WHERE id = ?;

-- name: InsertFileCopy :one
INSERT INTO file_copies (
  blob_id, provider, account_id, sidecar_id, storage_mount, remote_path,
  cloud_file_id, pickcode, status, last_seen
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: UpsertFileCopyLive :one
INSERT INTO file_copies (
  blob_id, provider, account_id, sidecar_id, storage_mount, remote_path,
  cloud_file_id, pickcode, status, last_seen
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, 'live', ?
)
ON CONFLICT(sidecar_id, storage_mount, remote_path) DO UPDATE
SET status = 'live',
    last_seen = excluded.last_seen,
    cloud_file_id = COALESCE(excluded.cloud_file_id, file_copies.cloud_file_id),
    pickcode = COALESCE(excluded.pickcode, file_copies.pickcode),
    -- A successful re-ingest revives a copy that was suspect/confirmed dead: reset
    -- the scheduler state and failure fields so the 0.3 live-copy filter
    -- (scheduler_state NOT IN ('confirmed_dead','suspect_dead')) stops hiding it.
    scheduler_state = 'healthy',
    cooldown_until = NULL,
    verify_after = NULL,
    failure_count = 0,
    last_failure_at = NULL,
    last_failure_kind = NULL,
    last_failure_confidence = NULL,
    last_failure_code = NULL,
    last_failure_message = NULL,
    dead_reason = NULL,
    dead_at = NULL
RETURNING *;

-- name: UpdateFileCopyLive :exec
UPDATE file_copies
SET status = 'live',
    last_seen = ?,
    cloud_file_id = COALESCE(?, cloud_file_id),
    pickcode = COALESCE(?, pickcode),
    -- A successful re-ingest revives a copy that was suspect/confirmed dead (this is
    -- the path the ingest pipeline takes for an already-known remote path): reset the
    -- scheduler state and failure fields so the 0.3 live-copy filter stops hiding it.
    scheduler_state = 'healthy',
    cooldown_until = NULL,
    verify_after = NULL,
    failure_count = 0,
    last_failure_at = NULL,
    last_failure_kind = NULL,
    last_failure_confidence = NULL,
    last_failure_code = NULL,
    last_failure_message = NULL,
    dead_reason = NULL,
    dead_at = NULL
WHERE id = ?;

-- name: MarkFileCopyDead :exec
UPDATE file_copies
SET status = 'dead',
    last_seen = ?
WHERE id = ?;

-- name: ListLiveCopiesByBlob :many
SELECT
  file_copies.id, file_copies.blob_id, file_copies.provider, file_copies.account_id,
  file_copies.sidecar_id, file_copies.storage_mount, file_copies.remote_path,
  file_copies.cloud_file_id, file_copies.pickcode, file_copies.status, file_copies.last_seen,
  file_copies.scheduler_state, file_copies.cooldown_until, file_copies.verify_after,
  file_copies.failure_count, file_copies.last_failure_at, file_copies.last_failure_kind,
  file_copies.last_failure_confidence, file_copies.last_failure_code, file_copies.last_failure_message,
  file_copies.dead_reason, file_copies.dead_at
FROM file_copies
JOIN accounts ON accounts.id = file_copies.account_id
WHERE file_copies.blob_id = ? AND file_copies.status = 'live'
  AND file_copies.scheduler_state NOT IN ('confirmed_dead','suspect_dead')
  AND (file_copies.cooldown_until IS NULL OR file_copies.cooldown_until <= unixepoch())
  AND accounts.status NOT IN ('disabled','deleted')
  AND accounts.scheduler_state NOT IN ('cooldown','unhealthy','token_suspect','disabled')
  AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= unixepoch())
ORDER BY file_copies.last_seen DESC
LIMIT ?;

-- name: ListLiveCopiesByBlobPreferProvider :many
SELECT
  id, blob_id, provider, account_id, sidecar_id, storage_mount, remote_path,
  cloud_file_id, pickcode, status, last_seen, scheduler_state, cooldown_until,
  verify_after, failure_count, last_failure_at, last_failure_kind,
  last_failure_confidence, last_failure_code, last_failure_message, dead_reason, dead_at
FROM (
  SELECT
    file_copies.*,
    CASE WHEN file_copies.provider = @preferred_provider THEN 0 ELSE 1 END AS provider_rank
  FROM file_copies
  JOIN accounts ON accounts.id = file_copies.account_id
  WHERE file_copies.blob_id = @blob_id AND file_copies.status = 'live'
    AND file_copies.scheduler_state NOT IN ('confirmed_dead','suspect_dead')
    AND (file_copies.cooldown_until IS NULL OR file_copies.cooldown_until <= unixepoch())
    AND accounts.status NOT IN ('disabled','deleted')
    AND accounts.scheduler_state NOT IN ('cooldown','unhealthy','token_suspect','disabled')
    AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= unixepoch())
)
ORDER BY provider_rank,
         last_seen DESC
LIMIT @limit;

-- name: MarkFileCopyConfirmedDead :exec
UPDATE file_copies
SET status = 'dead',
    scheduler_state = 'confirmed_dead',
    failure_count = failure_count + 1,
    last_failure_at = ?,
    last_failure_kind = ?,
    last_failure_confidence = 'confirmed',
    last_failure_code = ?,
    last_failure_message = ?,
    dead_reason = ?,
    dead_at = ?
WHERE id = ?;

-- name: MarkFileCopySuspectDead :exec
UPDATE file_copies
SET scheduler_state = 'suspect_dead',
    failure_count = failure_count + 1,
    last_failure_at = ?,
    last_failure_kind = ?,
    last_failure_confidence = 'suspect',
    last_failure_code = ?,
    last_failure_message = ?,
    verify_after = ?
WHERE id = ?;

-- name: ClearFileCopySchedulerFailure :exec
UPDATE file_copies
SET scheduler_state = 'healthy',
    cooldown_until = NULL,
    verify_after = NULL,
    failure_count = 0,
    last_failure_at = NULL,
    last_failure_kind = NULL,
    last_failure_confidence = NULL,
    last_failure_code = NULL,
    last_failure_message = NULL
WHERE id = ?;
