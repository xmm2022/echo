package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmm2022/echo/internal/discovery"
	"github.com/xmm2022/echo/internal/ingest"
	"github.com/xmm2022/echo/internal/job"
	"github.com/xmm2022/echo/internal/store"
	storeq "github.com/xmm2022/echo/internal/store/queries"
)

func TestDiscoveryFakeFullPathQueuesOneProducerJob(t *testing.T) {
	h := newDiscoveryHarness(t)
	h.SeedTMDB("123", "movie", "Known Movie")
	h.SeedSubscription("123", "movie")
	h.SeedTelegramMessage("channel", 10, "Known Movie 2024 2160p https://115.com/s/abc?password=pass")
	if err := h.RunSourceCrawl("channel"); err != nil {
		t.Fatal(err)
	}
	if err := h.RunSubscriptionCheck("123", "movie"); err != nil {
		t.Fatal(err)
	}
	matchID := h.RequireOneReviewMatch(t)
	h.AcceptMatchConcurrently(t, matchID, 2)
	if got := h.CountProducerJobs(t); got != 1 {
		t.Fatalf("producer jobs = %d, want 1", got)
	}
	h.RequireProducerPayloadRedacted(t)
}

type discoveryHarness struct {
	t      *testing.T
	store  *store.Store
	runner *job.Runner

	mu             sync.Mutex
	sourceID       int64
	subscriptionID int64
	now            time.Time
	tmdb           map[string]fakeDiscoveryMedia
	messages       map[string][]fakeTelegramMessage
	producerConfig ingest.ProducerConfig
}

type fakeDiscoveryMedia struct {
	TMDBID    string
	MediaType string
	Title     string
}

type fakeTelegramMessage struct {
	ID   int64
	Text string
}

func newDiscoveryHarness(t *testing.T) *discoveryHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "echo.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	h := &discoveryHarness{
		t:        t,
		store:    st,
		now:      time.Unix(100, 0).UTC(),
		tmdb:     make(map[string]fakeDiscoveryMedia),
		messages: make(map[string][]fakeTelegramMessage),
		producerConfig: ingest.ProducerConfig{
			WorkdirRoot:    filepath.Join(t.TempDir(), "producer"),
			SecretsRoot:    filepath.Join(t.TempDir(), "secrets"),
			DefaultTimeout: time.Second,
			Tools: map[string]ingest.ProducerToolConfig{
				"115share2cas": {
					Binary: "/fake/115share2cas",
					APIArgsAllowlist: []string{
						"share_url", "share_code", "receive_code",
						"cookie_file", "recycle_password_file", "mode",
					},
				},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	runner, err := job.New(job.Config{
		Store: st,
		Handlers: map[string]job.Handler{
			job.KindIngestProducer: func(ctx context.Context, queued storeq.Job) error {
				return nil
			},
		},
		MaxConcurrent: 2,
		Logger:        logger,
		Now:           func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatalf("new job runner: %v", err)
	}
	h.runner = runner
	runnerCtx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(runnerCtx); err != nil {
		cancel()
		t.Fatalf("start job runner: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		runner.Stop()
	})
	return h
}

func (h *discoveryHarness) SeedTMDB(tmdbID, mediaType, title string) {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	media := fakeDiscoveryMedia{TMDBID: tmdbID, MediaType: mediaType, Title: title}
	h.tmdb[mediaKey(tmdbID, mediaType)] = media
	raw, err := json.Marshal(media)
	if err != nil {
		h.t.Fatalf("marshal fake tmdb: %v", err)
	}
	now := h.now.Unix()
	if _, err := h.store.DB.ExecContext(context.Background(), `
INSERT INTO tmdb_media (
  tmdb_id, media_type, language, title, raw_json, fetched_at, next_refresh_at
) VALUES (?, ?, 'zh-CN', ?, ?, ?, ?)
ON CONFLICT(tmdb_id, media_type, language) DO UPDATE SET
  title = excluded.title,
  raw_json = excluded.raw_json,
  fetched_at = excluded.fetched_at,
  next_refresh_at = excluded.next_refresh_at`,
		tmdbID, mediaType, title, string(raw), now, now+86400); err != nil {
		h.t.Fatalf("seed tmdb: %v", err)
	}
}

func (h *discoveryHarness) SeedSubscription(tmdbID, mediaType string) {
	h.t.Helper()
	title := h.tmdbTitle(tmdbID, mediaType)
	now := h.now.Unix()
	ctx := context.Background()
	if _, err := h.store.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO accounts (
  id, provider, sidecar_id, storage_mount, status, owner_id, created_at, updated_at
) VALUES ('acc-115', '115', 'sidecar-1', '/115', 'active', 'admin', ?, ?)`, now, now); err != nil {
		h.t.Fatalf("seed account: %v", err)
	}
	var libraryID int64
	if err := h.store.DB.QueryRowContext(ctx, `
INSERT INTO libraries (name, echo_output_kind, echo_output_path, owner_id, created_at)
VALUES ('fake discovery gate', 'local', ?, 'admin', ?)
RETURNING id`, filepath.Join(h.t.TempDir(), "library"), now).Scan(&libraryID); err != nil {
		h.t.Fatalf("seed library: %v", err)
	}
	var producerProfileID int64
	if err := h.store.DB.QueryRowContext(ctx, `
INSERT INTO discovery_producer_profiles (
  name, provider, tool, target_account, target_subdir_template,
  library_rel_path_template, default_args_json, enabled, created_at, updated_at
) VALUES (
  'fake 115', '115', '115share2cas', 'acc-115', '{{.Title}}', '{{.Title}}',
  json_object('cookie_file', 'ref:fake/cookie.txt', 'recycle_password_file', 'ref:fake/recycle.txt', 'mode', 'transfer-batch'),
  1, ?, ?
) RETURNING id`, now, now).Scan(&producerProfileID); err != nil {
		h.t.Fatalf("seed producer profile: %v", err)
	}
	var ruleProfileID int64
	if err := h.store.DB.QueryRowContext(ctx, `
INSERT INTO rule_profiles (name, version, rules_json, enabled, created_at, updated_at)
VALUES ('fake review default', 1, '{"weights":["colors"]}', 1, ?, ?)
RETURNING id`, now, now).Scan(&ruleProfileID); err != nil {
		h.t.Fatalf("seed rule profile: %v", err)
	}
	var subscriptionID int64
	if err := h.store.DB.QueryRowContext(ctx, `
INSERT INTO discovery_subscriptions (
  owner_id, tmdb_id, media_type, tmdb_language, title_snapshot, library_id,
  producer_profile_id, rule_profile_id, status, next_check_at, created_at, updated_at
) VALUES ('admin', ?, ?, 'zh-CN', ?, ?, ?, ?, 'active', ?, ?, ?)
RETURNING id`, tmdbID, mediaType, title, libraryID, producerProfileID, ruleProfileID, now, now, now).Scan(&subscriptionID); err != nil {
		h.t.Fatalf("seed subscription: %v", err)
	}
	h.subscriptionID = subscriptionID
}

func (h *discoveryHarness) SeedTelegramMessage(channel string, id int64, text string) {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages[channel] = append(h.messages[channel], fakeTelegramMessage{ID: id, Text: text})
}

func (h *discoveryHarness) RunSourceCrawl(channel string) error {
	sourceID, err := h.ensureTelegramSource(channel)
	if err != nil {
		return err
	}
	return h.orchestrator().RunSourceCrawl(context.Background(), discovery.SourceCrawlPayload{SourceID: sourceID})
}

func (h *discoveryHarness) RunSubscriptionCheck(tmdbID, mediaType string) error {
	subscriptionID := h.subscriptionID
	if subscriptionID == 0 {
		if err := h.store.DB.QueryRowContext(context.Background(), `
SELECT id FROM discovery_subscriptions
WHERE tmdb_id = ? AND media_type = ?
ORDER BY id DESC
LIMIT 1`, tmdbID, mediaType).Scan(&subscriptionID); err != nil {
			return err
		}
	}
	return h.orchestrator().RunSubscriptionCheck(context.Background(), discovery.SubscriptionCheckPayload{SubscriptionID: subscriptionID})
}

func (h *discoveryHarness) RequireOneReviewMatch(t *testing.T) int64 {
	t.Helper()
	var id int64
	var count int
	if err := h.store.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*), COALESCE(MAX(id), 0)
FROM subscription_matches
WHERE decision = 'review' AND dispatch_state = 'none'`).Scan(&count, &id); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("review matches = %d, want 1", count)
	}
	return id
}

func (h *discoveryHarness) AcceptMatchConcurrently(t *testing.T, matchID int64, n int) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			if err := discovery.NewStore(h.store).AdminAcceptMatch(ctx, matchID, h.now); err != nil &&
				!errors.Is(err, discovery.ErrInvalidAdminMatchTransition) {
				errs <- fmt.Errorf("admin accept: %w", err)
				return
			}
			if err := h.orchestrator().RunDispatch(ctx, discovery.DispatchPayload{MatchID: matchID}); err != nil {
				errs <- fmt.Errorf("dispatch: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func (h *discoveryHarness) CountProducerJobs(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.store.DB.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM jobs WHERE kind = ?`, job.KindIngestProducer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (h *discoveryHarness) RequireProducerPayloadRedacted(t *testing.T) {
	t.Helper()
	var payload string
	if err := h.store.DB.QueryRowContext(context.Background(), `
SELECT payload FROM jobs WHERE kind = ? ORDER BY id DESC LIMIT 1`, job.KindIngestProducer).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "password=pass") || strings.Contains(payload, "https://115.com/s/abc?password=pass") {
		t.Fatalf("producer payload leaked raw share secret: %s", payload)
	}
	var decoded job.IngestPayload
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode producer payload: %v", err)
	}
	if _, ok := decoded.Args["share_url"]; ok {
		t.Fatalf("producer payload used raw share_url instead of share code args: %#v", decoded.Args)
	}
	if decoded.Args["share_code"] != "abc" || decoded.Args["receive_code"] != "pass" {
		t.Fatalf("producer share args = %#v, want share_code/receive_code", decoded.Args)
	}

	var parsedJSON string
	if err := h.store.DB.QueryRowContext(context.Background(), `
SELECT parsed_json FROM discovered_resources ORDER BY id DESC LIMIT 1`).Scan(&parsedJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(parsedJSON, "pass") || strings.Contains(parsedJSON, "password=pass") {
		t.Fatalf("parsed_json leaked secret: %s", parsedJSON)
	}
}

func (h *discoveryHarness) orchestrator() *discovery.Orchestrator {
	return discovery.NewOrchestrator(discovery.Deps{
		Store: discovery.NewStore(h.store),
		SourceAdapters: map[discovery.SourceKind]discovery.SourceAdapter{
			discovery.SourceTelegramMTProto: fakeDiscoveryTelegramAdapter{h: h},
		},
		NotifyJob:      h.runner.EnqueueExisting,
		ProducerConfig: h.producerConfig,
		Now:            func() time.Time { return h.now },
	})
}

func (h *discoveryHarness) ensureTelegramSource(channel string) (int64, error) {
	if h.sourceID != 0 {
		return h.sourceID, nil
	}
	now := h.now.Unix()
	config, err := json.Marshal(map[string]any{
		"channels": []map[string]any{{
			"ref":         channel,
			"session_ref": "ref:fake.session",
		}},
	})
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	var sourceID int64
	if err := h.store.DB.QueryRowContext(ctx, `
INSERT INTO discovery_sources (
  kind, name, enabled, config_json, secret_ref, scheduler_state, next_run_at, created_at, updated_at
) VALUES ('telegram_mtproto', 'fake telegram', 1, ?, 'ref:fake.session', 'healthy', ?, ?, ?)
RETURNING id`, string(config), now, now, now).Scan(&sourceID); err != nil {
		return 0, err
	}
	if _, err := h.store.DB.ExecContext(ctx, `
INSERT INTO telegram_channels (
  source_id, channel_ref, enabled, next_run_at, created_at, updated_at
) VALUES (?, ?, 1, ?, ?, ?)`, sourceID, channel, now, now, now); err != nil {
		return 0, err
	}
	h.sourceID = sourceID
	return sourceID, nil
}

func (h *discoveryHarness) tmdbTitle(tmdbID, mediaType string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if media, ok := h.tmdb[mediaKey(tmdbID, mediaType)]; ok {
		return media.Title
	}
	return tmdbID
}

func (h *discoveryHarness) matchMedia(text string) fakeDiscoveryMedia {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, media := range h.tmdb {
		if strings.Contains(strings.ToLower(text), strings.ToLower(media.Title)) {
			return media
		}
	}
	return fakeDiscoveryMedia{}
}

type fakeDiscoveryTelegramAdapter struct {
	h *discoveryHarness
}

func (a fakeDiscoveryTelegramAdapter) Crawl(ctx context.Context, source discovery.Source) (discovery.SourceCrawlResult, error) {
	var cfg struct {
		Channels []struct {
			Ref           string `json:"ref"`
			LastMessageID int64  `json:"last_message_id"`
		} `json:"channels"`
	}
	if err := json.Unmarshal([]byte(source.ConfigText()), &cfg); err != nil {
		return discovery.SourceCrawlResult{}, err
	}
	out := discovery.SourceCrawlResult{}
	for _, channel := range cfg.Channels {
		a.h.mu.Lock()
		messages := append([]fakeTelegramMessage(nil), a.h.messages[channel.Ref]...)
		a.h.mu.Unlock()
		lastMessageID := channel.LastMessageID
		for _, message := range messages {
			if message.ID <= channel.LastMessageID {
				continue
			}
			if message.ID > lastMessageID {
				lastMessageID = message.ID
			}
			share, err := parseFake115Share(message.Text)
			if err != nil {
				continue
			}
			media := a.h.matchMedia(message.Text)
			title := titleBeforeShare(message.Text, share.raw)
			parsedJSON, err := json.Marshal(map[string]any{
				"provider":             string(discovery.Provider115),
				"link_kind":            string(discovery.Link115Share),
				"source":               "telegram",
				"telegram_channel_ref": channel.Ref,
				"telegram_message_id":  message.ID,
				"title_redacted":       title,
			})
			if err != nil {
				return discovery.SourceCrawlResult{}, err
			}
			out.Items = append(out.Items, discovery.ParsedResource{
				Provider:         discovery.Provider115,
				LinkKind:         discovery.Link115Share,
				ExternalKey:      fmt.Sprintf("tg:%s:%d:115:0", channel.Ref, message.ID),
				TMDBID:           media.TMDBID,
				MediaType:        media.MediaType,
				Title:            title,
				ShareCode:        share.code,
				ReceiveCode:      share.receiveCode,
				ShareURLRedacted: share.redactedURL,
				RawText:          []byte(message.Text),
				RawTextRedacted:  redactFake115Text(message.Text, share),
				ParsedJSON:       string(parsedJSON),
				FeatureJSON:      "{}",
				ObservedAt:       a.h.now,
			})
		}
		out.TelegramCursors = append(out.TelegramCursors, discovery.TelegramCursorUpdate{
			ChannelRef:      channel.Ref,
			LastMessageID:   lastMessageID,
			LastMessageDate: a.h.now.Unix(),
		})
	}
	return out, nil
}

type fake115Share struct {
	raw         string
	code        string
	receiveCode string
	redactedURL string
}

func parseFake115Share(text string) (fake115Share, error) {
	start := strings.Index(text, "https://115.com/s/")
	if start < 0 {
		return fake115Share{}, sql.ErrNoRows
	}
	raw := text[start:]
	if end := strings.IndexFunc(raw, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '<' || r == '>' || r == '"' || r == '\''
	}); end >= 0 {
		raw = raw[:end]
	}
	raw = strings.TrimRight(raw, ".,;)")
	parsed, err := url.Parse(raw)
	if err != nil {
		return fake115Share{}, err
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 2 || parts[0] != "s" {
		return fake115Share{}, fmt.Errorf("not a 115 share url: %s", raw)
	}
	code, err := url.PathUnescape(parts[1])
	if err != nil {
		code = parts[1]
	}
	receiveCode := ""
	for _, key := range []string{"password", "pwd", "receive_code", "receiveCode"} {
		if value := parsed.Query().Get(key); value != "" {
			receiveCode = value
			break
		}
	}
	redacted := *parsed
	if receiveCode != "" {
		q := redacted.Query()
		for _, key := range []string{"password", "pwd", "receive_code", "receiveCode"} {
			if q.Get(key) != "" {
				q.Set(key, "[REDACTED]")
			}
		}
		redacted.RawQuery = q.Encode()
	}
	return fake115Share{
		raw:         raw,
		code:        code,
		receiveCode: receiveCode,
		redactedURL: strings.ReplaceAll(redacted.String(), "%5BREDACTED%5D", "[REDACTED]"),
	}, nil
}

func titleBeforeShare(text, rawShareURL string) string {
	title := strings.TrimSpace(strings.Replace(text, rawShareURL, "", 1))
	if title == "" {
		return "Telegram 115 share"
	}
	return strings.Join(strings.Fields(title), " ")
}

func redactFake115Text(text string, share fake115Share) string {
	redacted := strings.ReplaceAll(text, share.raw, share.redactedURL)
	if share.code != "" {
		redacted = strings.ReplaceAll(redacted, share.code, "[REDACTED]")
	}
	if share.receiveCode != "" {
		redacted = strings.ReplaceAll(redacted, share.receiveCode, "[REDACTED]")
	}
	return redacted
}

func mediaKey(tmdbID, mediaType string) string {
	return mediaType + ":" + tmdbID
}
