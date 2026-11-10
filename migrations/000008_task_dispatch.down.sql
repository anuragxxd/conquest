BEGIN;

DROP INDEX IF EXISTS idx_tasks_undispatched;
ALTER TABLE tasks DROP COLUMN IF EXISTS dispatch_lease_until;
ALTER TABLE tasks DROP COLUMN IF EXISTS dispatch_not_before;
ALTER TABLE tasks DROP COLUMN IF EXISTS dispatch_attempts;

COMMIT;
