package embyproxy

import (
	"encoding/json"
	"net/http"
	"strings"
)

// GuardConfig configures the playback guard. Phase selects the guard's posture: Phase 3
// (and any later phase wired with this Phase-3 value) is fail-closed for playback/stream
// endpoints, because the PlaybackInfo rewrite that would mint Echo stream tokens does not
// exist yet. Phase 4 replaces this guard wholesale with a mapping-aware one, so the switch
// here is intentionally minimal.
type GuardConfig struct {
	Phase int
}

// PlaybackGuard inspects a request bound for the transparent upstream fallback and decides
// whether it may proceed. Before the Phase 4 mapping guard exists, any request that looks
// like a media stream / download must NOT reach upstream Emby untokenized, so the guard
// fails closed with a controlled 503.
type PlaybackGuard struct {
	cfg GuardConfig
}

// NewPlaybackGuard builds a PlaybackGuard with the given config.
func NewPlaybackGuard(cfg GuardConfig) *PlaybackGuard {
	return &PlaybackGuard{cfg: cfg}
}

// Allow reports whether the request may proceed to the upstream fallback. When it denies,
// it has already written the controlled 503 response and returns false. When it allows, it
// returns true WITHOUT writing anything.
func (g *PlaybackGuard) Allow(r *http.Request, w http.ResponseWriter) bool {
	// Phase < 3 has no fail-closed posture defined; treat it as a pass-through. Phase 3+
	// (the only wiring we ship) is fail-closed for playback/stream-like requests.
	if g.cfg.Phase >= 3 && g.suspicious(r) {
		g.deny(w)
		return false
	}
	return true
}

// suspicious reports whether the request targets a media stream / download endpoint that
// must not be transparently proxied before Echo can rewrite it into tokenized sources.
// Path matching is case-insensitive (Emby route segments are case-insensitive in practice);
// query param NAMES are matched as written because Emby's query params are case-sensitive.
func (g *PlaybackGuard) suspicious(r *http.Request) bool {
	path := strings.ToLower(r.URL.Path)

	switch {
	case strings.Contains(path, "/videos/") && strings.Contains(path, "/stream"):
		return true
	case strings.Contains(path, "/videos/") && strings.Contains(path, "/original"):
		return true
	case strings.Contains(path, "/items/") && strings.Contains(path, "/download"):
		return true
	case strings.Contains(path, "/videos/") && strings.Contains(path, "/hls"):
		return true
	case strings.Contains(path, "/audio/") && strings.Contains(path, "/stream"):
		return true
	}

	// Query-based rule: a stream-like path carrying any of the playback query params is a
	// direct-play / transcode request. Gating on a stream-like path keeps ordinary API
	// calls (no query, or query on a non-stream path) ALLOWED.
	if streamLikePath(path) {
		q := r.URL.Query()
		if q.Get("Static") == "true" || q.Has("mediaSourceId") || q.Has("PlaySessionId") {
			return true
		}
	}
	return false
}

// streamLikePath defines, conservatively, the set of paths the query-based rule applies to:
// anything under /videos/ or /audio/, or any path containing a /stream segment. Defined
// against the already lower-cased path.
func streamLikePath(lowerPath string) bool {
	return strings.Contains(lowerPath, "/videos/") ||
		strings.Contains(lowerPath, "/audio/") ||
		strings.Contains(lowerPath, "/stream")
}

// deny writes the controlled 503 that tells the player to retry rather than fall back to a
// direct, untokenized upstream source.
func (g *PlaybackGuard) deny(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Echo-Reason", "temporary_unavailable")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "temporary_unavailable"})
}
