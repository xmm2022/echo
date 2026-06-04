package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func TestPauseResumeUserSubscriptionOwnerOnly(t *testing.T) {
	ctx := context.Background()
	fixture := newSubscriptionFixture(t)

	if _, err := fixture.svc.PauseSubscription(ctx, mediaActor("u2"), fixture.userSubscription.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PauseSubscription as non-owner error = %v, want %v", err, ErrNotFound)
	}
	paused, err := fixture.svc.PauseSubscription(ctx, mediaActor("u1"), fixture.userSubscription.ID)
	if err != nil {
		t.Fatalf("PauseSubscription as owner returned error: %v", err)
	}
	if paused.ID != fixture.userSubscription.ID || paused.UserStatus != "paused" {
		t.Fatalf("paused DTO = %+v, want owned paused subscription", paused)
	}
	row, err := fixture.st.GetUserMediaSubscriptionForUser(ctx, queries.GetUserMediaSubscriptionForUserParams{
		ID:         fixture.userSubscription.ID,
		EchoUserID: "u1",
	})
	if err != nil {
		t.Fatalf("get paused user subscription: %v", err)
	}
	if row.Status != "paused" {
		t.Fatalf("stored status = %q, want paused", row.Status)
	}

	resumed, err := fixture.svc.ResumeSubscription(ctx, mediaActor("u1"), fixture.userSubscription.ID)
	if err != nil {
		t.Fatalf("ResumeSubscription as owner returned error: %v", err)
	}
	if resumed.ID != fixture.userSubscription.ID || resumed.UserStatus != "active" {
		t.Fatalf("resumed DTO = %+v, want active subscription", resumed)
	}
	events, err := fixture.st.ListDiscoverySubscriptionRequestEvents(ctx, queries.ListDiscoverySubscriptionRequestEventsParams{
		RequestID: fixture.request.ID,
		Limit:     10,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("list request events: %v", err)
	}
	if len(events) != 2 || events[0].Action != "resumed" || events[1].Action != "paused" {
		t.Fatalf("events = %+v, want resumed then paused", events)
	}
}

func TestPauseResumeUserSubscriptionRejectsInvalidTransitions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		startStatus string
		action      string
	}{
		{name: "pause paused", startStatus: "paused", action: "pause"},
		{name: "pause canceled", startStatus: "canceled", action: "pause"},
		{name: "resume active", startStatus: "active", action: "resume"},
		{name: "resume canceled", startStatus: "canceled", action: "resume"},
		{name: "resume completed", startStatus: "completed", action: "resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newSubscriptionFixture(t)
			_, err := fixture.st.UpdateUserMediaSubscriptionStatus(ctx, queries.UpdateUserMediaSubscriptionStatusParams{
				ID:         fixture.userSubscription.ID,
				EchoUserID: "u1",
				Status:     tc.startStatus,
				UpdatedAt:  mediaTestNow,
			})
			if err != nil {
				t.Fatalf("seed subscription status: %v", err)
			}

			switch tc.action {
			case "pause":
				_, err = fixture.svc.PauseSubscription(ctx, mediaActor("u1"), fixture.userSubscription.ID)
			case "resume":
				_, err = fixture.svc.ResumeSubscription(ctx, mediaActor("u1"), fixture.userSubscription.ID)
			default:
				t.Fatalf("unknown action %q", tc.action)
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("%s from %s error = %v, want %v", tc.action, tc.startStatus, err, ErrInvalidTransition)
			}
			row, err := fixture.st.GetUserMediaSubscriptionForUser(ctx, queries.GetUserMediaSubscriptionForUserParams{
				ID:         fixture.userSubscription.ID,
				EchoUserID: "u1",
			})
			if err != nil {
				t.Fatalf("get user subscription: %v", err)
			}
			if row.Status != tc.startStatus {
				t.Fatalf("status after invalid transition = %q, want %q", row.Status, tc.startStatus)
			}
		})
	}
}

func TestPauseSubscriptionMissingOwnerWritesAuditInTransaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	fixture := newSubscriptionFixture(t)

	_, err := fixture.svc.PauseSubscription(ctx, mediaActor("u1"), fixture.userSubscription.ID+999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("PauseSubscription missing error = %v, want %v", err, ErrNotFound)
	}
	if reason := latestAuditSafeReason(t, fixture.st, "u1"); reason != "request-disabled" {
		t.Fatalf("audit reason = %q, want request-disabled", reason)
	}
}

func TestListUserSubscriptionsProjectsLatestSafeState(t *testing.T) {
	ctx := context.Background()
	fixture := newSubscriptionFixture(t)
	seedSensitiveSubscriptionMatch(t, fixture.st, fixture.subscription.ID, fixture.deps.RuleProfileID, "failed latest", 150)

	got, err := fixture.svc.ListSubscriptions(ctx, mediaActor("u1"), 10, 0)
	if err != nil {
		t.Fatalf("ListSubscriptions returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].ID != fixture.userSubscription.ID {
		t.Fatalf("subscription id = %d, want %d", got[0].ID, fixture.userSubscription.ID)
	}
	if got[0].TMDBID != "777" || got[0].MediaType != "tv" || got[0].Title != "Projected Show" {
		t.Fatalf("projected identity = %+v, want safe subscription projection", got[0])
	}
	if got[0].TargetLabel != "Media" {
		t.Fatalf("target label = %q, want library display name", got[0].TargetLabel)
	}
	if got[0].UserStatus != "active" || got[0].PipelineStatus != "active" {
		t.Fatalf("statuses = (%q,%q), want active/active", got[0].UserStatus, got[0].PipelineStatus)
	}
	if got[0].LatestState != "failed" {
		t.Fatalf("latest state = %q, want coarse failed state", got[0].LatestState)
	}
}

func TestListUserSubscriptionsFiltersLatestMatchBySeason(t *testing.T) {
	ctx := context.Background()
	fixture := newSubscriptionFixture(t)
	u1Season, u1Key, err := ValidateSeasonFilterForMedia("tv", "[1]")
	if err != nil {
		t.Fatalf("normalize u1 season filter: %v", err)
	}
	u2Season, u2Key, err := ValidateSeasonFilterForMedia("tv", "[2]")
	if err != nil {
		t.Fatalf("normalize u2 season filter: %v", err)
	}
	if _, err := fixture.st.DB.ExecContext(ctx, `
UPDATE user_media_subscriptions
SET season_filter_json = ?, season_filter_key = ?
WHERE id = ?`, u1Season, u1Key, fixture.userSubscription.ID); err != nil {
		t.Fatalf("update u1 season filter: %v", err)
	}
	u2Sub, err := fixture.st.UpsertUserMediaSubscription(ctx, queries.UpsertUserMediaSubscriptionParams{
		EchoUserID:              "u2",
		RequestID:               sql.NullInt64{},
		DiscoverySubscriptionID: fixture.subscription.ID,
		TmdbID:                  fixture.subscription.TmdbID,
		MediaType:               fixture.subscription.MediaType,
		SeasonFilterJson:        sql.NullString{String: u2Season, Valid: true},
		SeasonFilterKey:         u2Key,
		Status:                  "active",
		CreatedAt:               mediaTestNow,
		UpdatedAt:               mediaTestNow + 1,
	})
	if err != nil {
		t.Fatalf("create u2 user subscription: %v", err)
	}
	seedSensitiveSubscriptionMatchForSeason(t, fixture.st, fixture.subscription.ID, fixture.deps.RuleProfileID, 2, "season-2-latest", 200)

	u1Rows, err := fixture.svc.ListSubscriptions(ctx, mediaActor("u1"), 10, 0)
	if err != nil {
		t.Fatalf("ListSubscriptions u1 returned error: %v", err)
	}
	if len(u1Rows) != 1 {
		t.Fatalf("len(u1Rows) = %d, want 1", len(u1Rows))
	}
	if u1Rows[0].LatestState != "pending" {
		t.Fatalf("u1 latest state = %q, want pending when only season 2 matched", u1Rows[0].LatestState)
	}

	u2Rows, err := fixture.svc.ListSubscriptions(ctx, mediaActor("u2"), 10, 0)
	if err != nil {
		t.Fatalf("ListSubscriptions u2 returned error: %v", err)
	}
	if len(u2Rows) != 1 || u2Rows[0].ID != u2Sub.ID {
		t.Fatalf("u2 rows = %+v, want subscription %d", u2Rows, u2Sub.ID)
	}
	if u2Rows[0].LatestState != "failed" {
		t.Fatalf("u2 latest state = %q, want failed for season 2 match", u2Rows[0].LatestState)
	}
}

func TestListUserSubscriptionsDoesNotExposeRawDiscoveryFields(t *testing.T) {
	ctx := context.Background()
	fixture := newSubscriptionFixture(t)
	seedSensitiveSubscriptionMatch(t, fixture.st, fixture.subscription.ID, fixture.deps.RuleProfileID, "redaction latest", 160)

	got, err := fixture.svc.ListSubscriptions(ctx, mediaActor("u1"), 10, 0)
	if err != nil {
		t.Fatalf("ListSubscriptions returned error: %v", err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal subscription DTOs: %v", err)
	}
	jsonText := string(data)
	for _, forbidden := range []string{
		"share_code",
		"receive_code",
		"raw_text_ref",
		"target_account",
		"storage_mount",
		"default_args_json",
		"queued_job_id",
		"secret-share",
		"secret-receive",
		"raw-ref-secret",
		"failure detail secret",
		"raw user note secret",
		"raw admin note secret",
	} {
		if strings.Contains(jsonText, forbidden) {
			t.Fatalf("subscription DTO JSON leaked %q: %s", forbidden, jsonText)
		}
	}
}

type mediaSubscriptionFixture struct {
	st               *store.Store
	svc              Service
	deps             mediaTargetDeps
	policy           queries.DiscoveryAccessPolicy
	target           queries.DiscoveryPolicyTarget
	request          queries.DiscoverySubscriptionRequest
	subscription     queries.DiscoverySubscription
	userSubscription queries.UserMediaSubscription
}

func newSubscriptionFixture(t *testing.T) mediaSubscriptionFixture {
	t.Helper()
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	seedMediaUser(t, st, "u2")
	policy := createMediaPolicy(t, st, "subscription policy", "u1", 1, 100, "approval_required", 1)
	deps := seedMediaTargetDeps(t, st)
	target := createMediaTarget(t, st, deps, policy.ID, "Default Target", "tv", 1)
	request := seedApprovedSubscriptionRequest(t, st, "u1", policy, target, deps, "777", "tv", "Projected Show")
	seeded := seedMediaUserSubscription(t, st, "u1", request.ID, deps, "Projected Show", "777", "tv", "active")

	return mediaSubscriptionFixture{
		st:               st,
		svc:              Service{Store: st, Now: mediaNow},
		deps:             deps,
		policy:           policy,
		target:           target,
		request:          request,
		subscription:     seeded.DiscoverySubscription,
		userSubscription: seeded.UserSubscription,
	}
}

func seedApprovedSubscriptionRequest(
	t *testing.T,
	st *store.Store,
	userID string,
	policy queries.DiscoveryAccessPolicy,
	target queries.DiscoveryPolicyTarget,
	deps mediaTargetDeps,
	tmdbID,
	mediaType,
	title string,
) queries.DiscoverySubscriptionRequest {
	t.Helper()
	request, err := st.CreateDiscoverySubscriptionRequest(context.Background(), queries.CreateDiscoverySubscriptionRequestParams{
		RequesterUserID:             userID,
		Status:                      "approved",
		TmdbID:                      tmdbID,
		MediaType:                   mediaType,
		TmdbLanguage:                "zh-CN",
		TitleSnapshot:               title,
		OriginalTitleSnapshot:       sql.NullString{String: "Original " + title, Valid: true},
		ReleaseYearSnapshot:         sql.NullInt64{Int64: 2024, Valid: true},
		PosterPathSnapshot:          sql.NullString{String: "/poster.jpg", Valid: true},
		SeasonFilterJson:            sql.NullString{},
		PolicyIDSnapshot:            sql.NullInt64{Int64: policy.ID, Valid: true},
		PolicyTargetIDSnapshot:      sql.NullInt64{Int64: target.ID, Valid: true},
		TargetLabelSnapshot:         target.Label,
		TargetLibraryID:             deps.LibraryID,
		TargetLibraryNameSnapshot:   "Media",
		ProducerProfileIDSnapshot:   deps.ProducerProfileID,
		ProducerProfileNameSnapshot: "115 default",
		RuleProfileIDSnapshot:       deps.RuleProfileID,
		RuleProfileVersionSnapshot:  1,
		UserNote:                    sql.NullString{String: "raw user note secret", Valid: true},
		AdminNote:                   sql.NullString{String: "raw admin note secret", Valid: true},
		ReviewedBy:                  sql.NullString{String: "admin", Valid: true},
		ReviewedAt:                  sql.NullInt64{Int64: mediaTestNow, Valid: true},
		SubscriptionID:              sql.NullInt64{},
		IdempotencyKey:              "seed-request-" + userID + "-" + tmdbID,
		LastErrorKind:               sql.NullString{},
		LastErrorMessage:            sql.NullString{},
		CreatedAt:                   mediaTestNow,
		UpdatedAt:                   mediaTestNow,
	})
	if err != nil {
		t.Fatalf("seed approved subscription request: %v", err)
	}
	return request
}

func seedSensitiveSubscriptionMatch(
	t *testing.T,
	st *store.Store,
	subscriptionID,
	ruleProfileID int64,
	externalKey string,
	updatedAt int64,
) queries.SubscriptionMatch {
	return seedSensitiveSubscriptionMatchWithSeason(t, st, subscriptionID, ruleProfileID, sql.NullInt64{}, externalKey, updatedAt)
}

func seedSensitiveSubscriptionMatchForSeason(
	t *testing.T,
	st *store.Store,
	subscriptionID,
	ruleProfileID,
	seasonNumber int64,
	externalKey string,
	updatedAt int64,
) queries.SubscriptionMatch {
	return seedSensitiveSubscriptionMatchWithSeason(t, st, subscriptionID, ruleProfileID, sql.NullInt64{Int64: seasonNumber, Valid: true}, externalKey, updatedAt)
}

func seedSensitiveSubscriptionMatchWithSeason(
	t *testing.T,
	st *store.Store,
	subscriptionID,
	ruleProfileID int64,
	seasonNumber sql.NullInt64,
	externalKey string,
	updatedAt int64,
) queries.SubscriptionMatch {
	t.Helper()
	ctx := context.Background()
	source, err := st.CreateDiscoverySource(ctx, queries.CreateDiscoverySourceParams{
		Kind:          "manual",
		Name:          "sensitive source " + externalKey,
		Enabled:       1,
		ConfigJson:    `{"safe":true}`,
		SecretRef:     sql.NullString{String: "secret/source", Valid: true},
		RateLimitJson: sql.NullString{},
		NextRunAt:     sql.NullInt64{},
		CreatedAt:     updatedAt,
		UpdatedAt:     updatedAt,
	})
	if err != nil {
		t.Fatalf("create discovery source: %v", err)
	}
	resource, err := st.UpsertDiscoveredResource(ctx, queries.UpsertDiscoveredResourceParams{
		SourceID:         source.ID,
		Provider:         "115",
		LinkKind:         "115_share",
		ExternalKey:      externalKey,
		TmdbID:           sql.NullString{String: "777", Valid: true},
		MediaType:        sql.NullString{String: "tv", Valid: true},
		Title:            sql.NullString{String: "Projected Show", Valid: true},
		SeasonNumber:     seasonNumber,
		EpisodeStart:     sql.NullInt64{},
		EpisodeEnd:       sql.NullInt64{},
		ShareCode:        sql.NullString{String: "secret-share", Valid: true},
		ReceiveCode:      sql.NullString{String: "secret-receive", Valid: true},
		ShareUrlRedacted: sql.NullString{String: "https://115.example/s/redacted", Valid: true},
		RawTextRedacted:  sql.NullString{String: "redacted raw text", Valid: true},
		RawTextRef:       sql.NullString{String: "raw-ref-secret", Valid: true},
		ParsedJson:       `{"parsed":true}`,
		FeatureJson:      `{"feature":true}`,
		Status:           "failed",
		FirstSeenAt:      updatedAt,
		LastSeenAt:       updatedAt,
	})
	if err != nil {
		t.Fatalf("upsert discovered resource: %v", err)
	}
	match, err := st.CreateSubscriptionMatch(ctx, queries.CreateSubscriptionMatchParams{
		SubscriptionID:     subscriptionID,
		ResourceID:         resource.ID,
		RuleProfileID:      ruleProfileID,
		RuleProfileVersion: 1,
		SeasonNumber:       seasonNumber,
		EpisodeStart:       sql.NullInt64{},
		EpisodeEnd:         sql.NullInt64{},
		ScoreJson:          `{"score":1}`,
		PreviousScoreJson:  sql.NullString{},
		Decision:           "failed",
		Reason:             "safe failure",
		DispatchState:      "failed",
		IdempotencyKey:     "match-" + externalKey,
		CreatedAt:          updatedAt,
		UpdatedAt:          updatedAt,
		DecidedAt:          sql.NullInt64{Int64: updatedAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("create subscription match: %v", err)
	}
	if err := st.UpdateSubscriptionMatchResult(ctx, queries.UpdateSubscriptionMatchResultParams{
		Decision:             "failed",
		DispatchState:        "failed",
		ResultLibraryEntryID: sql.NullInt64{},
		ResultBlobID:         sql.NullInt64{},
		ResultCopyID:         sql.NullInt64{},
		FailureKind:          sql.NullString{String: "download_failed", Valid: true},
		FailureMessage:       sql.NullString{String: "failure detail secret", Valid: true},
		UpdatedAt:            updatedAt,
		FinishedAt:           sql.NullInt64{Int64: updatedAt, Valid: true},
		ID:                   match.ID,
	}); err != nil {
		t.Fatalf("update subscription match result: %v", err)
	}
	return match
}
