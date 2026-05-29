-- name: GetFileCopyByRemotePath :one
SELECT * FROM file_copies
WHERE sidecar_id = ? AND storage_mount = ? AND remote_path = ?;

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
    pickcode = COALESCE(excluded.pickcode, file_copies.pickcode)
RETURNING *;

-- name: UpdateFileCopyLive :exec
UPDATE file_copies
SET status = 'live',
    last_seen = ?,
    cloud_file_id = COALESCE(?, cloud_file_id),
    pickcode = COALESCE(?, pickcode)
WHERE id = ?;

-- name: MarkFileCopyDead :exec
UPDATE file_copies
SET status = 'dead',
    last_seen = ?
WHERE id = ?;

-- name: ListLiveCopiesByBlob :many
SELECT * FROM file_copies
WHERE blob_id = ? AND status = 'live'
ORDER BY last_seen DESC
LIMIT ?;

-- name: ListLiveCopiesByBlobPreferProvider :many
SELECT
  id, blob_id, provider, account_id, sidecar_id, storage_mount, remote_path,
  cloud_file_id, pickcode, status, last_seen
FROM (
  SELECT
    file_copies.*,
    CASE WHEN provider = @preferred_provider THEN 0 ELSE 1 END AS provider_rank
  FROM file_copies
  WHERE blob_id = @blob_id AND status = 'live'
)
ORDER BY provider_rank,
         last_seen DESC
LIMIT @limit;
