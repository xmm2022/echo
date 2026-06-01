-- name: CreateEmbyServer :exec
INSERT INTO emby_servers (
  id, name, base_url, api_key_ref, public_base_url, proxy_prefix, enabled, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: GetEnabledEmbyServer :one
SELECT * FROM emby_servers
WHERE id = ? AND enabled = 1;
