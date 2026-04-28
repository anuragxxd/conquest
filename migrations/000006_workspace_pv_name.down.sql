BEGIN;

ALTER TABLE runs DROP COLUMN IF EXISTS workspace_pv_name;
ALTER TABLE tasks DROP COLUMN IF EXISTS workspace_pv_name;

COMMIT;
