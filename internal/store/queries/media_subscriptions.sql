-- name: UpsertDiscoverySubscriptionForMediaRequest :one
INSERT INTO discovery_subscriptions (
  owner_id, tmdb_id, media_type, tmdb_language, title_snapshot, library_id,
  producer_profile_id, rule_profile_id, status, season_filter_json,
  next_check_at, created_at, updated_at
) VALUES (
  sqlc.arg(owner_id), sqlc.arg(tmdb_id), sqlc.arg(media_type),
  sqlc.arg(tmdb_language), sqlc.arg(title_snapshot), sqlc.arg(library_id),
  sqlc.arg(producer_profile_id), sqlc.arg(rule_profile_id), sqlc.arg(status),
  sqlc.arg(season_filter_json), sqlc.arg(next_check_at), sqlc.arg(created_at),
  sqlc.arg(updated_at)
)
ON CONFLICT(tmdb_id, media_type, owner_id, library_id) DO UPDATE SET
  updated_at = discovery_subscriptions.updated_at
RETURNING *;

-- name: FindCanonicalDiscoverySubscriptionForMediaRequest :one
SELECT * FROM discovery_subscriptions
WHERE tmdb_id = sqlc.arg(tmdb_id)
  AND media_type = sqlc.arg(media_type)
  AND owner_id = sqlc.arg(owner_id)
  AND library_id = sqlc.arg(library_id);

-- name: UpsertUserMediaSubscription :one
INSERT INTO user_media_subscriptions (
  echo_user_id, request_id, discovery_subscription_id, tmdb_id, media_type,
  season_filter_json, season_filter_key, status, created_at, updated_at
) VALUES (
  sqlc.arg(echo_user_id), sqlc.arg(request_id),
  sqlc.arg(discovery_subscription_id), sqlc.arg(tmdb_id), sqlc.arg(media_type),
  sqlc.arg(season_filter_json), sqlc.arg(season_filter_key), sqlc.arg(status),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT(echo_user_id, discovery_subscription_id, season_filter_key) DO UPDATE SET
  request_id = COALESCE(excluded.request_id, user_media_subscriptions.request_id),
  tmdb_id = excluded.tmdb_id,
  media_type = excluded.media_type,
  season_filter_json = excluded.season_filter_json,
  status = excluded.status,
  updated_at = excluded.updated_at
RETURNING *;

-- name: GetUserMediaSubscriptionForUser :one
SELECT * FROM user_media_subscriptions
WHERE id = sqlc.arg(id)
  AND echo_user_id = sqlc.arg(echo_user_id);

-- name: ListUserMediaSubscriptionsForUser :many
SELECT * FROM user_media_subscriptions
WHERE echo_user_id = sqlc.arg(echo_user_id)
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: CountActiveUserMediaSubscriptions :one
SELECT COUNT(*) FROM user_media_subscriptions
WHERE echo_user_id = sqlc.arg(echo_user_id)
  AND status = 'active';

-- name: UpdateUserMediaSubscriptionStatus :one
UPDATE user_media_subscriptions
SET status = sqlc.arg(status),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND echo_user_id = sqlc.arg(echo_user_id)
RETURNING *;

-- name: ProjectUserMediaSubscriptionStatus :one
SELECT
  ums.id AS user_media_subscription_id,
  ums.status AS user_subscription_status,
  ums.tmdb_id AS tmdb_id,
  ums.media_type AS media_type,
  ums.season_filter_json AS season_filter_json,
  ds.status AS discovery_subscription_status,
  ds.tmdb_language AS tmdb_language,
  ds.title_snapshot AS title_snapshot,
  l.id AS target_library_id,
  l.name AS target_library_name,
  ums.created_at AS created_at,
  ums.updated_at AS updated_at
FROM user_media_subscriptions AS ums
JOIN discovery_subscriptions AS ds ON ds.id = ums.discovery_subscription_id
JOIN libraries AS l ON l.id = ds.library_id
WHERE ums.id = sqlc.arg(id)
  AND ums.echo_user_id = sqlc.arg(echo_user_id);
