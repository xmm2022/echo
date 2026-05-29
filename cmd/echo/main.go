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

	"github.com/xmm2022/echo/internal/config"
	httpserver "github.com/xmm2022/echo/internal/http"
	"github.com/xmm2022/echo/internal/http/handlers"
	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/restore"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	"github.com/xmm2022/echo/internal/web"
)

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

	sidecar := sidecarclient.New(sidecarclient.FromEndpointConfig(cfg.Sidecar.Default))

	// Job runner: drives ingest jobs (manual + producer).
	runner, err := job.New(job.Config{
		Store: st,
		Handlers: job.IngestHandlers(ingest.Deps{
			Store:   st,
			Sidecar: sidecar,
			Config:  ingestConfig(cfg),
		}),
		MaxConcurrent: cfg.Jobs.MaxConcurrent,
		Logger:        logger,
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

	deps := httpserver.Deps{
		Logger:     logger,
		AdminToken: cfg.Auth.AdminToken,
		Restore:    &handlers.RestoreDeps{Resolver: resolver, Sidecar: sidecar, Cache: cache, Logger: logger},
		Stream:     &handlers.StreamDeps{Resolver: resolver, Sidecar: sidecar, Logger: logger},
		API: &handlers.APIDeps{
			Store:   st,
			Sidecar: sidecar,
			Jobs:    runner,
			Config:  apiConfig(cfg),
			Logger:  logger,
		},
		Web: &web.Deps{Store: st, Logger: logger},
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

func newLogger(cfg config.LogConfig) (*slog.Logger, error) {
	level, err := parseLogLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: level}
	switch strings.ToLower(cfg.Format) {
	case "", "json":
		return slog.New(slog.NewJSONHandler(os.Stdout, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stdout, opts)), nil
	default:
		return nil, fmt.Errorf("log.format must be json or text, got %q", cfg.Format)
	}
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
