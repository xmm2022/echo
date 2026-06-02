-- name: CreateDiscoveryProducerProfile :one
INSERT INTO discovery_producer_profiles (
  name, provider, tool, target_account, target_subdir_template,
  library_rel_path_template, default_args_json, enabled, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetDiscoveryProducerProfile :one
SELECT * FROM discovery_producer_profiles WHERE id = ?;

-- name: ListDiscoveryProducerProfiles :many
SELECT * FROM discovery_producer_profiles ORDER BY id DESC LIMIT ? OFFSET ?;

-- name: UpdateDiscoveryProducerProfile :one
UPDATE discovery_producer_profiles
SET name = ?, target_account = ?, target_subdir_template = ?,
    library_rel_path_template = ?, default_args_json = ?, enabled = ?,
    updated_at = ?
WHERE id = ?
RETURNING *;

-- name: CreateRuleProfile :one
INSERT INTO rule_profiles (
  name, version, rules_json, enabled, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRuleProfile :one
SELECT * FROM rule_profiles WHERE id = ?;

-- name: ListRuleProfiles :many
SELECT * FROM rule_profiles ORDER BY id DESC LIMIT ? OFFSET ?;

-- name: UpdateRuleProfile :one
UPDATE rule_profiles
SET name = ?, version = version + 1, rules_json = ?, enabled = ?, updated_at = ?
WHERE id = ?
RETURNING *;
