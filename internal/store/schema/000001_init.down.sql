DROP INDEX IF EXISTS idx_producer_runs_job;
DROP TABLE IF EXISTS producer_runs;

DROP INDEX IF EXISTS idx_jobs_status;
DROP TABLE IF EXISTS jobs;

DROP INDEX IF EXISTS idx_hash_conflicts_status;
DROP TABLE IF EXISTS hash_conflicts;

DROP TABLE IF EXISTS copy_failures;

DROP INDEX IF EXISTS idx_accounts_scheduler;
DROP INDEX IF EXISTS idx_file_copies_scheduler;
DROP INDEX IF EXISTS idx_file_copies_live;
DROP TABLE IF EXISTS file_copies;

DROP INDEX IF EXISTS idx_playback_events_unfinished;
DROP INDEX IF EXISTS idx_playback_events_user_time;
DROP INDEX IF EXISTS idx_account_pool_user_provider;
DROP INDEX IF EXISTS idx_library_grants_user;
DROP TABLE IF EXISTS playback_events;
DROP TABLE IF EXISTS quota_usage;
DROP TABLE IF EXISTS account_pool_assignments;
DROP TABLE IF EXISTS library_grants;

DROP INDEX IF EXISTS idx_blob_hashes_blob;
DROP TABLE IF EXISTS blob_hashes;

DROP INDEX IF EXISTS idx_library_entries_blob;
DROP TABLE IF EXISTS library_entries;

DROP TABLE IF EXISTS blobs;
DROP TABLE IF EXISTS libraries;
DROP TABLE IF EXISTS accounts;

DROP INDEX IF EXISTS idx_api_tokens_user;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS quota_policies;
