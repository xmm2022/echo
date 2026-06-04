DROP TABLE IF EXISTS discovery_user_audit_events;
DROP TABLE IF EXISTS discovery_subscription_request_events;
DROP TABLE IF EXISTS user_media_subscriptions;
DROP TABLE IF EXISTS discovery_subscription_requests;
DROP TABLE IF EXISTS discovery_policy_targets;
DROP TABLE IF EXISTS discovery_access_policies;
ALTER TABLE discovery_subscriptions DROP COLUMN match_mode;
