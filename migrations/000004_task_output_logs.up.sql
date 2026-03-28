-- Migration 004: Add task output logs for TES FULL view reconstruction.
--
-- After in-pod stage-out, the task needs to persist the actual uploaded
-- output file metadata (URLs, sizes) so that TES FULL responses can
-- return real OutputFileLog entries instead of reconstructing from
-- the original request.

BEGIN;

-- JSON array of OutputFileLog objects: [{url, path, size_bytes}, ...]
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS output_logs JSONB;

COMMIT;
