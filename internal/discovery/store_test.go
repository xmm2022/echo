package discovery

import (
	"context"
	"testing"
	"time"
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
