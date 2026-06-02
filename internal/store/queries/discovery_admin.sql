-- name: ListDiscoveredResourcesRedacted :many
SELECT
  id, source_id, provider, link_kind, external_key, tmdb_id, media_type, title,
  season_number, episode_start, episode_end, share_url_redacted,
  raw_text_redacted, parsed_json, feature_json, status, first_seen_at, last_seen_at
FROM discovered_resources
ORDER BY last_seen_at DESC
LIMIT ? OFFSET ?;

-- name: ListSubscriptionMatchesForAdmin :many
SELECT *
FROM subscription_matches
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: GetDiscoveryRawResourceForDebug :one
SELECT id, raw_text_redacted, raw_text_ref
FROM discovered_resources
WHERE id = ?;
