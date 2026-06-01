-- name: CreateEmbyLibraryMapping :one
INSERT INTO emby_library_mappings (
  emby_server_id, library_id, emby_path_prefix, emby_path_prefix_norm,
  echo_rel_prefix, case_sensitive, enabled, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: ListEnabledEmbyLibraryMappings :many
SELECT * FROM emby_library_mappings
WHERE emby_server_id = ? AND enabled = 1
ORDER BY length(emby_path_prefix_norm) DESC, id ASC;

-- name: GetEmbyLibraryMapping :one
SELECT * FROM emby_library_mappings
WHERE id = ? AND enabled = 1;

-- name: SetEmbyLibraryMappingEnabled :exec
UPDATE emby_library_mappings
SET enabled = ?, updated_at = ?
WHERE id = ?;
