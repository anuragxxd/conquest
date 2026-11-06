BEGIN;

-- Which task backend runs the task: '' for the local Kubernetes backend,
-- otherwise the name of a configured remote TES endpoint. backend_ref then
-- holds the endpoint's task ID.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS backend_name TEXT NOT NULL DEFAULT '';
-- How the task was routed: candidates, exclusions, attempts (JSON).
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS route JSONB;

COMMIT;
