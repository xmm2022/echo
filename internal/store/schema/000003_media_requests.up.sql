ALTER TABLE discovery_subscriptions
ADD COLUMN match_mode TEXT NOT NULL DEFAULT 'auto_dispatch'
  CHECK (match_mode IN ('admin_review','auto_dispatch'));

CREATE TABLE discovery_access_policies (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  name                     TEXT NOT NULL UNIQUE,
  enabled                  INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  priority                 INTEGER NOT NULL DEFAULT 100 CHECK (priority >= 0),
  subject_user_id          TEXT REFERENCES users(id),
  request_mode             TEXT NOT NULL
    CHECK (request_mode IN ('disabled','approval_required','auto_approve')),
  can_search               INTEGER NOT NULL DEFAULT 1 CHECK (can_search IN (0,1)),
  max_pending_requests     INTEGER CHECK (max_pending_requests IS NULL OR max_pending_requests >= 0),
  max_active_subscriptions INTEGER CHECK (max_active_subscriptions IS NULL OR max_active_subscriptions >= 0),
  request_cooldown_seconds INTEGER CHECK (request_cooldown_seconds IS NULL OR request_cooldown_seconds >= 0),
  created_by               TEXT REFERENCES users(id),
  created_at               INTEGER NOT NULL,
  updated_at               INTEGER NOT NULL
);

CREATE TABLE discovery_policy_targets (
  id                         INTEGER PRIMARY KEY AUTOINCREMENT,
  policy_id                  INTEGER NOT NULL REFERENCES discovery_access_policies(id) ON DELETE CASCADE,
  label                      TEXT NOT NULL,
  library_id                 INTEGER NOT NULL REFERENCES libraries(id),
  producer_profile_id        INTEGER NOT NULL REFERENCES discovery_producer_profiles(id),
  rule_profile_id            INTEGER NOT NULL REFERENCES rule_profiles(id),
  pipeline_owner_id          TEXT NOT NULL REFERENCES users(id) DEFAULT 'admin',
  media_type                 TEXT CHECK (media_type IS NULL OR media_type IN ('movie','tv')),
  match_mode                 TEXT NOT NULL CHECK (match_mode IN ('admin_review','auto_dispatch')),
  grant_playback_on_approval INTEGER NOT NULL DEFAULT 0 CHECK (grant_playback_on_approval IN (0,1)),
  enabled                    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  default_target             INTEGER NOT NULL DEFAULT 0 CHECK (default_target IN (0,1)),
  created_at                 INTEGER NOT NULL,
  updated_at                 INTEGER NOT NULL,
  UNIQUE (policy_id, label)
);

CREATE TABLE discovery_subscription_requests (
  id                             INTEGER PRIMARY KEY AUTOINCREMENT,
  requester_user_id              TEXT NOT NULL REFERENCES users(id),
  status                         TEXT NOT NULL CHECK (status IN ('pending_review','approved','rejected','canceled','failed')),
  tmdb_id                        TEXT NOT NULL,
  media_type                     TEXT NOT NULL CHECK (media_type IN ('movie','tv')),
  tmdb_language                  TEXT NOT NULL,
  title_snapshot                 TEXT NOT NULL,
  original_title_snapshot        TEXT,
  release_year_snapshot          INTEGER,
  poster_path_snapshot           TEXT,
  season_filter_json             TEXT,
  policy_id_snapshot             INTEGER,
  policy_target_id_snapshot      INTEGER,
  target_label_snapshot          TEXT NOT NULL,
  target_library_id              INTEGER NOT NULL,
  target_library_name_snapshot   TEXT NOT NULL,
  producer_profile_id_snapshot   INTEGER NOT NULL,
  producer_profile_name_snapshot TEXT NOT NULL,
  rule_profile_id_snapshot       INTEGER NOT NULL,
  rule_profile_version_snapshot  INTEGER NOT NULL,
  user_note                      TEXT,
  admin_note                     TEXT,
  reviewed_by                    TEXT REFERENCES users(id),
  reviewed_at                    INTEGER,
  subscription_id                INTEGER REFERENCES discovery_subscriptions(id),
  idempotency_key                TEXT NOT NULL UNIQUE,
  last_error_kind                TEXT,
  last_error_message             TEXT,
  created_at                     INTEGER NOT NULL,
  updated_at                     INTEGER NOT NULL
);

CREATE TABLE user_media_subscriptions (
  id                        INTEGER PRIMARY KEY AUTOINCREMENT,
  echo_user_id              TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  request_id                INTEGER REFERENCES discovery_subscription_requests(id),
  discovery_subscription_id INTEGER NOT NULL REFERENCES discovery_subscriptions(id) ON DELETE CASCADE,
  tmdb_id                   TEXT NOT NULL,
  media_type                TEXT NOT NULL CHECK (media_type IN ('movie','tv')),
  season_filter_json        TEXT,
  season_filter_key         TEXT NOT NULL,
  status                    TEXT NOT NULL CHECK (status IN ('active','paused','canceled','completed')),
  created_at                INTEGER NOT NULL,
  updated_at                INTEGER NOT NULL,
  UNIQUE (echo_user_id, discovery_subscription_id, season_filter_key)
);

CREATE TABLE discovery_subscription_request_events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id    INTEGER NOT NULL REFERENCES discovery_subscription_requests(id) ON DELETE CASCADE,
  actor_user_id TEXT REFERENCES users(id),
  action        TEXT NOT NULL,
  from_status   TEXT,
  to_status     TEXT,
  note          TEXT,
  snapshot_json TEXT NOT NULL DEFAULT '{}',
  created_at    INTEGER NOT NULL
);

CREATE TABLE discovery_user_audit_events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_user_id TEXT REFERENCES users(id),
  action        TEXT NOT NULL CHECK (action IN ('policy_deny','rate_limit_deny','search_deny','request_create_deny','subscription_action_deny')),
  target_kind   TEXT NOT NULL,
  target_id     TEXT,
  safe_reason   TEXT NOT NULL,
  snapshot_json TEXT NOT NULL DEFAULT '{}',
  created_at    INTEGER NOT NULL
);

CREATE INDEX idx_discovery_access_policies_subject ON discovery_access_policies(enabled, subject_user_id, priority);
CREATE INDEX idx_discovery_policy_targets_policy ON discovery_policy_targets(policy_id, enabled, media_type, default_target);
CREATE INDEX idx_discovery_requests_user_status ON discovery_subscription_requests(requester_user_id, status, updated_at DESC);
CREATE INDEX idx_discovery_requests_status_time ON discovery_subscription_requests(status, created_at DESC);
CREATE INDEX idx_discovery_requests_tmdb_target ON discovery_subscription_requests(tmdb_id, media_type, target_library_id, created_at DESC);
CREATE INDEX idx_user_media_subscriptions_user_status ON user_media_subscriptions(echo_user_id, status, updated_at DESC);
CREATE INDEX idx_user_media_subscriptions_discovery ON user_media_subscriptions(discovery_subscription_id, status);
CREATE INDEX idx_discovery_request_events_request ON discovery_subscription_request_events(request_id, created_at DESC);
CREATE INDEX idx_discovery_user_audit_actor_time ON discovery_user_audit_events(actor_user_id, created_at DESC);
CREATE INDEX idx_discovery_user_audit_action_time ON discovery_user_audit_events(action, created_at DESC);
