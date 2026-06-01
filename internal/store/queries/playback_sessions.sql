-- name: CreatePlaybackSession :exec
INSERT INTO playback_sessions (
  id, selector, token_hash, echo_user_id, emby_server_id, emby_user_id, device_id,
  item_id, media_source_id, emby_play_session_id, library_entry_id, blob_id,
  prefer_provider, state, failure_reason, created_at, last_seen_at, expires_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: GetPlaybackSessionBySelector :one
SELECT * FROM playback_sessions
WHERE selector = ?;

-- name: TouchPlaybackSession :exec
UPDATE playback_sessions
SET last_seen_at = ?
WHERE id = ?;

-- name: RevokePlaybackSession :exec
UPDATE playback_sessions
SET state = 'revoked', failure_reason = ?, last_seen_at = ?
WHERE id = ?;
