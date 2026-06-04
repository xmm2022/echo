package media

import (
	"context"
	"database/sql"
	"errors"

	"github.com/xmm2022/echo/internal/store/queries"
)

func (s *Service) ListSubscriptions(ctx context.Context, actor Actor, limit, offset int64) ([]SubscriptionDTO, error) {
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return nil, ErrPolicyDenied
	}
	userID := actorUserID(actor)
	if userID == "" {
		return nil, ErrPolicyDenied
	}
	limit, offset, err := normalizePagination(limit, offset)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.ProjectUserMediaSubscriptionStatus(ctx, queries.ProjectUserMediaSubscriptionStatusParams{
		EchoUserID: userID,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SubscriptionDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, subscriptionDTO(row))
	}
	return out, nil
}

func (s *Service) PauseSubscription(ctx context.Context, actor Actor, id int64) (SubscriptionDTO, error) {
	return s.setSubscriptionStatus(ctx, actor, id, "paused", "paused")
}

func (s *Service) ResumeSubscription(ctx context.Context, actor Actor, id int64) (SubscriptionDTO, error) {
	return s.setSubscriptionStatus(ctx, actor, id, "active", "resumed")
}

func (s *Service) setSubscriptionStatus(ctx context.Context, actor Actor, id int64, status, action string) (SubscriptionDTO, error) {
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return SubscriptionDTO{}, ErrPolicyDenied
	}
	userID := actorUserID(actor)
	if userID == "" || id <= 0 {
		return SubscriptionDTO{}, ErrNotFound
	}
	tx, q, err := s.Store.BeginImmediateTx(ctx)
	if err != nil {
		return SubscriptionDTO{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	before, err := q.GetUserMediaSubscriptionForUser(ctx, queries.GetUserMediaSubscriptionForUserParams{
		ID:         id,
		EchoUserID: userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		now := nowUnix(s)
		if err := createUserAuditEventWithQueries(ctx, q, actor, "subscription_action_deny", "subscription", "", "request-disabled", now); err != nil {
			return SubscriptionDTO{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SubscriptionDTO{}, err
		}
		committed = true
		return SubscriptionDTO{}, ErrNotFound
	}
	if err != nil {
		return SubscriptionDTO{}, err
	}
	if !validUserSubscriptionTransition(before.Status, status) {
		return SubscriptionDTO{}, ErrInvalidTransition
	}
	now := nowUnix(s)
	updated, err := q.UpdateUserMediaSubscriptionStatus(ctx, queries.UpdateUserMediaSubscriptionStatusParams{
		ID:         id,
		EchoUserID: userID,
		Status:     status,
		UpdatedAt:  now,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return SubscriptionDTO{}, ErrNotFound
	}
	if err != nil {
		return SubscriptionDTO{}, err
	}
	if before.RequestID.Valid {
		if err := appendRequestEventWithQueries(ctx, q, before.RequestID.Int64, actor, action, before.Status, status, now); err != nil {
			return SubscriptionDTO{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return SubscriptionDTO{}, err
	}
	committed = true
	return s.projectSubscriptionDTO(ctx, userID, id, updated)
}

func validUserSubscriptionTransition(from, to string) bool {
	return from == "active" && to == "paused" || from == "paused" && to == "active"
}

func (s *Service) projectSubscriptionDTO(ctx context.Context, userID string, id int64, fallback queries.UserMediaSubscription) (SubscriptionDTO, error) {
	rows, err := s.Store.ProjectUserMediaSubscriptionStatus(ctx, queries.ProjectUserMediaSubscriptionStatusParams{
		EchoUserID: userID,
		Limit:      maxListLimit,
		Offset:     0,
	})
	if err != nil {
		return SubscriptionDTO{}, err
	}
	for _, row := range rows {
		if row.UserMediaSubscriptionID == id {
			return subscriptionDTO(row), nil
		}
	}
	return SubscriptionDTO{
		ID:             fallback.ID,
		TMDBID:         fallback.TmdbID,
		MediaType:      fallback.MediaType,
		UserStatus:     fallback.Status,
		PipelineStatus: "unknown",
		LatestState:    fallback.Status,
		UpdatedAt:      fallback.UpdatedAt,
	}, nil
}

func subscriptionDTO(row queries.ProjectUserMediaSubscriptionStatusRow) SubscriptionDTO {
	return SubscriptionDTO{
		ID:             row.UserMediaSubscriptionID,
		TMDBID:         row.TmdbID,
		MediaType:      row.MediaType,
		Title:          row.TitleSnapshot,
		TargetLabel:    row.TargetLibraryName,
		UserStatus:     row.UserSubscriptionStatus,
		PipelineStatus: row.DiscoverySubscriptionStatus,
		LatestState:    latestSubscriptionState(row),
		UpdatedAt:      row.UpdatedAt,
	}
}

func latestSubscriptionState(row queries.ProjectUserMediaSubscriptionStatusRow) string {
	switch row.UserSubscriptionStatus {
	case "paused", "canceled", "completed":
		return row.UserSubscriptionStatus
	}
	switch row.DiscoverySubscriptionStatus {
	case "paused", "disabled", "completed":
		return row.DiscoverySubscriptionStatus
	}
	if !row.MatchID.Valid {
		return "pending"
	}

	decision := ""
	if row.MatchDecision.Valid {
		decision = row.MatchDecision.String
	}
	dispatch := ""
	if row.MatchDispatchState.Valid {
		dispatch = row.MatchDispatchState.String
	}
	if dispatch == "failed" || decision == "failed" || row.MatchFailureKind.Valid {
		return "failed"
	}
	if dispatch == "succeeded" || decision == "imported" || row.MatchResultLibraryEntryID.Valid {
		return "imported"
	}
	switch dispatch {
	case "running":
		return "processing"
	case "queued":
		return "queued"
	}
	switch decision {
	case "queue":
		return "queued"
	case "review":
		return "review"
	case "reject":
		return "rejected"
	case "accept":
		return "accepted"
	}
	return "pending"
}
