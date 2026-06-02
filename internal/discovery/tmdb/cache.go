package tmdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xmm2022/echo/internal/store/queries"
)

type Cache interface {
	Get(ctx context.Context, key string, now time.Time) (Media, bool, error)
	Set(ctx context.Context, key string, media Media, expiresAt time.Time) error
}

type StaleCache interface {
	GetStale(ctx context.Context, key string) (Media, bool, error)
}

type memoryEntry struct {
	media     Media
	expiresAt time.Time
}

type MemoryCache struct {
	mu    sync.Mutex
	items map[string]memoryEntry
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{items: make(map[string]memoryEntry)}
}

func (c *MemoryCache) Get(_ context.Context, key string, now time.Time) (Media, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return Media{}, false, nil
	}
	if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
		return Media{}, false, nil
	}
	return entry.media, true, nil
}

func (c *MemoryCache) GetStale(_ context.Context, key string) (Media, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return Media{}, false, nil
	}
	return entry.media, true, nil
}

func (c *MemoryCache) Set(_ context.Context, key string, media Media, expiresAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = memoryEntry{media: media, expiresAt: expiresAt}
	return nil
}

type SQLiteCache struct {
	queries  *queries.Queries
	language string
}

func NewSQLiteCache(q *queries.Queries, language string) *SQLiteCache {
	if language == "" {
		language = "zh-CN"
	}
	return &SQLiteCache{queries: q, language: language}
}

func (c *SQLiteCache) Get(ctx context.Context, key string, now time.Time) (Media, bool, error) {
	if c == nil || c.queries == nil {
		return Media{}, false, errors.New("tmdb sqlite cache has nil queries")
	}
	mediaType, tmdbID, err := splitCacheKey(key)
	if err != nil {
		return Media{}, false, err
	}
	row, err := c.queries.GetTMDBMedia(ctx, queries.GetTMDBMediaParams{
		TmdbID:    tmdbID,
		MediaType: mediaType,
		Language:  c.language,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, false, nil
	}
	if err != nil {
		return Media{}, false, err
	}
	if row.NextRefreshAt <= now.Unix() {
		return Media{}, false, nil
	}
	return mediaFromRow(row), true, nil
}

func (c *SQLiteCache) GetStale(ctx context.Context, key string) (Media, bool, error) {
	if c == nil || c.queries == nil {
		return Media{}, false, errors.New("tmdb sqlite cache has nil queries")
	}
	mediaType, tmdbID, err := splitCacheKey(key)
	if err != nil {
		return Media{}, false, err
	}
	row, err := c.queries.GetTMDBMedia(ctx, queries.GetTMDBMediaParams{
		TmdbID:    tmdbID,
		MediaType: mediaType,
		Language:  c.language,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, false, nil
	}
	if err != nil {
		return Media{}, false, err
	}
	return mediaFromRow(row), true, nil
}

func (c *SQLiteCache) Set(ctx context.Context, key string, media Media, expiresAt time.Time) error {
	if c == nil || c.queries == nil {
		return errors.New("tmdb sqlite cache has nil queries")
	}
	mediaType, tmdbID, err := splitCacheKey(key)
	if err != nil {
		return err
	}
	if media.MediaType == "" {
		media.MediaType = mediaType
	}
	if media.TMDBID == "" {
		media.TMDBID = tmdbID
	}
	if media.MediaType != mediaType || media.TMDBID != tmdbID {
		return fmt.Errorf("tmdb cache key %q does not match media %s:%s", key, media.MediaType, media.TMDBID)
	}
	_, err = c.queries.UpsertTMDBMedia(ctx, queries.UpsertTMDBMediaParams{
		TmdbID:        media.TMDBID,
		MediaType:     media.MediaType,
		Language:      c.language,
		Title:         media.Title,
		OriginalTitle: nullString(media.OriginalTitle),
		ReleaseYear:   nullInt64(media.ReleaseYear),
		PosterPath:    nullString(media.PosterPath),
		Status:        sql.NullString{String: "ok", Valid: true},
		RawJson:       media.RawJSON,
		FetchedAt:     time.Now().Unix(),
		NextRefreshAt: expiresAt.Unix(),
	})
	return err
}

func (c *SQLiteCache) SearchByTitle(ctx context.Context, title, mediaType string, limit int64) ([]Media, error) {
	if c == nil || c.queries == nil {
		return nil, errors.New("tmdb sqlite cache has nil queries")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("tmdb cache title is required")
	}
	mediaType = strings.TrimSpace(mediaType)
	if err := validateMediaType(mediaType); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := c.queries.SearchTMDBMediaByTitle(ctx, queries.SearchTMDBMediaByTitleParams{
		MediaType: mediaType,
		Language:  c.language,
		Title:     "%" + title + "%",
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Media, 0, len(rows))
	for _, row := range rows {
		out = append(out, mediaFromRow(row))
	}
	return out, nil
}

func splitCacheKey(key string) (string, string, error) {
	parts := strings.Split(key, ":")
	var mediaType, tmdbID string
	switch len(parts) {
	case 2:
		mediaType, tmdbID = parts[0], parts[1]
	case 3:
		mediaType, tmdbID = parts[1], parts[2]
	default:
		return "", "", fmt.Errorf("invalid tmdb cache key %q", key)
	}
	if mediaType == "" || tmdbID == "" {
		return "", "", fmt.Errorf("invalid tmdb cache key %q", key)
	}
	if err := validateMediaType(mediaType); err != nil {
		return "", "", err
	}
	return mediaType, tmdbID, nil
}

func mediaFromRow(row queries.TmdbMedium) Media {
	return Media{
		TMDBID:        row.TmdbID,
		MediaType:     row.MediaType,
		Title:         row.Title,
		OriginalTitle: row.OriginalTitle.String,
		ReleaseYear:   int(row.ReleaseYear.Int64),
		PosterPath:    row.PosterPath.String,
		RawJSON:       row.RawJson,
	}
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullInt64(value int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(value), Valid: value != 0}
}
