-- Migration 001: Initial schema for Conquest workflow execution platform.
--
-- Uses a single ga4gh_state enum shared by runs and tasks (both WES and TES
-- use the same 11-value GA4GH v1.1 state enum). The valid transitions differ
-- and are enforced in application code, not in the database.

BEGIN;

-- ---------------------------------------------------------------------------
-- Shared enum
-- ---------------------------------------------------------------------------

CREATE TYPE ga4gh_state AS ENUM (
    'UNKNOWN',
    'QUEUED',
    'INITIALIZING',
    'RUNNING',
    'PAUSED',
    'COMPLETE',
    'EXECUTOR_ERROR',
    'SYSTEM_ERROR',
    'CANCELED',
    'CANCELING',
    'PREEMPTED'
);

-- ---------------------------------------------------------------------------
-- Runs (WES)
-- ---------------------------------------------------------------------------

CREATE TABLE runs (
    run_id                  TEXT        PRIMARY KEY,
    workflow_type           TEXT        NOT NULL,        -- e.g. "NFL", "SMK"
    workflow_type_version   TEXT        NOT NULL,        -- e.g. "DSL2"
    workflow_engine         TEXT        NOT NULL,        -- e.g. "nextflow"
    workflow_engine_version TEXT        NOT NULL DEFAULT '',
    state                   ga4gh_state NOT NULL DEFAULT 'QUEUED',
    request_payload         JSONB       NOT NULL,        -- original WES RunRequest
    resolved_workflow_ref   TEXT,                         -- TRS URI or object-storage path
    output_prefix           TEXT        NOT NULL,        -- s3://bucket/v1/workflow-runs/<id>/outputs/
    layout_version          TEXT        NOT NULL DEFAULT 'v1',
    run_root_uri            TEXT        NOT NULL,        -- s3://bucket/v1/workflow-runs/<id>/
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at              TIMESTAMPTZ,                 -- set on INITIALIZING
    finished_at             TIMESTAMPTZ,                 -- set on terminal state
    error_summary           TEXT
);

-- Index for the reconciler: find non-terminal runs.
CREATE INDEX idx_runs_active ON runs (state)
    WHERE state NOT IN ('COMPLETE', 'EXECUTOR_ERROR', 'SYSTEM_ERROR', 'CANCELED', 'PREEMPTED');

-- Index for listing runs (most recent first).
CREATE INDEX idx_runs_created ON runs (created_at DESC);

-- ---------------------------------------------------------------------------
-- Tasks (TES)
-- ---------------------------------------------------------------------------

CREATE TABLE tasks (
    task_id             TEXT        PRIMARY KEY,
    run_id              TEXT        NOT NULL REFERENCES runs(run_id),
    engine_task_name    TEXT,                            -- e.g. Nextflow process name
    tes_task_id         TEXT,                            -- ID as known by the TES API
    state               ga4gh_state NOT NULL DEFAULT 'QUEUED',
    resources           JSONB,                           -- TES resources (cpu_cores, ram_gb, disk_gb, etc.)
    backend_ref         TEXT,                            -- K8s Job name, pod name, or other backend ID
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ,
    log_location        TEXT,                            -- S3 URI to task log prefix
    exit_code           INTEGER,                         -- from TES executor log (authoritative)
    attempt             INTEGER     NOT NULL DEFAULT 0   -- 0-based retry counter
);

-- Reconciler: find non-terminal tasks.
CREATE INDEX idx_tasks_active ON tasks (state)
    WHERE state NOT IN ('COMPLETE', 'EXECUTOR_ERROR', 'SYSTEM_ERROR', 'CANCELED', 'PREEMPTED');

-- Look up all tasks for a run.
CREATE INDEX idx_tasks_run_id ON tasks (run_id);

-- Look up task by TES ID (the TES API layer queries by this).
CREATE INDEX idx_tasks_tes_id ON tasks (tes_task_id) WHERE tes_task_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Events (append-only audit log)
-- ---------------------------------------------------------------------------

CREATE TABLE events (
    event_id    TEXT        PRIMARY KEY,
    run_id      TEXT        NOT NULL REFERENCES runs(run_id),
    task_id     TEXT        REFERENCES tasks(task_id),   -- NULL for run-level events
    type        TEXT        NOT NULL,                    -- e.g. "run.state_changed"
    payload     JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_events_run_id ON events (run_id, created_at);
CREATE INDEX idx_events_task_id ON events (task_id, created_at)
    WHERE task_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Updated_at trigger
-- ---------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_runs_updated_at
    BEFORE UPDATE ON runs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_tasks_updated_at
    BEFORE UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMIT;
