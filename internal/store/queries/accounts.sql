-- name: CreateAccount :exec
INSERT INTO accounts (
  id, provider, sidecar_id, storage_mount, status, last_check, owner_id, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: GetAccount :one
SELECT * FROM accounts
WHERE id = ?;

-- name: ListAccounts :many
SELECT * FROM accounts
ORDER BY id;

-- name: UpdateAccount :exec
UPDATE accounts
SET provider = ?,
    sidecar_id = ?,
    storage_mount = ?,
    status = ?,
    last_check = ?,
    owner_id = ?,
    updated_at = ?
WHERE id = ?;

-- name: UpdateAccountStatus :exec
UPDATE accounts
SET status = ?, last_check = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteAccount :exec
DELETE FROM accounts
WHERE id = ?;

-- name: MarkAccountSchedulerState :exec
UPDATE accounts
SET scheduler_state = ?,
    cooldown_until = ?,
    recheck_after = ?,
    status_reason = ?,
    last_error_at = ?,
    last_error_kind = ?,
    last_error_message = ?,
    updated_at = ?
WHERE id = ?;
