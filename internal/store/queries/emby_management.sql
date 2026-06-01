-- name: ListEmbyServers :many
SELECT id, name, base_url, public_base_url, proxy_prefix, enabled, created_at, updated_at
FROM emby_servers
ORDER BY id
LIMIT ? OFFSET ?;

-- name: UpsertEmbyServer :one
INSERT INTO emby_servers (
  id, name, base_url, api_key_ref, public_base_url, proxy_prefix, enabled, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  base_url = excluded.base_url,
  api_key_ref = excluded.api_key_ref,
  public_base_url = excluded.public_base_url,
  proxy_prefix = excluded.proxy_prefix,
  enabled = excluded.enabled,
  updated_at = excluded.updated_at
RETURNING id, name, base_url, public_base_url, proxy_prefix, enabled, created_at, updated_at;

-- name: ListEmbyUserLinks :many
SELECT id, emby_server_id, emby_user_id, echo_user_id, enabled, created_at, updated_at
FROM emby_user_links
ORDER BY emby_server_id, emby_user_id
LIMIT ? OFFSET ?;

-- name: UpsertEmbyUserLink :one
INSERT INTO emby_user_links (
  emby_server_id, emby_user_id, echo_user_id, enabled, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?
)
ON CONFLICT(emby_server_id, emby_user_id) DO UPDATE SET
  echo_user_id = excluded.echo_user_id,
  enabled = excluded.enabled,
  updated_at = excluded.updated_at
RETURNING *;

-- name: ListEmbyLibraryMappings :many
SELECT id, emby_server_id, emby_path_prefix, emby_path_prefix_norm, library_id, echo_rel_prefix, case_sensitive, enabled, created_at, updated_at
FROM emby_library_mappings
ORDER BY emby_server_id, length(emby_path_prefix) DESC, id
LIMIT ? OFFSET ?;

-- name: UpdateEmbyLibraryMapping :one
UPDATE emby_library_mappings
SET
  emby_path_prefix = ?,
  emby_path_prefix_norm = ?,
  library_id = ?,
  echo_rel_prefix = ?,
  case_sensitive = ?,
  enabled = ?,
  updated_at = ?
WHERE id = ?
RETURNING *;
