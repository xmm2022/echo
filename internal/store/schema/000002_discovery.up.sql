CREATE TABLE tmdb_media (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  tmdb_id         TEXT NOT NULL,
  media_type      TEXT NOT NULL CHECK (media_type IN ('movie','tv')),
  language        TEXT NOT NULL,
  title           TEXT NOT NULL,
  original_title  TEXT,
  release_year    INTEGER,
  poster_path     TEXT,
  status          TEXT,
  raw_json        TEXT NOT NULL,
  fetched_at      INTEGER NOT NULL,
  next_refresh_at INTEGER NOT NULL,
  last_error_kind TEXT,
  last_error_message TEXT,
  UNIQUE (tmdb_id, media_type, language)
);

CREATE TABLE discovery_sources (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  kind               TEXT NOT NULL CHECK (kind IN ('telegram_mtproto','poster_http','manual')),
  name               TEXT NOT NULL,
  enabled            INTEGER NOT NULL DEFAULT 1,
  config_json        TEXT NOT NULL,
  secret_ref         TEXT,
  rate_limit_json    TEXT,
  scheduler_state    TEXT NOT NULL DEFAULT 'healthy' CHECK (scheduler_state IN ('healthy','backoff','disabled','auth_failed','unhealthy')),
  next_run_at        INTEGER,
  locked_until       INTEGER,
  backoff_until      INTEGER,
  failure_count      INTEGER NOT NULL DEFAULT 0,
  last_success_at    INTEGER,
  last_error_kind    TEXT,
  last_error_message TEXT,
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL
);

CREATE TABLE telegram_channels (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  source_id           INTEGER NOT NULL REFERENCES discovery_sources(id) ON DELETE CASCADE,
  channel_ref         TEXT NOT NULL,
  stable_peer_id      TEXT,
  username_snapshot   TEXT,
  title_snapshot      TEXT,
  level_flags         INTEGER NOT NULL DEFAULT 0,
  enabled             INTEGER NOT NULL DEFAULT 1,
  last_message_id     INTEGER,
  last_message_date   INTEGER,
  next_run_at         INTEGER,
  locked_until        INTEGER,
  backoff_until       INTEGER,
  failure_count       INTEGER NOT NULL DEFAULT 0,
  last_error_kind     TEXT,
  last_error_message  TEXT,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL,
  UNIQUE (source_id, channel_ref)
);

CREATE UNIQUE INDEX idx_accounts_id_provider ON accounts(id, provider);

CREATE TABLE discovery_producer_profiles (
  id                      INTEGER PRIMARY KEY AUTOINCREMENT,
  name                    TEXT NOT NULL UNIQUE,
  provider                TEXT NOT NULL,
  tool                    TEXT NOT NULL,
  target_account          TEXT NOT NULL,
  target_subdir_template  TEXT NOT NULL DEFAULT '',
  library_rel_path_template TEXT NOT NULL DEFAULT '',
  default_args_json       TEXT NOT NULL,
  enabled                 INTEGER NOT NULL DEFAULT 1,
  created_at              INTEGER NOT NULL,
  updated_at              INTEGER NOT NULL,
  FOREIGN KEY (target_account, provider) REFERENCES accounts(id, provider),
  CHECK (provider = '115'),
  CHECK (tool = '115share2cas')
);

CREATE TABLE rule_profiles (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  name           TEXT NOT NULL UNIQUE,
  version        INTEGER NOT NULL DEFAULT 1,
  rules_json     TEXT NOT NULL,
  enabled        INTEGER NOT NULL DEFAULT 1,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);

CREATE TABLE discovery_subscriptions (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_id                 TEXT NOT NULL REFERENCES users(id),
  tmdb_id                  TEXT NOT NULL,
  media_type               TEXT NOT NULL CHECK (media_type IN ('movie','tv')),
  tmdb_language            TEXT NOT NULL DEFAULT 'zh-CN',
  title_snapshot           TEXT NOT NULL,
  library_id               INTEGER NOT NULL REFERENCES libraries(id),
  producer_profile_id      INTEGER NOT NULL REFERENCES discovery_producer_profiles(id),
  rule_profile_id          INTEGER NOT NULL REFERENCES rule_profiles(id),
  status                   TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','completed','disabled')),
  season_filter_json       TEXT,
  current_best_match_id    INTEGER,
  current_best_score_json  TEXT,
  next_check_at            INTEGER,
  locked_until             INTEGER,
  last_checked_at          INTEGER,
  failure_count            INTEGER NOT NULL DEFAULT 0,
  last_error_kind          TEXT,
  last_error_message       TEXT,
  created_at               INTEGER NOT NULL,
  updated_at               INTEGER NOT NULL,
  UNIQUE (tmdb_id, media_type, owner_id, library_id)
);

CREATE TABLE discovered_resources (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  source_id          INTEGER NOT NULL REFERENCES discovery_sources(id),
  provider           TEXT NOT NULL,
  link_kind          TEXT NOT NULL CHECK (link_kind IN ('115_share','unknown')),
  external_key       TEXT NOT NULL,
  tmdb_id            TEXT,
  media_type         TEXT CHECK (media_type IS NULL OR media_type IN ('movie','tv')),
  title              TEXT,
  season_number      INTEGER,
  episode_start      INTEGER,
  episode_end        INTEGER,
  share_code         TEXT,
  receive_code       TEXT,
  share_url_redacted TEXT,
  raw_text_redacted  TEXT,
  raw_text_ref       TEXT,
  parsed_json        TEXT NOT NULL,
  feature_json       TEXT NOT NULL,
  status             TEXT NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate','review','rejected','unsupported_provider','accepted','queued','imported','failed')),
  first_seen_at      INTEGER NOT NULL,
  last_seen_at       INTEGER NOT NULL,
  UNIQUE (source_id, external_key)
);

CREATE TABLE subscription_matches (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  subscription_id      INTEGER NOT NULL REFERENCES discovery_subscriptions(id) ON DELETE CASCADE,
  resource_id          INTEGER NOT NULL REFERENCES discovered_resources(id) ON DELETE CASCADE,
  rule_profile_id      INTEGER NOT NULL REFERENCES rule_profiles(id),
  rule_profile_version INTEGER NOT NULL,
  season_number        INTEGER,
  episode_start        INTEGER,
  episode_end          INTEGER,
  score_json           TEXT NOT NULL,
  previous_score_json  TEXT,
  decision             TEXT NOT NULL CHECK (decision IN ('reject','review','accept','queue','imported','failed')),
  reason               TEXT NOT NULL,
  dispatch_state       TEXT NOT NULL DEFAULT 'none' CHECK (dispatch_state IN ('none','queued','running','succeeded','failed')),
  idempotency_key      TEXT NOT NULL UNIQUE,
  queued_job_id        INTEGER REFERENCES jobs(id),
  result_library_entry_id INTEGER REFERENCES library_entries(id),
  result_blob_id       INTEGER REFERENCES blobs(id),
  result_copy_id       INTEGER REFERENCES file_copies(id),
  failure_kind         TEXT,
  failure_message      TEXT,
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL,
  decided_at           INTEGER,
  finished_at          INTEGER
);

CREATE TABLE discovery_runs (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  kind              TEXT NOT NULL CHECK (kind IN ('source_crawl','subscription_check','dispatch','reconcile','tmdb_refresh')),
  source_id         INTEGER REFERENCES discovery_sources(id),
  subscription_id   INTEGER REFERENCES discovery_subscriptions(id),
  job_id            INTEGER REFERENCES jobs(id),
  status            TEXT NOT NULL CHECK (status IN ('pending','running','succeeded','failed','canceled')),
  counters_json     TEXT NOT NULL DEFAULT '{}',
  error_kind        TEXT,
  error_message     TEXT,
  started_at        INTEGER,
  finished_at       INTEGER,
  created_at        INTEGER NOT NULL
);

CREATE TABLE discovery_raw_access_events (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  resource_id    INTEGER NOT NULL REFERENCES discovered_resources(id) ON DELETE CASCADE,
  actor_user_id  TEXT NOT NULL REFERENCES users(id),
  request_id     TEXT,
  response_bytes INTEGER NOT NULL,
  redacted        INTEGER NOT NULL DEFAULT 1,
  accessed_at     INTEGER NOT NULL
);

CREATE INDEX idx_discovery_sources_due ON discovery_sources(enabled, scheduler_state, next_run_at, locked_until);
CREATE INDEX idx_telegram_channels_due ON telegram_channels(source_id, enabled, next_run_at, locked_until);
CREATE INDEX idx_discovery_subscriptions_due ON discovery_subscriptions(status, next_check_at, locked_until);
CREATE INDEX idx_discovered_resources_tmdb ON discovered_resources(tmdb_id, media_type, status, last_seen_at DESC);
CREATE INDEX idx_subscription_matches_subscription ON subscription_matches(subscription_id, decision, dispatch_state, updated_at DESC);
CREATE INDEX idx_subscription_matches_job ON subscription_matches(queued_job_id);
CREATE INDEX idx_discovery_raw_access_resource ON discovery_raw_access_events(resource_id, accessed_at DESC);
