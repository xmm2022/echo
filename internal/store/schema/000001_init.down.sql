DROP INDEX IF EXISTS idx_producer_runs_job;
DROP TABLE IF EXISTS producer_runs;

DROP INDEX IF EXISTS idx_jobs_status;
DROP TABLE IF EXISTS jobs;

DROP INDEX IF EXISTS idx_hash_conflicts_status;
DROP TABLE IF EXISTS hash_conflicts;

DROP INDEX IF EXISTS idx_file_copies_live;
DROP TABLE IF EXISTS file_copies;

DROP INDEX IF EXISTS idx_blob_hashes_blob;
DROP TABLE IF EXISTS blob_hashes;

DROP INDEX IF EXISTS idx_library_entries_blob;
DROP TABLE IF EXISTS library_entries;

DROP TABLE IF EXISTS blobs;
DROP TABLE IF EXISTS libraries;
DROP TABLE IF EXISTS accounts;
