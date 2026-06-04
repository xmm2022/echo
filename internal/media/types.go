package media

import (
	"context"
	"time"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/store"
)

type MetadataClient interface {
	Search(ctx context.Context, query, mediaType string) ([]tmdb.Media, error)
	MovieDetails(ctx context.Context, tmdbID string) (tmdb.Media, error)
	TVDetails(ctx context.Context, tmdbID string) (tmdb.Media, error)
}

type RateLimiter interface {
	Allow(key string, limit int, window time.Duration) bool
	AllowAll(checks []RateLimitCheck) bool
}

type RateLimitCheck struct {
	Key    string
	Limit  int
	Window time.Duration
}

type Service struct {
	Store   *store.Store
	TMDB    MetadataClient
	Limiter RateLimiter
	Now     func() time.Time
}

type Actor struct {
	User auth.UserContext
	IP   string
}

type SearchInput struct {
	Query     string
	MediaType string
	Language  string
}

type CreateRequestInput struct {
	TMDBID           string
	MediaType        string
	TMDBLanguage     string
	SeasonFilterJSON string
	TargetID         int64
	UserNote         string
}
