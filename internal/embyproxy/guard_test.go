package embyproxy

import (
	"net/http"
	"net/http/httptest"
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
