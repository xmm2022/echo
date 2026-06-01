-- name: CreatePlaybackErrorToken :exec
INSERT INTO playback_error_tokens (
  id, selector, token_hash, echo_user_id, emby_server_id, emby_user_id, device_id,
  item_id, media_source_id, reason, http_status, created_at, expires_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: GetPlaybackErrorTokenBySelector :one
SELECT * FROM playback_error_tokens
WHERE selector = ?;
