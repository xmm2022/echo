package discovery

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/xmm2022/echo/internal/job"
	storepkg "github.com/xmm2022/echo/internal/store"
)

type discoveryFixture struct {
	SourceID          int64
	SubscriptionID    int64
	ResourceID        int64
	ProducerProfileID int64
	RuleProfileID     int64
	LibraryID         int64
}

func openDiscoveryTestStore(t *testing.T) *storepkg.Store {
	t.Helper()
	st, err := storepkg.Open(filepath.Join(t.TempDir(), "echo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedDiscoveryFixture(t *testing.T, st *storepkg.Store) discoveryFixture {
	t.Helper()
	ctx := context.Background()
	now := int64(100)
	if _, err := st.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO accounts (
  id, provider, sidecar_id, storage_mount, status, owner_id, created_at, updated_at
) VALUES ('acc-115', '115', 'sidecar-1', '/115', 'active', 'admin', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	var libraryID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO libraries (name, echo_output_kind, echo_output_path, owner_id, created_at)
VALUES ('Movies', 'local', '/tmp/echo-test', 'admin', ?)
RETURNING id`, now).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	sourceID := seedDiscoverySource(t, st, "telegram_mtproto")
	var producerProfileID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_producer_profiles (
  name, provider, tool, target_account, target_subdir_template,
  library_rel_path_template, default_args_json, enabled, created_at, updated_at
) VALUES ('115 default', '115', '115share2cas', 'acc-115', '{{.Title}}', '{{.Title}}', '{}', 1, ?, ?)
RETURNING id`, now, now).Scan(&producerProfileID); err != nil {
		t.Fatal(err)
	}
	var ruleProfileID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO rule_profiles (name, version, rules_json, enabled, created_at, updated_at)
VALUES ('default', 1, '{"weights":["resolutions"]}', 1, ?, ?)
RETURNING id`, now, now).Scan(&ruleProfileID); err != nil {
		t.Fatal(err)
	}
	var subscriptionID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_subscriptions (
  owner_id, tmdb_id, media_type, tmdb_language, title_snapshot, library_id,
  producer_profile_id, rule_profile_id, status, next_check_at, created_at, updated_at
) VALUES ('admin', '123', 'movie', 'zh-CN', 'Known Movie', ?, ?, ?, 'active', ?, ?, ?)
RETURNING id`, libraryID, producerProfileID, ruleProfileID, now, now, now).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	var resourceID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovered_resources (
  source_id, provider, link_kind, external_key, tmdb_id, media_type, title,
  share_code, receive_code, share_url_redacted, raw_text_redacted,
  parsed_json, feature_json, status, first_seen_at, last_seen_at
) VALUES (?, '115', '115_share', 'tg:10', '123', 'movie', 'Known Movie',
  'abc', 'pass', 'https://115.com/s/abc?password=[REDACTED]', 'Known Movie',
  '{}', '{}', 'candidate', ?, ?)
RETURNING id`, sourceID, now, now).Scan(&resourceID); err != nil {
		t.Fatal(err)
	}
	return discoveryFixture{
		SourceID:          sourceID,
		SubscriptionID:    subscriptionID,
		ResourceID:        resourceID,
		ProducerProfileID: producerProfileID,
		RuleProfileID:     ruleProfileID,
		LibraryID:         libraryID,
	}
}

func seedDiscoverySource(t *testing.T, st *storepkg.Store, kind string) int64 {
	t.Helper()
	ctx := context.Background()
	now := int64(100)
	var id int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_sources (
  kind, name, enabled, config_json, scheduler_state, next_run_at, created_at, updated_at
) VALUES (?, 'source', 1, '{}', 'healthy', ?, ?, ?)
RETURNING id`, kind, now, now, now).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedTelegramChannel(t *testing.T, st *storepkg.Store, sourceID int64, channelRef string) int64 {
	t.Helper()
	ctx := context.Background()
	now := int64(100)
	var id int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO telegram_channels (
  source_id, channel_ref, enabled, next_run_at, created_at, updated_at
) VALUES (?, ?, 1, ?, ?, ?)
RETURNING id`, sourceID, channelRef, now, now, now).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedDueSource(t *testing.T, st *storepkg.Store, dueAt int64) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO discovery_sources (
  kind, name, enabled, config_json, scheduler_state, next_run_at, created_at, updated_at
) VALUES ('poster_http', 'due source', 1, '{}', 'healthy', ?, ?, ?)
RETURNING id`, dueAt, dueAt, dueAt).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedDueSubscription(t *testing.T, st *storepkg.Store, subscriptionID int64, dueAt int64) {
	t.Helper()
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE discovery_subscriptions
SET next_check_at = ?, locked_until = NULL
WHERE id = ?`, dueAt, subscriptionID); err != nil {
		t.Fatal(err)
	}
}

func seedAcceptingDiscoveryRule(t *testing.T, st *storepkg.Store, ruleProfileID int64) {
	t.Helper()
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE rule_profiles
SET rules_json = '{"weights":["resolutions"],"resolutions":[{"name":"4K","enabled":true}]}'
WHERE id = ?`, ruleProfileID); err != nil {
		t.Fatal(err)
	}
}

func seedAcceptingDiscoveredResourceTitle(t *testing.T, st *storepkg.Store, resourceID int64) {
	t.Helper()
	if _, err := st.DB.ExecContext(context.Background(), `
UPDATE discovered_resources
SET title = 'Known.Movie.2160p.mkv', raw_text_redacted = 'Known.Movie.2160p.mkv'
WHERE id = ?`, resourceID); err != nil {
		t.Fatal(err)
	}
}

func countDiscoveredResources(t *testing.T, st *storepkg.Store) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM discovered_resources`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func lastTelegramMessageID(t *testing.T, st *storepkg.Store, sourceID int64, channelRef string) int64 {
	t.Helper()
	var id int64
	if err := st.DB.QueryRowContext(context.Background(), `
SELECT COALESCE(last_message_id, 0)
FROM telegram_channels
WHERE source_id = ? AND channel_ref = ?`, sourceID, channelRef).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedDispatchableMatch(t *testing.T, st *storepkg.Store, subscriptionID, resourceID, ruleProfileID int64) int64 {
	t.Helper()
	ctx := context.Background()
	now := int64(100)
	var id int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO subscription_matches (
  subscription_id, resource_id, rule_profile_id, rule_profile_version,
  score_json, decision, reason, dispatch_state, idempotency_key,
  created_at, updated_at, decided_at
) VALUES (?, ?, ?, 1, '{}', 'accept', 'accepted', 'none', ?, ?, ?, ?)
RETURNING id`,
		subscriptionID,
		resourceID,
		ruleProfileID,
		fmt.Sprintf("dispatchable:%d:%d:%d", subscriptionID, resourceID, now),
		now,
		now,
		now,
	).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedQueuedMatchForReconcile(t *testing.T, st *storepkg.Store, subscriptionID, resourceID, ruleProfileID int64) int64 {
	t.Helper()
	ctx := context.Background()
	now := int64(100)
	var jobID int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO jobs (kind, status, payload, progress, owner_id, created_at, finished_at)
VALUES (?, 'done', '{}', '{}', 'discovery', ?, ?)
RETURNING id`, job.KindIngestProducer, now, now).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.DB.QueryRowContext(ctx, `
INSERT INTO subscription_matches (
  subscription_id, resource_id, rule_profile_id, rule_profile_version,
  score_json, decision, reason, dispatch_state, idempotency_key, queued_job_id,
  created_at, updated_at, decided_at
) VALUES (?, ?, ?, 1, '{}', 'queue', 'queued', 'queued', ?, ?, ?, ?, ?)
RETURNING id`,
		subscriptionID,
		resourceID,
		ruleProfileID,
		fmt.Sprintf("queued:%d:%d:%d", subscriptionID, resourceID, jobID),
		jobID,
		now,
		now,
		now,
	).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedDueTMDBMedia(t *testing.T, st *storepkg.Store, tmdbID, mediaType string, dueAt int64) int64 {
	t.Helper()
	var id int64
	if err := st.DB.QueryRowContext(context.Background(), `
INSERT INTO tmdb_media (
  tmdb_id, media_type, language, title, raw_json, fetched_at, next_refresh_at
) VALUES (?, ?, 'zh-CN', 'Known Movie', '{}', ?, ?)
RETURNING id`, tmdbID, mediaType, dueAt, dueAt).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
