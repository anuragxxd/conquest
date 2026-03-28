-- Migration 003: Add run-level backend tracking fields for Batch D.
--
-- The runner Job (e.g. Nextflow engine runner) needs to be tracked per-run
-- just like task Jobs are tracked per-task. These columns store the K8s
-- Job name and backend-specific state blob for the runner.

BEGIN;

-- Runner Job name (e.g. "conquest-runner-<runID prefix>").
ALTER TABLE runs ADD COLUMN IF NOT EXISTS backend_ref TEXT;

-- Backend-specific state blob (pod phase, pod name, etc.) updated by
-- the run reconciler.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS backend_state JSONB;

COMMIT;
