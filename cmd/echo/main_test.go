package main

import (
	"context"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
)

type fakeMainTMDBClient struct{}

func (fakeMainTMDBClient) Search(context.Context, string, string) ([]tmdb.Media, error) {
	return nil, nil
}

func (fakeMainTMDBClient) MovieDetails(context.Context, string) (tmdb.Media, error) {
	return tmdb.Media{}, nil
}

func (fakeMainTMDBClient) TVDetails(context.Context, string) (tmdb.Media, error) {
	return tmdb.Media{}, nil
}

func TestBuildAPIDepsInjectsMediaServiceWhenTMDBClientConfigured(t *testing.T) {
	now := func() time.Time { return time.Unix(123, 0) }
	tmdbClient := fakeMainTMDBClient{}

	deps := buildAPIDeps(nil, nil, nil, &config.Config{}, nil, tmdbClient, now)

	if deps.DiscoveryTMDB != tmdbClient {
		t.Fatalf("DiscoveryTMDB not wired to configured tmdb client")
	}
	if deps.Media == nil {
		t.Fatalf("Media service is nil, want configured service")
	}
	if deps.Media.TMDB != tmdbClient {
		t.Fatalf("Media.TMDB not wired to configured tmdb client")
	}
	if deps.Media.Now == nil || deps.Media.Now().Unix() != 123 {
		t.Fatalf("Media.Now not wired to supplied clock")
	}
	if deps.Media.Limiter == nil {
		t.Fatalf("Media.Limiter is nil, want default user media rate limiter")
	}
}

func TestBuildAPIDepsLeavesMediaNilWithoutTMDBClient(t *testing.T) {
	deps := buildAPIDeps(nil, nil, nil, &config.Config{}, nil, nil, time.Now)

	if deps.DiscoveryTMDB != nil {
		t.Fatalf("DiscoveryTMDB=%v, want nil without tmdb client", deps.DiscoveryTMDB)
	}
	if deps.Media != nil {
		t.Fatalf("Media=%v, want nil safe degradation without tmdb client", deps.Media)
	}
}

func TestBuildWebDepsReusesAPIMediaServiceForAppFragments(t *testing.T) {
	now := func() time.Time { return time.Unix(123, 0) }
	apiDeps := buildAPIDeps(nil, nil, nil, &config.Config{}, nil, fakeMainTMDBClient{}, now)

	webDeps := buildWebDeps(nil, nil, apiDeps.Media)

	if webDeps.Media == nil {
		t.Fatalf("Web.Media is nil, want app fragments wired to media service")
	}
	if webDeps.Media != apiDeps.Media {
		t.Fatalf("Web.Media does not reuse API media service")
	}
}
