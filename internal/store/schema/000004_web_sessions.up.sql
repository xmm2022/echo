PRAGMA foreign_keys = ON;

CREATE TABLE web_sessions (
  selector       TEXT PRIMARY KEY,
  user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  secret_hash    TEXT NOT NULL,
  csrf_hash      TEXT NOT NULL,
  scopes         TEXT NOT NULL,
  user_agent     TEXT,
  ip_hint        TEXT,
  created_at     INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  expires_at     INTEGER NOT NULL,
  revoked_at     INTEGER
);

CREATE INDEX idx_web_sessions_user ON web_sessions(user_id, revoked_at, expires_at);
CREATE INDEX idx_web_sessions_expiry ON web_sessions(expires_at, revoked_at);
