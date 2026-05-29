-- name: InsertBlobHash :one
INSERT INTO blob_hashes (
  blob_id, hash_type, hash_value, hash_value_norm, size
) VALUES (
  ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetBlobHashByKey :one
SELECT * FROM blob_hashes
WHERE hash_type = ? AND hash_value_norm = ? AND size = ?;

-- name: ListBlobHashesByBlob :many
SELECT * FROM blob_hashes
WHERE blob_id = ?
ORDER BY id;
