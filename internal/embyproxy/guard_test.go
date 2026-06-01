package embyproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlaybackGuardDeniesSuspiciousEndpointsBeforeMapping(t *testing.T) {
	guard := NewPlaybackGuard(GuardConfig{Phase: 3})
	paths := []string{
		"/emby/Videos/123/stream",
		"/emby/Videos/123/original",
		"/emby/Items/123/Download",
		"/emby/videos/123/hls/master.m3u8",
		"/emby/Audio/123/stream.mp3",
		"/emby/Videos/123/stream?Static=true&mediaSourceId=ms1&PlaySessionId=ps1",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		allowed := guard.Allow(req, rec)
		if allowed {
			t.Fatalf("%s allowed, want denied before Phase 4 mapping guard", path)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d, want 503 controlled denial", path, rec.Code)
		}
	}
}

func TestPlaybackGuardAllowsOrdinaryEmbyAPI(t *testing.T) {
	guard := NewPlaybackGuard(GuardConfig{Phase: 3})
	req := httptest.NewRequest(http.MethodGet, "/emby/Users/emby-u1/Items", nil)
	rec := httptest.NewRecorder()
	if !guard.Allow(req, rec) {
		t.Fatalf("ordinary API denied: status=%d", rec.Code)
	}
}

func TestPlaybackGuardPhase4AllowsUnmappedButDeniesMappedFallback(t *testing.T) {
	mappedIDs := map[string]bool{
		"mapped":                true,
		"item-mapped-no-source": true,
	}
	guard := NewPlaybackGuard(GuardConfig{
		Phase: 4,
		Lookup: func(r *http.Request) (GuardDecision, error) {
			if mappedIDs[r.URL.Query().Get("mediaSourceId")] || strings.Contains(r.URL.Path, "item-mapped-no-source") || r.URL.Query().Get("PlaySessionId") == "mapped-play-session" {
				return GuardDecision{Mapped: true, HistoricalEvidence: true, Reason: "mapped_source_requires_echo_stream"}, nil
			}
			return GuardDecision{Mapped: false}, nil
		},
	})

	mappedPaths := []string{
		"/emby/Videos/item1/stream?mediaSourceId=mapped",
		"/emby/Videos/item1/original?mediaSourceId=mapped",
		"/emby/Items/item1/Download?mediaSourceId=mapped",
		"/emby/videos/item1/hls/master.m3u8?mediaSourceId=mapped",
		"/emby/Audio/item1/stream.mp3?mediaSourceId=mapped",
		"/emby/Videos/item1/stream?Static=true&PlaySessionId=mapped-play-session",
		"/emby/Videos/item-mapped-no-source/stream?Static=true",
		"/emby/Videos/item-mapped-no-source/stream",
	}
	for _, path := range mappedPaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		if guard.Allow(req, rec) {
			t.Fatalf("%s allowed, want mapped fallback fail closed", path)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", path, rec.Code)
		}
	}

	unmapped := httptest.NewRequest(http.MethodGet, "/emby/Videos/item1/stream?mediaSourceId=unmapped", nil)
	rec := httptest.NewRecorder()
	if !guard.Allow(unmapped, rec) {
		t.Fatalf("unmapped status = %d, want allowed", rec.Code)
	}
}
