package embyproxy

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
)

func newEmbyProxyStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("file:" + t.TempDir() + "/echo.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSeedFromConfigRespectsConfigSync(t *testing.T) {
	st := newEmbyProxyStore(t)
	if _, err := st.CreateLibrary(context.Background(), queries.CreateLibraryParams{
		Name: "media", EchoOutputKind: "local", EchoOutputPath: "/tmp/media", OwnerID: "admin", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.EmbyProxyConfig{
		Enabled:       true,
		ConfigSync:    "seed_if_missing",
		PublicBaseURL: "https://echo.example.com",
		ProxyPrefix:   "/emby",
		Upstream:      config.EmbyUpstreamConfig{ID: "default", Name: "main", BaseURL: "http://emby:8096", APIKeyRef: "env:EMBY_API_KEY"},
		PathMappings:  []config.EmbyPathMappingConfig{{LibraryID: 1, EmbyPathPrefix: "/media", EchoRelPrefix: "movies", CaseSensitive: true}},
	}
	if err := SeedFromConfig(context.Background(), st.Queries, cfg, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertEmbyServer(context.Background(), queries.UpsertEmbyServerParams{
		ID: "default", Name: "edited", BaseUrl: "http://edited:8096", ApiKeyRef: sql.NullString{String: "env:EDITED", Valid: true},
		PublicBaseUrl: "https://edited.example.com", ProxyPrefix: "/emby", Enabled: 1, CreatedAt: 1000, UpdatedAt: 1001,
	}); err != nil {
		t.Fatal(err)
	}
	if err := SeedFromConfig(context.Background(), st.Queries, cfg, time.Unix(1002, 0)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetEnabledEmbyServer(context.Background(), queries.GetEnabledEmbyServerParams{ID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "edited" {
		t.Fatalf("seed_if_missing overwrote DB row: %+v", got)
	}

	cfg.ConfigSync = "overwrite_on_startup"
	if err := SeedFromConfig(context.Background(), st.Queries, cfg, time.Unix(1003, 0)); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetEnabledEmbyServer(context.Background(), queries.GetEnabledEmbyServerParams{ID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "main" || got.PublicBaseUrl != "https://echo.example.com" {
		t.Fatalf("overwrite_on_startup did not restore config row: %+v", got)
	}
}
