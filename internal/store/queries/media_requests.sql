-- name: CreateDiscoverySubscriptionRequest :one
INSERT INTO discovery_subscription_requests (
  requester_user_id, status, tmdb_id, media_type, tmdb_language, title_snapshot,
  original_title_snapshot, release_year_snapshot, poster_path_snapshot,
  season_filter_json, policy_id_snapshot, policy_target_id_snapshot,
  target_label_snapshot, target_library_id, target_library_name_snapshot,
  producer_profile_id_snapshot, producer_profile_name_snapshot,
  rule_profile_id_snapshot, rule_profile_version_snapshot, user_note,
  admin_note, reviewed_by, reviewed_at, subscription_id, idempotency_key,
  last_error_kind, last_error_message, created_at, updated_at
) VALUES (
  sqlc.arg(requester_user_id), sqlc.arg(status), sqlc.arg(tmdb_id),
  sqlc.arg(media_type), sqlc.arg(tmdb_language), sqlc.arg(title_snapshot),
  sqlc.arg(original_title_snapshot), sqlc.arg(release_year_snapshot),
  sqlc.arg(poster_path_snapshot), sqlc.arg(season_filter_json),
  sqlc.arg(policy_id_snapshot), sqlc.arg(policy_target_id_snapshot),
  sqlc.arg(target_label_snapshot), sqlc.arg(target_library_id),
  sqlc.arg(target_library_name_snapshot), sqlc.arg(producer_profile_id_snapshot),
  sqlc.arg(producer_profile_name_snapshot), sqlc.arg(rule_profile_id_snapshot),
  sqlc.arg(rule_profile_version_snapshot), sqlc.arg(user_note),
  sqlc.arg(admin_note), sqlc.arg(reviewed_by), sqlc.arg(reviewed_at),
  sqlc.arg(subscription_id), sqlc.arg(idempotency_key),
  sqlc.arg(last_error_kind), sqlc.arg(last_error_message),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT(idempotency_key) DO UPDATE SET
  updated_at = discovery_subscription_requests.updated_at
RETURNING *;

-- name: GetDiscoverySubscriptionRequest :one
SELECT * FROM discovery_subscription_requests
WHERE id = sqlc.arg(id);

-- name: GetDiscoverySubscriptionRequestByIdempotency :one
SELECT * FROM discovery_subscription_requests
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: GetDiscoverySubscriptionRequestForUser :one
SELECT * FROM discovery_subscription_requests
WHERE id = sqlc.arg(id)
  AND requester_user_id = sqlc.arg(requester_user_id);

-- name: ListDiscoverySubscriptionRequestsForUser :many
SELECT * FROM discovery_subscription_requests
WHERE requester_user_id = sqlc.arg(requester_user_id)
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: ListDiscoverySubscriptionRequestsForAdmin :many
SELECT * FROM discovery_subscription_requests
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: CountPendingDiscoveryRequestsForUser :one
SELECT COUNT(*) FROM discovery_subscription_requests
WHERE requester_user_id = sqlc.arg(requester_user_id)
  AND status = 'pending_review';

-- name: CountRecentDiscoveryRequestsForUser :one
SELECT COUNT(*) FROM discovery_subscription_requests
WHERE requester_user_id = sqlc.arg(requester_user_id)
  AND created_at >= sqlc.arg(created_since);

-- name: ApproveDiscoverySubscriptionRequest :one
UPDATE discovery_subscription_requests
SET status = 'approved',
    admin_note = sqlc.arg(admin_note),
    reviewed_by = sqlc.arg(reviewed_by),
    reviewed_at = sqlc.arg(reviewed_at),
    subscription_id = sqlc.arg(subscription_id),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND status = sqlc.arg(current_status)
RETURNING *;

-- name: RejectDiscoverySubscriptionRequest :one
UPDATE discovery_subscription_requests
SET status = 'rejected',
    admin_note = sqlc.arg(admin_note),
    reviewed_by = sqlc.arg(reviewed_by),
    reviewed_at = sqlc.arg(reviewed_at),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND status = sqlc.arg(current_status)
RETURNING *;

-- name: CancelPendingDiscoverySubscriptionRequest :one
UPDATE discovery_subscription_requests
SET status = 'canceled',
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND requester_user_id = sqlc.arg(requester_user_id)
  AND status = 'pending_review'
RETURNING *;

-- name: MarkDiscoverySubscriptionRequestFailed :one
UPDATE discovery_subscription_requests
SET status = 'failed',
    last_error_kind = sqlc.arg(last_error_kind),
    last_error_message = sqlc.arg(last_error_message),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND status = sqlc.arg(current_status)
RETURNING *;

-- name: CreateDiscoverySubscriptionRequestEvent :one
INSERT INTO discovery_subscription_request_events (
  request_id, actor_user_id, action, from_status, to_status, note, snapshot_json,
  created_at
) VALUES (
  sqlc.arg(request_id), sqlc.arg(actor_user_id), sqlc.arg(action),
  sqlc.arg(from_status), sqlc.arg(to_status), sqlc.arg(note),
  sqlc.arg(snapshot_json), sqlc.arg(created_at)
)
RETURNING *;

-- name: CreateDiscoveryUserAuditEvent :one
INSERT INTO discovery_user_audit_events (
  actor_user_id, action, target_kind, target_id, safe_reason, snapshot_json,
  created_at
) VALUES (
  sqlc.arg(actor_user_id), sqlc.arg(action), sqlc.arg(target_kind),
  sqlc.arg(target_id), sqlc.arg(safe_reason), sqlc.arg(snapshot_json),
  sqlc.arg(created_at)
)
RETURNING *;

-- name: ListDiscoverySubscriptionRequestEvents :many
SELECT * FROM discovery_subscription_request_events
WHERE request_id = sqlc.arg(request_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);
