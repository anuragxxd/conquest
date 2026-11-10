BEGIN;

-- Asynchronous dispatch: a task is created QUEUED without a backend, and a
-- dispatcher routes and submits it. Undispatched tasks are QUEUED with an
-- empty backend_ref.
--
-- dispatch_attempts counts submissions tried; dispatch_not_before delays the
-- next one after a retryable failure; dispatch_lease_until is how long the
-- dispatcher that claimed the task owns it, so that a dispatcher that
-- crashed mid-submission gives the task back.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS dispatch_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS dispatch_not_before TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS dispatch_lease_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_tasks_undispatched ON tasks (created_at)
    WHERE state = 'QUEUED' AND COALESCE(backend_ref, '') = '';

COMMIT;
