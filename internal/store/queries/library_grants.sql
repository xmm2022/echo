-- name: GrantLibraryPlayback :exec
INSERT INTO library_grants (
  library_id, echo_user_id, permission, enabled, created_by, created_at, updated_at
) VALUES (
  ?, ?, 'playback', 1, ?, ?, ?
)
ON CONFLICT(library_id, echo_user_id, permission) DO UPDATE
SET enabled = 1, updated_at = excluded.updated_at;

-- name: UserCanPlaybackLibrary :one
SELECT EXISTS (
  SELECT 1 FROM libraries
  WHERE libraries.id = sqlc.arg(library_id) AND owner_id = sqlc.arg(echo_user_id)
  UNION
  SELECT 1 FROM library_grants
  WHERE library_id = sqlc.arg(library_id)
    AND echo_user_id = sqlc.arg(echo_user_id)
    AND permission = 'playback'
    AND enabled = 1
) AS allowed;
