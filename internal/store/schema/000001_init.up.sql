-- SQLite only, v0.1, do NOT assume PG compatibility

PRAGMA foreign_keys = ON;

CREATE TABLE quota_policies (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  name                  TEXT NOT NULL,
  period                TEXT NOT NULL CHECK (period IN ('day','month','rolling_24h','none')),
  max_bytes             INTEGER,
  max_streams           INTEGER,
  max_playback_sessions INTEGER,
  version               INTEGER NOT NULL DEFAULT 1,
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL
);

INSERT INTO quota_policies (
  id, name, period, max_bytes, max_streams, max_playback_sessions, version, created_at, updated_at
) VALUES (
  1, 'unlimited', 'none', NULL, NULL, NULL, 1, 0, 0
);

CREATE TABLE users (
  id               TEXT PRIMARY KEY,
  username         TEXT NOT NULL UNIQUE,
  role             TEXT NOT NULL CHECK (role IN ('admin','user')),
  status           TEXT NOT NULL CHECK (status IN ('active','disabled')),
  quota_policy_id  INTEGER NOT NULL REFERENCES quota_policies(id) DEFAULT 1,
  password_hash    TEXT,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  last_login_at    INTEGER
);

INSERT INTO users (
  id, username, role, status, password_hash, created_at, updated_at, last_login_at
) VALUES (
  'admin', 'admin', 'admin', 'active', NULL, 0, 0, NULL
);

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  scopes       TEXT NOT NULL,
  expires_at   INTEGER,
  last_used_at INTEGER,
  created_at   INTEGER NOT NULL,
  revoked_at   INTEGER
);

CREATE INDEX idx_api_tokens_user ON api_tokens(user_id, revoked_at, expires_at);

CREATE TABLE accounts (
  id              TEXT PRIMARY KEY,
  provider        TEXT NOT NULL,
  sidecar_id      TEXT NOT NULL,
  storage_mount   TEXT NOT NULL,
  status          TEXT NOT NULL,
  last_check      INTEGER,
  owner_id        TEXT NOT NULL DEFAULT 'admin',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  scheduler_state TEXT NOT NULL DEFAULT 'healthy' CHECK (scheduler_state IN ('healthy','cooldown','unhealthy','token_suspect','disabled')),
  cooldown_until INTEGER,
  recheck_after INTEGER,
  status_reason TEXT,
  last_error_at INTEGER,
  last_error_kind TEXT,
  last_error_message TEXT,
  UNIQUE (sidecar_id, storage_mount)
);

CREATE TABLE libraries (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  name             TEXT NOT NULL,
  echo_output_kind TEXT NOT NULL,
  echo_output_path TEXT NOT NULL,
  owner_id         TEXT NOT NULL DEFAULT 'admin',
  created_at       INTEGER NOT NULL
);

CREATE TABLE library_grants (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id   INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  echo_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  permission   TEXT NOT NULL CHECK (permission IN ('playback')),
  enabled      INTEGER NOT NULL DEFAULT 1,
  created_by   TEXT REFERENCES users(id),
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  UNIQUE (library_id, echo_user_id, permission)
);

CREATE TABLE account_pool_assignments (
  id                     INTEGER PRIMARY KEY AUTOINCREMENT,
  echo_user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id              TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  provider                TEXT NOT NULL,
  priority                INTEGER NOT NULL DEFAULT 100,
  weight                  INTEGER NOT NULL DEFAULT 1,
  max_concurrent_streams  INTEGER,
  daily_bytes_limit       INTEGER,
  enabled                 INTEGER NOT NULL DEFAULT 1,
  created_at              INTEGER NOT NULL,
  updated_at              INTEGER NOT NULL,
  UNIQUE (echo_user_id, account_id)
);

CREATE TABLE quota_usage (
  user_id          TEXT NOT NULL REFERENCES users(id),
  quota_policy_id  INTEGER NOT NULL,
  policy_version   INTEGER NOT NULL,
  period_start     INTEGER NOT NULL,
  period_end       INTEGER NOT NULL,
  bytes_used       INTEGER NOT NULL DEFAULT 0,
  stream_count     INTEGER NOT NULL DEFAULT 0,
  updated_at       INTEGER NOT NULL,
  PRIMARY KEY (user_id, quota_policy_id, policy_version, period_start, period_end)
);

CREATE TABLE playback_events (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id        TEXT NOT NULL,
  session_id        TEXT,
  error_token_id    TEXT,
  echo_user_id       TEXT REFERENCES users(id),
  library_entry_id   INTEGER,
  blob_id            INTEGER,
  copy_id            INTEGER,
  provider           TEXT,
  account_id         TEXT,
  operation          TEXT NOT NULL CHECK (operation IN ('playback_info','stream','stream_probe')),
  status             TEXT NOT NULL,
  bytes_sent         INTEGER NOT NULL DEFAULT 0,
  range_header       TEXT,
  http_status        INTEGER,
  failure_kind       TEXT,
  failure_message    TEXT,
  started_at         INTEGER NOT NULL,
  finished_at        INTEGER
);

CREATE INDEX idx_library_grants_user ON library_grants(echo_user_id, library_id, permission, enabled);
CREATE INDEX idx_account_pool_user_provider ON account_pool_assignments(echo_user_id, provider, enabled, priority);
CREATE INDEX idx_playback_events_user_time ON playback_events(echo_user_id, started_at DESC);
CREATE INDEX idx_playback_events_unfinished ON playback_events(operation, finished_at, started_at);

CREATE TABLE blobs (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  size            INTEGER NOT NULL,
  canonical_name  TEXT,
  source_mtime    INTEGER,
  owner_id        TEXT NOT NULL DEFAULT 'admin',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);

CREATE TABLE library_entries (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id    INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  rel_path      TEXT NOT NULL,
  name          TEXT NOT NULL,
  blob_id       INTEGER NOT NULL REFERENCES blobs(id) ON DELETE CASCADE,
  echo_written  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE (library_id, rel_path)
);

CREATE INDEX idx_library_entries_blob ON library_entries(blob_id);

CREATE TABLE blob_hashes (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  blob_id          INTEGER NOT NULL REFERENCES blobs(id) ON DELETE CASCADE,
  hash_type        TEXT NOT NULL,
  hash_value       TEXT NOT NULL,
  hash_value_norm  TEXT NOT NULL,
  size             INTEGER NOT NULL,
  UNIQUE (hash_type, hash_value_norm, size)
);

CREATE INDEX idx_blob_hashes_blob ON blob_hashes(blob_id);

CREATE TABLE file_copies (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  blob_id        INTEGER NOT NULL REFERENCES blobs(id) ON DELETE CASCADE,
  provider       TEXT NOT NULL,
  account_id     TEXT NOT NULL REFERENCES accounts(id),
  sidecar_id     TEXT NOT NULL,
  storage_mount  TEXT NOT NULL,
  remote_path    TEXT NOT NULL,
  cloud_file_id  TEXT,
  pickcode       TEXT,
  status         TEXT NOT NULL CHECK (status IN ('live','dead','pending')),
  last_seen      INTEGER NOT NULL,
  scheduler_state TEXT NOT NULL DEFAULT 'healthy' CHECK (scheduler_state IN ('healthy','suspect_dead','confirmed_dead','cooldown')),
  cooldown_until INTEGER,
  verify_after INTEGER,
  failure_count INTEGER NOT NULL DEFAULT 0,
  last_failure_at INTEGER,
  last_failure_kind TEXT,
  last_failure_confidence TEXT CHECK (last_failure_confidence IS NULL OR last_failure_confidence IN ('confirmed','suspect','low')),
  last_failure_code INTEGER,
  last_failure_message TEXT,
  dead_reason TEXT,
  dead_at INTEGER,
  UNIQUE (sidecar_id, storage_mount, remote_path)
);

CREATE INDEX idx_file_copies_live ON file_copies(blob_id, status, last_seen DESC);
CREATE INDEX idx_file_copies_scheduler ON file_copies(blob_id, status, scheduler_state, cooldown_until, last_seen DESC);
CREATE INDEX idx_accounts_scheduler ON accounts(provider, scheduler_state, cooldown_until);

CREATE TABLE copy_failures (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  copy_id         INTEGER,
  account_id      TEXT,
  sidecar_id      TEXT NOT NULL,
  storage_mount   TEXT NOT NULL,
  operation       TEXT NOT NULL,
  kind            TEXT NOT NULL,
  confidence      TEXT NOT NULL CHECK (confidence IN ('confirmed','suspect','low')),
  evidence_class  TEXT NOT NULL CHECK (evidence_class IN ('json_envelope','http_status','html_snippet','transport')),
  http_status     INTEGER,
  openlist_code   INTEGER,
  safe_message    TEXT,
  observed_at     INTEGER NOT NULL,
  request_id      TEXT
);

CREATE TABLE hash_conflicts (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  blob_id_a       INTEGER NOT NULL REFERENCES blobs(id),
  blob_id_b       INTEGER NOT NULL REFERENCES blobs(id),
  reason          TEXT NOT NULL,
  detail          TEXT NOT NULL,
  observed_at     INTEGER NOT NULL,
  status          TEXT NOT NULL DEFAULT 'open'
);

CREATE INDEX idx_hash_conflicts_status ON hash_conflicts(status, observed_at DESC);

CREATE TABLE jobs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  kind          TEXT NOT NULL,
  status        TEXT NOT NULL,
  payload       TEXT NOT NULL,
  progress      TEXT,
  error         TEXT,
  owner_id      TEXT NOT NULL DEFAULT 'admin',
  created_at    INTEGER NOT NULL,
  started_at    INTEGER,
  finished_at   INTEGER
);

CREATE INDEX idx_jobs_status ON jobs(status, created_at);

CREATE TABLE producer_runs (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id         INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  tool           TEXT NOT NULL,
  tool_version   TEXT,
  cmdline        TEXT NOT NULL,
  workdir        TEXT NOT NULL,
  output_dir     TEXT NOT NULL,
  manifest_path  TEXT,
  stdout_path    TEXT,
  stderr_path    TEXT,
  exit_code      INTEGER,
  started_at     INTEGER NOT NULL,
  finished_at    INTEGER
);

CREATE INDEX idx_producer_runs_job ON producer_runs(job_id);

CREATE TABLE emby_servers (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  base_url        TEXT NOT NULL,
  api_key_ref     TEXT,
  public_base_url TEXT NOT NULL,
  proxy_prefix    TEXT NOT NULL DEFAULT '/emby',
  enabled         INTEGER NOT NULL DEFAULT 1,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);

CREATE TABLE emby_user_links (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  emby_server_id  TEXT NOT NULL REFERENCES emby_servers(id) ON DELETE CASCADE,
  emby_user_id    TEXT NOT NULL,
  emby_username   TEXT,
  echo_user_id    TEXT NOT NULL REFERENCES users(id),
  enabled         INTEGER NOT NULL DEFAULT 1,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  UNIQUE (emby_server_id, emby_user_id)
);

CREATE TABLE playback_sessions (
  id                   TEXT PRIMARY KEY,
  selector             TEXT NOT NULL UNIQUE,
  token_hash           TEXT NOT NULL,
  echo_user_id          TEXT NOT NULL REFERENCES users(id),
  emby_server_id        TEXT NOT NULL REFERENCES emby_servers(id),
  emby_user_id          TEXT NOT NULL,
  device_id             TEXT,
  item_id               TEXT NOT NULL,
  media_source_id       TEXT NOT NULL,
  emby_play_session_id  TEXT,
  library_entry_id      INTEGER REFERENCES library_entries(id),
  blob_id               INTEGER REFERENCES blobs(id),
  prefer_provider       TEXT,
  state                 TEXT NOT NULL CHECK (state IN ('active','expired','revoked','failed')),
  failure_reason        TEXT,
  created_at            INTEGER NOT NULL,
  last_seen_at          INTEGER NOT NULL,
  expires_at            INTEGER NOT NULL,
  CHECK (
    (state = 'active' AND library_entry_id IS NOT NULL AND blob_id IS NOT NULL)
    OR state IN ('expired','revoked','failed')
  )
);

CREATE TABLE playback_error_tokens (
  id                   TEXT PRIMARY KEY,
  selector             TEXT NOT NULL UNIQUE,
  token_hash           TEXT NOT NULL,
  echo_user_id          TEXT REFERENCES users(id),
  emby_server_id        TEXT REFERENCES emby_servers(id),
  emby_user_id          TEXT,
  device_id             TEXT,
  item_id               TEXT,
  media_source_id       TEXT,
  reason                TEXT NOT NULL CHECK (reason IN (
    'unauthorized',
    'quota_exceeded',
    'account_unavailable',
    'temporary_unavailable',
    'no_live_copy',
    'rewrite_failed',
    'unsupported_transcode',
    'unknown_playable_url'
  )),
  http_status           INTEGER NOT NULL,
  created_at            INTEGER NOT NULL,
  expires_at            INTEGER NOT NULL
);

CREATE INDEX idx_playback_sessions_token ON playback_sessions(selector, state, expires_at);
CREATE INDEX idx_playback_error_tokens_token ON playback_error_tokens(selector, expires_at);
CREATE INDEX idx_emby_user_links_lookup ON emby_user_links(emby_server_id, emby_user_id, enabled);
