BEGIN;

ALTER TABLE tasks DROP COLUMN IF EXISTS workspace_access_mode;
ALTER TABLE tasks DROP COLUMN IF EXISTS workspace_claim_name;
ALTER TABLE tasks DROP COLUMN IF EXISTS workspace_kind;

ALTER TABLE runs DROP COLUMN IF EXISTS workspace_access_mode;
ALTER TABLE runs DROP COLUMN IF EXISTS workspace_claim_name;
ALTER TABLE runs DROP COLUMN IF EXISTS workspace_kind;

COMMIT;
