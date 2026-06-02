package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	defaultSchedulerInterval      = time.Minute
	defaultSchedulerLeaseDuration = 5 * time.Minute
	defaultSchedulerBatchLimit    = 50
)

type SchedulerConfig struct {
	Store         *Store
	Enqueue       func(ctx context.Context, kind string, payload any) (int64, error)
	Interval      time.Duration
	LeaseDuration time.Duration
	BatchLimit    int64
	Now           func() time.Time
	Logger        *slog.Logger
}

type Scheduler struct {
	cfg SchedulerConfig
}

func NewScheduler(cfg SchedulerConfig) *Scheduler {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultSchedulerInterval
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = defaultSchedulerLeaseDuration
	}
	if cfg.BatchLimit <= 0 {
		cfg.BatchLimit = defaultSchedulerBatchLimit
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Scheduler{cfg: cfg}
}

func (s *Scheduler) Tick(ctx context.Context) error {
	if s == nil {
		return errors.New("discovery scheduler is nil")
	}
	if s.cfg.Store == nil {
		return errors.New("discovery scheduler store is nil")
	}
	if s.cfg.Enqueue == nil {
		return errors.New("discovery scheduler enqueue is nil")
	}

	now := s.cfg.Now()
	lockedUntil := now.Add(s.cfg.LeaseDuration)
	limit := s.cfg.BatchLimit

	sources, err := s.cfg.Store.LeaseDueSources(ctx, now, lockedUntil, limit)
	if err != nil {
		return fmt.Errorf("lease due sources: %w", err)
	}
	for _, source := range sources {
		if _, err := s.cfg.Enqueue(ctx, KindSourceCrawl, SourceCrawlPayload{SourceID: source.ID}); err != nil {
			return fmt.Errorf("enqueue source crawl %d: %w", source.ID, err)
		}
	}

	subscriptions, err := s.cfg.Store.LeaseDueSubscriptions(ctx, now, lockedUntil, limit)
	if err != nil {
		return fmt.Errorf("lease due subscriptions: %w", err)
	}
	for _, subscription := range subscriptions {
		if _, err := s.cfg.Enqueue(ctx, KindSubscriptionCheck, SubscriptionCheckPayload{SubscriptionID: subscription.SubscriptionID}); err != nil {
			return fmt.Errorf("enqueue subscription check %d: %w", subscription.SubscriptionID, err)
		}
	}

	dispatchMatches, err := s.cfg.Store.ListDueDiscoveryDispatchMatches(ctx, limit)
	if err != nil {
		return fmt.Errorf("list due dispatch matches: %w", err)
	}
	for _, matchID := range dispatchMatches {
		if _, err := s.cfg.Enqueue(ctx, KindDispatch, DispatchPayload{MatchID: matchID}); err != nil {
			return fmt.Errorf("enqueue dispatch match %d: %w", matchID, err)
		}
	}

	reconcileMatches, err := s.cfg.Store.ListQueuedDiscoveryMatchesForReconcile(ctx, limit)
	if err != nil {
		return fmt.Errorf("list queued matches for reconcile: %w", err)
	}
	for _, match := range reconcileMatches {
		if _, err := s.cfg.Enqueue(ctx, KindReconcile, ReconcilePayload{MatchID: match.MatchID, JobID: match.JobID}); err != nil {
			return fmt.Errorf("enqueue reconcile match %d job %d: %w", match.MatchID, match.JobID, err)
		}
	}

	count, err := s.cfg.Store.CountDueTMDBMediaRefresh(ctx, now)
	if err != nil {
		return fmt.Errorf("count due tmdb media refresh: %w", err)
	}
	if count > 0 {
		if _, err := s.cfg.Enqueue(ctx, KindTMDBRefresh, nil); err != nil {
			return fmt.Errorf("enqueue tmdb refresh: %w", err)
		}
	}

	return nil
}

func (s *Scheduler) Start(ctx context.Context) <-chan error {
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		sendErr := func(err error) bool {
			if err == nil {
				return true
			}
			select {
			case errs <- err:
				return true
			case <-ctx.Done():
				return false
			}
		}

		if !sendErr(s.Tick(ctx)) {
			return
		}

		ticker := time.NewTicker(s.cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !sendErr(s.Tick(ctx)) {
					return
				}
			}
		}
	}()
	return errs
}
