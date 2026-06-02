package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	storepkg "github.com/xmm2022/echo/internal/store"
	storeq "github.com/xmm2022/echo/internal/store/queries"
)

type Store struct {
	store *storepkg.Store
}

type DiscoveredResourceStore struct {
	writer *SafeDiscoveredResourceWriter
}

type AcceptMatchParams struct {
	SubscriptionID     int64
	ResourceID         int64
	RuleProfileID      int64
	RuleProfileVersion int64
	SeasonNumber       int64
	EpisodeStart       int64
	EpisodeEnd         int64
	ScoreJSON          string
	PreviousScoreJSON  string
	Decision           string
	Reason             string
	DispatchState      string
	IdempotencyKey     string
	Now                time.Time
}

func NewStore(st *storepkg.Store) *Store {
	return &Store{store: st}
}

// NewDiscoveredResourceStore is the narrow Task-2 discovery storage facade for
// writes that must enforce parsed_json redaction before reaching sqlc.
func NewDiscoveredResourceStore(inner discoveredResourceWriter) *DiscoveredResourceStore {
	return &DiscoveredResourceStore{writer: NewSafeDiscoveredResourceWriter(inner)}
}

func (s *DiscoveredResourceStore) UpsertDiscoveredResource(ctx context.Context, arg storeq.UpsertDiscoveredResourceParams) (storeq.DiscoveredResource, error) {
	return s.writer.UpsertDiscoveredResource(ctx, arg)
}

func (s *Store) AcceptMatch(ctx context.Context, params AcceptMatchParams) (SubscriptionMatch, error) {
	if err := validateAcceptMatchParams(params); err != nil {
		return SubscriptionMatch{}, err
	}
	now := unixOrNow(params.Now)
	tx, q, err := s.store.BeginImmediateTx(ctx)
	if err != nil {
		return SubscriptionMatch{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := q.GetSubscriptionMatchByIdempotencyKey(ctx, storeq.GetSubscriptionMatchByIdempotencyKeyParams{
		IdempotencyKey: params.IdempotencyKey,
	})
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return SubscriptionMatch{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SubscriptionMatch{}, err
	}

	match, err := q.CreateSubscriptionMatch(ctx, storeq.CreateSubscriptionMatchParams{
		SubscriptionID:     params.SubscriptionID,
		ResourceID:         params.ResourceID,
		RuleProfileID:      params.RuleProfileID,
		RuleProfileVersion: params.RuleProfileVersion,
		SeasonNumber:       nullInt64Zero(params.SeasonNumber),
		EpisodeStart:       nullInt64Zero(params.EpisodeStart),
		EpisodeEnd:         nullInt64Zero(params.EpisodeEnd),
		ScoreJson:          params.ScoreJSON,
		PreviousScoreJson:  nullStringEmpty(params.PreviousScoreJSON),
		Decision:           params.Decision,
		Reason:             params.Reason,
		DispatchState:      params.DispatchState,
		IdempotencyKey:     params.IdempotencyKey,
		CreatedAt:          now,
		UpdatedAt:          now,
		DecidedAt:          decidedAtFor(params.Decision, now),
	})
	if err != nil {
		return SubscriptionMatch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SubscriptionMatch{}, err
	}
	return match, nil
}

func (s *Store) GetSource(ctx context.Context, id int64) (Source, error) {
	return s.store.GetDiscoverySource(ctx, storeq.GetDiscoverySourceParams{ID: id})
}

func (s *Store) UpsertDiscoveredResource(ctx context.Context, sourceID int64, item ParsedResource) (int64, error) {
	observedAt := item.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	parsedJSON := item.ParsedJSON
	if strings.TrimSpace(parsedJSON) == "" {
		parsedJSON = "{}"
	}
	featureJSON := item.FeatureJSON
	if strings.TrimSpace(featureJSON) == "" {
		featureJSON = "{}"
	}
	writer := NewSafeDiscoveredResourceWriter(s.store.Queries)
	resource, err := writer.UpsertDiscoveredResource(ctx, storeq.UpsertDiscoveredResourceParams{
		SourceID:         sourceID,
		Provider:         string(item.Provider),
		LinkKind:         string(item.LinkKind),
		ExternalKey:      item.ExternalKey,
		TmdbID:           nullStringEmpty(item.TMDBID),
		MediaType:        nullStringEmpty(item.MediaType),
		Title:            nullStringEmpty(item.Title),
		SeasonNumber:     nullInt64Zero(item.SeasonNumber),
		EpisodeStart:     nullInt64Zero(item.EpisodeStart),
		EpisodeEnd:       nullInt64Zero(item.EpisodeEnd),
		ShareCode:        nullStringEmpty(item.ShareCode),
		ReceiveCode:      nullStringEmpty(item.ReceiveCode),
		ShareUrlRedacted: nullStringEmpty(item.ShareURLRedacted),
		RawTextRedacted:  nullStringEmpty(item.RawTextRedacted),
		RawTextRef:       nullStringEmpty(item.RawTextRef),
		ParsedJson:       parsedJSON,
		FeatureJson:      featureJSON,
		Status:           "candidate",
		FirstSeenAt:      observedAt.Unix(),
		LastSeenAt:       observedAt.Unix(),
	})
	if err != nil {
		return 0, err
	}
	return resource.ID, nil
}

func (s *Store) LeaseDueSources(ctx context.Context, now, lockedUntil time.Time, limit int64) ([]Source, error) {
	if err := validateLeaseLimit(limit); err != nil {
		return nil, err
	}
	return s.store.LeaseDueDiscoverySources(ctx, storeq.LeaseDueDiscoverySourcesParams{
		LockedUntil:   nullInt64(lockedUntil.Unix()),
		UpdatedAt:     now.Unix(),
		NextRunAt:     nullInt64(now.Unix()),
		LockedUntil_2: nullInt64(now.Unix()),
		BackoffUntil:  nullInt64(now.Unix()),
		Limit:         limit,
	})
}

func (s *Store) LeaseDueSubscriptions(ctx context.Context, now, lockedUntil time.Time, limit int64) ([]SubscriptionBundle, error) {
	if err := validateLeaseLimit(limit); err != nil {
		return nil, err
	}
	subscriptions, err := s.store.LeaseDueDiscoverySubscriptions(ctx, storeq.LeaseDueDiscoverySubscriptionsParams{
		LockedUntil:   nullInt64(lockedUntil.Unix()),
		UpdatedAt:     now.Unix(),
		NextCheckAt:   nullInt64(now.Unix()),
		LockedUntil_2: nullInt64(now.Unix()),
		Limit:         limit,
	})
	if err != nil {
		return nil, err
	}
	bundles := make([]SubscriptionBundle, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		bundle, err := s.store.GetDiscoverySubscriptionBundle(ctx, storeq.GetDiscoverySubscriptionBundleParams{
			ID: subscription.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("load discovery subscription bundle %d: %w", subscription.ID, err)
		}
		bundles = append(bundles, SubscriptionBundle{
			Subscription: subscription,
			Bundle:       bundle,
		})
	}
	return bundles, nil
}

func (s *Store) ClearSourceLease(ctx context.Context, sourceID int64, nextRunAt time.Time) error {
	now := time.Now().Unix()
	return s.store.ClearDiscoverySourceLease(ctx, storeq.ClearDiscoverySourceLeaseParams{
		LastSuccessAt: nullInt64(now),
		NextRunAt:     nullInt64(nextRunAt.Unix()),
		UpdatedAt:     now,
		ID:            sourceID,
	})
}

func (s *Store) BackoffSource(ctx context.Context, sourceID int64, backoffUntil time.Time, kind, message string) error {
	return s.store.BackoffDiscoverySource(ctx, storeq.BackoffDiscoverySourceParams{
		BackoffUntil:     nullInt64(backoffUntil.Unix()),
		LastErrorKind:    nullStringEmpty(kind),
		LastErrorMessage: nullStringEmpty(message),
		UpdatedAt:        time.Now().Unix(),
		ID:               sourceID,
	})
}

func (s *Store) ClearSubscriptionLease(ctx context.Context, subscriptionID int64, nextCheckAt time.Time) error {
	now := time.Now().Unix()
	return s.store.ClearDiscoverySubscriptionLease(ctx, storeq.ClearDiscoverySubscriptionLeaseParams{
		LastCheckedAt: nullInt64(now),
		NextCheckAt:   nullInt64(nextCheckAt.Unix()),
		UpdatedAt:     now,
		ID:            subscriptionID,
	})
}

func (s *Store) BackoffSubscription(ctx context.Context, subscriptionID int64, nextCheckAt time.Time, kind, message string) error {
	return s.store.BackoffDiscoverySubscription(ctx, storeq.BackoffDiscoverySubscriptionParams{
		NextCheckAt:      nullInt64(nextCheckAt.Unix()),
		LastErrorKind:    nullStringEmpty(kind),
		LastErrorMessage: nullStringEmpty(message),
		UpdatedAt:        time.Now().Unix(),
		ID:               subscriptionID,
	})
}

func validateAcceptMatchParams(params AcceptMatchParams) error {
	if strings.TrimSpace(params.ScoreJSON) == "" {
		return errors.New("score_json is required")
	}
	if strings.TrimSpace(params.Reason) == "" {
		return errors.New("reason is required")
	}
	if strings.TrimSpace(params.IdempotencyKey) == "" {
		return errors.New("idempotency_key is required")
	}
	if !validDecision(params.Decision) {
		return fmt.Errorf("invalid decision %q", params.Decision)
	}
	if !validDispatchState(params.DispatchState) {
		return fmt.Errorf("invalid dispatch_state %q", params.DispatchState)
	}
	if params.DispatchState != "none" {
		return fmt.Errorf("dispatch_state %q cannot be created by AcceptMatch", params.DispatchState)
	}
	if !validAcceptMatchDecision(params.Decision) {
		return fmt.Errorf("decision %q cannot be created by AcceptMatch", params.Decision)
	}
	return nil
}

func validDecision(value string) bool {
	switch value {
	case "reject", "review", "accept", "queue", "imported", "failed":
		return true
	default:
		return false
	}
}

func validAcceptMatchDecision(value string) bool {
	switch value {
	case "reject", "review", "accept", "queue":
		return true
	default:
		return false
	}
}

func validDispatchState(value string) bool {
	switch value {
	case "none", "queued", "running", "succeeded", "failed":
		return true
	default:
		return false
	}
}

func validateLeaseLimit(limit int64) error {
	if limit <= 0 {
		return fmt.Errorf("lease limit must be positive, got %d", limit)
	}
	return nil
}

func decidedAtFor(decision string, now int64) sql.NullInt64 {
	switch decision {
	case "reject", "accept", "queue":
		return nullInt64(now)
	default:
		return sql.NullInt64{}
	}
}

func unixOrNow(value time.Time) int64 {
	if value.IsZero() {
		return time.Now().Unix()
	}
	return value.Unix()
}

func nullInt64Zero(value int64) sql.NullInt64 {
	if value == 0 {
		return sql.NullInt64{}
	}
	return nullInt64(value)
}

func nullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func nullStringEmpty(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
