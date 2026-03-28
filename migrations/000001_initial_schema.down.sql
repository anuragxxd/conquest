BEGIN;

DROP TRIGGER IF EXISTS trg_tasks_updated_at ON tasks;
DROP TRIGGER IF EXISTS trg_runs_updated_at ON runs;
DROP FUNCTION IF EXISTS set_updated_at();

DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS runs;

DROP TYPE IF EXISTS ga4gh_state;

COMMIT;
