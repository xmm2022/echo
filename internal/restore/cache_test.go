package restore

import (
	"sync"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient"
)

type fakeClock struct {
	t time.Time
}

func (f *fakeClock) now() time.Time { return f.t }

func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func TestLinkCachePutThenGetReturnsLink(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewLinkCache(clk.now)
	link := &sidecarclient.DirectLink{URL: "https://example/x"}

	c.Put(1, 7, link)

	got, ok := c.Get(1, 7)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got != link {
		t.Fatalf("got %v, want same link pointer", got)
	}
}

func TestLinkCacheGetMissForUnknownKey(t *testing.T) {
	c := NewLinkCache((&fakeClock{t: time.Unix(1000, 0)}).now)

	if _, ok := c.Get(1, 7); ok {
		t.Fatal("expected miss for unknown key")
	}
}

func TestLinkCacheExpiresAfterTTL(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewLinkCache(clk.now)
	c.Put(1, 7, &sidecarclient.DirectLink{URL: "https://example/x"})

	clk.advance(61 * time.Second)

	if _, ok := c.Get(1, 7); ok {
		t.Fatal("expected miss after 60s TTL")
	}
}

func TestLinkCacheKeyDistinguishesCopyID(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewLinkCache(clk.now)
	c.Put(1, 7, &sidecarclient.DirectLink{URL: "https://example/copy7"})
	c.Put(1, 8, &sidecarclient.DirectLink{URL: "https://example/copy8"})

	got7, ok := c.Get(1, 7)
	if !ok || got7.URL != "https://example/copy7" {
		t.Fatalf("copy 7 = %v (ok=%v), want copy7 link", got7, ok)
	}
	got8, ok := c.Get(1, 8)
	if !ok || got8.URL != "https://example/copy8" {
		t.Fatalf("copy 8 = %v (ok=%v), want copy8 link", got8, ok)
	}
}

func TestLinkCacheEvictsLeastRecentlyUsed(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := newLinkCache(clk.now, time.Hour, 2) // long TTL so nothing expires

	c.Put(1, 1, &sidecarclient.DirectLink{URL: "one"})
	c.Put(1, 2, &sidecarclient.DirectLink{URL: "two"})

	// Touch key 1 so key 2 becomes least-recently-used.
	clk.advance(time.Second)
	if _, ok := c.Get(1, 1); !ok {
		t.Fatal("expected hit on key 1")
	}

	// Capacity is 2; inserting a third evicts the LRU entry (key 2).
	clk.advance(time.Second)
	c.Put(1, 3, &sidecarclient.DirectLink{URL: "three"})

	if _, ok := c.Get(1, 2); ok {
		t.Fatal("expected key 2 evicted as least-recently-used")
	}
	if _, ok := c.Get(1, 1); !ok {
		t.Fatal("expected key 1 retained")
	}
	if _, ok := c.Get(1, 3); !ok {
		t.Fatal("expected key 3 present")
	}
}

func TestLinkCachePutReclaimsExpiredBeforeEvicting(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := newLinkCache(clk.now, 60*time.Second, 2)

	c.Put(1, 1, &sidecarclient.DirectLink{URL: "one"})
	clk.advance(61 * time.Second) // key 1 now expired
	c.Put(1, 2, &sidecarclient.DirectLink{URL: "two"})
	c.Put(1, 3, &sidecarclient.DirectLink{URL: "three"}) // expired key 1 reclaimed, no live eviction

	if _, ok := c.Get(1, 2); !ok {
		t.Fatal("expected key 2 retained (expired key 1 should be reclaimed first)")
	}
	if _, ok := c.Get(1, 3); !ok {
		t.Fatal("expected key 3 present")
	}
}

func TestLinkCacheConcurrentAccessIsRaceFree(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := newLinkCache(clk.now, time.Hour, 64)
	link := &sidecarclient.DirectLink{URL: "https://example/x"}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := int64((worker + j) % 8)
				c.Put(1, key, link)
				c.Get(1, key)
			}
		}(i)
	}
	wg.Wait()
}
