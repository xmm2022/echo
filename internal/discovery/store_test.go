package discovery

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	storeq "github.com/xmm2022/echo/internal/store/queries"
)

func TestAcceptMatchIsIdempotent(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	ctx := context.Background()
	fixture := seedDiscoveryFixture(t, st)
	now := time.Unix(100, 0)
	first, err := ds.AcceptMatch(ctx, AcceptMatchParams{
		SubscriptionID:     fixture.SubscriptionID,
		ResourceID:         fixture.ResourceID,
		RuleProfileID:      fixture.RuleProfileID,
		RuleProfileVersion: 1,
		SeasonNumber:       0,
		EpisodeStart:       0,
		EpisodeEnd:         0,
		ScoreJSON:          `{"tuple":[1,2,3]}`,
		PreviousScoreJSON:  "",
		Decision:           "review",
		Reason:             "test match",
		DispatchState:      "none",
		IdempotencyKey:     "sub-1-resource-1",
		Now:                now,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ds.AcceptMatch(ctx, AcceptMatchParams{
		SubscriptionID:     fixture.SubscriptionID,
		ResourceID:         fixture.ResourceID,
		RuleProfileID:      fixture.RuleProfileID,
		RuleProfileVersion: 1,
		SeasonNumber:       0,
		EpisodeStart:       0,
		EpisodeEnd:         0,
		ScoreJSON:          `{"tuple":[1,2,3]}`,
		PreviousScoreJSON:  "",
		Decision:           "review",
		Reason:             "test match",
		DispatchState:      "none",
		IdempotencyKey:     "sub-1-resource-1",
		Now:                now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same match id, got %d and %d", first.ID, second.ID)
	}
}

func TestAcceptMatchRejectsLifecycleStatesFacadeCannotCreate(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	ctx := context.Background()
	fixture := seedDiscoveryFixture(t, st)

	tests := []struct {
		name          string
		decision      string
		dispatchState string
	}{
		{
			name:          "queued dispatch state",
			decision:      "review",
			dispatchState: "queued",
		},
		{
			name:          "terminal imported decision",
			decision:      "imported",
			dispatchState: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ds.AcceptMatch(ctx, AcceptMatchParams{
				SubscriptionID:     fixture.SubscriptionID,
				ResourceID:         fixture.ResourceID,
				RuleProfileID:      fixture.RuleProfileID,
				RuleProfileVersion: 1,
				ScoreJSON:          `{"tuple":[1,2,3]}`,
				Decision:           tt.decision,
				Reason:             "test match",
				DispatchState:      tt.dispatchState,
				IdempotencyKey:     "reject-lifecycle-" + tt.name,
				Now:                time.Unix(100, 0),
			})
			if err == nil {
				t.Fatal("expected lifecycle validation error")
			}
		})
	}
}

func TestUpdateTelegramCursorMissingChannelReturnsError(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	sourceID := seedDiscoverySource(t, st, string(SourceTelegramMTProto))
	err := ds.UpdateTelegramCursor(context.Background(), sourceID, TelegramCursorUpdate{
		ChannelRef:      "missing",
		LastMessageID:   10,
		LastMessageDate: 100,
	}, time.Unix(100, 0))
	if err == nil {
		t.Fatal("expected missing channel cursor error")
	}
}

func TestAdminAcceptMatchUpdatesMatchAndResource(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	fixture := seedDiscoveryFixture(t, st)
	matchID := seedAdminTransitionMatch(t, st, fixture, "review", "none")

	if err := ds.AdminAcceptMatch(context.Background(), matchID, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	assertAdminTransitionMatch(t, st, matchID, "accept", "none")
	assertAdminTransitionResourceStatus(t, st, fixture.ResourceID, "accepted")
}

func TestAdminMatchTransitionErrorsAreClassifiedAndDoNotMutate(t *testing.T) {
	st := openDiscoveryTestStore(t)
	ds := NewStore(st)
	fixture := seedDiscoveryFixture(t, st)
	runningID := seedAdminTransitionMatch(t, st, fixture, "accept", "running")

	if err := ds.AdminAcceptMatch(context.Background(), 99999, time.Unix(200, 0)); !errors.Is(err, ErrAdminMatchNotFound) {
		t.Fatalf("missing accept err=%v, want ErrAdminMatchNotFound", err)
	}
	if err := ds.AdminAcceptMatch(context.Background(), runningID, time.Unix(200, 0)); !errors.Is(err, ErrInvalidAdminMatchTransition) {
		t.Fatalf("running accept err=%v, want ErrInvalidAdminMatchTransition", err)
	}
	assertAdminTransitionMatch(t, st, runningID, "accept", "running")
}

func TestAdminAcceptConditionalQueryRefusesCurrentInFlightState(t *testing.T) {
	st := openDiscoveryTestStore(t)
	fixture := seedDiscoveryFixture(t, st)
	matchID := seedAdminTransitionMatch(t, st, fixture, "review", "none")
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE subscription_matches SET decision = 'accept', dispatch_state = 'running'
WHERE id = ?`, matchID); err != nil {
		t.Fatal(err)
	}

	_, err := st.AdminAcceptSubscriptionMatch(context.Background(), storeq.AdminAcceptSubscriptionMatchParams{
		UpdatedAt: 200,
		ID:        matchID,
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("conditional accept err=%v, want sql.ErrNoRows", err)
	}
	assertAdminTransitionMatch(t, st, matchID, "accept", "running")
}

func seedAdminTransitionMatch(t *testing.T, st interface {
	CreateSubscriptionMatch(context.Context, storeq.CreateSubscriptionMatchParams) (storeq.SubscriptionMatch, error)
}, fixture discoveryFixture, decision, dispatchState string) int64 {
	t.Helper()
	match, err := st.CreateSubscriptionMatch(context.Background(), storeq.CreateSubscriptionMatchParams{
		SubscriptionID:     fixture.SubscriptionID,
		ResourceID:         fixture.ResourceID,
		RuleProfileID:      fixture.RuleProfileID,
		RuleProfileVersion: 1,
		ScoreJson:          `{"tuple":[1]}`,
		Decision:           decision,
		Reason:             "admin transition test",
		DispatchState:      dispatchState,
		IdempotencyKey:     "admin-transition-" + decision + "-" + dispatchState + "-" + time.Now().String(),
		CreatedAt:          100,
		UpdatedAt:          100,
	})
	if err != nil {
		t.Fatal(err)
	}
	return match.ID
}

func assertAdminTransitionMatch(t *testing.T, st interface {
	GetSubscriptionMatch(context.Context, storeq.GetSubscriptionMatchParams) (storeq.SubscriptionMatch, error)
}, matchID int64, decision, dispatchState string) {
	t.Helper()
	match, err := st.GetSubscriptionMatch(context.Background(), storeq.GetSubscriptionMatchParams{ID: matchID})
	if err != nil {
		t.Fatal(err)
	}
	if match.Decision != decision || match.DispatchState != dispatchState {
		t.Fatalf("match state=(%s,%s), want (%s,%s)", match.Decision, match.DispatchState, decision, dispatchState)
	}
}

func assertAdminTransitionResourceStatus(t *testing.T, st interface {
	GetDiscoveredResource(context.Context, storeq.GetDiscoveredResourceParams) (storeq.DiscoveredResource, error)
}, resourceID int64, status string) {
	t.Helper()
	resource, err := st.GetDiscoveredResource(context.Background(), storeq.GetDiscoveredResourceParams{ID: resourceID})
	if err != nil {
		t.Fatal(err)
	}
	if resource.Status != status {
		t.Fatalf("resource status=%q, want %q", resource.Status, status)
	}
}
