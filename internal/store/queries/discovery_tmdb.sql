-- name: UpsertTMDBMedia :one
INSERT INTO tmdb_media (
  tmdb_id, media_type, language, title, original_title, release_year,
  poster_path, status, raw_json, fetched_at, next_refresh_at,
  last_error_kind, last_error_message
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(tmdb_id, media_type, language) DO UPDATE SET
  title = excluded.title,
  original_title = excluded.original_title,
  release_year = excluded.release_year,
  poster_path = excluded.poster_path,
  status = excluded.status,
  raw_json = excluded.raw_json,
  fetched_at = excluded.fetched_at,
  next_refresh_at = excluded.next_refresh_at,
  last_error_kind = excluded.last_error_kind,
  last_error_message = excluded.last_error_message
RETURNING *;

-- name: GetTMDBMedia :one
SELECT * FROM tmdb_media
WHERE tmdb_id = ? AND media_type = ? AND language = ?;

-- name: SearchTMDBMediaByTitle :many
SELECT * FROM tmdb_media
WHERE media_type = ? AND language = ? AND title LIKE ?
ORDER BY release_year DESC, fetched_at DESC
LIMIT ?;
