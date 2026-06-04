package media

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

const mediaTestNow = int64(100)

type mediaTargetDeps struct {
	LibraryID         int64
	ProducerProfileID int64
	RuleProfileID     int64
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

	policy, err := st.CreateDiscoveryAccessPolicy(context.Background(), queries.CreateDiscoveryAccessPolicyParams{
		Name:          name,
		Enabled:       enabled,
		Priority:      priority,
		SubjectUserID: nullString(userID),
		RequestMode:   requestMode,
		CanSearch:     canSearch,
		CreatedBy:     sql.NullString{String: "admin", Valid: true},
		CreatedAt:     mediaTestNow,
		UpdatedAt:     mediaTestNow,
	})
	if err != nil {
		t.Fatalf("create policy %s: %v", name, err)
	}
	return policy
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

	target, err := st.CreateDiscoveryPolicyTarget(context.Background(), queries.CreateDiscoveryPolicyTargetParams{
		PolicyID:                policyID,
		Label:                   label,
		LibraryID:               deps.LibraryID,
		ProducerProfileID:       deps.ProducerProfileID,
		RuleProfileID:           deps.RuleProfileID,
		PipelineOwnerID:         "admin",
		MediaType:               nullString(mediaType),
		MatchMode:               "admin_review",
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

func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
