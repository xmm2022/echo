-- SQLite only, v0.1, do NOT assume PG compatibility

PRAGMA foreign_keys = ON;

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
  UNIQUE (sidecar_id, storage_mount, remote_path)
);

CREATE INDEX idx_file_copies_live ON file_copies(blob_id, status, last_seen DESC);

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
