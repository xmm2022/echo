-- name: GetDiscoverySubscriptionBundle :one
SELECT
  s.id AS subscription_id,
  s.tmdb_id,
  s.media_type,
  s.library_id,
  s.producer_profile_id,
  s.rule_profile_id,
  s.match_mode,
  rp.version AS rule_profile_version,
  rp.rules_json AS rule_profile_json
FROM discovery_subscriptions s
JOIN rule_profiles rp ON rp.id = s.rule_profile_id
WHERE s.id = ?;

-- name: ListCandidateResourcesForSubscription :many
SELECT
  id, provider, link_kind, tmdb_id, media_type, title, season_number,
  episode_start, episode_end, share_code, receive_code, share_url_redacted,
  feature_json
FROM discovered_resources
WHERE tmdb_id = ? AND media_type = ? AND status IN ('candidate','review','accepted','failed')
ORDER BY last_seen_at DESC
LIMIT ?;

-- name: LoadDispatchBundle :one
SELECT
  m.id AS match_id,
  m.subscription_id,
  r.id AS resource_id,
  s.library_id,
  p.provider,
  p.tool,
  p.target_account,
  p.target_subdir_template,
  p.library_rel_path_template,
  p.default_args_json,
  r.share_url_redacted,
  r.share_code,
  r.receive_code,
  r.title,
  r.provider AS resource_provider,
  r.link_kind AS resource_link_kind,
  r.status AS resource_status
FROM subscription_matches m
JOIN discovery_subscriptions s ON s.id = m.subscription_id
JOIN discovery_producer_profiles p ON p.id = s.producer_profile_id
JOIN discovered_resources r ON r.id = m.resource_id
WHERE m.id = ?
  AND m.decision IN ('accept','queue')
  AND r.provider = '115'
  AND r.link_kind = '115_share'
  AND r.status NOT IN ('rejected','unsupported_provider');

-- name: LinkSubscriptionMatchDispatchJobIfClaimable :one
UPDATE subscription_matches
SET decision = 'queue',
    dispatch_state = 'queued',
    queued_job_id = ?,
    updated_at = ?,
    decided_at = COALESCE(decided_at, ?),
    failure_kind = NULL,
    failure_message = NULL
WHERE subscription_matches.id = ?
  AND subscription_matches.dispatch_state IN ('none','failed')
  AND subscription_matches.decision IN ('accept','queue')
  AND EXISTS (
    SELECT 1
    FROM discovered_resources r
    WHERE r.id = subscription_matches.resource_id
      AND r.provider = '115'
      AND r.link_kind = '115_share'
      AND r.status NOT IN ('rejected','unsupported_provider')
  )
RETURNING *;

-- name: LoadReconcileBundle :one
SELECT
  m.id AS match_id,
  j.id AS job_id,
  j.status AS job_status,
  COALESCE(j.progress, '{}') AS job_progress_json,
  COALESCE(j.error, '') AS job_error
FROM subscription_matches m
JOIN jobs j ON j.id = m.queued_job_id
WHERE m.id = ? AND j.id = ?;

-- name: UpdateTelegramChannelCursorByRef :exec
UPDATE telegram_channels
SET last_message_id = ?, last_message_date = ?, failure_count = 0,
    last_error_kind = NULL, last_error_message = NULL, locked_until = NULL,
    updated_at = ?
WHERE source_id = ? AND channel_ref = ?;
