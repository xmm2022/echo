-- name: UpsertDiscoverySubscriptionForMediaRequest :one
INSERT INTO discovery_subscriptions (
  owner_id, tmdb_id, media_type, tmdb_language, title_snapshot, library_id,
  producer_profile_id, rule_profile_id, status, season_filter_json,
  next_check_at, created_at, updated_at, match_mode
) VALUES (
  sqlc.arg(owner_id), sqlc.arg(tmdb_id), sqlc.arg(media_type),
  sqlc.arg(tmdb_language), sqlc.arg(title_snapshot), sqlc.arg(library_id),
  sqlc.arg(producer_profile_id), sqlc.arg(rule_profile_id), sqlc.arg(status),
  sqlc.arg(season_filter_json), sqlc.arg(next_check_at), sqlc.arg(created_at),
  sqlc.arg(updated_at), sqlc.arg(match_mode)
)
ON CONFLICT(tmdb_id, media_type, owner_id, library_id) DO UPDATE SET
  match_mode = CASE
    WHEN discovery_subscriptions.match_mode = 'admin_review'
      OR excluded.match_mode = 'admin_review'
    THEN 'admin_review'
    ELSE 'auto_dispatch'
  END,
  updated_at = CASE
    WHEN discovery_subscriptions.match_mode = excluded.match_mode
    THEN discovery_subscriptions.updated_at
    ELSE excluded.updated_at
  END
RETURNING *;

-- name: DowngradeDispatchableSubscriptionMatchesForAdminReview :exec
UPDATE subscription_matches
SET decision = 'review',
    reason = 'admin_review',
    dispatch_state = 'none',
    queued_job_id = NULL,
    failure_kind = NULL,
    failure_message = NULL,
    decided_at = NULL,
    finished_at = NULL,
    updated_at = sqlc.arg(updated_at)
WHERE subscription_id = sqlc.arg(subscription_id)
  AND decision IN ('accept','queue')
  AND dispatch_state IN ('none','failed')
  AND reason != 'admin_accept';

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

-- name: ProjectUserMediaSubscriptionStatus :many
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
  lm.id AS match_id,
  lm.decision AS match_decision,
  lm.dispatch_state AS match_dispatch_state,
  lm.result_library_entry_id AS match_result_library_entry_id,
  lm.result_blob_id AS match_result_blob_id,
  lm.result_copy_id AS match_result_copy_id,
  lm.failure_kind AS match_failure_kind,
  lm.finished_at AS match_finished_at,
  lm.updated_at AS match_updated_at,
  ums.created_at AS created_at,
  ums.updated_at AS updated_at
FROM user_media_subscriptions AS ums
JOIN discovery_subscriptions AS ds ON ds.id = ums.discovery_subscription_id
JOIN libraries AS l ON l.id = ds.library_id
LEFT JOIN subscription_matches AS lm ON lm.id = (
  SELECT sm.id
  FROM subscription_matches AS sm
  WHERE sm.subscription_id = ds.id
    AND (
      ums.season_filter_json IS NULL
      OR TRIM(ums.season_filter_json) = ''
      OR EXISTS (
        SELECT 1
        FROM json_each(ums.season_filter_json) AS season_filter
        WHERE CAST(season_filter.value AS INTEGER) = sm.season_number
      )
    )
  ORDER BY sm.updated_at DESC, sm.id DESC
  LIMIT 1
)
WHERE ums.echo_user_id = sqlc.arg(echo_user_id)
ORDER BY ums.updated_at DESC, ums.id DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);
