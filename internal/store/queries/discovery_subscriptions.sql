-- name: CreateDiscoverySubscription :one
INSERT INTO discovery_subscriptions (
  owner_id, tmdb_id, media_type, tmdb_language, title_snapshot, library_id,
  producer_profile_id, rule_profile_id, status, season_filter_json,
  next_check_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetDiscoverySubscription :one
SELECT * FROM discovery_subscriptions WHERE id = ?;

-- name: ListDiscoverySubscriptions :many
SELECT * FROM discovery_subscriptions ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: UpdateDiscoverySubscription :one
UPDATE discovery_subscriptions
SET library_id = ?, producer_profile_id = ?, rule_profile_id = ?,
    status = ?, season_filter_json = ?, next_check_at = ?, updated_at = ?
WHERE id = ?
RETURNING *;

-- name: LeaseDueDiscoverySubscriptions :many
UPDATE discovery_subscriptions
SET locked_until = ?, updated_at = ?
WHERE id IN (
  SELECT ds.id FROM discovery_subscriptions AS ds
  WHERE ds.status = 'active'
    AND (ds.next_check_at IS NULL OR ds.next_check_at <= ?)
    AND (ds.locked_until IS NULL OR ds.locked_until <= ?)
  ORDER BY COALESCE(ds.next_check_at, 0), ds.id
  LIMIT ?
)
RETURNING *;

-- name: UpdateDiscoverySubscriptionBestMatch :exec
UPDATE discovery_subscriptions
SET current_best_match_id = ?, current_best_score_json = ?, last_checked_at = ?,
    failure_count = 0, last_error_kind = NULL, last_error_message = NULL,
    updated_at = ?
WHERE id = ?;

-- name: ClearDiscoverySubscriptionLease :exec
UPDATE discovery_subscriptions
SET locked_until = NULL, last_checked_at = ?, next_check_at = ?,
    failure_count = 0, last_error_kind = NULL, last_error_message = NULL,
    updated_at = ?
WHERE id = ?;

-- name: BackoffDiscoverySubscription :exec
UPDATE discovery_subscriptions
SET locked_until = NULL, next_check_at = ?, failure_count = failure_count + 1,
    last_error_kind = ?, last_error_message = ?, updated_at = ?
WHERE id = ?;
