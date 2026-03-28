-- Migration 002: Add task_request_payload to persist the full TES task body.
--
-- This is required for Batch C (TES K8s backend) so that the backend can
-- reconstruct the full K8s Job spec from the original TES request, and the
-- TES API can return FULL-view responses without re-deriving them.

BEGIN;

-- The full TES task body as submitted by the client. Used to:
-- 1. Build the K8s Job spec (executors, inputs, outputs, resources, volumes)
-- 2. Return FULL-view TES responses without re-derivation
-- 3. Audit trail of exactly what the client requested
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS task_request_payload JSONB;

-- Backend-specific state blob. For K8s backend: job name, pod name, pod phase,
-- container statuses, termination details. Updated by the reconciler.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS backend_state JSONB;

COMMIT;
