package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/embyproxy"
	httpserver "github.com/xmm2022/echo/internal/http"
	"github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/logging"
	"github.com/xmm2022/echo/internal/metrics"
	"github.com/xmm2022/echo/internal/playback"
	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/store/queries"
	"github.com/xmm2022/echo/internal/web"
)

// version is the Echo build version, surfaced via echo_build_info and /readyz.
// Override at build time with -ldflags "-X main.version=<tag>".
var version = "v0.1.0-dev"

const shutdownTimeout = 30 * time.Second

// playbackStreamTimeout bounds how far back an unfinished playback_events stream is
// still treated as active before reconciliation reclaims it as 'interrupted'. It
// mirrors playback's defaultStreamTimeout (6h); a later task wires it to config.
const playbackStreamTimeout = 6 * time.Hour

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: echo serve --config <path>")
	}

	switch args[0] {
	case "migrate":
		return runMigrate(args[1:])
	case "serve":
		return runServe(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runServe(args []string) error {
	defaultConfigPath := os.Getenv("ECHO_CONFIG_PATH")
	if defaultConfigPath == "" {
		defaultConfigPath = config.DefaultPath
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultConfigPath, "path to config.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadPath(*configPath)
	if err != nil {
		return err
	}

	logger, err := newLogger(cfg.Log)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)

	// Data + execution-plane clients.
	st, err := store.Open("file:" + cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Metrics: a dedicated registry (kept off the global default) holds Echo's
	// collectors plus the standard Go/process collectors and the pull-based state
	// gauges. /metrics serves this registry (see Deps.Registry).
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector())
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := metrics.New(registry)
	m.SetBuildInfo(version, cfg.Sidecar.Default.MinVersion)
	registry.MustRegister(metrics.NewStateCollector(st.DB, &storeStateSource{store: st}, logger))

	// rawSidecar drives readiness probes; the instrumented wrapper records
	// business calls so periodic /readyz pings do not inflate sidecar metrics.
	rawSidecar := sidecarclient.New(sidecarclient.FromEndpointConfig(cfg.Sidecar.Default))
	sidecar := metrics.InstrumentSidecar(rawSidecar, m, "default")

	// Job runner: drives ingest jobs (manual + producer).
	runner, err := job.New(job.Config{
		Store: st,
		Handlers: job.IngestHandlers(ingest.Deps{
			Store:   st,
			Sidecar: sidecar,
			Config:  ingestConfig(cfg),
			Metrics: m,
			Logger:  logger,
		}),
		MaxConcurrent: cfg.Jobs.MaxConcurrent,
		Logger:        logger,
		Metrics:       m,
	})
	if err != nil {
		return fmt.Errorf("build job runner: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Reclaim playback streams left unfinished by a previous crash so their leases do
	// not count against users' concurrency quota forever. The full Emby proxy wiring
	// (Upstream + config) lands in a later task; this reconcile is safe to run alone.
	playbackQuota := playback.NewQuota(st.Queries, nil, playbackStreamTimeout)
	if err := playbackQuota.ReconcileInterruptedStreams(ctx); err != nil {
		return fmt.Errorf("reconcile playback streams: %w", err)
	}

	// Correct the active-sessions gauge to the authoritative DB count after the
	// reconcile reclaims crash-orphaned leases. The stream handler keeps it fresh
	// thereafter via inc/dec; this startup Set is the source of truth across restarts
	// (the gauge is push-style, never sampled by the pull collector — registered once).
	if active, err := st.Queries.CountActivePlaybackSessions(ctx); err != nil {
		logger.Warn("count active playback sessions for gauge", "err", err)
	} else {
		m.PlaybackSessionsActive(active)
	}

	if err := runner.Start(ctx); err != nil {
		return fmt.Errorf("start job runner: %w", err)
	}
	defer runner.Stop()

	// Restore/stream plane.
	resolver := restore.NewResolver(st.Queries, nil)
	cache := restore.NewLinkCache(nil)
	authenticator := &auth.Authenticator{Store: st}

	// Seed the configured Emby upstream + library mappings into the DB before the proxy
	// reads them; config_sync governs seed-if-missing vs overwrite-on-startup. No-op when
	// emby_proxy is disabled. Must run before buildEmbyDeps, which reads GetEnabledEmbyServer.
	if err := embyproxy.SeedFromConfig(ctx, st.Queries, cfg.EmbyProxy, time.Now()); err != nil {
		return fmt.Errorf("seed emby proxy config: %w", err)
	}

	// Emby reverse proxy. The proxy is mounted only when a single enabled Emby server is
	// configured; on a fresh DB (no enabled server) we still mount a fully fail-closed Deps
	// so every /emby/* answers a controlled 503 rather than nil-handler-panicking or
	// silently proxying an untokenized upstream source.
	embyDeps, err := buildEmbyDeps(ctx, st, sidecar, playbackQuota, m, cfg.EmbyProxy, logger)
	if err != nil {
		return err
	}

	deps := httpserver.Deps{
		Logger:     logger,
		AdminToken: cfg.Auth.AdminToken,
		AuthCheck:  authenticator.Authenticate,
		Restore:    &handlers.RestoreDeps{Resolver: resolver, Sidecar: sidecar, Cache: cache, Logger: logger, Metrics: m},
		Stream:     &handlers.StreamDeps{Resolver: resolver, Sidecar: sidecar, Logger: logger, Metrics: m},
		API: &handlers.APIDeps{
			Store:   st,
			Sidecar: sidecar,
			Jobs:    runner,
			Config:  apiConfig(cfg),
			Logger:  logger,
		},
		Bootstrap: &handlers.APIDeps{Store: st, BootstrapAdminToken: cfg.Auth.BootstrapAdminToken, Logger: logger},
		Web:       &web.Deps{Store: st, Logger: logger},
		Ready:     buildReadyChecker(cfg, st.DB, rawSidecar, version),
		Registry:  registry,
		Emby:      embyDeps,
	}

	srv := httpserver.New(cfg, deps)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting echo server", "bind", cfg.Server.Bind)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down echo server")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// buildEmbyDeps constructs the Emby reverse-proxy Deps. When no enabled Emby server is
// configured (the fresh-DB case), it returns a fully fail-closed Deps so every /emby/*
// answers a controlled 503 — it deliberately does NOT mount a transparent upstream fallback,
// because without a configured server there is no upstream to safely proxy to. When a single
// enabled server exists, it wires the full proxy: tokenized stream/error routes, the
// mapping-aware PlaybackInfo rewriter, and the transparent upstream fallback for everything
// else. playbackQuota is reused (never rebuilt) so all playback paths share one lease registry.
func buildEmbyDeps(ctx context.Context, st *store.Store, sidecar sidecarclient.Sidecar, playbackQuota *playback.Quota, m *metrics.Metrics, embyCfg config.EmbyProxyConfig, logger *slog.Logger) (*embyproxy.Deps, error) {
	embyServer, err := st.Queries.GetEnabledEmbyServer(ctx, queries.GetEnabledEmbyServerParams{ID: "default"})
	if errors.Is(err, sql.ErrNoRows) {
		embyDisabled := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Echo-Reason", "temporary_unavailable")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "temporary_unavailable"})
		})
		return &embyproxy.Deps{
			ProxyPrefix:  "/emby",
			Stream:       embyDisabled,
			Error:        embyDisabled,
			PlaybackInfo: embyproxy.PlaybackInfoFailClosedHandler(),
			Upstream:     embyDisabled,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load emby server: %w", err)
	}

	upstreamBase, err := url.Parse(embyServer.BaseUrl)
	if err != nil {
		return nil, fmt.Errorf("parse emby base_url: %w", err)
	}
	publicBase, err := url.Parse(embyServer.PublicBaseUrl)
	if err != nil {
		return nil, fmt.Errorf("parse emby public_base_url: %w", err)
	}
	prefix := embyServer.ProxyPrefix
	if prefix == "" {
		prefix = "/emby"
	}

	mgr := embyproxy.NewSessionManager(st.Queries, embyproxy.SessionConfig{TTL: embyCfg.Playback.SessionTTL.Duration}, nil)
	embyResolver := playback.NewResolver(st.Queries, nil)
	embyFailures := playback.NewFailureRecorder(st.Queries, nil)
	mapper := embyproxy.NewDBSourceMapper(st.Queries, nil)
	rewriter := embyproxy.NewRewriter(mapper, mgr, playbackQuota, embyResolver)
	// RedactMappedPath defaults to true in NewRewriter; honor the operator's config toggle.
	rewriter.RedactMappedPath = embyCfg.Playback.RedactMappedPath

	playbackInfo := embyproxy.PlaybackInfoHandler(embyproxy.PlaybackInfoConfig{
		PublicBaseURL: embyServer.PublicBaseUrl,
		ProxyPrefix:   prefix,
		EmbyServerID:  embyServer.ID,
		UpstreamBase:  upstreamBase,
		Querier:       st.Queries,
		Metrics:       m,
	}, rewriter, http.DefaultTransport, logger)

	// guardLookup upgrades the transparent fallback's playback guard to the mapping-aware
	// Phase-4 posture: a suspicious stream/download request whose Emby item is (or was)
	// Echo-managed must be played via an Echo stream token, never proxied untokenized to
	// upstream. We fail closed when we cannot identify the item or cannot read the mapping
	// table; only a request for a genuinely unmapped item is allowed through.
	guardLookup := func(r *http.Request) (embyproxy.GuardDecision, error) {
		itemID := embyItemIDFromPath(r.URL.Path, prefix)
		if itemID == "" {
			// A suspicious playback endpoint whose item we cannot identify must not bypass
			// Echo: conservatively treat it as mapped (fail closed).
			return embyproxy.GuardDecision{Mapped: true, Reason: "unidentified_playback_target"}, nil
		}
		rows, err := st.Queries.ListItemMappingsByItem(r.Context(), queries.ListItemMappingsByItemParams{
			EmbyServerID: embyServer.ID,
			EmbyItemID:   itemID,
		})
		if err != nil {
			return embyproxy.GuardDecision{}, err // fail closed
		}
		if len(rows) > 0 {
			return embyproxy.GuardDecision{Mapped: true, HistoricalEvidence: true, Reason: "mapped_source_requires_echo_stream"}, nil
		}
		return embyproxy.GuardDecision{Mapped: false}, nil
	}

	return &embyproxy.Deps{
		ProxyPrefix:  prefix,
		Stream:       embyproxy.StreamHandler(mgr, embyResolver, playbackQuota, sidecar, embyFailures, m, logger),
		Error:        embyproxy.ErrorHandler(mgr),
		PlaybackInfo: playbackInfo,
		Upstream: embyproxy.NewReverseProxy(embyproxy.ProxyConfig{
			UpstreamBase: upstreamBase,
			PublicBase:   publicBase,
			ProxyPrefix:  prefix,
			GuardLookup:  guardLookup,
		}, http.DefaultClient, logger),
	}, nil
}

// embyItemIDFromPath pulls the Emby item id out of a playback path like
// /emby/Videos/{id}/stream, /emby/Items/{id}/Download, or /emby/Audio/{id}/stream.
func embyItemIDFromPath(path, prefix string) string {
	p := strings.TrimPrefix(path, strings.TrimRight(prefix, "/"))
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i := 0; i+1 < len(segs); i++ {
		switch strings.ToLower(segs[i]) {
		case "videos", "items", "audio":
			return segs[i+1]
		}
	}
	return ""
}

// ingestConfig adapts the loaded config to the ingest pipeline's config.
func ingestConfig(cfg *config.Config) ingest.Config {
	return ingest.Config{
		WorkerPerJob: cfg.Jobs.WorkerPerJob,
		Producer:     producerConfig(cfg.Producer),
	}
}

func producerConfig(p config.ProducerConfig) ingest.ProducerConfig {
	tools := make(map[string]ingest.ProducerToolConfig, len(p.Tools))
	for name, tool := range p.Tools {
		tools[name] = ingest.ProducerToolConfig{
			Binary:           tool.Binary,
			APIArgsAllowlist: tool.APIArgsAllowlist,
		}
	}
	return ingest.ProducerConfig{
		WorkdirRoot:    p.WorkdirRoot,
		SecretsRoot:    p.SecretsRoot,
		DefaultTimeout: p.DefaultTimeout.Duration,
		Tools:          tools,
	}
}

// apiConfig adapts the loaded config to the admin API handlers' config subset.
func apiConfig(cfg *config.Config) handlers.APIConfig {
	return handlers.APIConfig{
		ManualImportRoots:   cfg.ManualImportRoots,
		ProducerWorkdirRoot: cfg.Producer.WorkdirRoot,
		EchoOutputBasePath:  cfg.EchoOutputDefaults.BasePath,
		Producer:            producerConfig(cfg.Producer),
	}
}

// storeStateSource adapts the store to the metrics gauge collector's read side
// (echo_account_status / echo_hash_conflicts_open), sampled at scrape time.
type storeStateSource struct {
	store *store.Store
}

func (s *storeStateSource) AccountStatuses(ctx context.Context) ([]metrics.AccountStatus, error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]metrics.AccountStatus, len(accounts))
	for i, a := range accounts {
		out[i] = metrics.AccountStatus{Provider: a.Provider, AccountID: a.ID, Status: a.Status}
	}
	return out, nil
}

func (s *storeStateSource) OpenHashConflicts(ctx context.Context) (int64, error) {
	return s.store.CountOpenHashConflicts(ctx)
}

func newLogger(cfg config.LogConfig) (*slog.Logger, error) {
	level, err := parseLogLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: level}
	var base slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "", "json":
		base = slog.NewJSONHandler(os.Stdout, opts)
	case "text":
		base = slog.NewTextHandler(os.Stdout, opts)
	default:
		return nil, fmt.Errorf("log.format must be json or text, got %q", cfg.Format)
	}
	// Wrap with the redactor so secrets never reach stdout (spec §8 backstop).
	return slog.New(logging.NewRedactHandler(base)), nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("log.level must be debug, info, warn, or error, got %q", value)
	}
}

// readyProbeTimeout bounds each v0.2 readiness probe (sidecar contract, Emby
// connectivity) so /readyz cannot hang on a slow upstream.
const readyProbeTimeout = 3 * time.Second

// buildReadyChecker selects the /readyz mode. With the Emby proxy enabled it uses the
// v0.2 config-driven checker (sidecar-contract + Emby probes, soft/hard rules from
// config). Otherwise it keeps the v0.1 legacy checker (DB + sidecar ping/version, all
// hard) so single-plane deployments retain their original /readyz semantics.
func buildReadyChecker(cfg *config.Config, db httpserver.Pinger, sidecar httpserver.SidecarHealth, version string) *httpserver.ReadyChecker {
	if !cfg.EmbyProxy.Enabled {
		return &httpserver.ReadyChecker{DB: db, Sidecar: sidecar, Version: version}
	}
	rc := httpserver.NewReadyChecker(httpserver.ReadyDeps{
		DB:              db,
		SidecarContract: sidecarContractProbe{sidecar: sidecar},
		Emby:            embyProbe{base: cfg.EmbyProxy.Upstream.BaseURL, client: &http.Client{Timeout: readyProbeTimeout}},
		Config: httpserver.ReadyConfig{
			RequireSidecarContract:     cfg.Readiness.RequireSidecarContract,
			RequireSidecarConnectivity: cfg.Readiness.RequireSidecarConnectivity,
			RequireEmbyConnectivity:    cfg.Readiness.RequireEmbyConnectivity,
			MappedOnlyPlayback:         cfg.EmbyProxy.Playback.MappedOnly,
		},
	})
	rc.Version = version
	return rc
}

// sidecarContractProbe adapts the sidecar health surface (Ping + Version) to the v0.2
// readiness Probe: unreachable when Ping fails, incompatible on a version-gate mismatch,
// unknown on any other Version error, ok otherwise.
type sidecarContractProbe struct{ sidecar httpserver.SidecarHealth }

func (p sidecarContractProbe) Check(ctx context.Context) httpserver.ProbeResult {
	if err := p.sidecar.Ping(ctx); err != nil {
		return httpserver.ProbeResult{Status: "unreachable", Detail: err.Error()}
	}
	if _, err := p.sidecar.Version(ctx); err != nil {
		if errors.Is(err, sidecarclient.ErrSidecarVersionTooOld) {
			return httpserver.ProbeResult{Status: "incompatible", Detail: err.Error()}
		}
		return httpserver.ProbeResult{Status: "unknown", Detail: err.Error()}
	}
	return httpserver.ProbeResult{Status: "ok"}
}

// embyProbe checks upstream Emby reachability via its unauthenticated public-info
// endpoint. Any HTTP response (including 401/404) proves the server is up, so only a
// transport error or a 5xx counts as unreachable.
type embyProbe struct {
	base   string
	client *http.Client
}

func (p embyProbe) Check(ctx context.Context) httpserver.ProbeResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.base, "/")+"/System/Info/Public", nil)
	if err != nil {
		return httpserver.ProbeResult{Status: "unreachable", Detail: err.Error()}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return httpserver.ProbeResult{Status: "unreachable", Detail: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return httpserver.ProbeResult{Status: "unreachable", Detail: fmt.Sprintf("upstream status %d", resp.StatusCode)}
	}
	return httpserver.ProbeResult{Status: "ok"}
}
