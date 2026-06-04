-- name: CreateDiscoveryAccessPolicy :one
INSERT INTO discovery_access_policies (
  name, enabled, priority, subject_user_id, request_mode, can_search,
  max_pending_requests, max_active_subscriptions, request_cooldown_seconds,
  created_by, created_at, updated_at
) VALUES (
  sqlc.arg(name), sqlc.arg(enabled), sqlc.arg(priority), sqlc.arg(subject_user_id),
  sqlc.arg(request_mode), sqlc.arg(can_search), sqlc.arg(max_pending_requests),
  sqlc.arg(max_active_subscriptions), sqlc.arg(request_cooldown_seconds),
  sqlc.arg(created_by), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: UpdateDiscoveryAccessPolicy :one
UPDATE discovery_access_policies
SET name = sqlc.arg(name),
    enabled = sqlc.arg(enabled),
    priority = sqlc.arg(priority),
    subject_user_id = sqlc.arg(subject_user_id),
    request_mode = sqlc.arg(request_mode),
    can_search = sqlc.arg(can_search),
    max_pending_requests = sqlc.arg(max_pending_requests),
    max_active_subscriptions = sqlc.arg(max_active_subscriptions),
    request_cooldown_seconds = sqlc.arg(request_cooldown_seconds),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetDiscoveryAccessPolicy :one
SELECT * FROM discovery_access_policies
WHERE id = sqlc.arg(id);

-- name: ListDiscoveryAccessPolicies :many
SELECT * FROM discovery_access_policies
ORDER BY enabled DESC, priority DESC, id DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: ResolveDiscoveryAccessPolicyForUser :one
SELECT * FROM discovery_access_policies
WHERE enabled = 1
  AND (subject_user_id = sqlc.arg(user_id) OR subject_user_id IS NULL)
ORDER BY priority DESC, subject_user_id IS NOT NULL DESC, id DESC
LIMIT 1;

-- name: CreateDiscoveryPolicyTarget :one
INSERT INTO discovery_policy_targets (
  policy_id, label, library_id, producer_profile_id, rule_profile_id,
  pipeline_owner_id, media_type, match_mode, grant_playback_on_approval,
  enabled, default_target, created_at, updated_at
) VALUES (
  sqlc.arg(policy_id), sqlc.arg(label), sqlc.arg(library_id),
  sqlc.arg(producer_profile_id), sqlc.arg(rule_profile_id),
  sqlc.arg(pipeline_owner_id), sqlc.arg(media_type), sqlc.arg(match_mode),
  sqlc.arg(grant_playback_on_approval), sqlc.arg(enabled),
  sqlc.arg(default_target), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: UpdateDiscoveryPolicyTarget :one
UPDATE discovery_policy_targets
SET label = sqlc.arg(label),
    library_id = sqlc.arg(library_id),
    producer_profile_id = sqlc.arg(producer_profile_id),
    rule_profile_id = sqlc.arg(rule_profile_id),
    pipeline_owner_id = sqlc.arg(pipeline_owner_id),
    media_type = sqlc.arg(media_type),
    match_mode = sqlc.arg(match_mode),
    grant_playback_on_approval = sqlc.arg(grant_playback_on_approval),
    enabled = sqlc.arg(enabled),
    default_target = sqlc.arg(default_target),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetDiscoveryPolicyTarget :one
SELECT * FROM discovery_policy_targets
WHERE id = sqlc.arg(id);

-- name: ListDiscoveryPolicyTargets :many
SELECT * FROM discovery_policy_targets
WHERE policy_id = sqlc.arg(policy_id)
ORDER BY default_target DESC, label, id
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: ListEnabledDiscoveryPolicyTargetsForPolicy :many
SELECT * FROM discovery_policy_targets
WHERE policy_id = sqlc.arg(policy_id)
  AND enabled = 1
  AND (media_type IS NULL OR media_type = sqlc.arg(media_type))
ORDER BY default_target DESC, id DESC;

-- name: LoadDiscoveryPolicyTargetBundle :one
SELECT
  t.id AS target_id,
  t.policy_id AS policy_id,
  t.label AS target_label,
  t.library_id AS target_library_id,
  l.name AS target_library_name,
  t.producer_profile_id AS producer_profile_id,
  p.name AS producer_profile_name,
  t.rule_profile_id AS rule_profile_id,
  r.version AS rule_profile_version,
  t.pipeline_owner_id AS pipeline_owner_id,
  t.media_type AS media_type,
  t.match_mode AS match_mode,
  t.grant_playback_on_approval AS grant_playback_on_approval,
  t.enabled AS target_enabled,
  t.default_target AS default_target
FROM discovery_policy_targets AS t
JOIN libraries AS l ON l.id = t.library_id
JOIN discovery_producer_profiles AS p ON p.id = t.producer_profile_id
JOIN rule_profiles AS r ON r.id = t.rule_profile_id
WHERE t.id = sqlc.arg(id);
