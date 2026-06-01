-- name: GetEmbyItemMapping :one
SELECT * FROM emby_item_mappings
WHERE emby_server_id = ?
  AND emby_item_id = ?
  AND media_source_id = ?
  AND media_source_path_norm = ?;

-- name: GetValidEmbyItemMapping :one
SELECT eim.*
FROM emby_item_mappings eim
JOIN emby_library_mappings elm
  ON elm.id = eim.mapping_id
 AND elm.enabled = 1
JOIN library_entries le
  ON le.id = eim.library_entry_id
WHERE eim.emby_server_id = ?
  AND eim.emby_item_id = ?
  AND eim.media_source_id = ?
  AND eim.media_source_path_norm = ?
  AND eim.path_norm_version = ?
  AND le.library_id = eim.library_id
  AND le.blob_id = eim.blob_id
  AND le.updated_at = eim.library_entry_updated_at;

-- name: UpsertEmbyItemMapping :exec
INSERT INTO emby_item_mappings (
  emby_server_id, emby_item_id, media_source_id, mapping_id,
  media_source_path_raw, media_source_path_norm, path_norm_version,
  library_id, rel_path, library_entry_id, blob_id, library_entry_updated_at,
  emby_item_etag, last_seen_at, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(emby_server_id, emby_item_id, media_source_id, media_source_path_norm) DO UPDATE
SET mapping_id = excluded.mapping_id,
    path_norm_version = excluded.path_norm_version,
    library_id = excluded.library_id,
    rel_path = excluded.rel_path,
    library_entry_id = excluded.library_entry_id,
    blob_id = excluded.blob_id,
    library_entry_updated_at = excluded.library_entry_updated_at,
    emby_item_etag = excluded.emby_item_etag,
    last_seen_at = excluded.last_seen_at,
    updated_at = excluded.updated_at;

-- name: ListItemMappingsByItem :many
SELECT * FROM emby_item_mappings
WHERE emby_server_id = ? AND emby_item_id = ?;
