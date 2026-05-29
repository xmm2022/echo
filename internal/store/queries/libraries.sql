-- name: CreateLibrary :one
INSERT INTO libraries (
  name, echo_output_kind, echo_output_path, owner_id, created_at
) VALUES (
  ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetLibrary :one
SELECT * FROM libraries
WHERE id = ?;

-- name: ListLibraries :many
SELECT * FROM libraries
ORDER BY id;

-- name: UpdateLibrary :exec
UPDATE libraries
SET name = ?,
    echo_output_kind = ?,
    echo_output_path = ?,
    owner_id = ?
WHERE id = ?;

-- name: DeleteLibrary :exec
DELETE FROM libraries
WHERE id = ?;
