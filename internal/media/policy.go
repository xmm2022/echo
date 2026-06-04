package media

import (
	"context"
	"database/sql"
	"errors"

	"github.com/xmm2022/echo/internal/store/queries"
)

func (s *Service) ResolvePolicy(ctx context.Context, userID string) (queries.DiscoveryAccessPolicy, error) {
	if s == nil || s.Store == nil || s.Store.DB == nil || s.Store.Queries == nil {
		return queries.DiscoveryAccessPolicy{}, ErrPolicyDenied
	}

	topPolicy, err := s.loadTopDiscoveryAccessPolicy(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoveryAccessPolicy{}, ErrPolicyDenied
	}
	if err != nil {
		return queries.DiscoveryAccessPolicy{}, err
	}
	if err := validateResolvedPolicy(topPolicy); err != nil {
		return queries.DiscoveryAccessPolicy{}, err
	}

	policy, err := s.Store.ResolveDiscoveryAccessPolicyForUser(ctx, queries.ResolveDiscoveryAccessPolicyForUserParams{
		UserID: sqlNullString(userID),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoveryAccessPolicy{}, ErrPolicyDenied
	}
	if err != nil {
		return queries.DiscoveryAccessPolicy{}, err
	}
	if policy.ID != topPolicy.ID {
		return queries.DiscoveryAccessPolicy{}, ErrPolicyDenied
	}
	if err := validateResolvedPolicy(policy); err != nil {
		return queries.DiscoveryAccessPolicy{}, err
	}
	return policy, nil
}

func (s *Service) ResolveTarget(ctx context.Context, policy queries.DiscoveryAccessPolicy, targetID int64, mediaType string) (queries.LoadDiscoveryPolicyTargetBundleRow, error) {
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return queries.LoadDiscoveryPolicyTargetBundleRow{}, ErrPolicyDenied
	}
	if err := validateResolvedPolicy(policy); err != nil {
		return queries.LoadDiscoveryPolicyTargetBundleRow{}, err
	}

	row, err := s.Store.LoadDiscoveryPolicyTargetBundle(ctx, queries.LoadDiscoveryPolicyTargetBundleParams{ID: targetID})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.LoadDiscoveryPolicyTargetBundleRow{}, ErrPolicyDenied
	}
	if err != nil {
		return queries.LoadDiscoveryPolicyTargetBundleRow{}, err
	}
	if row.PolicyID != policy.ID {
		return queries.LoadDiscoveryPolicyTargetBundleRow{}, ErrPolicyDenied
	}
	if row.TargetEnabled != 1 {
		return queries.LoadDiscoveryPolicyTargetBundleRow{}, ErrPolicyDenied
	}
	if row.MediaType.Valid && row.MediaType.String != mediaType {
		return queries.LoadDiscoveryPolicyTargetBundleRow{}, ErrPolicyDenied
	}
	return row, nil
}

func (s *Service) SearchAllowed(ctx context.Context, userID string) error {
	policy, err := s.ResolvePolicy(ctx, userID)
	if err != nil {
		return err
	}
	return validateResolvedPolicy(policy)
}

func (s *Service) loadTopDiscoveryAccessPolicy(ctx context.Context, userID string) (queries.DiscoveryAccessPolicy, error) {
	row := s.Store.DB.QueryRowContext(ctx, `
SELECT id, name, enabled, priority, subject_user_id, request_mode, can_search,
       max_pending_requests, max_active_subscriptions, request_cooldown_seconds,
       created_by, created_at, updated_at
FROM discovery_access_policies
WHERE subject_user_id = ? OR subject_user_id IS NULL
ORDER BY priority DESC, subject_user_id IS NOT NULL DESC, id DESC
LIMIT 1`, sqlNullString(userID))

	var policy queries.DiscoveryAccessPolicy
	err := row.Scan(
		&policy.ID,
		&policy.Name,
		&policy.Enabled,
		&policy.Priority,
		&policy.SubjectUserID,
		&policy.RequestMode,
		&policy.CanSearch,
		&policy.MaxPendingRequests,
		&policy.MaxActiveSubscriptions,
		&policy.RequestCooldownSeconds,
		&policy.CreatedBy,
		&policy.CreatedAt,
		&policy.UpdatedAt,
	)
	return policy, err
}

func validateResolvedPolicy(policy queries.DiscoveryAccessPolicy) error {
	if policy.Enabled != 1 {
		return ErrPolicyDenied
	}
	if policy.RequestMode == "disabled" {
		return ErrPolicyDenied
	}
	if policy.CanSearch != 1 {
		return ErrPolicyDenied
	}
	return nil
}

func sqlNullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
