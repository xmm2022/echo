-- name: ListDueDiscoveryDispatchMatches :many
SELECT id
FROM subscription_matches
WHERE decision IN ('accept','queue')
  AND dispatch_state IN ('none','failed')
ORDER BY updated_at, id
LIMIT ?;

-- name: ListQueuedDiscoveryMatchesForReconcile :many
SELECT id, queued_job_id
FROM subscription_matches
WHERE dispatch_state = 'queued'
  AND queued_job_id IS NOT NULL
ORDER BY updated_at, id
LIMIT ?;

-- name: LeaseDueTMDBMediaRefresh :many
UPDATE tmdb_media
SET next_refresh_at = ?
WHERE id IN (
  SELECT tm.id FROM tmdb_media AS tm
  WHERE tm.next_refresh_at <= ?
  ORDER BY tm.next_refresh_at, tm.id
  LIMIT ?
)
RETURNING *;

-- name: CountDueTMDBMediaRefresh :one
SELECT COUNT(*) FROM tmdb_media
WHERE next_refresh_at <= ?;
