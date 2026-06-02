package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	mediaTypeMovie = "movie"
	mediaTypeTV    = "tv"
)

type ErrorKind string

const (
	KindUnknown              ErrorKind = ""
	KindRateLimited          ErrorKind = "rate_limited"
	KindTemporaryUnavailable ErrorKind = "temporary_unavailable"
	KindPermanent            ErrorKind = "permanent"
)

type Error struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("tmdb %s: status %d: %s", e.Kind, e.StatusCode, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("tmdb %s: %v", e.Kind, e.Err)
	}
	return fmt.Sprintf("tmdb %s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ErrorKindOf(err error) ErrorKind {
	var tmdbErr *Error
	if errors.As(err, &tmdbErr) {
		return tmdbErr.Kind
	}
	return KindUnknown
}

func IsKind(err error, kind ErrorKind) bool {
	return ErrorKindOf(err) == kind
}

type Config struct {
	BaseURL  string
	APIKey   string
	Language string
	CacheTTL time.Duration
}

type Media struct {
	TMDBID        string `json:"tmdb_id"`
	MediaType     string `json:"media_type"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseYear   int    `json:"release_year"`
	PosterPath    string `json:"poster_path"`
	RawJSON       string `json:"raw_json"`
}

type Client struct {
	cfg   Config
	http  *http.Client
	cache Cache
}

func NewClient(cfg Config, cache Cache) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.themoviedb.org/3"
	}
	if cfg.Language == "" {
		cfg.Language = "zh-CN"
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 24 * time.Hour
	}
	if cache == nil {
		cache = NewMemoryCache()
	}
	return &Client{
		cfg:   cfg,
		http:  http.DefaultClient,
		cache: cache,
	}
}

func (c *Client) MovieDetails(ctx context.Context, tmdbID string) (Media, error) {
	return c.details(ctx, mediaTypeMovie, tmdbID, true, true)
}

func (c *Client) TVDetails(ctx context.Context, tmdbID string) (Media, error) {
	return c.details(ctx, mediaTypeTV, tmdbID, true, true)
}

func (c *Client) MovieDetailsFresh(ctx context.Context, tmdbID string) (Media, error) {
	return c.details(ctx, mediaTypeMovie, tmdbID, false, false)
}

func (c *Client) TVDetailsFresh(ctx context.Context, tmdbID string) (Media, error) {
	return c.details(ctx, mediaTypeTV, tmdbID, false, false)
}

func (c *Client) Search(ctx context.Context, query, mediaType string) ([]Media, error) {
	query = strings.TrimSpace(query)
	mediaType = strings.TrimSpace(mediaType)
	if query == "" {
		return nil, errors.New("tmdb search query is required")
	}
	if err := validateMediaType(mediaType); err != nil {
		return nil, err
	}
	body, err := c.get(ctx, fmt.Sprintf("/search/%s", mediaType), map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	var decoded searchResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode tmdb search %s response: %w", mediaType, err)
	}
	results := make([]Media, 0, len(decoded.Results))
	for _, raw := range decoded.Results {
		var item detailResponse
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("decode tmdb search %s result: %w", mediaType, err)
		}
		media := item.toMedia(mediaType, raw)
		if media.TMDBID != "" {
			results = append(results, media)
		}
	}
	return results, nil
}

func (c *Client) details(ctx context.Context, mediaType, tmdbID string, readFreshCache bool, allowStaleFallback bool) (Media, error) {
	tmdbID = strings.TrimSpace(tmdbID)
	if tmdbID == "" {
		return Media{}, errors.New("tmdb id is required")
	}
	if err := validateMediaType(mediaType); err != nil {
		return Media{}, err
	}
	now := time.Now()
	baseKey := cacheKey(mediaType, tmdbID)
	key := cacheKeyForLanguage(c.cfg.Language, mediaType, tmdbID)
	if readFreshCache && c.cache != nil {
		media, ok, err := c.cache.Get(ctx, key, now)
		if err != nil {
			return Media{}, fmt.Errorf("get tmdb cache %s: %w", key, err)
		}
		if ok {
			return media, nil
		}
	}
	body, err := c.get(ctx, fmt.Sprintf("/%s/%s", mediaType, url.PathEscape(tmdbID)), nil)
	if err != nil {
		if allowStaleFallback && isRetryableKind(ErrorKindOf(err)) {
			if stale, ok, staleErr := c.getStale(ctx, key); staleErr != nil {
				return Media{}, fmt.Errorf("get stale tmdb cache %s: %w", key, staleErr)
			} else if ok {
				return stale, nil
			}
			if key != baseKey {
				if stale, ok, staleErr := c.getStale(ctx, baseKey); staleErr != nil {
					return Media{}, fmt.Errorf("get stale tmdb cache %s: %w", baseKey, staleErr)
				} else if ok {
					return stale, nil
				}
			}
		}
		return Media{}, err
	}
	var decoded detailResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Media{}, fmt.Errorf("decode tmdb %s details %s: %w", mediaType, tmdbID, err)
	}
	media := decoded.toMedia(mediaType, body)
	if media.TMDBID == "" {
		return Media{}, fmt.Errorf("tmdb %s details %s missing id", mediaType, tmdbID)
	}
	if c.cache != nil {
		if err := c.cache.Set(ctx, key, media, now.Add(c.cfg.CacheTTL)); err != nil {
			return Media{}, fmt.Errorf("set tmdb cache %s: %w", key, err)
		}
	}
	return media, nil
}

func (c *Client) getStale(ctx context.Context, key string) (Media, bool, error) {
	if c.cache == nil {
		return Media{}, false, nil
	}
	stale, ok := c.cache.(StaleCache)
	if !ok {
		return Media{}, false, nil
	}
	return stale.GetStale(ctx, key)
}

func (c *Client) get(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	endpoint, err := url.JoinPath(strings.TrimRight(c.cfg.BaseURL, "/"), path)
	if err != nil {
		return nil, fmt.Errorf("build tmdb url: %w", err)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse tmdb url: %w", err)
	}
	q := u.Query()
	if c.cfg.APIKey != "" {
		q.Set("api_key", c.cfg.APIKey)
	}
	if c.cfg.Language != "" {
		q.Set("language", c.cfg.Language)
	}
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build tmdb request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Kind: KindTemporaryUnavailable, Message: "request failed", Err: err}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{
			Kind:       statusErrorKind(resp.StatusCode),
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(body)),
		}
	}
	if readErr != nil {
		return nil, &Error{Kind: KindTemporaryUnavailable, Message: "read response", Err: readErr}
	}
	return body, nil
}

func statusErrorKind(statusCode int) ErrorKind {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return KindRateLimited
	case statusCode >= 500:
		return KindTemporaryUnavailable
	default:
		return KindPermanent
	}
}

func isRetryableKind(kind ErrorKind) bool {
	return kind == KindRateLimited || kind == KindTemporaryUnavailable
}

type detailResponse struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
	Name          string `json:"name"`
	OriginalName  string `json:"original_name"`
	FirstAirDate  string `json:"first_air_date"`
	PosterPath    string `json:"poster_path"`
}

type searchResponse struct {
	Results []json.RawMessage `json:"results"`
}

func (r detailResponse) toMedia(mediaType string, raw []byte) Media {
	title := r.Title
	originalTitle := r.OriginalTitle
	date := r.ReleaseDate
	if mediaType == mediaTypeTV {
		title = r.Name
		originalTitle = r.OriginalName
		date = r.FirstAirDate
	}
	return Media{
		TMDBID:        idString(r.ID),
		MediaType:     mediaType,
		Title:         title,
		OriginalTitle: originalTitle,
		ReleaseYear:   releaseYear(date),
		PosterPath:    r.PosterPath,
		RawJSON:       string(raw),
	}
}

func idString(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func releaseYear(date string) int {
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return year
}

func validateMediaType(mediaType string) error {
	switch mediaType {
	case mediaTypeMovie, mediaTypeTV:
		return nil
	default:
		return fmt.Errorf("unsupported tmdb media type %q", mediaType)
	}
}

func cacheKey(mediaType, tmdbID string) string {
	return mediaType + ":" + tmdbID
}

func cacheKeyForLanguage(language, mediaType, tmdbID string) string {
	language = strings.TrimSpace(language)
	if language == "" {
		return cacheKey(mediaType, tmdbID)
	}
	return language + ":" + cacheKey(mediaType, tmdbID)
}
