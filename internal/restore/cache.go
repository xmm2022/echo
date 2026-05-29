package restore

import (
	"sync"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient"
)

const (
	// linkCacheTTL bounds how long a sidecar direct link is reused. The sidecar's
	// own signed-link validity window is the real ceiling (spec §4: TTL=60s).
	linkCacheTTL = 60 * time.Second
	// linkCacheMaxEntries caps memory use; access-order eviction keeps it bounded.
	linkCacheMaxEntries = 1024
)

type cacheKey struct {
	blobID int64
	copyID int64
}

type cacheEntry struct {
	link       *sidecarclient.DirectLink
	expiresAt  time.Time
	lastAccess time.Time
}

// LinkCache is an in-memory, mutex-guarded cache of sidecar direct links keyed by
// (blob_id, copy_id). Entries expire after a TTL (reclaimed lazily on access and
// on insert) and the cache evicts the least-recently-used live entry when it
// reaches capacity. No third-party dependency.
type LinkCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	now     func() time.Time
	entries map[cacheKey]cacheEntry
}

// NewLinkCache builds a LinkCache with the spec defaults (60s TTL). A nil now
// defaults to time.Now.
func NewLinkCache(now func() time.Time) *LinkCache {
	return newLinkCache(now, linkCacheTTL, linkCacheMaxEntries)
}

func newLinkCache(now func() time.Time, ttl time.Duration, max int) *LinkCache {
	if now == nil {
		now = time.Now
	}
	return &LinkCache{
		ttl:     ttl,
		max:     max,
		now:     now,
		entries: make(map[cacheKey]cacheEntry),
	}
}

// Get returns the cached link for (blobID, copyID) when present and unexpired.
// Expired entries are dropped lazily on access.
func (c *LinkCache) Get(blobID, copyID int64) (*sidecarclient.DirectLink, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey{blobID: blobID, copyID: copyID}
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	now := c.now()
	if !now.Before(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	entry.lastAccess = now
	c.entries[key] = entry
	return entry.link, true
}

// Put stores link under (blobID, copyID) with a fresh TTL. When inserting a new
// key at capacity it first reclaims expired entries, then evicts the
// least-recently-used live entry.
func (c *LinkCache) Put(blobID, copyID int64, link *sidecarclient.DirectLink) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey{blobID: blobID, copyID: copyID}
	now := c.now()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		c.evictLocked(now)
	}
	c.entries[key] = cacheEntry{
		link:       link,
		expiresAt:  now.Add(c.ttl),
		lastAccess: now,
	}
}

// evictLocked reclaims expired entries first; if the cache is still at capacity
// it drops the least-recently-used entry. Caller must hold c.mu.
func (c *LinkCache) evictLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) < c.max {
		return
	}
	var lruKey cacheKey
	var lruAccess time.Time
	first := true
	for key, entry := range c.entries {
		if first || entry.lastAccess.Before(lruAccess) {
			lruKey = key
			lruAccess = entry.lastAccess
			first = false
		}
	}
	if !first {
		delete(c.entries, lruKey)
	}
}
