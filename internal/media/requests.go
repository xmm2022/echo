package media

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/store/queries"
)

const (
	defaultTMDBLanguage = "zh-CN"
	defaultListLimit    = int64(50)
	maxListLimit        = int64(100)
)

func (s *Service) Search(ctx context.Context, actor Actor, in SearchInput) ([]TMDBSummaryDTO, error) {
	if s == nil || s.TMDB == nil {
		return nil, ErrMetadataUnavailable
	}
	userID := actorUserID(actor)
	if userID == "" {
		return nil, ErrPolicyDenied
	}
	if err := s.SearchAllowed(ctx, userID); err != nil {
		_ = s.RecordUserAuditEvent(ctx, actor, "search_deny", "tmdb_search", "", "request-disabled")
		return nil, err
	}

	query := strings.TrimSpace(in.Query)
	if query == "" || len([]byte(query)) > 120 {
		return nil, ErrInvalidRequest
	}
	mediaType := normalizeMediaType(in.MediaType)
	if !isAllowedMediaType(mediaType) {
		return nil, ErrInvalidRequest
	}

	results, err := s.TMDB.Search(ctx, query, mediaType)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMetadataUnavailable, err)
	}
	dtos := make([]TMDBSummaryDTO, 0, len(results))
	for _, media := range results {
		dtos = append(dtos, TMDBSummaryDTO{
			TMDBID:       media.TMDBID,
			MediaType:    media.MediaType,
			Title:        media.Title,
			ReleaseYear:  media.ReleaseYear,
			PosterPath:   media.PosterPath,
			Availability: "unknown",
		})
	}
	return dtos, nil
}

func (s *Service) Catalog(ctx context.Context, actor Actor) ([]TargetDTO, error) {
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return nil, ErrPolicyDenied
	}
	userID := actorUserID(actor)
	if userID == "" {
		return nil, ErrPolicyDenied
	}
	policy, err := s.ResolvePolicy(ctx, userID)
	if err != nil {
		return nil, err
	}

	seen := map[int64]struct{}{}
	var out []TargetDTO
	for _, mediaType := range []string{"movie", "tv"} {
		targets, err := s.Store.ListEnabledDiscoveryPolicyTargetsForPolicy(ctx, queries.ListEnabledDiscoveryPolicyTargetsForPolicyParams{
			PolicyID:  policy.ID,
			MediaType: sqlNullString(mediaType),
		})
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			if _, ok := seen[target.ID]; ok {
				continue
			}
			seen[target.ID] = struct{}{}
			dto := TargetDTO{
				ID:          target.ID,
				Label:       target.Label,
				Default:     target.DefaultTarget == 1,
				RequestMode: policy.RequestMode,
				CanSearch:   policy.CanSearch == 1,
			}
			if target.MediaType.Valid {
				dto.MediaType = target.MediaType.String
			}
			out = append(out, dto)
		}
	}
	return out, nil
}

func (s *Service) CreateRequest(ctx context.Context, actor Actor, in CreateRequestInput) (RequestDTO, error) {
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return RequestDTO{}, ErrPolicyDenied
	}
	userID := actorUserID(actor)
	if userID == "" {
		return RequestDTO{}, ErrPolicyDenied
	}

	tmdbID := strings.TrimSpace(in.TMDBID)
	mediaType := normalizeMediaType(in.MediaType)
	language := strings.TrimSpace(in.TMDBLanguage)
	if language == "" {
		language = defaultTMDBLanguage
	}
	userNote := strings.TrimSpace(in.UserNote)
	if tmdbID == "" || len([]byte(tmdbID)) > 64 || !isAllowedMediaType(mediaType) || in.TargetID <= 0 {
		return RequestDTO{}, ErrInvalidRequest
	}
	if !validTMDBLanguage(language) || len([]byte(userNote)) > 1000 {
		return RequestDTO{}, ErrInvalidRequest
	}
	seasonFilterJSON, _, err := ValidateSeasonFilterForMedia(mediaType, in.SeasonFilterJSON)
	if err != nil {
		return RequestDTO{}, err
	}

	policy, err := s.ResolvePolicy(ctx, userID)
	if err != nil {
		_ = s.RecordUserAuditEvent(ctx, actor, "request_create_deny", "request", tmdbID, "request-disabled")
		return RequestDTO{}, err
	}
	autoApprove := policy.RequestMode == "auto_approve"
	if !autoApprove && policy.RequestMode != "approval_required" {
		_ = s.RecordUserAuditEvent(ctx, actor, "request_create_deny", "request", tmdbID, "request-disabled")
		return RequestDTO{}, ErrPolicyDenied
	}
	target, err := s.ResolveTarget(ctx, policy, in.TargetID, mediaType)
	if err != nil {
		_ = s.RecordUserAuditEvent(ctx, actor, "request_create_deny", "request", tmdbID, "request-disabled")
		return RequestDTO{}, err
	}

	idempotencyKey, err := requestIdempotencyKey(userID, tmdbID, mediaType, language, target.TargetID, seasonFilterJSON)
	if err != nil {
		return RequestDTO{}, err
	}
	existing, err := s.Store.GetDiscoverySubscriptionRequestByIdempotency(ctx, queries.GetDiscoverySubscriptionRequestByIdempotencyParams{
		IdempotencyKey: idempotencyKey,
	})
	if err == nil {
		return requestDTO(existing), nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RequestDTO{}, err
	}
	if !autoApprove {
		preflightExisting, foundExisting, err := s.preflightRequestCreate(ctx, actor, policy, tmdbID, idempotencyKey)
		if err != nil {
			return RequestDTO{}, err
		}
		if foundExisting {
			return requestDTO(preflightExisting), nil
		}
	}

	media, err := s.fetchDetails(ctx, tmdbID, mediaType)
	if err != nil {
		return RequestDTO{}, err
	}
	media.TMDBID = tmdbID
	media.MediaType = mediaType
	if media.Title == "" {
		media.Title = tmdbID
	}
	if err := s.upsertTMDBMedia(ctx, media, language); err != nil {
		return RequestDTO{}, err
	}

	if autoApprove {
		row, err := s.createAndApproveRequest(ctx, actor, policy, target, media, language, seasonFilterJSON, userNote, idempotencyKey)
		if err != nil {
			return RequestDTO{}, err
		}
		return requestDTO(row), nil
	}

	row, err := s.createPendingRequest(ctx, actor, policy, target, media, language, seasonFilterJSON, userNote, idempotencyKey)
	if err != nil {
		return RequestDTO{}, err
	}
	return requestDTO(row), nil
}

func (s *Service) ApproveRequest(ctx context.Context, actor Actor, requestID int64, note string) (RequestDTO, error) {
	if !actor.User.HasScope("admin") {
		return RequestDTO{}, ErrPolicyDenied
	}
	if s == nil || s.Store == nil || s.Store.Queries == nil || requestID <= 0 {
		return RequestDTO{}, ErrNotFound
	}
	tx, q, err := s.Store.BeginImmediateTx(ctx)
	if err != nil {
		return RequestDTO{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	row, err := s.approvePendingRequestTx(ctx, q, actor, requestID, note, false)
	if err != nil {
		return RequestDTO{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RequestDTO{}, err
	}
	committed = true
	return requestDTO(row), nil
}

func (s *Service) RejectRequest(ctx context.Context, actor Actor, requestID int64, note string) (RequestDTO, error) {
	if !actor.User.HasScope("admin") {
		return RequestDTO{}, ErrPolicyDenied
	}
	if s == nil || s.Store == nil || s.Store.Queries == nil || requestID <= 0 {
		return RequestDTO{}, ErrNotFound
	}
	tx, q, err := s.Store.BeginImmediateTx(ctx)
	if err != nil {
		return RequestDTO{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	before, err := q.GetDiscoverySubscriptionRequest(ctx, queries.GetDiscoverySubscriptionRequestParams{ID: requestID})
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDTO{}, ErrNotFound
	}
	if err != nil {
		return RequestDTO{}, err
	}
	if before.Status == "rejected" {
		if err := tx.Commit(ctx); err != nil {
			return RequestDTO{}, err
		}
		committed = true
		return requestDTO(before), nil
	}
	if before.Status != "pending_review" {
		return RequestDTO{}, ErrInvalidTransition
	}

	now := nowUnix(s)
	row, err := q.RejectDiscoverySubscriptionRequest(ctx, queries.RejectDiscoverySubscriptionRequestParams{
		AdminNote:     sqlNullString(strings.TrimSpace(note)),
		ReviewedBy:    sqlNullString(actorUserID(actor)),
		ReviewedAt:    sql.NullInt64{Int64: now, Valid: true},
		UpdatedAt:     now,
		ID:            requestID,
		CurrentStatus: "pending_review",
	})
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDTO{}, ErrInvalidTransition
	}
	if err != nil {
		return RequestDTO{}, err
	}
	if err := appendRequestEventWithNoteWithQueries(ctx, q, row.ID, actor, "rejected", "pending_review", "rejected", "admin_action", now); err != nil {
		return RequestDTO{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RequestDTO{}, err
	}
	committed = true
	return requestDTO(row), nil
}

func (s *Service) ListRequestsForAdmin(ctx context.Context, actor Actor, status string, limit, offset int64) ([]RequestDTO, error) {
	if !actor.User.HasScope("admin") {
		return nil, ErrPolicyDenied
	}
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return nil, ErrPolicyDenied
	}
	status = strings.TrimSpace(status)
	if status != "" && !isAllowedRequestStatus(status) {
		return nil, ErrInvalidRequest
	}
	limit, offset, err := normalizePagination(limit, offset)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.ListDiscoverySubscriptionRequestsForAdmin(ctx, queries.ListDiscoverySubscriptionRequestsForAdminParams{
		Status: status,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]RequestDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, requestDTO(row))
	}
	return out, nil
}

func (s *Service) GetRequestForAdmin(ctx context.Context, actor Actor, requestID int64) (RequestDTO, error) {
	if !actor.User.HasScope("admin") {
		return RequestDTO{}, ErrPolicyDenied
	}
	if s == nil || s.Store == nil || s.Store.Queries == nil || requestID <= 0 {
		return RequestDTO{}, ErrNotFound
	}
	row, err := s.Store.GetDiscoverySubscriptionRequest(ctx, queries.GetDiscoverySubscriptionRequestParams{ID: requestID})
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDTO{}, ErrNotFound
	}
	if err != nil {
		return RequestDTO{}, err
	}
	return requestDTO(row), nil
}

func (s *Service) ListRequests(ctx context.Context, actor Actor, limit, offset int64) ([]RequestDTO, error) {
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
	rows, err := s.Store.ListDiscoverySubscriptionRequestsForUser(ctx, queries.ListDiscoverySubscriptionRequestsForUserParams{
		RequesterUserID: userID,
		Limit:           limit,
		Offset:          offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]RequestDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, requestDTO(row))
	}
	return out, nil
}

func (s *Service) GetRequest(ctx context.Context, actor Actor, id int64) (RequestDTO, error) {
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return RequestDTO{}, ErrPolicyDenied
	}
	userID := actorUserID(actor)
	if userID == "" || id <= 0 {
		return RequestDTO{}, ErrNotFound
	}
	row, err := s.Store.GetDiscoverySubscriptionRequestForUser(ctx, queries.GetDiscoverySubscriptionRequestForUserParams{
		ID:              id,
		RequesterUserID: userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDTO{}, ErrNotFound
	}
	if err != nil {
		return RequestDTO{}, err
	}
	return requestDTO(row), nil
}

func (s *Service) CancelRequest(ctx context.Context, actor Actor, id int64) (RequestDTO, error) {
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return RequestDTO{}, ErrPolicyDenied
	}
	userID := actorUserID(actor)
	if userID == "" || id <= 0 {
		return RequestDTO{}, ErrNotFound
	}
	tx, q, err := s.Store.BeginImmediateTx(ctx)
	if err != nil {
		return RequestDTO{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	before, err := q.GetDiscoverySubscriptionRequestForUser(ctx, queries.GetDiscoverySubscriptionRequestForUserParams{
		ID:              id,
		RequesterUserID: userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDTO{}, ErrNotFound
	}
	if err != nil {
		return RequestDTO{}, err
	}
	if before.Status != "pending_review" {
		return RequestDTO{}, ErrInvalidTransition
	}
	now := nowUnix(s)
	row, err := q.CancelPendingDiscoverySubscriptionRequest(ctx, queries.CancelPendingDiscoverySubscriptionRequestParams{
		ID:              id,
		RequesterUserID: userID,
		UpdatedAt:       now,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDTO{}, ErrInvalidTransition
	}
	if err != nil {
		return RequestDTO{}, err
	}
	if err := appendRequestEventWithQueries(ctx, q, row.ID, actor, "canceled", "pending_review", "canceled", now); err != nil {
		return RequestDTO{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RequestDTO{}, err
	}
	committed = true
	return requestDTO(row), nil
}

func (s *Service) RecordUserAuditEvent(ctx context.Context, actor Actor, action, targetKind, targetID, safeReason string) error {
	if s == nil || s.Store == nil || s.Store.Queries == nil {
		return ErrPolicyDenied
	}
	action = safeAuditAction(action)
	targetKind = safeAuditValue(targetKind, "unknown")
	safeReason = safeAuditValue(safeReason, "policy-denied")
	return createUserAuditEventWithQueries(ctx, s.Store.Queries, actor, action, targetKind, targetID, safeReason, nowUnix(s))
}

func createUserAuditEventWithQueries(ctx context.Context, q *queries.Queries, actor Actor, action, targetKind, targetID, safeReason string, now int64) error {
	_, err := q.CreateDiscoveryUserAuditEvent(ctx, queries.CreateDiscoveryUserAuditEventParams{
		ActorUserID:  sqlNullString(actorUserID(actor)),
		Action:       action,
		TargetKind:   targetKind,
		TargetID:     sqlNullString(safeAuditTargetID(targetID)),
		SafeReason:   safeReason,
		SnapshotJson: "{}",
		CreatedAt:    now,
	})
	return err
}

func (s *Service) preflightRequestCreate(ctx context.Context, actor Actor, policy queries.DiscoveryAccessPolicy, tmdbID, idempotencyKey string) (queries.DiscoverySubscriptionRequest, bool, error) {
	tx, q, err := s.Store.BeginImmediateTx(ctx)
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	existing, err := q.GetDiscoverySubscriptionRequestByIdempotency(ctx, queries.GetDiscoverySubscriptionRequestByIdempotencyParams{
		IdempotencyKey: idempotencyKey,
	})
	if err == nil {
		return existing, true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, false, err
	}

	now := nowUnix(s)
	if err := enforceRequestLimitsWithQueries(ctx, q, actor, policy, tmdbID, now); err != nil {
		if errors.Is(err, ErrLimitReached) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return queries.DiscoverySubscriptionRequest{}, false, commitErr
			}
			committed = true
		}
		return queries.DiscoverySubscriptionRequest{}, false, err
	}
	return queries.DiscoverySubscriptionRequest{}, false, nil
}

func enforceRequestLimitsWithQueries(ctx context.Context, q *queries.Queries, actor Actor, policy queries.DiscoveryAccessPolicy, tmdbID string, now int64) error {
	userID := actorUserID(actor)
	if policy.MaxPendingRequests.Valid {
		count, err := q.CountPendingDiscoveryRequestsForUser(ctx, queries.CountPendingDiscoveryRequestsForUserParams{
			RequesterUserID: userID,
		})
		if err != nil {
			return err
		}
		if count >= policy.MaxPendingRequests.Int64 {
			if err := createUserAuditEventWithQueries(ctx, q, actor, "request_create_deny", "request", tmdbID, "request-limit-reached", now); err != nil {
				return err
			}
			return ErrLimitReached
		}
	}
	if policy.MaxActiveSubscriptions.Valid {
		count, err := q.CountActiveUserMediaSubscriptions(ctx, queries.CountActiveUserMediaSubscriptionsParams{
			EchoUserID: userID,
		})
		if err != nil {
			return err
		}
		if count >= policy.MaxActiveSubscriptions.Int64 {
			if err := createUserAuditEventWithQueries(ctx, q, actor, "request_create_deny", "request", tmdbID, "request-limit-reached", now); err != nil {
				return err
			}
			return ErrLimitReached
		}
	}
	if policy.RequestCooldownSeconds.Valid && policy.RequestCooldownSeconds.Int64 > 0 {
		count, err := q.CountRecentDiscoveryRequestsForUser(ctx, queries.CountRecentDiscoveryRequestsForUserParams{
			RequesterUserID: userID,
			CreatedSince:    now - policy.RequestCooldownSeconds.Int64,
		})
		if err != nil {
			return err
		}
		if count > 0 {
			if err := createUserAuditEventWithQueries(ctx, q, actor, "rate_limit_deny", "request", tmdbID, "rate-limit-reached", now); err != nil {
				return err
			}
			return ErrLimitReached
		}
	}
	return nil
}

func (s *Service) fetchDetails(ctx context.Context, tmdbID, mediaType string) (tmdb.Media, error) {
	if s == nil || s.TMDB == nil {
		return tmdb.Media{}, ErrMetadataUnavailable
	}
	var (
		media tmdb.Media
		err   error
	)
	switch mediaType {
	case "movie":
		media, err = s.TMDB.MovieDetails(ctx, tmdbID)
	case "tv":
		media, err = s.TMDB.TVDetails(ctx, tmdbID)
	default:
		return tmdb.Media{}, ErrInvalidRequest
	}
	if err != nil {
		return tmdb.Media{}, fmt.Errorf("%w: %v", ErrMetadataUnavailable, err)
	}
	return media, nil
}

func (s *Service) upsertTMDBMedia(ctx context.Context, media tmdb.Media, language string) error {
	rawJSON := strings.TrimSpace(media.RawJSON)
	if rawJSON == "" {
		data, err := json.Marshal(media)
		if err != nil {
			rawJSON = "{}"
		} else {
			rawJSON = string(data)
		}
	}
	if rawJSON == "" {
		rawJSON = "{}"
	}
	now := nowUnix(s)
	_, err := s.Store.UpsertTMDBMedia(ctx, queries.UpsertTMDBMediaParams{
		TmdbID:        media.TMDBID,
		MediaType:     media.MediaType,
		Language:      language,
		Title:         media.Title,
		OriginalTitle: sqlNullString(media.OriginalTitle),
		ReleaseYear:   sqlNullInt64(int64(media.ReleaseYear)),
		PosterPath:    sqlNullString(media.PosterPath),
		Status:        sql.NullString{String: "ok", Valid: true},
		RawJson:       rawJSON,
		FetchedAt:     now,
		NextRefreshAt: now + 86400,
	})
	return err
}

func (s *Service) createPendingRequest(
	ctx context.Context,
	actor Actor,
	policy queries.DiscoveryAccessPolicy,
	target queries.LoadDiscoveryPolicyTargetBundleRow,
	media tmdb.Media,
	language,
	seasonFilterJSON,
	userNote,
	idempotencyKey string,
) (queries.DiscoverySubscriptionRequest, error) {
	tx, q, err := s.Store.BeginImmediateTx(ctx)
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	existing, err := q.GetDiscoverySubscriptionRequestByIdempotency(ctx, queries.GetDiscoverySubscriptionRequestByIdempotencyParams{
		IdempotencyKey: idempotencyKey,
	})
	if err == nil {
		return existing, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, err
	}

	now := nowUnix(s)
	if err := enforceRequestLimitsWithQueries(ctx, q, actor, policy, media.TMDBID, now); err != nil {
		if errors.Is(err, ErrLimitReached) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return queries.DiscoverySubscriptionRequest{}, commitErr
			}
			committed = true
		}
		return queries.DiscoverySubscriptionRequest{}, err
	}

	row, err := createPendingRequestTx(ctx, q, actor, policy, target, media, language, seasonFilterJSON, userNote, idempotencyKey, now)
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	committed = true
	return row, nil
}

func (s *Service) createAndApproveRequest(
	ctx context.Context,
	actor Actor,
	policy queries.DiscoveryAccessPolicy,
	target queries.LoadDiscoveryPolicyTargetBundleRow,
	media tmdb.Media,
	language,
	seasonFilterJSON,
	userNote,
	idempotencyKey string,
) (queries.DiscoverySubscriptionRequest, error) {
	tx, q, err := s.Store.BeginImmediateTx(ctx)
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	existing, err := q.GetDiscoverySubscriptionRequestByIdempotency(ctx, queries.GetDiscoverySubscriptionRequestByIdempotencyParams{
		IdempotencyKey: idempotencyKey,
	})
	if err == nil {
		if existing.Status == "pending_review" {
			existing, err = s.approvePendingRequestTx(ctx, q, actor, existing.ID, "", true)
			if err != nil {
				return queries.DiscoverySubscriptionRequest{}, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return queries.DiscoverySubscriptionRequest{}, err
		}
		committed = true
		return existing, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, err
	}

	now := nowUnix(s)
	if err := enforceRequestLimitsWithQueries(ctx, q, actor, policy, media.TMDBID, now); err != nil {
		if errors.Is(err, ErrLimitReached) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return queries.DiscoverySubscriptionRequest{}, commitErr
			}
			committed = true
		}
		return queries.DiscoverySubscriptionRequest{}, err
	}

	pending, err := createPendingRequestTx(ctx, q, actor, policy, target, media, language, seasonFilterJSON, userNote, idempotencyKey, now)
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	row, err := s.approvePendingRequestTx(ctx, q, actor, pending.ID, "", true)
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	committed = true
	return row, nil
}

func createPendingRequestTx(
	ctx context.Context,
	q *queries.Queries,
	actor Actor,
	policy queries.DiscoveryAccessPolicy,
	target queries.LoadDiscoveryPolicyTargetBundleRow,
	media tmdb.Media,
	language,
	seasonFilterJSON,
	userNote,
	idempotencyKey string,
	now int64,
) (queries.DiscoverySubscriptionRequest, error) {
	row, err := q.CreateDiscoverySubscriptionRequest(ctx, queries.CreateDiscoverySubscriptionRequestParams{
		RequesterUserID:             actorUserID(actor),
		Status:                      "pending_review",
		TmdbID:                      media.TMDBID,
		MediaType:                   media.MediaType,
		TmdbLanguage:                language,
		TitleSnapshot:               media.Title,
		OriginalTitleSnapshot:       sqlNullString(media.OriginalTitle),
		ReleaseYearSnapshot:         sqlNullInt64(int64(media.ReleaseYear)),
		PosterPathSnapshot:          sqlNullString(media.PosterPath),
		SeasonFilterJson:            sqlNullString(seasonFilterJSON),
		PolicyIDSnapshot:            sql.NullInt64{Int64: policy.ID, Valid: true},
		PolicyTargetIDSnapshot:      sql.NullInt64{Int64: target.TargetID, Valid: true},
		TargetLabelSnapshot:         target.TargetLabel,
		TargetLibraryID:             target.TargetLibraryID,
		TargetLibraryNameSnapshot:   target.TargetLibraryName,
		ProducerProfileIDSnapshot:   target.ProducerProfileID,
		ProducerProfileNameSnapshot: target.ProducerProfileName,
		RuleProfileIDSnapshot:       target.RuleProfileID,
		RuleProfileVersionSnapshot:  target.RuleProfileVersion,
		UserNote:                    sqlNullString(userNote),
		AdminNote:                   sql.NullString{},
		ReviewedBy:                  sql.NullString{},
		ReviewedAt:                  sql.NullInt64{},
		SubscriptionID:              sql.NullInt64{},
		IdempotencyKey:              idempotencyKey,
		LastErrorKind:               sql.NullString{},
		LastErrorMessage:            sql.NullString{},
		CreatedAt:                   now,
		UpdatedAt:                   now,
	})
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if _, err := q.CreateDiscoverySubscriptionRequestEvent(ctx, queries.CreateDiscoverySubscriptionRequestEventParams{
		RequestID:    row.ID,
		ActorUserID:  sqlNullString(actorUserID(actor)),
		Action:       "created",
		FromStatus:   sql.NullString{},
		ToStatus:     sql.NullString{String: "pending_review", Valid: true},
		Note:         sql.NullString{},
		SnapshotJson: "{}",
		CreatedAt:    now,
	}); err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	return row, nil
}

func (s *Service) approvePendingRequestTx(ctx context.Context, q *queries.Queries, actor Actor, requestID int64, note string, auto bool) (queries.DiscoverySubscriptionRequest, error) {
	request, err := q.GetDiscoverySubscriptionRequest(ctx, queries.GetDiscoverySubscriptionRequestParams{ID: requestID})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, ErrNotFound
	}
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if request.Status == "approved" {
		return request, nil
	}
	if request.Status != "pending_review" {
		return queries.DiscoverySubscriptionRequest{}, ErrInvalidTransition
	}
	if !request.PolicyTargetIDSnapshot.Valid {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	}

	target, err := q.LoadDiscoveryPolicyTargetBundle(ctx, queries.LoadDiscoveryPolicyTargetBundleParams{
		ID: request.PolicyTargetIDSnapshot.Int64,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	}
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if target.TargetEnabled != 1 {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	}
	if target.MediaType.Valid && target.MediaType.String != request.MediaType {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	}
	if _, err := q.GetLibrary(ctx, queries.GetLibraryParams{ID: target.TargetLibraryID}); errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	} else if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	producer, err := q.GetDiscoveryProducerProfile(ctx, queries.GetDiscoveryProducerProfileParams{ID: target.ProducerProfileID})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	}
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if producer.Enabled != 1 {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	}
	rule, err := q.GetRuleProfile(ctx, queries.GetRuleProfileParams{ID: target.RuleProfileID})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	}
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if rule.Enabled != 1 {
		return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
	}

	allowed, err := q.UserCanPlaybackLibrary(ctx, queries.UserCanPlaybackLibraryParams{
		LibraryID:  target.TargetLibraryID,
		EchoUserID: request.RequesterUserID,
	})
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if allowed != 1 {
		if target.GrantPlaybackOnApproval != 1 {
			return queries.DiscoverySubscriptionRequest{}, ErrPolicyDenied
		}
		if err := q.GrantLibraryPlayback(ctx, queries.GrantLibraryPlaybackParams{
			LibraryID:  target.TargetLibraryID,
			EchoUserID: request.RequesterUserID,
			CreatedBy:  sqlNullString(actorUserID(actor)),
			CreatedAt:  nowUnix(s),
			UpdatedAt:  nowUnix(s),
		}); err != nil {
			return queries.DiscoverySubscriptionRequest{}, err
		}
	}

	now := nowUnix(s)
	subscription, err := q.UpsertDiscoverySubscriptionForMediaRequest(ctx, queries.UpsertDiscoverySubscriptionForMediaRequestParams{
		OwnerID:           target.PipelineOwnerID,
		TmdbID:            request.TmdbID,
		MediaType:         request.MediaType,
		TmdbLanguage:      request.TmdbLanguage,
		TitleSnapshot:     request.TitleSnapshot,
		LibraryID:         target.TargetLibraryID,
		ProducerProfileID: target.ProducerProfileID,
		RuleProfileID:     target.RuleProfileID,
		Status:            "active",
		SeasonFilterJson:  sql.NullString{},
		NextCheckAt:       sql.NullInt64{},
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if subscription.ProducerProfileID != target.ProducerProfileID || subscription.RuleProfileID != target.RuleProfileID {
		return queries.DiscoverySubscriptionRequest{}, ErrConflict
	}

	seasonFilterJSON := ""
	if request.SeasonFilterJson.Valid {
		seasonFilterJSON = request.SeasonFilterJson.String
	}
	normalizedSeasonFilter, seasonFilterKey, err := ValidateSeasonFilterForMedia(request.MediaType, seasonFilterJSON)
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	if _, err := q.UpsertUserMediaSubscription(ctx, queries.UpsertUserMediaSubscriptionParams{
		EchoUserID:              request.RequesterUserID,
		RequestID:               sql.NullInt64{Int64: request.ID, Valid: true},
		DiscoverySubscriptionID: subscription.ID,
		TmdbID:                  request.TmdbID,
		MediaType:               request.MediaType,
		SeasonFilterJson:        sqlNullString(normalizedSeasonFilter),
		SeasonFilterKey:         seasonFilterKey,
		Status:                  "active",
		CreatedAt:               now,
		UpdatedAt:               now,
	}); err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}

	row, err := q.ApproveDiscoverySubscriptionRequest(ctx, queries.ApproveDiscoverySubscriptionRequestParams{
		AdminNote:      sqlNullString(strings.TrimSpace(note)),
		ReviewedBy:     sqlNullString(actorUserID(actor)),
		ReviewedAt:     sql.NullInt64{Int64: now, Valid: true},
		SubscriptionID: sql.NullInt64{Int64: subscription.ID, Valid: true},
		UpdatedAt:      now,
		ID:             request.ID,
		CurrentStatus:  "pending_review",
	})
	if errors.Is(err, sql.ErrNoRows) {
		return queries.DiscoverySubscriptionRequest{}, ErrInvalidTransition
	}
	if err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	eventNote := "admin_action"
	if auto {
		eventNote = "auto_approve"
	}
	if err := appendRequestEventWithNoteWithQueries(ctx, q, row.ID, actor, "approved", "pending_review", "approved", eventNote, now); err != nil {
		return queries.DiscoverySubscriptionRequest{}, err
	}
	return row, nil
}

func appendRequestEventWithQueries(ctx context.Context, q *queries.Queries, requestID int64, actor Actor, action, fromStatus, toStatus string, now int64) error {
	return appendRequestEventWithNoteWithQueries(ctx, q, requestID, actor, action, fromStatus, toStatus, "", now)
}

func appendRequestEventWithNoteWithQueries(ctx context.Context, q *queries.Queries, requestID int64, actor Actor, action, fromStatus, toStatus, note string, now int64) error {
	_, err := q.CreateDiscoverySubscriptionRequestEvent(ctx, queries.CreateDiscoverySubscriptionRequestEventParams{
		RequestID:    requestID,
		ActorUserID:  sqlNullString(actorUserID(actor)),
		Action:       action,
		FromStatus:   sqlNullString(fromStatus),
		ToStatus:     sqlNullString(toStatus),
		Note:         sqlNullString(strings.TrimSpace(note)),
		SnapshotJson: "{}",
		CreatedAt:    now,
	})
	return err
}

func requestDTO(row queries.DiscoverySubscriptionRequest) RequestDTO {
	dto := RequestDTO{
		ID:          row.ID,
		TMDBID:      row.TmdbID,
		MediaType:   row.MediaType,
		Title:       row.TitleSnapshot,
		TargetLabel: row.TargetLabelSnapshot,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.ReviewedAt.Valid {
		dto.ReviewedAt = row.ReviewedAt.Int64
	}
	if row.SubscriptionID.Valid {
		dto.SubscriptionID = row.SubscriptionID.Int64
	}
	if row.LastErrorKind.Valid {
		dto.SafeReason = safeRequestReason(row.LastErrorKind.String)
	}
	return dto
}

func requestIdempotencyKey(userID, tmdbID, mediaType, language string, targetID int64, seasonFilterJSON string) (string, error) {
	data, err := json.Marshal([]any{userID, tmdbID, mediaType, language, targetID, seasonFilterJSON})
	if err != nil {
		return "", fmt.Errorf("%w: idempotency key", ErrInvalidRequest)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func normalizePagination(limit, offset int64) (int64, int64, error) {
	if offset < 0 {
		return 0, 0, ErrInvalidRequest
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return limit, offset, nil
}

func nowUnix(s *Service) int64 {
	if s != nil && s.Now != nil {
		return s.Now().Unix()
	}
	return time.Now().Unix()
}

func actorUserID(actor Actor) string {
	return strings.TrimSpace(actor.User.UserID)
}

func normalizeMediaType(mediaType string) string {
	return strings.ToLower(strings.TrimSpace(mediaType))
}

func isAllowedRequestStatus(status string) bool {
	switch status {
	case "pending_review", "approved", "rejected", "canceled", "failed":
		return true
	default:
		return false
	}
}

func validTMDBLanguage(language string) bool {
	if language == "" || len([]byte(language)) > 32 {
		return false
	}
	return !strings.ContainsAny(language, " \t\r\n")
}

func sqlNullInt64(value int64) sql.NullInt64 {
	if value <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: value, Valid: true}
}

func safeAuditAction(action string) string {
	switch strings.TrimSpace(action) {
	case "policy_deny", "rate_limit_deny", "search_deny", "request_create_deny", "subscription_action_deny":
		return strings.TrimSpace(action)
	default:
		return "request_create_deny"
	}
}

func safeAuditValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len([]byte(value)) > 120 {
		return value[:120]
	}
	return value
}

func safeAuditTargetID(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	for len([]byte(value)) > 120 {
		runes = runes[:len(runes)-1]
		value = string(runes)
	}
	return value
}

func safeRequestReason(kind string) string {
	switch strings.TrimSpace(kind) {
	case "metadata_unavailable", "policy_denied", "request_failed":
		return strings.TrimSpace(kind)
	default:
		return "request_failed"
	}
}
