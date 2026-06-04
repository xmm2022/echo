package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/xmm2022/echo/internal/discovery/dispatch"
	"github.com/xmm2022/echo/internal/discovery/rules"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	storeq "github.com/xmm2022/echo/internal/store/queries"
)

const (
	runKindSourceCrawl       = "source_crawl"
	runKindSubscriptionCheck = "subscription_check"
	rawPreviewMaxBytes       = 4096
	defaultDiscoveryDelay    = time.Hour
	defaultBackoffDelay      = 5 * time.Minute
	tmdbRefreshLeaseDelay    = 10 * time.Minute
	tmdbRefreshTTL           = 24 * time.Hour
	tmdbRefreshLimit         = 100
)

type SourceAdapter interface {
	Crawl(ctx context.Context, source Source) (SourceCrawlResult, error)
}

type TMDBClient interface {
	MovieDetails(ctx context.Context, tmdbID string) (tmdb.Media, error)
	TVDetails(ctx context.Context, tmdbID string) (tmdb.Media, error)
	Search(ctx context.Context, query, mediaType string) ([]tmdb.Media, error)
}

type freshTMDBClient interface {
	MovieDetailsFresh(ctx context.Context, tmdbID string) (tmdb.Media, error)
	TVDetailsFresh(ctx context.Context, tmdbID string) (tmdb.Media, error)
}

type SourceCrawlPayload struct {
	SourceID int64 `json:"source_id"`
}

type SubscriptionCheckPayload struct {
	SubscriptionID int64 `json:"subscription_id"`
}

type DispatchPayload struct {
	MatchID int64 `json:"match_id"`
}

type ReconcilePayload struct {
	MatchID int64 `json:"match_id"`
	JobID   int64 `json:"job_id"`
}

type Orchestrator struct {
	deps Deps
}

type Deps struct {
	Store          *Store
	RawStore       *RawStore
	SourceAdapters map[SourceKind]SourceAdapter
	TMDB           TMDBClient
	Enqueue        func(ctx context.Context, kind string, payload any) (int64, error)
	NotifyJob      func(jobID int64)
	ProducerConfig ingest.ProducerConfig
	Logger         *slog.Logger
	Now            func() time.Time
}

func NewOrchestrator(deps Deps) *Orchestrator {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Orchestrator{deps: deps}
}

func (o *Orchestrator) RunSourceCrawl(ctx context.Context, payload SourceCrawlPayload) error {
	if err := o.requireStore(); err != nil {
		return err
	}
	now := o.now()
	runID, err := o.deps.Store.CreateDiscoveryRun(ctx, runKindSourceCrawl, RunIDs{SourceID: payload.SourceID}, now)
	if err != nil {
		return err
	}

	source, err := o.deps.Store.GetSource(ctx, payload.SourceID)
	if err != nil {
		if errors.Is(err, errSourceConfig) {
			backoffUntil := o.now().Add(defaultBackoffDelay)
			backoffErr := o.deps.Store.BackoffSource(ctx, payload.SourceID, backoffUntil, "invalid_config", err.Error())
			if channelErr := o.deps.Store.BackoffTelegramChannelsBySource(ctx, payload.SourceID, backoffUntil, "invalid_config", err.Error(), o.now()); backoffErr == nil {
				backoffErr = channelErr
			}
			_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
			if backoffErr != nil {
				return fmt.Errorf("record crawl backoff: %w", backoffErr)
			}
			return err
		}
		_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
		return err
	}
	adapter, ok := o.deps.SourceAdapters[SourceKind(source.Kind)]
	if !ok || adapter == nil {
		err := fmt.Errorf("no source adapter for %q", source.Kind)
		_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
		return err
	}
	result, err := adapter.Crawl(ctx, source)
	if err != nil {
		backoffUntil := o.now().Add(crawlBackoffDelay(err))
		errorKind := crawlErrorKind(err)
		backoffErr := o.deps.Store.BackoffSource(ctx, payload.SourceID, backoffUntil, errorKind, err.Error())
		if source.Kind == SourceTelegramMTProto {
			if channelErr := o.deps.Store.BackoffTelegramChannelsBySource(ctx, payload.SourceID, backoffUntil, errorKind, err.Error(), o.now()); backoffErr == nil {
				backoffErr = channelErr
			}
		}
		_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
		if backoffErr != nil {
			return fmt.Errorf("record crawl backoff: %w", backoffErr)
		}
		return err
	}

	for _, item := range result.Items {
		if len(item.RawText) > 0 {
			item, err = o.prepareRawText(ctx, source, item)
			if err != nil {
				_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
				return err
			}
		}
		if _, err := o.deps.Store.UpsertDiscoveredResource(ctx, payload.SourceID, item); err != nil {
			_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
			return err
		}
	}
	for _, cursor := range result.TelegramCursors {
		if err := o.deps.Store.UpdateTelegramCursor(ctx, payload.SourceID, cursor, o.now()); err != nil {
			_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
			return err
		}
	}
	if err := o.deps.Store.ClearSourceLease(ctx, payload.SourceID, o.now().Add(defaultDiscoveryDelay)); err != nil {
		_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
		return err
	}
	counters, _ := json.Marshal(map[string]int{
		"items":            len(result.Items),
		"telegram_cursors": len(result.TelegramCursors),
	})
	return o.deps.Store.FinishDiscoveryRun(ctx, runID, "succeeded", string(counters), nil, o.now())
}

func (o *Orchestrator) RunSubscriptionCheck(ctx context.Context, payload SubscriptionCheckPayload) error {
	if err := o.requireStore(); err != nil {
		return err
	}
	now := o.now()
	runID, err := o.deps.Store.CreateDiscoveryRun(ctx, runKindSubscriptionCheck, RunIDs{SubscriptionID: payload.SubscriptionID}, now)
	if err != nil {
		return err
	}
	bundle, err := o.deps.Store.GetSubscriptionBundle(ctx, payload.SubscriptionID)
	if err != nil {
		return o.failSubscriptionCheck(ctx, runID, payload.SubscriptionID, err)
	}
	profile, err := rules.ParseProfileJSON([]byte(bundle.RuleProfileJSON))
	if err != nil {
		return o.failSubscriptionCheck(ctx, runID, payload.SubscriptionID, err)
	}
	candidates, err := o.deps.Store.ListCandidateResourcesForSubscription(ctx, bundle.TMDBID, bundle.MediaType, 100)
	if err != nil {
		return o.failSubscriptionCheck(ctx, runID, payload.SubscriptionID, err)
	}
	for _, candidate := range candidates {
		features := rules.ParseFeatures(candidate.Title, candidate.FeatureJSON)
		tuple, decision := rules.Score(features, profile)
		scoreJSON, err := json.Marshal(map[string]any{
			"tuple":    tuple,
			"decision": string(decision),
		})
		if err != nil {
			return o.failSubscriptionCheck(ctx, runID, payload.SubscriptionID, err)
		}
		matchDecision := string(decision)
		reason := "rule " + matchDecision
		if candidate.Provider != Provider115 || candidate.LinkKind != Link115Share {
			matchDecision = "review"
			reason = "unsupported_provider"
			if err := o.deps.Store.MarkDiscoveredResourceStatus(ctx, candidate.ID, "unsupported_provider", now); err != nil {
				return o.failSubscriptionCheck(ctx, runID, payload.SubscriptionID, err)
			}
		} else if bundle.MatchMode == "admin_review" && (matchDecision == "accept" || matchDecision == "queue") {
			matchDecision = "review"
			reason = "admin_review"
		}
		if _, err := o.deps.Store.CreateOrGetMatch(ctx, AcceptMatchParams{
			SubscriptionID:     bundle.SubscriptionID,
			ResourceID:         candidate.ID,
			RuleProfileID:      bundle.RuleProfileID,
			RuleProfileVersion: bundle.RuleProfileVersion,
			SeasonNumber:       candidate.SeasonNumber,
			EpisodeStart:       candidate.EpisodeStart,
			EpisodeEnd:         candidate.EpisodeEnd,
			ScoreJSON:          string(scoreJSON),
			Decision:           matchDecision,
			Reason:             reason,
			DispatchState:      "none",
			IdempotencyKey:     subscriptionCandidateKey(bundle.SubscriptionID, candidate.ID, bundle.RuleProfileVersion),
			Now:                now,
		}); err != nil {
			return o.failSubscriptionCheck(ctx, runID, payload.SubscriptionID, err)
		}
	}
	if err := o.deps.Store.ClearSubscriptionLease(ctx, payload.SubscriptionID, o.now().Add(defaultDiscoveryDelay)); err != nil {
		_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
		return err
	}
	counters, _ := json.Marshal(map[string]int{"candidates": len(candidates)})
	return o.deps.Store.FinishDiscoveryRun(ctx, runID, "succeeded", string(counters), nil, o.now())
}

func (o *Orchestrator) RunDispatch(ctx context.Context, payload DispatchPayload) error {
	if err := o.requireStore(); err != nil {
		return err
	}
	bundle, err := o.deps.Store.LoadDispatchBundle(ctx, payload.MatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	producerPayload, err := dispatch.BuildProducerPayload(bundle.Profile, bundle.Resource)
	if err != nil {
		return err
	}
	if err := dispatch.ValidatePayload(producerPayload, o.deps.ProducerConfig); err != nil {
		return err
	}
	jobID, claimed, err := o.deps.Store.ClaimAndCreateMatchDispatchJob(ctx, payload.MatchID, producerPayload, o.now())
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if o.deps.NotifyJob != nil {
		o.deps.NotifyJob(jobID)
	}
	return nil
}

func (o *Orchestrator) RunReconcile(ctx context.Context, payload ReconcilePayload) error {
	if err := o.requireStore(); err != nil {
		return err
	}
	bundle, err := o.deps.Store.LoadReconcileBundle(ctx, payload.MatchID, payload.JobID)
	if err != nil {
		return err
	}
	switch bundle.JobStatus {
	case "pending", "running":
		return nil
	case "failed":
		return o.deps.Store.MarkMatchFinished(ctx, MatchResult{
			MatchID:        bundle.MatchID,
			Decision:       "failed",
			DispatchState:  "failed",
			FailureKind:    "job_failed",
			FailureMessage: bundle.JobError,
			FinishedAt:     o.now(),
		})
	case "done":
		progress, ok, err := job.ParseProgress(sql.NullString{String: bundle.JobProgressJSON, Valid: bundle.JobProgressJSON != ""})
		if err != nil {
			return err
		}
		if ok && len(progress.FailedItems) > 0 {
			return o.deps.Store.MarkMatchFinished(ctx, MatchResult{
				MatchID:        bundle.MatchID,
				Decision:       "failed",
				DispatchState:  "failed",
				FailureKind:    "ingest_failed_items",
				FailureMessage: progress.FailedItems[0].Reason,
				FinishedAt:     o.now(),
			})
		}
		return o.deps.Store.MarkMatchFinished(ctx, MatchResult{
			MatchID:       bundle.MatchID,
			Decision:      "imported",
			DispatchState: "succeeded",
			FinishedAt:    o.now(),
		})
	default:
		return fmt.Errorf("unknown reconcile job status %q", bundle.JobStatus)
	}
}

func (o *Orchestrator) RunTMDBRefresh(ctx context.Context) error {
	if err := o.requireStore(); err != nil {
		return err
	}
	if o.deps.TMDB == nil {
		return errors.New("tmdb client is nil")
	}
	now := o.now()
	rows, err := o.deps.Store.store.LeaseDueTMDBMediaRefresh(ctx, storeq.LeaseDueTMDBMediaRefreshParams{
		NextRefreshAt:   now.Add(tmdbRefreshLeaseDelay).Unix(),
		NextRefreshAt_2: now.Unix(),
		Limit:           tmdbRefreshLimit,
	})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := o.refreshTMDBRow(ctx, row, now); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) requireStore() error {
	if o == nil || o.deps.Store == nil {
		return errors.New("discovery orchestrator store is nil")
	}
	return nil
}

func (o *Orchestrator) now() time.Time {
	if o.deps.Now == nil {
		return time.Now()
	}
	return o.deps.Now()
}

func (o *Orchestrator) prepareRawText(ctx context.Context, source Source, item ParsedResource) (ParsedResource, error) {
	if o.deps.RawStore != nil {
		ref, redacted, err := o.deps.RawStore.Put(ctx, fmt.Sprintf("%s:%d", source.Kind, source.ID), item.ExternalKey, item.RawText)
		if err == nil {
			item.RawTextRef = ref
			item.RawTextRedacted = redacted
			return item, nil
		}
		return item, fmt.Errorf("store raw source text: %w", err)
	}
	if strings.TrimSpace(item.RawTextRedacted) == "" {
		item.RawTextRedacted = capStringBytes(redactRawText(string(item.RawText)), rawPreviewMaxBytes)
	}
	item.RawTextRef = ""
	return item, nil
}

func (o *Orchestrator) failSubscriptionCheck(ctx context.Context, runID, subscriptionID int64, err error) error {
	_ = o.deps.Store.BackoffSubscription(ctx, subscriptionID, o.now().Add(defaultBackoffDelay), "temporary", err.Error())
	_ = o.deps.Store.FinishDiscoveryRun(ctx, runID, "failed", "{}", err, o.now())
	return err
}

func (o *Orchestrator) refreshTMDBRow(ctx context.Context, row storeq.TmdbMedium, now time.Time) error {
	var media tmdb.Media
	var err error
	media, err = o.fetchFreshTMDBMedia(ctx, row)
	if err != nil {
		if tmdb.IsKind(err, tmdb.KindRateLimited) || tmdb.IsKind(err, tmdb.KindTemporaryUnavailable) {
			_, upsertErr := o.deps.Store.store.UpsertTMDBMedia(ctx, storeq.UpsertTMDBMediaParams{
				TmdbID:           row.TmdbID,
				MediaType:        row.MediaType,
				Language:         row.Language,
				Title:            row.Title,
				OriginalTitle:    row.OriginalTitle,
				ReleaseYear:      row.ReleaseYear,
				PosterPath:       row.PosterPath,
				Status:           row.Status,
				RawJson:          row.RawJson,
				FetchedAt:        row.FetchedAt,
				NextRefreshAt:    now.Add(defaultBackoffDelay).Unix(),
				LastErrorKind:    nullStringEmpty(string(tmdb.ErrorKindOf(err))),
				LastErrorMessage: nullStringEmpty(err.Error()),
			})
			return upsertErr
		}
		return err
	}
	if media.MediaType == "" {
		media.MediaType = row.MediaType
	}
	if media.TMDBID == "" {
		media.TMDBID = row.TmdbID
	}
	_, err = o.deps.Store.store.UpsertTMDBMedia(ctx, storeq.UpsertTMDBMediaParams{
		TmdbID:        media.TMDBID,
		MediaType:     media.MediaType,
		Language:      row.Language,
		Title:         media.Title,
		OriginalTitle: nullStringEmpty(media.OriginalTitle),
		ReleaseYear:   nullInt64Zero(int64(media.ReleaseYear)),
		PosterPath:    nullStringEmpty(media.PosterPath),
		Status:        nullStringEmpty("ok"),
		RawJson:       media.RawJSON,
		FetchedAt:     now.Unix(),
		NextRefreshAt: now.Add(tmdbRefreshTTL).Unix(),
	})
	return err
}

func subscriptionCandidateKey(subscriptionID, resourceID, ruleProfileVersion int64) string {
	return fmt.Sprintf("subscription:%d:resource:%d:rule:%d", subscriptionID, resourceID, ruleProfileVersion)
}

type retryAfterError interface {
	RetryAfter() time.Duration
}

func crawlBackoffDelay(err error) time.Duration {
	var retry retryAfterError
	if errors.As(err, &retry) {
		if delay := retry.RetryAfter(); delay > defaultBackoffDelay {
			return delay
		}
	}
	return defaultBackoffDelay
}

func crawlErrorKind(err error) string {
	if err == nil {
		return ""
	}
	var authFailed AuthFailedError
	if errors.As(err, &authFailed) {
		return "auth_failed"
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "flood") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "rate-limited") ||
		strings.Contains(lower, "rate_limited") {
		return "rate_limited"
	}
	return "temporary"
}

func (o *Orchestrator) fetchFreshTMDBMedia(ctx context.Context, row storeq.TmdbMedium) (tmdb.Media, error) {
	if fresh, ok := o.deps.TMDB.(freshTMDBClient); ok {
		switch row.MediaType {
		case "movie":
			return fresh.MovieDetailsFresh(ctx, row.TmdbID)
		case "tv":
			return fresh.TVDetailsFresh(ctx, row.TmdbID)
		default:
			return tmdb.Media{}, fmt.Errorf("unsupported tmdb media_type %q", row.MediaType)
		}
	}
	switch row.MediaType {
	case "movie":
		return o.deps.TMDB.MovieDetails(ctx, row.TmdbID)
	case "tv":
		return o.deps.TMDB.TVDetails(ctx, row.TmdbID)
	default:
		return tmdb.Media{}, fmt.Errorf("unsupported tmdb media_type %q", row.MediaType)
	}
}
