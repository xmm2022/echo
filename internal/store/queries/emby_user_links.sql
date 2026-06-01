-- name: CreateEmbyUserLink :exec
INSERT INTO emby_user_links (
  emby_server_id, emby_user_id, emby_username, echo_user_id, enabled, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?
);

-- name: GetEmbyUserLink :one
SELECT * FROM emby_user_links
WHERE emby_server_id = ? AND emby_user_id = ? AND enabled = 1;
