package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/xmm2022/echo/internal/config"
	"github.com/xmm2022/echo/internal/discovery"
	"github.com/xmm2022/echo/internal/discovery/sources/telegram"
	"github.com/xmm2022/echo/internal/discovery/tmdb"
	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/metrics"
	"github.com/xmm2022/echo/internal/secrets"
	"github.com/xmm2022/echo/internal/sidecarclient"
	"github.com/xmm2022/echo/internal/store"
	storeq "github.com/xmm2022/echo/internal/store/queries"
)

func TestDiscoveryRealTelegram115Gate(t *testing.T) {
	if os.Getenv("ECHO_REAL_DISCOVERY_GATE") != "1" {
		t.Skip("set ECHO_REAL_DISCOVERY_GATE=1 to run real discovery gate")
	}
	required := []string{
		"ECHO_DISCOVERY_SECRETS_ROOT",
		"ECHO_DISCOVERY_TG_API_ID",
		"ECHO_DISCOVERY_TG_API_HASH_REF",
		"ECHO_DISCOVERY_TG_SESSION_REF",
		"ECHO_DISCOVERY_TG_CHANNEL",
		"ECHO_DISCOVERY_TMDB_ID",
		"ECHO_DISCOVERY_TMDB_API_KEY_REF",
		"ECHO_DISCOVERY_115_ACCOUNT_ID",
		"ECHO_DISCOVERY_115_SIDECAR_ID",
		"ECHO_DISCOVERY_115_STORAGE_MOUNT",
		"ECHO_DISCOVERY_SIDECAR_BASE_URL",
		"ECHO_DISCOVERY_SIDECAR_TOKEN_ENV",
		"ECHO_DISCOVERY_LIBRARY_OUTPUT_PATH",
		"ECHO_DISCOVERY_115SHARE2CAS_BIN",
		"ECHO_DISCOVERY_115_COOKIE_REF",
		"ECHO_DISCOVERY_115_RECYCLE_REF",
	}
	for _, name := range required {
		if os.Getenv(name) == "" {
			t.Skipf("%s is required", name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	gate := NewRealDiscoveryGateFromEnv(t)
	result, err := gate.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidates != 1 {
		t.Fatalf("candidates = %d, want 1", result.Candidates)
	}
	if result.ProducerJobs != 1 {
		t.Fatalf("producer jobs = %d, want 1", result.ProducerJobs)
	}
	if result.EchoFilesWritten == 0 {
		t.Fatal("expected at least one .echo file")
	}
}

type RealDiscoveryGate struct {
	SecretsRoot       string
	TGAPIID           int
	TGAPIHashRef      string
	SessionRef        string
	Channel           string
	TMDBID            string
	TMDBAPIKeyRef     string
	AccountID         string
	SidecarID         string
	StorageMount      string
	SidecarBaseURL    string
	SidecarTokenEnv   string
	LibraryOutputPath string
	ProducerBinary    string
	CookieRef         string
	RecycleRef        string
	HistoryLimit      int
}

type RealDiscoveryResult struct {
	Candidates       int
	ProducerJobs     int
	EchoFilesWritten int
}

func NewRealDiscoveryGateFromEnv(t *testing.T) RealDiscoveryGate {
	t.Helper()
	apiID, err := strconv.Atoi(os.Getenv("ECHO_DISCOVERY_TG_API_ID"))
	if err != nil {
		t.Fatalf("ECHO_DISCOVERY_TG_API_ID: %v", err)
	}
	historyLimit := 50
	if raw := os.Getenv("ECHO_DISCOVERY_HISTORY_LIMIT"); raw != "" {
		historyLimit, err = strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("ECHO_DISCOVERY_HISTORY_LIMIT: %v", err)
		}
	}
	return RealDiscoveryGate{
		SecretsRoot:       os.Getenv("ECHO_DISCOVERY_SECRETS_ROOT"),
		TGAPIID:           apiID,
		TGAPIHashRef:      os.Getenv("ECHO_DISCOVERY_TG_API_HASH_REF"),
		SessionRef:        os.Getenv("ECHO_DISCOVERY_TG_SESSION_REF"),
		Channel:           os.Getenv("ECHO_DISCOVERY_TG_CHANNEL"),
		TMDBID:            os.Getenv("ECHO_DISCOVERY_TMDB_ID"),
		TMDBAPIKeyRef:     os.Getenv("ECHO_DISCOVERY_TMDB_API_KEY_REF"),
		AccountID:         os.Getenv("ECHO_DISCOVERY_115_ACCOUNT_ID"),
		SidecarID:         os.Getenv("ECHO_DISCOVERY_115_SIDECAR_ID"),
		StorageMount:      os.Getenv("ECHO_DISCOVERY_115_STORAGE_MOUNT"),
		SidecarBaseURL:    os.Getenv("ECHO_DISCOVERY_SIDECAR_BASE_URL"),
		SidecarTokenEnv:   os.Getenv("ECHO_DISCOVERY_SIDECAR_TOKEN_ENV"),
		LibraryOutputPath: os.Getenv("ECHO_DISCOVERY_LIBRARY_OUTPUT_PATH"),
		ProducerBinary:    os.Getenv("ECHO_DISCOVERY_115SHARE2CAS_BIN"),
		CookieRef:         os.Getenv("ECHO_DISCOVERY_115_COOKIE_REF"),
		RecycleRef:        os.Getenv("ECHO_DISCOVERY_115_RECYCLE_REF"),
		HistoryLimit:      historyLimit,
	}
}

func (g RealDiscoveryGate) Run(ctx context.Context) (RealDiscoveryResult, error) {
	echoFilesBefore := countEchoFiles(g.LibraryOutputPath)
	tempRoot, err := os.MkdirTemp("", "echo-real-discovery-")
	if err != nil {
		return RealDiscoveryResult{}, err
	}
	defer os.RemoveAll(tempRoot)

	dbPath := filepath.Join(tempRoot, "echo.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return RealDiscoveryResult{}, err
	}
	defer st.Close()
	ds := discovery.NewStore(st)
	now := time.Now().Unix()
	if _, err := st.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO accounts (
  id, provider, sidecar_id, storage_mount, status, owner_id, created_at, updated_at
) VALUES (?, '115', ?, ?, 'active', 'admin', ?, ?)`,
		g.AccountID, g.SidecarID, g.StorageMount, now, now); err != nil {
		return RealDiscoveryResult{}, err
	}
	var libraryID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO libraries (name, echo_output_kind, echo_output_path, owner_id, created_at)
VALUES ('real discovery gate', 'local', ?, 'admin', ?)
RETURNING id`, g.LibraryOutputPath, now).Scan(&libraryID); err != nil {
		return RealDiscoveryResult{}, err
	}
	var producerProfileID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_producer_profiles (
  name, provider, tool, target_account, target_subdir_template,
  library_rel_path_template, default_args_json, enabled, created_at, updated_at
) VALUES (
  'real 115', '115', '115share2cas', ?, '{{.Title}}', '{{.Title}}',
  json_object('cookie_file', ?, 'recycle_password_file', ?), 1, ?, ?
) RETURNING id`, g.AccountID, g.CookieRef, g.RecycleRef, now, now).Scan(&producerProfileID); err != nil {
		return RealDiscoveryResult{}, err
	}
	var ruleProfileID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO rule_profiles (name, version, rules_json, enabled, created_at, updated_at)
VALUES ('real gate default', 1, '{"weights":["resolutions","colors","audios","extensions"]}', 1, ?, ?)
RETURNING id`, now, now).Scan(&ruleProfileID); err != nil {
		return RealDiscoveryResult{}, err
	}
	sourceID, subscriptionID, err := seedRealDiscoveryRows(ctx, st, RealSeedRows{
		SessionRef:        g.SessionRef,
		Channel:           g.Channel,
		TMDBID:            g.TMDBID,
		LibraryID:         libraryID,
		ProducerProfileID: producerProfileID,
		RuleProfileID:     ruleProfileID,
		HistoryLimit:      g.HistoryLimit,
		Now:               now,
	})
	if err != nil {
		return RealDiscoveryResult{}, err
	}
	tmdbKey, err := resolveGateSecret(g.SecretsRoot, g.TMDBAPIKeyRef)
	if err != nil {
		return RealDiscoveryResult{}, err
	}
	tgAPIHash, err := resolveGateSecret(g.SecretsRoot, g.TGAPIHashRef)
	if err != nil {
		return RealDiscoveryResult{}, err
	}
	producerConfig := realGateProducerConfig(g, tempRoot)
	orch := discovery.NewOrchestrator(discovery.Deps{
		Store: ds,
		SourceAdapters: map[discovery.SourceKind]discovery.SourceAdapter{
			discovery.SourceTelegramMTProto: telegram.NewAdapter(
				telegram.NewMTProtoClient(g.SecretsRoot, g.TGAPIID, tgAPIHash),
			),
		},
		Enqueue:        realGateEnqueuer(ctx, st, g),
		ProducerConfig: producerConfig,
	})
	if err := orch.RunSourceCrawl(ctx, discovery.SourceCrawlPayload{SourceID: sourceID}); err != nil {
		return RealDiscoveryResult{}, err
	}
	media, err := tmdb.NewClient(tmdb.Config{APIKey: tmdbKey, Language: "zh-CN"}, tmdb.NewSQLiteCache(st.Queries, "zh-CN")).MovieDetails(ctx, g.TMDBID)
	if err != nil {
		return RealDiscoveryResult{}, err
	}
	if err := tagRealGateCandidates(ctx, st, media); err != nil {
		return RealDiscoveryResult{}, err
	}
	if err := orch.RunSubscriptionCheck(ctx, discovery.SubscriptionCheckPayload{SubscriptionID: subscriptionID}); err != nil {
		return RealDiscoveryResult{}, err
	}
	matchID, err := acceptBestRealGateMatch(ctx, st, ds, orch, subscriptionID, 2)
	if err != nil {
		return RealDiscoveryResult{}, err
	}
	if err := runQueuedProducerJob(ctx, st, g, producerConfig); err != nil {
		return RealDiscoveryResult{}, err
	}
	if err := orch.RunReconcile(ctx, discovery.ReconcilePayload{MatchID: matchID, JobID: latestProducerJobID(ctx, st)}); err != nil {
		return RealDiscoveryResult{}, err
	}
	return RealDiscoveryResult{
		Candidates:       countRealGateCandidates(ctx, st),
		ProducerJobs:     countRealGateProducerJobs(ctx, st),
		EchoFilesWritten: echoFilesWrittenSince(g.LibraryOutputPath, echoFilesBefore),
	}, nil
}

type RealSeedRows struct {
	SessionRef        string
	Channel           string
	TMDBID            string
	LibraryID         int64
	ProducerProfileID int64
	RuleProfileID     int64
	HistoryLimit      int
	Now               int64
}

func seedRealDiscoveryRows(ctx context.Context, st *store.Store, rows RealSeedRows) (sourceID int64, subscriptionID int64, err error) {
	configJSON, err := json.Marshal(map[string]any{
		"channel":       rows.Channel,
		"history_limit": rows.HistoryLimit,
		"channels": []map[string]any{{
			"ref":         rows.Channel,
			"session_ref": rows.SessionRef,
		}},
	})
	if err != nil {
		return 0, 0, err
	}
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_sources (
  kind, name, enabled, config_json, secret_ref, scheduler_state, next_run_at, created_at, updated_at
) VALUES ('telegram_mtproto', 'real discovery gate', 1, ?, ?, 'healthy', ?, ?, ?)
RETURNING id`, string(configJSON), rows.SessionRef, rows.Now, rows.Now, rows.Now).Scan(&sourceID); err != nil {
		return 0, 0, err
	}
	if _, err := st.DB.ExecContext(ctx, `
INSERT INTO telegram_channels (
  source_id, channel_ref, enabled, next_run_at, created_at, updated_at
) VALUES (?, ?, 1, ?, ?, ?)`, sourceID, rows.Channel, rows.Now, rows.Now, rows.Now); err != nil {
		return 0, 0, err
	}
	if _, err := st.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO tmdb_media (
  tmdb_id, media_type, language, title, raw_json, fetched_at, next_refresh_at
) VALUES (?, 'movie', 'zh-CN', ?, '{}', ?, 0)`, rows.TMDBID, rows.TMDBID, rows.Now); err != nil {
		return 0, 0, err
	}
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_subscriptions (
  owner_id, tmdb_id, media_type, tmdb_language, title_snapshot, library_id,
  producer_profile_id, rule_profile_id, status, next_check_at, created_at, updated_at
) VALUES ('admin', ?, 'movie', 'zh-CN', ?, ?, ?, ?, 'active', ?, ?, ?)
RETURNING id`, rows.TMDBID, rows.TMDBID, rows.LibraryID, rows.ProducerProfileID, rows.RuleProfileID, rows.Now, rows.Now, rows.Now).Scan(&subscriptionID); err != nil {
		return 0, 0, err
	}
	return sourceID, subscriptionID, nil
}

func resolveGateSecret(root, ref string) (string, error) {
	if strings.HasPrefix(ref, "env:") {
		value, err := secrets.ResolveEnv(ref)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("secret env ref %q is empty", ref)
		}
		return value, nil
	}
	path, err := secrets.Resolve(root, ref)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(body))
	if value == "" {
		return "", fmt.Errorf("secret file ref %q is empty", ref)
	}
	return value, nil
}

func realGateProducerConfig(g RealDiscoveryGate, tempRoot string) ingest.ProducerConfig {
	workdirRoot := filepath.Join(tempRoot, "producer")
	_ = os.MkdirAll(workdirRoot, 0o700)
	return ingest.ProducerConfig{
		WorkdirRoot:    workdirRoot,
		SecretsRoot:    g.SecretsRoot,
		DefaultTimeout: 30 * time.Minute,
		Tools: map[string]ingest.ProducerToolConfig{
			"115share2cas": {
				Binary: g.ProducerBinary,
				APIArgsAllowlist: []string{
					"share_url", "share_code", "receive_code",
					"cookie_file", "recycle_password_file", "mode",
				},
			},
		},
	}
}

func realGateEnqueuer(ctx context.Context, st *store.Store, g RealDiscoveryGate) func(context.Context, string, any) (int64, error) {
	return func(callCtx context.Context, kind string, payload any) (int64, error) {
		body, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		if len(body) == 0 || string(body) == "null" {
			body = []byte("{}")
		}
		row, err := st.CreateJob(callCtx, storeq.CreateJobParams{
			Kind:      kind,
			Status:    "pending",
			Payload:   string(body),
			OwnerID:   "discovery",
			CreatedAt: time.Now().Unix(),
		})
		if err != nil {
			return 0, err
		}
		return row.ID, nil
	}
}

func bestRealGateMatch(ctx context.Context, st *store.Store, subscriptionID int64) (int64, error) {
	var matchID int64
	if err := st.DB.QueryRowContext(ctx, `
SELECT id
FROM subscription_matches
WHERE subscription_id = ?
ORDER BY updated_at DESC, id DESC
LIMIT 1`, subscriptionID).Scan(&matchID); err != nil {
		return 0, err
	}
	return matchID, nil
}

func acceptBestRealGateMatch(ctx context.Context, st *store.Store, ds *discovery.Store, orch *discovery.Orchestrator, subscriptionID int64, attempts int) (int64, error) {
	matchID, err := bestRealGateMatch(ctx, st, subscriptionID)
	if err != nil {
		return 0, err
	}
	if err := acceptAndDispatchRealGateMatchConcurrently(ctx, ds, orch, matchID, attempts); err != nil {
		return 0, err
	}
	return matchID, nil
}

func acceptAndDispatchRealGateMatchConcurrently(ctx context.Context, ds *discovery.Store, orch *discovery.Orchestrator, matchID int64, n int) error {
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ds.AdminAcceptMatch(ctx, matchID, time.Now()); err != nil &&
				!errors.Is(err, discovery.ErrInvalidAdminMatchTransition) {
				errs <- fmt.Errorf("admin accept: %w", err)
				return
			}
			if err := orch.RunDispatch(ctx, discovery.DispatchPayload{MatchID: matchID}); err != nil {
				errs <- fmt.Errorf("dispatch: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	return nil
}

func runQueuedProducerJob(ctx context.Context, st *store.Store, g RealDiscoveryGate, producerConfig ingest.ProducerConfig) error {
	jobID := latestProducerJobID(ctx, st)
	if jobID == 0 {
		return errors.New("no queued producer job")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	registry := prometheus.NewRegistry()
	m := metrics.New(registry)
	sidecar := sidecarclient.New(sidecarclient.Config{
		BaseURL:        g.SidecarBaseURL,
		AuthTokenEnv:   g.SidecarTokenEnv,
		RequestTimeout: config.Duration{Duration: 30 * time.Minute},
		StreamTimeout:  config.Duration{Duration: 30 * time.Minute},
	})
	runner, err := job.New(job.Config{
		Store: st,
		Handlers: job.IngestHandlers(ingest.Deps{
			Store:   st,
			Sidecar: sidecar,
			Config: ingest.Config{
				WorkerPerJob: 2,
				Producer:     producerConfig,
			},
			Metrics: m,
			Logger:  logger,
		}),
		MaxConcurrent: 1,
		Logger:        logger,
		Metrics:       m,
	})
	if err != nil {
		return err
	}
	runnerCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		runner.Stop()
	}()
	if err := runner.Start(runnerCtx); err != nil {
		return err
	}
	runner.EnqueueExisting(jobID)
	return waitRealGateJobDone(ctx, st, jobID)
}

func latestProducerJobID(ctx context.Context, st *store.Store) int64 {
	var id int64
	_ = st.DB.QueryRowContext(ctx, `
SELECT COALESCE(MAX(id), 0)
FROM jobs
WHERE kind = ?`, job.KindIngestProducer).Scan(&id)
	return id
}

func countRealGateCandidates(ctx context.Context, st *store.Store) int {
	var n int
	_ = st.DB.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM discovered_resources
WHERE provider = '115' AND link_kind = '115_share' AND status IN ('candidate','review','accepted','queued','imported')`).Scan(&n)
	return n
}

func countRealGateProducerJobs(ctx context.Context, st *store.Store) int {
	var n int
	_ = st.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM jobs WHERE kind = ?`, job.KindIngestProducer).Scan(&n)
	return n
}

func countEchoFiles(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".echo" {
			count++
		}
		return nil
	})
	return count
}

func echoFilesWrittenSince(root string, before int) int {
	written := countEchoFiles(root) - before
	if written < 0 {
		return 0
	}
	return written
}

func TestEchoFilesWrittenSinceIgnoresPreexistingEchoFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "preexisting.echo"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := countEchoFiles(root)
	if err := os.WriteFile(filepath.Join(root, "new.echo"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-echo.txt"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := echoFilesWrittenSince(root, before); got != 1 {
		t.Fatalf("new echo files = %d, want 1", got)
	}
}

func tagRealGateCandidates(ctx context.Context, st *store.Store, media tmdb.Media) error {
	rows, err := st.DB.QueryContext(ctx, `
SELECT id, COALESCE(title, '')
FROM discovered_resources
WHERE provider = '115' AND link_kind = '115_share' AND tmdb_id IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return err
		}
		matched, ok := tmdb.MatchTitleYear([]tmdb.Media{media}, title, firstYear(title), "movie")
		if !ok && titleContainsMediaTitle(title, media) {
			matched, ok = media, true
		}
		if ok {
			if _, err := st.DB.ExecContext(ctx, `
UPDATE discovered_resources
SET tmdb_id = ?, media_type = ?
WHERE id = ?`, matched.TMDBID, matched.MediaType, id); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func waitRealGateJobDone(ctx context.Context, st *store.Store, jobID int64) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		row, err := st.GetJob(ctx, storeq.GetJobParams{ID: jobID})
		if err != nil {
			return err
		}
		switch row.Status {
		case "done":
			return nil
		case "failed":
			if row.Error.Valid {
				return fmt.Errorf("producer job failed: %s", row.Error.String)
			}
			return errors.New("producer job failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

var yearRE = regexp.MustCompile(`(?:19|20)\d{2}`)

func firstYear(value string) int {
	raw := yearRE.FindString(value)
	if raw == "" {
		return 0
	}
	year, _ := strconv.Atoi(raw)
	return year
}

func titleContainsMediaTitle(title string, media tmdb.Media) bool {
	normalizedTitle := normalizeRealGateTitle(title)
	for _, candidate := range []string{media.Title, media.OriginalTitle, media.TMDBID} {
		normalizedCandidate := normalizeRealGateTitle(candidate)
		if normalizedCandidate != "" && strings.Contains(normalizedTitle, normalizedCandidate) {
			return true
		}
	}
	return false
}

func normalizeRealGateTitle(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	space := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			if space && out.Len() > 0 {
				out.WriteByte(' ')
			}
			out.WriteRune(r)
			space = false
		case r == ' ' || r == '.' || r == '-' || r == '_' || r == ':' || r == '/' || r == '[' || r == ']':
			space = out.Len() > 0
		}
	}
	return out.String()
}
