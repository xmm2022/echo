package media

import (
	"context"
	"errors"
	"testing"

	"github.com/xmm2022/echo/internal/discovery/tmdb"
)

func TestResolvePolicyFailsClosedWithoutEnabledPolicy(t *testing.T) {
	st := openMediaTestStore(t)
	svc := Service{Store: st}

	_, err := svc.ResolvePolicy(context.Background(), "u1")
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("ResolvePolicy error = %v, want %v", err, ErrPolicyDenied)
	}
}

func TestResolvePolicyFailsClosedWithoutStore(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		svc  *Service
	}{
		{name: "nil service", svc: nil},
		{name: "nil store", svc: &Service{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.svc.ResolvePolicy(ctx, "u1")
			if !errors.Is(err, ErrPolicyDenied) {
				t.Fatalf("ResolvePolicy error = %v, want %v", err, ErrPolicyDenied)
			}
		})
	}
}

func TestResolvePolicyHigherPriorityWins(t *testing.T) {
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	createMediaPolicy(t, st, "default allow", "", 1, 10, "auto_approve", 1)
	createMediaPolicy(t, st, "default disabled", "", 0, 20, "auto_approve", 1)
	svc := Service{Store: st}

	_, err := svc.ResolvePolicy(context.Background(), "u1")
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("ResolvePolicy error = %v, want %v", err, ErrPolicyDenied)
	}
}

func TestResolvePolicyPrefersUserPolicyOverDefaultAtSamePriority(t *testing.T) {
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	createMediaPolicy(t, st, "default policy", "", 1, 100, "approval_required", 1)
	userPolicy := createMediaPolicy(t, st, "u1 policy", "u1", 1, 100, "auto_approve", 1)
	svc := Service{Store: st}

	got, err := svc.ResolvePolicy(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ResolvePolicy returned error: %v", err)
	}
	if got.ID != userPolicy.ID {
		t.Fatalf("ResolvePolicy ID = %d, want user policy %d", got.ID, userPolicy.ID)
	}
}

func TestResolvePolicyRejectsDisabledRequestMode(t *testing.T) {
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	createMediaPolicy(t, st, "disabled mode", "", 1, 100, "disabled", 1)
	svc := Service{Store: st}

	_, err := svc.ResolvePolicy(context.Background(), "u1")
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("ResolvePolicy error = %v, want %v", err, ErrPolicyDenied)
	}
}

func TestResolvePolicyRejectsUnknownRequestMode(t *testing.T) {
	st := openMediaTestStore(t)
	insertUncheckedMediaPolicy(t, st, "future mode", "future_mode")
	svc := Service{Store: st}

	_, err := svc.ResolvePolicy(context.Background(), "u1")
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("ResolvePolicy error = %v, want %v", err, ErrPolicyDenied)
	}
}

func TestResolvePolicyAllowsCanSearchFalseForRequesting(t *testing.T) {
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	policy := createMediaPolicy(t, st, "request without search", "", 1, 100, "approval_required", 0)
	svc := Service{Store: st}

	got, err := svc.ResolvePolicy(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ResolvePolicy returned error: %v", err)
	}
	if got.ID != policy.ID || got.CanSearch != 0 {
		t.Fatalf("ResolvePolicy = %+v, want policy %d with can_search=0", got, policy.ID)
	}
}

func TestSearchAllowedRejectsSearchWhenCanSearchFalse(t *testing.T) {
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	createMediaPolicy(t, st, "no search", "", 1, 100, "auto_approve", 0)
	fake := newFakeMetadataClient()
	fake.searches["movie:Matrix"] = []tmdb.Media{{TMDBID: "603", MediaType: "movie", Title: "The Matrix"}}
	svc := Service{Store: st, TMDB: fake, Now: mediaNow}

	err := svc.SearchAllowed(context.Background(), "u1")
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("SearchAllowed error = %v, want %v", err, ErrPolicyDenied)
	}
	_, err = svc.Search(context.Background(), mediaActor("u1"), SearchInput{Query: "Matrix", MediaType: "movie"})
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("Search error = %v, want %v", err, ErrPolicyDenied)
	}
	if fake.searchCalls != 0 {
		t.Fatalf("tmdb search calls = %d, want 0 when can_search=0", fake.searchCalls)
	}
}

func TestSearchAllowedFailsClosedWithoutStore(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		svc  *Service
	}{
		{name: "nil service", svc: nil},
		{name: "nil store", svc: &Service{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.svc.SearchAllowed(ctx, "u1")
			if !errors.Is(err, ErrPolicyDenied) {
				t.Fatalf("SearchAllowed error = %v, want %v", err, ErrPolicyDenied)
			}
		})
	}
}

func TestResolveTargetRejectsDisabledOrWrongMediaType(t *testing.T) {
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	policy := createMediaPolicy(t, st, "default allow", "", 1, 100, "auto_approve", 1)
	otherPolicy := createMediaPolicy(t, st, "other allow", "u1", 1, 90, "auto_approve", 1)
	deps := seedMediaTargetDeps(t, st)
	disabledTarget := createMediaTarget(t, st, deps, policy.ID, "disabled tv", "tv", 0)
	movieTarget := createMediaTarget(t, st, deps, policy.ID, "movie only", "movie", 1)
	otherTarget := createMediaTarget(t, st, deps, otherPolicy.ID, "other policy", "tv", 1)
	svc := Service{Store: st}

	for _, tc := range []struct {
		name     string
		targetID int64
	}{
		{name: "disabled", targetID: disabledTarget.ID},
		{name: "wrong media type", targetID: movieTarget.ID},
		{name: "policy mismatch", targetID: otherTarget.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ResolveTarget(context.Background(), policy, tc.targetID, "tv")
			if !errors.Is(err, ErrPolicyDenied) {
				t.Fatalf("ResolveTarget error = %v, want %v", err, ErrPolicyDenied)
			}
		})
	}
}

func TestResolveTargetRejectsInvalidRequestedMediaType(t *testing.T) {
	st := openMediaTestStore(t)
	seedMediaUser(t, st, "u1")
	policy := createMediaPolicy(t, st, "default allow", "", 1, 100, "auto_approve", 1)
	deps := seedMediaTargetDeps(t, st)
	target := createMediaTarget(t, st, deps, policy.ID, "all media", "", 1)
	svc := Service{Store: st}

	_, err := svc.ResolveTarget(context.Background(), policy, target.ID, "music")
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("ResolveTarget error = %v, want %v", err, ErrPolicyDenied)
	}
}

func TestResolveTargetFailsClosedForMissingTarget(t *testing.T) {
	st := openMediaTestStore(t)
	policy := createMediaPolicy(t, st, "default allow", "", 1, 100, "auto_approve", 1)
	svc := Service{Store: st}

	_, err := svc.ResolveTarget(context.Background(), policy, 999, "tv")
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("ResolveTarget error = %v, want %v", err, ErrPolicyDenied)
	}
}
