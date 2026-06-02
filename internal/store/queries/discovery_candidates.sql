-- name: UpsertDiscoveredResource :one
INSERT INTO discovered_resources (
  source_id, provider, link_kind, external_key, tmdb_id, media_type, title,
  season_number, episode_start, episode_end, share_code, receive_code,
  share_url_redacted, raw_text_redacted, raw_text_ref, parsed_json,
  feature_json, status, first_seen_at, last_seen_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_id, external_key) DO UPDATE SET
  tmdb_id = excluded.tmdb_id,
  media_type = excluded.media_type,
  title = excluded.title,
  season_number = excluded.season_number,
  episode_start = excluded.episode_start,
  episode_end = excluded.episode_end,
  share_code = excluded.share_code,
  receive_code = excluded.receive_code,
  share_url_redacted = excluded.share_url_redacted,
  raw_text_redacted = excluded.raw_text_redacted,
  raw_text_ref = excluded.raw_text_ref,
  parsed_json = excluded.parsed_json,
  feature_json = excluded.feature_json,
  last_seen_at = excluded.last_seen_at
RETURNING *;

-- name: GetDiscoveredResource :one
SELECT * FROM discovered_resources WHERE id = ?;

-- name: ListDiscoveredResources :many
SELECT * FROM discovered_resources ORDER BY last_seen_at DESC LIMIT ? OFFSET ?;

-- name: CreateSubscriptionMatch :one
INSERT INTO subscription_matches (
  subscription_id, resource_id, rule_profile_id, rule_profile_version,
  season_number, episode_start, episode_end, score_json, previous_score_json,
  decision, reason, dispatch_state, idempotency_key, created_at, updated_at, decided_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSubscriptionMatch :one
SELECT * FROM subscription_matches WHERE id = ?;

-- name: GetSubscriptionMatchByIdempotencyKey :one
SELECT * FROM subscription_matches WHERE idempotency_key = ?;

-- name: UpdateSubscriptionMatchDispatch :exec
UPDATE subscription_matches
SET decision = ?, dispatch_state = ?, queued_job_id = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateSubscriptionMatchResult :exec
UPDATE subscription_matches
SET decision = ?, dispatch_state = ?, result_library_entry_id = ?, result_blob_id = ?,
    result_copy_id = ?, failure_kind = ?, failure_message = ?, updated_at = ?,
    finished_at = ?
WHERE id = ?;
