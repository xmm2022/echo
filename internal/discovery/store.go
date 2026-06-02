package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/discovery/dispatch"
	"github.com/xmm2022/echo/internal/job"
	storepkg "github.com/xmm2022/echo/internal/store"
	storeq "github.com/xmm2022/echo/internal/store/queries"
)

type Store struct {
	store *storepkg.Store
}

var errSourceConfig = errors.New("source config error")

var (
	ErrAdminMatchNotFound          = errors.New("discovery match not found")
	ErrInvalidAdminMatchTransition = errors.New("invalid discovery match admin transition")
)

type DiscoveredResourceStore struct {
	writer *SafeDiscoveredResourceWriter
}

type QueuedMatchForReconcile struct {
	MatchID int64
	JobID   int64
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

func (s *Store) CreateOrGetMatch(ctx context.Context, params AcceptMatchParams) (Match, error) {
	match, err := s.AcceptMatch(ctx, params)
	if err != nil {
		return Match{}, err
	}
	return Match{ID: match.ID}, nil
}

func (s *Store) GetSource(ctx context.Context, id int64) (Source, error) {
	row, err := s.store.GetDiscoverySource(ctx, storeq.GetDiscoverySourceParams{ID: id})
	if err != nil {
		return Source{}, err
	}
	source := sourceFromRow(row)
	if source.Kind == SourceTelegramMTProto {
		return s.overlayTelegramSourceCursors(ctx, source)
	}
	return source, nil
}

func (s *Store) CreateDiscoveryRun(ctx context.Context, kind string, ids RunIDs, now time.Time) (int64, error) {
	run, err := s.store.CreateDiscoveryRun(ctx, storeq.CreateDiscoveryRunParams{
		Kind:           kind,
		SourceID:       nullInt64Zero(ids.SourceID),
		SubscriptionID: nullInt64Zero(ids.SubscriptionID),
		JobID:          nullInt64Zero(ids.JobID),
		Status:         "running",
		CountersJson:   "{}",
		CreatedAt:      unixOrNow(now),
	})
	if err != nil {
		return 0, err
	}
	return run.ID, nil
}

func (s *Store) FinishDiscoveryRun(ctx context.Context, runID int64, status string, countersJSON string, runErr error, finishedAt time.Time) error {
	if strings.TrimSpace(countersJSON) == "" {
		countersJSON = "{}"
	}
	var errKind, errMessage sql.NullString
	if runErr != nil {
		errKind = nullStringEmpty("error")
		errMessage = nullStringEmpty(runErr.Error())
	}
	finishedUnix := unixOrNow(finishedAt)
	return s.store.FinishDiscoveryRun(ctx, storeq.FinishDiscoveryRunParams{
		Status:       status,
		CountersJson: countersJSON,
		ErrorKind:    errKind,
		ErrorMessage: errMessage,
		StartedAt:    nullInt64(finishedUnix),
		FinishedAt:   nullInt64(finishedUnix),
		ID:           runID,
	})
}

func (s *Store) GetSubscriptionBundle(ctx context.Context, subscriptionID int64) (SubscriptionBundle, error) {
	row, err := s.store.GetDiscoverySubscriptionBundle(ctx, storeq.GetDiscoverySubscriptionBundleParams{ID: subscriptionID})
	if err != nil {
		return SubscriptionBundle{}, err
	}
	return SubscriptionBundle{
		SubscriptionID:     row.SubscriptionID,
		TMDBID:             row.TmdbID,
		MediaType:          row.MediaType,
		LibraryID:          row.LibraryID,
		ProducerProfileID:  row.ProducerProfileID,
		RuleProfileID:      row.RuleProfileID,
		RuleProfileVersion: row.RuleProfileVersion,
		RuleProfileJSON:    row.RuleProfileJson,
	}, nil
}

func (s *Store) ListCandidateResourcesForSubscription(ctx context.Context, tmdbID, mediaType string, limit int64) ([]CandidateResource, error) {
	if err := validateLeaseLimit(limit); err != nil {
		return nil, err
	}
	rows, err := s.store.ListCandidateResourcesForSubscription(ctx, storeq.ListCandidateResourcesForSubscriptionParams{
		TmdbID:    nullStringEmpty(tmdbID),
		MediaType: nullStringEmpty(mediaType),
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]CandidateResource, 0, len(rows))
	for _, row := range rows {
		out = append(out, CandidateResource{
			ID:               row.ID,
			Provider:         Provider(row.Provider),
			LinkKind:         LinkKind(row.LinkKind),
			TMDBID:           row.TmdbID.String,
			MediaType:        row.MediaType.String,
			Title:            row.Title.String,
			SeasonNumber:     row.SeasonNumber.Int64,
			EpisodeStart:     row.EpisodeStart.Int64,
			EpisodeEnd:       row.EpisodeEnd.Int64,
			ShareCode:        row.ShareCode.String,
			ReceiveCode:      row.ReceiveCode.String,
			ShareURLRedacted: row.ShareUrlRedacted.String,
			FeatureJSON:      row.FeatureJson,
		})
	}
	return out, nil
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
	rows, err := s.store.LeaseDueDiscoverySources(ctx, storeq.LeaseDueDiscoverySourcesParams{
		LockedUntil:   nullInt64(lockedUntil.Unix()),
		UpdatedAt:     now.Unix(),
		NextRunAt:     nullInt64(now.Unix()),
		LockedUntil_2: nullInt64(now.Unix()),
		BackoffUntil:  nullInt64(now.Unix()),
		Limit:         limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(rows))
	for _, row := range rows {
		out = append(out, sourceFromRow(row))
	}
	return out, nil
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
			SubscriptionID:     bundle.SubscriptionID,
			TMDBID:             bundle.TmdbID,
			MediaType:          bundle.MediaType,
			LibraryID:          bundle.LibraryID,
			ProducerProfileID:  bundle.ProducerProfileID,
			RuleProfileID:      bundle.RuleProfileID,
			RuleProfileVersion: bundle.RuleProfileVersion,
			RuleProfileJSON:    bundle.RuleProfileJson,
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

func (s *Store) BackoffTelegramChannelsBySource(ctx context.Context, sourceID int64, backoffUntil time.Time, kind, message string, now time.Time) error {
	channels, err := s.store.ListTelegramChannelsBySource(ctx, storeq.ListTelegramChannelsBySourceParams{SourceID: sourceID})
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if err := s.store.BackoffTelegramChannel(ctx, storeq.BackoffTelegramChannelParams{
			BackoffUntil:     nullInt64(backoffUntil.Unix()),
			LastErrorKind:    nullStringEmpty(kind),
			LastErrorMessage: nullStringEmpty(message),
			UpdatedAt:        unixOrNow(now),
			ID:               channel.ID,
		}); err != nil {
			return err
		}
	}
	return nil
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

func (s *Store) ListDueDiscoveryDispatchMatches(ctx context.Context, limit int64) ([]int64, error) {
	if err := validateLeaseLimit(limit); err != nil {
		return nil, err
	}
	return s.store.ListDueDiscoveryDispatchMatches(ctx, storeq.ListDueDiscoveryDispatchMatchesParams{
		Limit: limit,
	})
}

func (s *Store) ListQueuedDiscoveryMatchesForReconcile(ctx context.Context, limit int64) ([]QueuedMatchForReconcile, error) {
	if err := validateLeaseLimit(limit); err != nil {
		return nil, err
	}
	rows, err := s.store.ListQueuedDiscoveryMatchesForReconcile(ctx, storeq.ListQueuedDiscoveryMatchesForReconcileParams{
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]QueuedMatchForReconcile, 0, len(rows))
	for _, row := range rows {
		if !row.QueuedJobID.Valid {
			continue
		}
		out = append(out, QueuedMatchForReconcile{
			MatchID: row.ID,
			JobID:   row.QueuedJobID.Int64,
		})
	}
	return out, nil
}

func (s *Store) CountDueTMDBMediaRefresh(ctx context.Context, now time.Time) (int64, error) {
	return s.store.CountDueTMDBMediaRefresh(ctx, storeq.CountDueTMDBMediaRefreshParams{
		NextRefreshAt: unixOrNow(now),
	})
}

func (s *Store) ClaimAndCreateMatchDispatchJob(ctx context.Context, matchID int64, payload job.IngestPayload, now time.Time) (jobID int64, claimed bool, err error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, false, fmt.Errorf("marshal dispatch job payload: %w", err)
	}
	tx, q, err := s.store.BeginImmediateTx(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	createdAt := unixOrNow(now)
	created, err := q.CreateJob(ctx, storeq.CreateJobParams{
		Kind:      job.KindIngestProducer,
		Status:    "pending",
		Payload:   string(body),
		OwnerID:   "discovery",
		CreatedAt: createdAt,
	})
	if err != nil {
		return 0, false, err
	}
	if _, err := q.LinkSubscriptionMatchDispatchJobIfClaimable(ctx, storeq.LinkSubscriptionMatchDispatchJobIfClaimableParams{
		QueuedJobID: nullInt64(created.ID),
		UpdatedAt:   createdAt,
		DecidedAt:   nullInt64(createdAt),
		ID:          matchID,
	}); errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return created.ID, true, nil
}

func (s *Store) LoadDispatchBundle(ctx context.Context, matchID int64) (DispatchBundle, error) {
	row, err := s.store.LoadDispatchBundle(ctx, storeq.LoadDispatchBundleParams{ID: matchID})
	if err != nil {
		return DispatchBundle{}, err
	}
	defaultArgs := map[string]any{}
	if strings.TrimSpace(row.DefaultArgsJson) != "" {
		if err := json.Unmarshal([]byte(row.DefaultArgsJson), &defaultArgs); err != nil {
			return DispatchBundle{}, fmt.Errorf("decode producer default args: %w", err)
		}
	}
	return DispatchBundle{
		MatchID:        row.MatchID,
		SubscriptionID: row.SubscriptionID,
		ResourceID:     row.ResourceID,
		Profile: dispatch.ProducerProfile{
			LibraryID:              row.LibraryID,
			Provider:               row.Provider,
			Tool:                   row.Tool,
			TargetAccount:          row.TargetAccount,
			TargetSubdirTemplate:   row.TargetSubdirTemplate,
			LibraryRelPathTemplate: row.LibraryRelPathTemplate,
			DefaultArgs:            defaultArgs,
		},
		Resource: dispatch.Resource{
			ShareURL:    row.ShareUrlRedacted.String,
			ShareCode:   row.ShareCode.String,
			ReceiveCode: row.ReceiveCode.String,
			Title:       row.Title.String,
		},
	}, nil
}

func (s *Store) LoadReconcileBundle(ctx context.Context, matchID, jobID int64) (ReconcileBundle, error) {
	row, err := s.store.LoadReconcileBundle(ctx, storeq.LoadReconcileBundleParams{ID: matchID, ID_2: jobID})
	if err != nil {
		return ReconcileBundle{}, err
	}
	return ReconcileBundle{
		MatchID:         row.MatchID,
		JobID:           row.JobID,
		JobStatus:       row.JobStatus,
		JobProgressJSON: row.JobProgressJson,
		JobError:        row.JobError,
	}, nil
}

func (s *Store) MarkMatchFinished(ctx context.Context, result MatchResult) error {
	now := unixOrNow(result.FinishedAt)
	decision := result.Decision
	if decision == "" {
		decision = "failed"
	}
	dispatchState := result.DispatchState
	if dispatchState == "" {
		dispatchState = "failed"
	}
	return s.store.UpdateSubscriptionMatchResult(ctx, storeq.UpdateSubscriptionMatchResultParams{
		Decision:             decision,
		DispatchState:        dispatchState,
		ResultLibraryEntryID: nullInt64Zero(result.LibraryEntryID),
		ResultBlobID:         nullInt64Zero(result.BlobID),
		ResultCopyID:         nullInt64Zero(result.CopyID),
		FailureKind:          nullStringEmpty(result.FailureKind),
		FailureMessage:       nullStringEmpty(result.FailureMessage),
		UpdatedAt:            now,
		FinishedAt:           nullInt64(now),
		ID:                   result.MatchID,
	})
}

// AdminAcceptMatch accepts only idle, non-terminal matches. It deliberately
// refuses queued/running/succeeded matches so an admin action cannot detach an
// in-flight producer job from its lifecycle.
func (s *Store) AdminAcceptMatch(ctx context.Context, matchID int64, now time.Time) error {
	match, err := s.store.GetSubscriptionMatch(ctx, storeq.GetSubscriptionMatchParams{ID: matchID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAdminMatchNotFound
	}
	if err != nil {
		return err
	}
	if !canAdminAcceptMatch(match) {
		return fmt.Errorf("%w: accept match %d from decision=%s dispatch_state=%s", ErrInvalidAdminMatchTransition, matchID, match.Decision, match.DispatchState)
	}
	updatedAt := unixOrNow(now)
	if err := s.store.UpdateSubscriptionMatchDispatch(ctx, storeq.UpdateSubscriptionMatchDispatchParams{
		Decision:      "accept",
		DispatchState: "none",
		QueuedJobID:   sql.NullInt64{},
		UpdatedAt:     updatedAt,
		ID:            matchID,
	}); err != nil {
		return err
	}
	return s.MarkDiscoveredResourceStatus(ctx, match.ResourceID, "accepted", now)
}

// AdminRejectMatch rejects only idle, non-terminal matches. In-flight or finished
// dispatches must finish through dispatch/reconcile instead of being reset here.
func (s *Store) AdminRejectMatch(ctx context.Context, matchID int64, now time.Time) error {
	match, err := s.store.GetSubscriptionMatch(ctx, storeq.GetSubscriptionMatchParams{ID: matchID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAdminMatchNotFound
	}
	if err != nil {
		return err
	}
	if !canAdminRejectMatch(match) {
		return fmt.Errorf("%w: reject match %d from decision=%s dispatch_state=%s", ErrInvalidAdminMatchTransition, matchID, match.Decision, match.DispatchState)
	}
	updatedAt := unixOrNow(now)
	if err := s.store.UpdateSubscriptionMatchDispatch(ctx, storeq.UpdateSubscriptionMatchDispatchParams{
		Decision:      "reject",
		DispatchState: "none",
		QueuedJobID:   sql.NullInt64{},
		UpdatedAt:     updatedAt,
		ID:            matchID,
	}); err != nil {
		return err
	}
	return s.MarkDiscoveredResourceStatus(ctx, match.ResourceID, "rejected", now)
}

// AdminRetryMatch requeues failed dispatches, plus idle accepted/queued matches
// that have not started. It refuses queued/running/succeeded matches because those
// are already owned by the dispatcher/reconciler lifecycle.
func (s *Store) AdminRetryMatch(ctx context.Context, matchID int64, now time.Time) error {
	match, err := s.store.GetSubscriptionMatch(ctx, storeq.GetSubscriptionMatchParams{ID: matchID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAdminMatchNotFound
	}
	if err != nil {
		return err
	}
	if !canAdminRetryMatch(match) {
		return fmt.Errorf("%w: retry match %d from decision=%s dispatch_state=%s", ErrInvalidAdminMatchTransition, matchID, match.Decision, match.DispatchState)
	}
	return s.store.UpdateSubscriptionMatchDispatch(ctx, storeq.UpdateSubscriptionMatchDispatchParams{
		Decision:      "queue",
		DispatchState: "none",
		QueuedJobID:   sql.NullInt64{},
		UpdatedAt:     unixOrNow(now),
		ID:            matchID,
	})
}

func (s *Store) MarkDiscoveredResourceStatus(ctx context.Context, resourceID int64, status string, now time.Time) error {
	_, err := s.store.DB.ExecContext(ctx, `
UPDATE discovered_resources
SET status = ?, last_seen_at = ?
WHERE id = ?`, status, unixOrNow(now), resourceID)
	return err
}

func canAdminAcceptMatch(match storeq.SubscriptionMatch) bool {
	if match.DispatchState != "none" {
		return false
	}
	switch match.Decision {
	case "review", "accept", "queue":
		return true
	default:
		return false
	}
}

func canAdminRejectMatch(match storeq.SubscriptionMatch) bool {
	if match.DispatchState != "none" {
		return false
	}
	switch match.Decision {
	case "review", "accept", "queue", "reject":
		return true
	default:
		return false
	}
}

func canAdminRetryMatch(match storeq.SubscriptionMatch) bool {
	switch match.DispatchState {
	case "failed":
		return true
	case "none":
		return match.Decision == "accept" || match.Decision == "queue"
	default:
		return false
	}
}

func (s *Store) UpdateTelegramCursor(ctx context.Context, sourceID int64, update TelegramCursorUpdate, now time.Time) error {
	result, err := s.store.DB.ExecContext(ctx, `
UPDATE telegram_channels
SET last_message_id = ?, last_message_date = ?, failure_count = 0,
    last_error_kind = NULL, last_error_message = NULL, locked_until = NULL,
    updated_at = ?
WHERE source_id = ? AND channel_ref = ?`,
		nullInt64Zero(update.LastMessageID),
		nullInt64Zero(update.LastMessageDate),
		unixOrNow(now),
		sourceID,
		update.ChannelRef,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("telegram cursor channel not found: source_id=%d channel_ref=%q", sourceID, update.ChannelRef)
	}
	return nil
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

func sourceFromRow(row storeq.DiscoverySource) Source {
	return Source{
		ID:         row.ID,
		Kind:       SourceKind(row.Kind),
		Name:       row.Name,
		ConfigJSON: row.ConfigJson,
		ConfigJson: row.ConfigJson,
		SecretRef:  row.SecretRef.String,
	}
}

func (s *Store) overlayTelegramSourceCursors(ctx context.Context, source Source) (Source, error) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(source.ConfigText()), &cfg); err != nil {
		return Source{}, fmt.Errorf("%w: parse telegram source config: %v", errSourceConfig, err)
	}
	rawChannels, ok := cfg["channels"].([]any)
	if !ok {
		return source, nil
	}
	rows, err := s.store.ListTelegramChannelsBySource(ctx, storeq.ListTelegramChannelsBySourceParams{SourceID: source.ID})
	if err != nil {
		return Source{}, err
	}
	byRef := make(map[string]storeq.TelegramChannel, len(rows))
	for _, row := range rows {
		byRef[row.ChannelRef] = row
	}
	for _, rawChannel := range rawChannels {
		channel, ok := rawChannel.(map[string]any)
		if !ok {
			continue
		}
		ref, _ := channel["ref"].(string)
		row, ok := byRef[ref]
		if !ok {
			continue
		}
		if row.LastMessageID.Valid {
			channel["last_message_id"] = row.LastMessageID.Int64
		}
		if row.LastMessageDate.Valid {
			channel["last_message_date"] = row.LastMessageDate.Int64
		}
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return Source{}, fmt.Errorf("marshal telegram source config: %w", err)
	}
	source.ConfigJSON = string(body)
	source.ConfigJson = string(body)
	return source, nil
}
