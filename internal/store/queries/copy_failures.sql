-- name: InsertCopyFailure :exec
INSERT INTO copy_failures (
  copy_id, account_id, sidecar_id, storage_mount, operation, kind, confidence,
  evidence_class, http_status, openlist_code, safe_message, observed_at, request_id
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);
