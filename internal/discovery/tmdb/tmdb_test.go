package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDetailsUsesCacheWhenFresh(t *testing.T) {
	ctx := context.Background()
	serverCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":123,"title":"Cached Movie","release_date":"2024-01-01"}`))
	}))
	defer srv.Close()
	cache := NewMemoryCache()
	client := NewClient(Config{BaseURL: srv.URL, APIKey: "key", Language: "zh-CN", CacheTTL: time.Hour}, cache)
	if _, err := client.MovieDetails(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.MovieDetails(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	if serverCalls != 1 {
		t.Fatalf("server calls = %d, want 1", serverCalls)
	}
}

func TestCacheKeyIncludesMediaTypeAndID(t *testing.T) {
	if got, want := cacheKey("movie", "123"), "movie:123"; got != want {
		t.Fatalf("cache key = %q, want %q", got, want)
	}
	if got, want := cacheKey("tv", "123"), "tv:123"; got != want {
		t.Fatalf("cache key = %q, want %q", got, want)
	}
}

func TestClientCacheKeyIncludesLanguage(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("language") {
		case "zh-CN":
			_, _ = w.Write([]byte(`{"id":123,"title":"盗梦空间","original_title":"Inception","release_date":"2010-07-16"}`))
		case "en-US":
			_, _ = w.Write([]byte(`{"id":123,"title":"Inception","original_title":"Inception","release_date":"2010-07-16"}`))
		default:
			t.Fatalf("unexpected language %q", r.URL.Query().Get("language"))
		}
	}))
	defer srv.Close()
	zh := NewClient(Config{BaseURL: srv.URL, APIKey: "key", Language: "zh-CN", CacheTTL: time.Hour}, cache)
	en := NewClient(Config{BaseURL: srv.URL, APIKey: "key", Language: "en-US", CacheTTL: time.Hour}, cache)
	if _, err := zh.MovieDetails(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	got, err := en.MovieDetails(ctx, "123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Inception" {
		t.Fatalf("en title = %q, want Inception", got.Title)
	}
	if calls != 2 {
		t.Fatalf("server calls = %d, want 2", calls)
	}
}

func TestMemoryCacheExpiresFreshButKeepsStale(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()
	key := cacheKey("movie", "123")
	stale := Media{TMDBID: "123", MediaType: "movie", Title: "Expired Movie"}
	now := time.Unix(100, 0)
	if err := cache.Set(ctx, key, stale, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.Get(ctx, key, now.Add(2*time.Second)); err != nil || ok {
		t.Fatalf("fresh get after expiration ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	got, ok, err := cache.GetStale(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("stale get ok=false, want true")
	}
	if got.Title != stale.Title {
		t.Fatalf("stale title = %q, want %q", got.Title, stale.Title)
	}
}

func TestMatchTitleYearPrefersTMDBID(t *testing.T) {
	candidates := []Media{
		{TMDBID: "123", MediaType: "movie", Title: "Wrong", ReleaseYear: 2000},
		{TMDBID: "456", MediaType: "movie", Title: "123", ReleaseYear: 2024},
	}
	got, ok := MatchTitleYear(candidates, "123", 2024, "movie")
	if !ok {
		t.Fatal("MatchTitleYear ok=false, want true")
	}
	if got.TMDBID != "123" {
		t.Fatalf("tmdb id = %q, want 123", got.TMDBID)
	}
}

func TestMatchTitleYearPrefersTitleAndYear(t *testing.T) {
	candidates := []Media{
		{TMDBID: "old", MediaType: "movie", Title: "Dune", ReleaseYear: 1984},
		{TMDBID: "new", MediaType: "movie", Title: "Dune", ReleaseYear: 2021},
	}
	got, ok := MatchTitleYear(candidates, "dune", 2021, "movie")
	if !ok {
		t.Fatal("MatchTitleYear ok=false, want true")
	}
	if got.TMDBID != "new" {
		t.Fatalf("tmdb id = %q, want new", got.TMDBID)
	}
}

func TestMatchTitleYearFallsBackToTitleOnly(t *testing.T) {
	candidates := []Media{{TMDBID: "movie", MediaType: "movie", Title: "Arrival", ReleaseYear: 2016}}
	got, ok := MatchTitleYear(candidates, "arrival", 0, "movie")
	if !ok {
		t.Fatal("MatchTitleYear ok=false, want true")
	}
	if got.TMDBID != "movie" {
		t.Fatalf("tmdb id = %q, want movie", got.TMDBID)
	}
}

func TestMatchTitleYearFiltersMediaType(t *testing.T) {
	candidates := []Media{
		{TMDBID: "movie", MediaType: "movie", Title: "Watchmen", ReleaseYear: 2009},
		{TMDBID: "tv", MediaType: "tv", Title: "Watchmen", ReleaseYear: 2019},
	}
	got, ok := MatchTitleYear(candidates, "watchmen", 2019, "tv")
	if !ok {
		t.Fatal("MatchTitleYear ok=false, want true")
	}
	if got.TMDBID != "tv" {
		t.Fatalf("tmdb id = %q, want tv", got.TMDBID)
	}
}

func TestMatchTitleYearMatchesOriginalTitle(t *testing.T) {
	candidates := []Media{{
		TMDBID:        "27205",
		MediaType:     "movie",
		Title:         "盗梦空间",
		OriginalTitle: "Inception",
		ReleaseYear:   2010,
	}}
	got, ok := MatchTitleYear(candidates, "Inception", 2010, "movie")
	if !ok {
		t.Fatal("MatchTitleYear ok=false, want true")
	}
	if got.TMDBID != "27205" {
		t.Fatalf("tmdb id = %q, want 27205", got.TMDBID)
	}
}

func TestDetailsFallsBackToStaleCacheOnRateLimit(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()
	key := cacheKey("movie", "123")
	stale := Media{TMDBID: "123", MediaType: "movie", Title: "Stale Movie"}
	if err := cache.Set(ctx, key, stale, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	client := NewClient(Config{BaseURL: srv.URL, APIKey: "key", Language: "zh-CN", CacheTTL: time.Hour}, cache)
	got, err := client.MovieDetails(ctx, "123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != stale.Title {
		t.Fatalf("title = %q, want stale %q", got.Title, stale.Title)
	}
}

func TestHTTPStatusErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		code int
		want ErrorKind
	}{
		{name: "rate limited", code: http.StatusTooManyRequests, want: KindRateLimited},
		{name: "server unavailable", code: http.StatusServiceUnavailable, want: KindTemporaryUnavailable},
		{name: "not found", code: http.StatusNotFound, want: KindPermanent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "tmdb error", tt.code)
			}))
			defer srv.Close()
			client := NewClient(Config{BaseURL: srv.URL, APIKey: "key"}, NewMemoryCache())
			_, err := client.MovieDetails(context.Background(), "123")
			if err == nil {
				t.Fatal("MovieDetails err=nil, want error")
			}
			if got := ErrorKindOf(err); got != tt.want {
				t.Fatalf("ErrorKindOf = %q, want %q; err=%v", got, tt.want, err)
			}
		})
	}
}

func TestTimeoutErrorClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	client := NewClient(Config{BaseURL: srv.URL, APIKey: "key"}, NewMemoryCache())
	_, err := client.MovieDetails(ctx, "123")
	if err == nil {
		t.Fatal("MovieDetails err=nil, want timeout error")
	}
	if got := ErrorKindOf(err); got != KindTemporaryUnavailable {
		t.Fatalf("ErrorKindOf = %q, want %q; err=%v", got, KindTemporaryUnavailable, err)
	}
}
