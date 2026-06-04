package media

import (
	"net"
	"strings"
	"sync"
	"time"
)

const (
	searchUserLimit        = 30
	searchIPLimit          = 60
	tmdbSearchLimit        = 300
	tmdbRefreshLimit       = 120
	requestCreateUserLimit = 10
	requestCreateIPLimit   = 30
	requestActionUserLimit = 30
	requestActionIPLimit   = 90
	statusPollUserLimit    = 120
	statusPollIPLimit      = 300
)

type rateWindow struct {
	start time.Time
	count int
}

type FixedWindowLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	windows map[string]rateWindow
}

func NewFixedWindowLimiter(now func() time.Time) *FixedWindowLimiter {
	if now == nil {
		now = time.Now
	}
	return &FixedWindowLimiter{
		now:     now,
		windows: make(map[string]rateWindow),
	}
}

func (l *FixedWindowLimiter) Allow(key string, limit int, window time.Duration) bool {
	return l.AllowAll([]RateLimitCheck{{Key: key, Limit: limit, Window: window}})
}

func (l *FixedWindowLimiter) AllowAll(checks []RateLimitCheck) bool {
	if l == nil || len(checks) == 0 {
		return false
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	next := make([]rateWindow, len(checks))
	seen := make(map[string]struct{}, len(checks))
	for i, check := range checks {
		if check.Key == "" || check.Limit <= 0 || check.Window <= 0 {
			return false
		}
		if _, ok := seen[check.Key]; ok {
			return false
		}
		seen[check.Key] = struct{}{}
		current := l.windows[check.Key]
		if current.start.IsZero() || !now.Before(current.start.Add(check.Window)) {
			current = rateWindow{start: now}
		}
		if current.count >= check.Limit {
			return false
		}
		next[i] = current
	}

	for i, check := range checks {
		current := next[i]
		current.count++
		l.windows[check.Key] = current
	}
	return true
}

func (s *Service) AllowSearchRate(actor Actor) bool {
	return s.allowRate(actor, "search", searchUserLimit, searchIPLimit, time.Minute)
}

func (s *Service) AllowSearchWithTMDBRate(actor Actor) bool {
	checks := s.rateChecks(actor, "search", searchUserLimit, searchIPLimit, time.Minute)
	if len(checks) == 0 {
		return s.allowAllRate(nil)
	}
	checks = append(checks, RateLimitCheck{Key: "tmdb:global:search", Limit: tmdbSearchLimit, Window: time.Minute})
	return s.allowAllRate(checks)
}

func (s *Service) AllowSearchPageRate(actor Actor) bool {
	checks := s.rateChecks(actor, "status-poll", statusPollUserLimit, statusPollIPLimit, time.Minute)
	searchChecks := s.rateChecks(actor, "search", searchUserLimit, searchIPLimit, time.Minute)
	if len(checks) == 0 || len(searchChecks) == 0 {
		return s.allowAllRate(nil)
	}
	checks = append(checks, searchChecks...)
	checks = append(checks, RateLimitCheck{Key: "tmdb:global:search", Limit: tmdbSearchLimit, Window: time.Minute})
	return s.allowAllRate(checks)
}

func (s *Service) AllowTMDBSearchRate() bool {
	return s.allowAllRate([]RateLimitCheck{{Key: "tmdb:global:search", Limit: tmdbSearchLimit, Window: time.Minute}})
}

func (s *Service) AllowTMDBRefreshRate() bool {
	return s.allowAllRate([]RateLimitCheck{{Key: "tmdb:global:refresh", Limit: tmdbRefreshLimit, Window: time.Minute}})
}

func (s *Service) AllowRequestCreateRate(actor Actor) bool {
	return s.allowRate(actor, "request-create", requestCreateUserLimit, requestCreateIPLimit, time.Hour)
}

func (s *Service) AllowRequestCreateWithTMDBRefreshRate(actor Actor) bool {
	checks := s.rateChecks(actor, "request-create", requestCreateUserLimit, requestCreateIPLimit, time.Hour)
	if len(checks) == 0 {
		return s.allowAllRate(nil)
	}
	checks = append(checks, RateLimitCheck{Key: "tmdb:global:refresh", Limit: tmdbRefreshLimit, Window: time.Minute})
	return s.allowAllRate(checks)
}

func (s *Service) AllowRequestActionRate(actor Actor) bool {
	return s.allowRate(actor, "request-action", requestActionUserLimit, requestActionIPLimit, time.Minute)
}

func (s *Service) AllowStatusPollRate(actor Actor) bool {
	return s.allowRate(actor, "status-poll", statusPollUserLimit, statusPollIPLimit, time.Minute)
}

func (s *Service) allowRate(actor Actor, prefix string, userLimit, ipLimit int, window time.Duration) bool {
	return s.allowAllRate(s.rateChecks(actor, prefix, userLimit, ipLimit, window))
}

func (s *Service) rateChecks(actor Actor, prefix string, userLimit, ipLimit int, window time.Duration) []RateLimitCheck {
	userID := strings.TrimSpace(actor.User.UserID)
	if userID == "" {
		return nil
	}
	ip := clientIP(actor.IP)
	if ip == "" {
		ip = "unknown"
	}
	return []RateLimitCheck{
		{Key: prefix + ":user:" + userID, Limit: userLimit, Window: window},
		{Key: prefix + ":ip:" + ip, Limit: ipLimit, Window: window},
	}
}

func (s *Service) allowAllRate(checks []RateLimitCheck) bool {
	if s == nil || s.Limiter == nil {
		return true
	}
	if len(checks) == 0 {
		return false
	}
	return s.Limiter.AllowAll(checks)
}

func clientIP(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}
