package embyproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/store/queries"
)

// SeedFromConfig reconciles the configured Emby upstream and library mappings
// into the database according to cfg.ConfigSync.
//
//   - When cfg.Enabled is false it is a no-op.
//   - "seed_if_missing" (also the empty-string default) inserts the configured
//     emby_servers row only when it does not already exist, and inserts a
//     configured path mapping only when no enabled mapping with the same
//     normalized prefix already exists for the server.
//   - "overwrite_on_startup" upserts the emby_servers row, updates any existing
//     enabled mapping that matches a configured normalized prefix, and creates
//     the rest.
//
// It never deletes rows that are absent from config; deletion remains an admin
// API operation.
func SeedFromConfig(ctx context.Context, q *queries.Queries, cfg config.EmbyProxyConfig, now time.Time) error {
	if !cfg.Enabled {
		return nil
	}

	sync := cfg.ConfigSync
	if sync == "" {
		sync = "seed_if_missing"
	}

	ts := now.Unix()
	apiKeyRef := sql.NullString{String: cfg.Upstream.APIKeyRef, Valid: cfg.Upstream.APIKeyRef != ""}

	switch sync {
	case "seed_if_missing":
		if err := seedServerIfMissing(ctx, q, cfg, apiKeyRef, ts); err != nil {
			return err
		}
	case "overwrite_on_startup":
		if _, err := q.UpsertEmbyServer(ctx, queries.UpsertEmbyServerParams{
			ID:            cfg.Upstream.ID,
			Name:          cfg.Upstream.Name,
			BaseUrl:       cfg.Upstream.BaseURL,
			ApiKeyRef:     apiKeyRef,
			PublicBaseUrl: cfg.PublicBaseURL,
			ProxyPrefix:   cfg.ProxyPrefix,
			Enabled:       1,
			CreatedAt:     ts,
			UpdatedAt:     ts,
		}); err != nil {
			return fmt.Errorf("seed emby server %q: %w", cfg.Upstream.ID, err)
		}
	default:
		return fmt.Errorf("unsupported emby_proxy.config_sync %q", sync)
	}

	return seedMappings(ctx, q, cfg, sync, ts)
}

func seedServerIfMissing(ctx context.Context, q *queries.Queries, cfg config.EmbyProxyConfig, apiKeyRef sql.NullString, ts int64) error {
	_, err := q.GetEmbyServer(ctx, queries.GetEmbyServerParams{ID: cfg.Upstream.ID})
	switch {
	case err == nil:
		// Row already exists (enabled or not); leave operator edits untouched.
		return nil
	case errors.Is(err, sql.ErrNoRows):
		if cerr := q.CreateEmbyServer(ctx, queries.CreateEmbyServerParams{
			ID:            cfg.Upstream.ID,
			Name:          cfg.Upstream.Name,
			BaseUrl:       cfg.Upstream.BaseURL,
			ApiKeyRef:     apiKeyRef,
			PublicBaseUrl: cfg.PublicBaseURL,
			ProxyPrefix:   cfg.ProxyPrefix,
			Enabled:       1,
			CreatedAt:     ts,
			UpdatedAt:     ts,
		}); cerr != nil {
			return fmt.Errorf("seed emby server %q: %w", cfg.Upstream.ID, cerr)
		}
		return nil
	default:
		return fmt.Errorf("lookup emby server %q: %w", cfg.Upstream.ID, err)
	}
}

func seedMappings(ctx context.Context, q *queries.Queries, cfg config.EmbyProxyConfig, sync string, ts int64) error {
	existing, err := q.ListEnabledEmbyLibraryMappings(ctx, queries.ListEnabledEmbyLibraryMappingsParams{
		EmbyServerID: cfg.Upstream.ID,
	})
	if err != nil {
		return fmt.Errorf("list emby mappings for %q: %w", cfg.Upstream.ID, err)
	}
	byNorm := make(map[string]int64, len(existing))
	for _, m := range existing {
		byNorm[m.EmbyPathPrefixNorm] = m.ID
	}

	for i, m := range cfg.PathMappings {
		norm, nerr := NormalizeEmbyPath(m.EmbyPathPrefix)
		if nerr != nil {
			return fmt.Errorf("emby_proxy.path_mappings[%d].emby_path_prefix %q: %w", i, m.EmbyPathPrefix, nerr)
		}
		id, ok := byNorm[norm]
		if ok && sync == "seed_if_missing" {
			// An enabled mapping with this normalized prefix already exists; skip.
			continue
		}
		if ok && sync == "overwrite_on_startup" {
			if _, uerr := q.UpdateEmbyLibraryMapping(ctx, queries.UpdateEmbyLibraryMappingParams{
				EmbyPathPrefix:     m.EmbyPathPrefix,
				EmbyPathPrefixNorm: norm,
				LibraryID:          m.LibraryID,
				EchoRelPrefix:      m.EchoRelPrefix,
				CaseSensitive:      boolToInt64(m.CaseSensitive),
				Enabled:            1,
				UpdatedAt:          ts,
				ID:                 id,
			}); uerr != nil {
				return fmt.Errorf("update emby mapping %q: %w", norm, uerr)
			}
			continue
		}
		if _, cerr := q.CreateEmbyLibraryMapping(ctx, queries.CreateEmbyLibraryMappingParams{
			EmbyServerID:       cfg.Upstream.ID,
			LibraryID:          m.LibraryID,
			EmbyPathPrefix:     m.EmbyPathPrefix,
			EmbyPathPrefixNorm: norm,
			EchoRelPrefix:      m.EchoRelPrefix,
			CaseSensitive:      boolToInt64(m.CaseSensitive),
			Enabled:            1,
			CreatedAt:          ts,
			UpdatedAt:          ts,
		}); cerr != nil {
			return fmt.Errorf("create emby mapping %q: %w", norm, cerr)
		}
	}
	return nil
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
