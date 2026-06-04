package media

import (
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/auth"
)

func TestFixedWindowLimiterAllowAllIsAtomic(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewFixedWindowLimiter(func() time.Time { return now })

	if !limiter.Allow("search:ip:203.0.113.10", 1, time.Minute) {
		t.Fatalf("pre-fill ip limit failed")
	}
	if limiter.AllowAll([]RateLimitCheck{
		{Key: "search:user:u1", Limit: 1, Window: time.Minute},
		{Key: "search:ip:203.0.113.10", Limit: 1, Window: time.Minute},
	}) {
		t.Fatalf("AllowAll returned true with exhausted IP key")
	}
	if !limiter.Allow("search:user:u1", 1, time.Minute) {
		t.Fatalf("AllowAll consumed user key even though IP key denied")
	}

	if !limiter.Allow("tmdb:global:search", 1, time.Minute) {
		t.Fatalf("pre-fill global search limit failed")
	}
	if limiter.AllowAll([]RateLimitCheck{
		{Key: "search:user:u2", Limit: 1, Window: time.Minute},
		{Key: "search:ip:198.51.100.10", Limit: 1, Window: time.Minute},
		{Key: "tmdb:global:search", Limit: 1, Window: time.Minute},
	}) {
		t.Fatalf("AllowAll returned true with exhausted global key")
	}
	if !limiter.Allow("search:user:u2", 1, time.Minute) {
		t.Fatalf("AllowAll consumed user key even though global key denied")
	}
	if !limiter.Allow("search:ip:198.51.100.10", 1, time.Minute) {
		t.Fatalf("AllowAll consumed IP key even though global key denied")
	}
}

func TestFixedWindowLimiterAllowAllRejectsDuplicateKeys(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewFixedWindowLimiter(func() time.Time { return now })

	if limiter.AllowAll([]RateLimitCheck{
		{Key: "search:user:u1", Limit: 1, Window: time.Minute},
		{Key: "search:user:u1", Limit: 1, Window: time.Minute},
	}) {
		t.Fatalf("AllowAll allowed duplicate keys")
	}
	if !limiter.Allow("search:user:u1", 1, time.Minute) {
		t.Fatalf("AllowAll duplicate-key denial consumed the key")
	}
}

func TestFixedWindowLimiterResetsAfterWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewFixedWindowLimiter(func() time.Time { return now })

	if !limiter.Allow("status-poll:user:u1", 2, time.Minute) {
		t.Fatalf("first request denied")
	}
	if !limiter.Allow("status-poll:user:u1", 2, time.Minute) {
		t.Fatalf("second request denied")
	}
	if limiter.Allow("status-poll:user:u1", 2, time.Minute) {
		t.Fatalf("third request allowed before window reset")
	}

	now = now.Add(time.Minute)
	if !limiter.Allow("status-poll:user:u1", 2, time.Minute) {
		t.Fatalf("request denied after fixed window reset")
	}
}

func TestServiceRateLimitWrappersUseRequiredKeysLimitsAndWindows(t *testing.T) {
	limiter := &recordingRateLimiter{}
	svc := Service{Limiter: limiter}
	actor := Actor{
		User: auth.UserContext{UserID: "u1", Scopes: []string{"discovery"}},
		IP:   "203.0.113.10:54321",
	}

	if !svc.AllowSearchWithTMDBRate(actor) {
		t.Fatalf("search rate denied")
	}
	if !svc.AllowRequestCreateWithTMDBRefreshRate(actor) {
		t.Fatalf("create rate denied")
	}
	if !svc.AllowRequestActionRate(actor) {
		t.Fatalf("action rate denied")
	}
	if !svc.AllowStatusPollRate(actor) {
		t.Fatalf("status rate denied")
	}

	limiter.assertCall(t, 0, []RateLimitCheck{
		{Key: "search:user:u1", Limit: 30, Window: time.Minute},
		{Key: "search:ip:203.0.113.10", Limit: 60, Window: time.Minute},
		{Key: "tmdb:global:search", Limit: 300, Window: time.Minute},
	})
	limiter.assertCall(t, 1, []RateLimitCheck{
		{Key: "request-create:user:u1", Limit: 10, Window: time.Hour},
		{Key: "request-create:ip:203.0.113.10", Limit: 30, Window: time.Hour},
		{Key: "tmdb:global:refresh", Limit: 120, Window: time.Minute},
	})
	limiter.assertCall(t, 2, []RateLimitCheck{
		{Key: "request-action:user:u1", Limit: 30, Window: time.Minute},
		{Key: "request-action:ip:203.0.113.10", Limit: 90, Window: time.Minute},
	})
	limiter.assertCall(t, 3, []RateLimitCheck{
		{Key: "status-poll:user:u1", Limit: 120, Window: time.Minute},
		{Key: "status-poll:ip:203.0.113.10", Limit: 300, Window: time.Minute},
	})
}

func TestServiceCompositeRateLimitRejectsMissingUser(t *testing.T) {
	limiter := &recordingRateLimiter{}
	svc := Service{Limiter: limiter}
	actor := Actor{IP: "203.0.113.10:54321"}

	if svc.AllowSearchWithTMDBRate(actor) {
		t.Fatalf("search rate allowed actor without user id")
	}
	if svc.AllowSearchPageRate(actor) {
		t.Fatalf("search page rate allowed actor without user id")
	}
	if svc.AllowRequestCreateWithTMDBRefreshRate(actor) {
		t.Fatalf("create rate allowed actor without user id")
	}
	if len(limiter.calls) != 0 {
		t.Fatalf("limiter calls=%v, want no global-only checks for missing user", limiter.calls)
	}
}

type recordingRateLimiter struct {
	calls [][]RateLimitCheck
}

func (l *recordingRateLimiter) Allow(key string, limit int, window time.Duration) bool {
	return l.AllowAll([]RateLimitCheck{{Key: key, Limit: limit, Window: window}})
}

func (l *recordingRateLimiter) AllowAll(checks []RateLimitCheck) bool {
	l.calls = append(l.calls, append([]RateLimitCheck(nil), checks...))
	return true
}

func (l *recordingRateLimiter) assertCall(t *testing.T, index int, want []RateLimitCheck) {
	t.Helper()
	if len(l.calls) <= index {
		t.Fatalf("rate limiter calls=%v, missing call %d", l.calls, index)
	}
	got := l.calls[index]
	if len(got) != len(want) {
		t.Fatalf("call %d len=%d want %d; got=%v", index, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d check %d = %+v, want %+v", index, i, got[i], want[i])
		}
	}
}
