package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/xmm2022/echo/internal/auth"
	"github.com/xmm2022/echo/internal/config"
	httpserver "github.com/xmm2022/echo/internal/http"
	"github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/logging"
	"github.com/xmm2022/echo/internal/metrics"
	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/web"
)

// version is the Echo build version, surfaced via echo_build_info and /readyz.
// Override at build time with -ldflags "-X main.version=<tag>".
var version = "v0.1.0-dev"

const shutdownTimeout = 30 * time.Second

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

	if err := runner.Start(ctx); err != nil {
		return fmt.Errorf("start job runner: %w", err)
	}
	defer runner.Stop()

	// Restore/stream plane.
	resolver := restore.NewResolver(st.Queries, nil)
	cache := restore.NewLinkCache(nil)
	authenticator := &auth.Authenticator{Store: st}

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
		Ready: &httpserver.ReadyChecker{
			DB:      st.DB,
			Sidecar: rawSidecar,
			Version: version,
		},
		Registry: registry,
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
