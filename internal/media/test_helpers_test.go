package media

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

const mediaTestNow = int64(100)

type mediaTargetDeps struct {
	LibraryID         int64
	ProducerProfileID int64
	RuleProfileID     int64
}

type seededMediaUserSubscription struct {
	UserSubscription      queries.UserMediaSubscription
	DiscoverySubscription queries.DiscoverySubscription
}

func openMediaTestStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "echo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}

func seedMediaUser(t *testing.T, st *store.Store, id string) {
	t.Helper()

	if err := st.CreateUser(context.Background(), queries.CreateUserParams{
		ID:            id,
		Username:      id,
		Role:          "user",
		Status:        "active",
		QuotaPolicyID: 1,
		CreatedAt:     mediaTestNow,
		UpdatedAt:     mediaTestNow,
	}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
}

func createMediaPolicy(t *testing.T, st *store.Store, name, userID string, enabled, priority int64, requestMode string, canSearch int64) queries.DiscoveryAccessPolicy {
	t.Helper()
	return createMediaPolicyWithLimits(t, st, name, userID, enabled, priority, requestMode, canSearch, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
}

func createMediaPolicyWithLimits(
	t *testing.T,
	st *store.Store,
	name,
	userID string,
	enabled,
	priority int64,
	requestMode string,
	canSearch int64,
	maxPendingRequests,
	maxActiveSubscriptions,
	requestCooldownSeconds sql.NullInt64,
) queries.DiscoveryAccessPolicy {
	t.Helper()

	policy, err := st.CreateDiscoveryAccessPolicy(context.Background(), queries.CreateDiscoveryAccessPolicyParams{
		Name:                   name,
		Enabled:                enabled,
		Priority:               priority,
		SubjectUserID:          nullString(userID),
		RequestMode:            requestMode,
		CanSearch:              canSearch,
		MaxPendingRequests:     maxPendingRequests,
		MaxActiveSubscriptions: maxActiveSubscriptions,
		RequestCooldownSeconds: requestCooldownSeconds,
		CreatedBy:              sql.NullString{String: "admin", Valid: true},
		CreatedAt:              mediaTestNow,
		UpdatedAt:              mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create policy %s: %v", name, err)
	}
	return policy
}

func insertUncheckedMediaPolicy(t *testing.T, st *store.Store, name, requestMode string) {
	t.Helper()
	ctx := context.Background()

	conn, err := st.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("open db conn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("ignore check constraints: %v", err)
	}
	defer conn.ExecContext(ctx, `PRAGMA ignore_check_constraints = OFF`)

	if _, err := conn.ExecContext(ctx, `
INSERT INTO discovery_access_policies (
  name, enabled, priority, subject_user_id, request_mode, can_search,
  created_by, created_at, updated_at
) VALUES (?, 1, 100, NULL, ?, 1, 'admin', ?, ?)`, name, requestMode, mediaTestNow, mediaTestNow); err != nil {
		t.Fatalf("insert unchecked policy %s: %v", name, err)
	}
}

func seedMediaTargetDeps(t *testing.T, st *store.Store) mediaTargetDeps {
	t.Helper()
	ctx := context.Background()

	if err := st.CreateAccount(ctx, queries.CreateAccountParams{
		ID:           "acc-115",
		Provider:     "115",
		SidecarID:    "sidecar-1",
		StorageMount: "/115",
		Status:       "active",
		OwnerID:      "admin",
		CreatedAt:    mediaTestNow,
		UpdatedAt:    mediaTestNow,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}

	library, err := st.CreateLibrary(ctx, queries.CreateLibraryParams{
		Name:           "Media",
		EchoOutputKind: "local",
		EchoOutputPath: "/tmp/echo-media-test",
		OwnerID:        "admin",
		CreatedAt:      mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}

	profile, err := st.CreateDiscoveryProducerProfile(ctx, queries.CreateDiscoveryProducerProfileParams{
		Name:                   "115 default",
		Provider:               "115",
		Tool:                   "115share2cas",
		TargetAccount:          "acc-115",
		TargetSubdirTemplate:   "{{.Title}}",
		LibraryRelPathTemplate: "{{.Title}}",
		DefaultArgsJson:        "{}",
		Enabled:                1,
		CreatedAt:              mediaTestNow,
		UpdatedAt:              mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create producer profile: %v", err)
	}

	ruleProfile, err := st.CreateRuleProfile(ctx, queries.CreateRuleProfileParams{
		Name:      "default rules",
		Version:   1,
		RulesJson: `{"weights":["resolutions"]}`,
		Enabled:   1,
		CreatedAt: mediaTestNow,
		UpdatedAt: mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create rule profile: %v", err)
	}

	return mediaTargetDeps{
		LibraryID:         library.ID,
		ProducerProfileID: profile.ID,
		RuleProfileID:     ruleProfile.ID,
	}
}

func createMediaTarget(t *testing.T, st *store.Store, deps mediaTargetDeps, policyID int64, label, mediaType string, enabled int64) queries.DiscoveryPolicyTarget {
	t.Helper()
	return createMediaTargetWithMatchMode(t, st, deps, policyID, label, mediaType, enabled, "admin_review")
}

func createMediaTargetWithMatchMode(t *testing.T, st *store.Store, deps mediaTargetDeps, policyID int64, label, mediaType string, enabled int64, matchMode string) queries.DiscoveryPolicyTarget {
	t.Helper()

	target, err := st.CreateDiscoveryPolicyTarget(context.Background(), queries.CreateDiscoveryPolicyTargetParams{
		PolicyID:                policyID,
		Label:                   label,
		LibraryID:               deps.LibraryID,
		ProducerProfileID:       deps.ProducerProfileID,
		RuleProfileID:           deps.RuleProfileID,
		PipelineOwnerID:         "admin",
		MediaType:               nullString(mediaType),
		MatchMode:               matchMode,
		GrantPlaybackOnApproval: 1,
		Enabled:                 enabled,
		DefaultTarget:           1,
		CreatedAt:               mediaTestNow,
		UpdatedAt:               mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create target %s: %v", label, err)
	}
	return target
}

func seedMediaUserSubscription(
	t *testing.T,
	st *store.Store,
	userID string,
	requestID int64,
	deps mediaTargetDeps,
	title,
	tmdbID,
	mediaType,
	status string,
) seededMediaUserSubscription {
	t.Helper()
	ctx := context.Background()

	subscription, err := st.CreateDiscoverySubscription(ctx, queries.CreateDiscoverySubscriptionParams{
		OwnerID:           "admin",
		TmdbID:            tmdbID,
		MediaType:         mediaType,
		TmdbLanguage:      "zh-CN",
		TitleSnapshot:     title,
		LibraryID:         deps.LibraryID,
		ProducerProfileID: deps.ProducerProfileID,
		RuleProfileID:     deps.RuleProfileID,
		Status:            "active",
		SeasonFilterJson:  sql.NullString{},
		NextCheckAt:       sql.NullInt64{},
		CreatedAt:         mediaTestNow,
		UpdatedAt:         mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create discovery subscription: %v", err)
	}

	requestRef := sql.NullInt64{}
	if requestID != 0 {
		requestRef = sql.NullInt64{Int64: requestID, Valid: true}
	}
	userSubscription, err := st.UpsertUserMediaSubscription(ctx, queries.UpsertUserMediaSubscriptionParams{
		EchoUserID:              userID,
		RequestID:               requestRef,
		DiscoverySubscriptionID: subscription.ID,
		TmdbID:                  tmdbID,
		MediaType:               mediaType,
		SeasonFilterJson:        sql.NullString{},
		SeasonFilterKey:         "all",
		Status:                  status,
		CreatedAt:               mediaTestNow,
		UpdatedAt:               mediaTestNow,
	})
	if err != nil {
		t.Fatalf("upsert user media subscription: %v", err)
	}
	return seededMediaUserSubscription{
		UserSubscription:      userSubscription,
		DiscoverySubscription: subscription,
	}
}

func mediaActor(userID string) Actor {
	return Actor{
		User: auth.UserContext{
			UserID: userID,
			Role:   "user",
			Scopes: []string{"discovery"},
			Now:    time.Unix(mediaTestNow, 0),
		},
		IP: "127.0.0.1",
	}
}

func mediaNow() time.Time {
	return time.Unix(mediaTestNow, 0)
}

func nullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
