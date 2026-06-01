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

-- name: ListLibraryEntries :many
SELECT
  le.id, le.library_id, le.rel_path, le.name, le.blob_id, le.echo_written, le.created_at, le.updated_at,
  COUNT(fc.id) AS live_copies
FROM library_entries le
LEFT JOIN file_copies fc
  ON fc.blob_id = le.blob_id AND fc.status = 'live'
WHERE le.library_id = ?
GROUP BY le.id
ORDER BY le.rel_path
LIMIT ?;

-- name: ListLibraryEntriesByLibraryPrefix :many
-- Half-open range [prefix_lo, prefix_hi) is the wildcard-free way to do a rel_path
-- prefix scan (sqlc's SQLite grammar rejects LIKE ... ESCAPE, and file paths contain
-- literal _ and %). The handler computes prefix_hi = prefix with its last byte bumped.
SELECT
  le.id, le.library_id, le.rel_path, le.name, le.blob_id, le.echo_written, le.created_at, le.updated_at,
  COUNT(fc.id) AS live_copies
FROM library_entries le
LEFT JOIN file_copies fc
  ON fc.blob_id = le.blob_id AND fc.status = 'live'
WHERE le.library_id = sqlc.arg(library_id)
  AND le.rel_path >= sqlc.arg(prefix_lo)
  AND le.rel_path < sqlc.arg(prefix_hi)
GROUP BY le.id
ORDER BY le.rel_path
LIMIT sqlc.arg(limit);

-- name: UpdateLibraryEntryBlobForTest :exec
UPDATE library_entries
SET blob_id = ?, updated_at = ?
WHERE id = ?;
