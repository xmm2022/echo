-- name: GetLibraryEntry :one
SELECT * FROM library_entries
WHERE library_id = ? AND rel_path = ?;

-- name: GetLibraryEntryByID :one
SELECT * FROM library_entries
WHERE id = ?;

-- name: UpsertLibraryEntry :one
INSERT INTO library_entries (
  library_id, rel_path, name, blob_id, echo_written, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(library_id, rel_path) DO UPDATE
SET name = excluded.name,
    blob_id = excluded.blob_id,
    echo_written = excluded.echo_written,
    updated_at = excluded.updated_at
RETURNING *;

-- name: MarkLibraryEntryEchoWritten :exec
UPDATE library_entries
SET echo_written = 1,
    updated_at = ?
WHERE id = ?;

-- name: ListLibraryEntriesNeedingEcho :many
SELECT * FROM library_entries
WHERE echo_written = 0
ORDER BY id
LIMIT ?;
