package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/xmm2022/echo/internal/store/queries"
)

func TestDiscoveryMigrationCreatesCoreTables(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	tables := []string{
		"tmdb_media",
		"discovery_sources",
		"telegram_channels",
		"discovery_producer_profiles",
		"rule_profiles",
		"discovery_subscriptions",
		"discovered_resources",
		"subscription_matches",
		"discovery_runs",
		"discovery_raw_access_events",
	}
	for _, table := range tables {
		var name string
		err := st.DB.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Fatalf("missing table %s: %v", table, err)
		}
	}
}

func TestDiscoveryDispatchClaimRequiresAccepted115Share(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	q := st.Queries
	seed := seedDiscoveryDispatchTest(t, ctx, q)
	job := createDiscoveryDispatchJob(t, ctx, q)

	cases := []struct {
		name       string
		decision   string
		provider   string
		linkKind   string
		status     string
		wantNoRows bool
	}{
		{name: "reject decision", decision: "reject", provider: "115", linkKind: "115_share", status: "accepted", wantNoRows: true},
		{name: "review decision", decision: "review", provider: "115", linkKind: "115_share", status: "accepted", wantNoRows: true},
		{name: "non 115 provider", decision: "accept", provider: "other", linkKind: "115_share", status: "accepted", wantNoRows: true},
		{name: "unsupported resource", decision: "accept", provider: "115", linkKind: "115_share", status: "unsupported_provider", wantNoRows: true},
		{name: "accepted 115 share", decision: "accept", provider: "115", linkKind: "115_share", status: "accepted"},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match := createDiscoveryMatchForDispatch(t, ctx, q, seed, i, tc.provider, tc.linkKind, tc.status, tc.decision)
			_, loadErr := q.LoadDispatchBundle(ctx, queries.LoadDispatchBundleParams{ID: match.ID})
			claimed, claimErr := q.LinkSubscriptionMatchDispatchJobIfClaimable(ctx, queries.LinkSubscriptionMatchDispatchJobIfClaimableParams{
				QueuedJobID: sql.NullInt64{Int64: job.ID, Valid: true},
				UpdatedAt:   100,
				DecidedAt:   sql.NullInt64{Int64: 100, Valid: true},
				ID:          match.ID,
			})

			if tc.wantNoRows {
				if !errors.Is(loadErr, sql.ErrNoRows) {
					t.Fatalf("load dispatch bundle err = %v, want sql.ErrNoRows", loadErr)
				}
				if !errors.Is(claimErr, sql.ErrNoRows) {
					t.Fatalf("claim err = %v, want sql.ErrNoRows", claimErr)
				}
				return
			}
			if loadErr != nil {
				t.Fatalf("load dispatch bundle: %v", loadErr)
			}
			if claimErr != nil {
				t.Fatalf("claim accepted 115 share: %v", claimErr)
			}
			if claimed.Decision != "queue" || claimed.DispatchState != "queued" || !claimed.QueuedJobID.Valid || claimed.QueuedJobID.Int64 != job.ID {
				t.Fatalf("claimed match = %#v, want queued decision/state/job", claimed)
			}
		})
	}
}

func TestDiscoveryProducerProfileTargetAccountMustMatch115Provider(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	q := st.Queries

	createAccountWith(t, ctx, q, "other-main", "other", "/other-main")
	if _, err := q.CreateDiscoveryProducerProfile(ctx, queries.CreateDiscoveryProducerProfileParams{
		Name:                   "bad-profile",
		Provider:               "115",
		Tool:                   "115share2cas",
		TargetAccount:          "other-main",
		TargetSubdirTemplate:   "",
		LibraryRelPathTemplate: "",
		DefaultArgsJson:        "{}",
		Enabled:                1,
		CreatedAt:              1,
		UpdatedAt:              1,
	}); err == nil {
		t.Fatal("created 115 producer profile targeting non-115 account, want foreign key failure")
	}

	createAccountWith(t, ctx, q, "profile-115", "115", "/profile-115")
	if _, err := q.CreateDiscoveryProducerProfile(ctx, queries.CreateDiscoveryProducerProfileParams{
		Name:                   "good-profile",
		Provider:               "115",
		Tool:                   "115share2cas",
		TargetAccount:          "profile-115",
		TargetSubdirTemplate:   "",
		LibraryRelPathTemplate: "",
		DefaultArgsJson:        "{}",
		Enabled:                1,
		CreatedAt:              1,
		UpdatedAt:              1,
	}); err != nil {
		t.Fatalf("create 115 producer profile targeting 115 account: %v", err)
	}
}

type discoveryDispatchSeed struct {
	SourceID       int64
	SubscriptionID int64
	RuleProfileID  int64
}

func seedDiscoveryDispatchTest(t *testing.T, ctx context.Context, q *queries.Queries) discoveryDispatchSeed {
	t.Helper()

	createAccountWith(t, ctx, q, "dispatch-115", "115", "/dispatch-115")
	library := createLibrary(t, ctx, q)
	profile, err := q.CreateDiscoveryProducerProfile(ctx, queries.CreateDiscoveryProducerProfileParams{
		Name:                   "dispatch-profile",
		Provider:               "115",
		Tool:                   "115share2cas",
		TargetAccount:          "dispatch-115",
		TargetSubdirTemplate:   "",
		LibraryRelPathTemplate: "",
		DefaultArgsJson:        "{}",
		Enabled:                1,
		CreatedAt:              1,
		UpdatedAt:              1,
	})
	if err != nil {
		t.Fatalf("create discovery producer profile: %v", err)
	}
	rule, err := q.CreateRuleProfile(ctx, queries.CreateRuleProfileParams{
		Name:      "dispatch-rules",
		Version:   1,
		RulesJson: "{}",
		Enabled:   1,
		CreatedAt: 1,
		UpdatedAt: 1,
	})
	if err != nil {
		t.Fatalf("create rule profile: %v", err)
	}
	subscription, err := q.CreateDiscoverySubscription(ctx, queries.CreateDiscoverySubscriptionParams{
		OwnerID:           "admin",
		TmdbID:            "123",
		MediaType:         "movie",
		TmdbLanguage:      "zh-CN",
		TitleSnapshot:     "Movie",
		LibraryID:         library.ID,
		ProducerProfileID: profile.ID,
		RuleProfileID:     rule.ID,
		Status:            "active",
		SeasonFilterJson:  sql.NullString{},
		NextCheckAt:       sql.NullInt64{},
		CreatedAt:         1,
		UpdatedAt:         1,
	})
	if err != nil {
		t.Fatalf("create discovery subscription: %v", err)
	}
	source, err := q.CreateDiscoverySource(ctx, queries.CreateDiscoverySourceParams{
		Kind:          "manual",
		Name:          "manual",
		Enabled:       1,
		ConfigJson:    "{}",
		SecretRef:     sql.NullString{},
		RateLimitJson: sql.NullString{},
		NextRunAt:     sql.NullInt64{},
		CreatedAt:     1,
		UpdatedAt:     1,
	})
	if err != nil {
		t.Fatalf("create discovery source: %v", err)
	}
	return discoveryDispatchSeed{SourceID: source.ID, SubscriptionID: subscription.ID, RuleProfileID: rule.ID}
}

func createDiscoveryMatchForDispatch(
	t *testing.T,
	ctx context.Context,
	q *queries.Queries,
	seed discoveryDispatchSeed,
	index int,
	provider string,
	linkKind string,
	status string,
	decision string,
) queries.SubscriptionMatch {
	t.Helper()

	resource, err := q.UpsertDiscoveredResource(ctx, queries.UpsertDiscoveredResourceParams{
		SourceID:         seed.SourceID,
		Provider:         provider,
		LinkKind:         linkKind,
		ExternalKey:      "resource-" + string(rune('a'+index)),
		TmdbID:           sql.NullString{String: "123", Valid: true},
		MediaType:        sql.NullString{String: "movie", Valid: true},
		Title:            sql.NullString{String: "Movie", Valid: true},
		SeasonNumber:     sql.NullInt64{},
		EpisodeStart:     sql.NullInt64{},
		EpisodeEnd:       sql.NullInt64{},
		ShareCode:        sql.NullString{String: "share", Valid: true},
		ReceiveCode:      sql.NullString{String: "receive", Valid: true},
		ShareUrlRedacted: sql.NullString{String: "https://115.example/s/share", Valid: true},
		RawTextRedacted:  sql.NullString{},
		RawTextRef:       sql.NullString{},
		ParsedJson:       "{}",
		FeatureJson:      "{}",
		Status:           status,
		FirstSeenAt:      1,
		LastSeenAt:       1,
	})
	if err != nil {
		t.Fatalf("upsert discovered resource: %v", err)
	}
	match, err := q.CreateSubscriptionMatch(ctx, queries.CreateSubscriptionMatchParams{
		SubscriptionID:     seed.SubscriptionID,
		ResourceID:         resource.ID,
		RuleProfileID:      seed.RuleProfileID,
		RuleProfileVersion: 1,
		SeasonNumber:       sql.NullInt64{},
		EpisodeStart:       sql.NullInt64{},
		EpisodeEnd:         sql.NullInt64{},
		ScoreJson:          "{}",
		PreviousScoreJson:  sql.NullString{},
		Decision:           decision,
		Reason:             "test",
		DispatchState:      "none",
		IdempotencyKey:     "match-" + string(rune('a'+index)),
		CreatedAt:          1,
		UpdatedAt:          1,
		DecidedAt:          sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("create subscription match: %v", err)
	}
	return match
}

func createDiscoveryDispatchJob(t *testing.T, ctx context.Context, q *queries.Queries) queries.Job {
	t.Helper()
	job, err := q.CreateJob(ctx, queries.CreateJobParams{
		Kind:       "discovery_dispatch",
		Status:     "pending",
		Payload:    "{}",
		Progress:   sql.NullString{},
		Error:      sql.NullString{},
		OwnerID:    "admin",
		CreatedAt:  1,
		StartedAt:  sql.NullInt64{},
		FinishedAt: sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("create discovery dispatch job: %v", err)
	}
	return job
}
