-- WO-04e: prune workloads deleted on agent-backed clusters.
-- clusters.agent_pull_at / agent_prev_pull_at: the last two recommendation pulls of the cluster's
-- agent (hub clock); a full status batch follows every pull. workloads.deleted_by: what marked a
-- workload deleted ('metrics': gone from the inventory; 'agent': absent from the agent's full
-- status batches and without live metrics).
-- Idempotent: safe to re-run on a database that already has it.

-- +goose Up
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS agent_pull_at timestamptz;
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS agent_prev_pull_at timestamptz;
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS deleted_by text;
ALTER TABLE workloads DROP CONSTRAINT IF EXISTS workloads_deleted_by_check;
ALTER TABLE workloads ADD CONSTRAINT workloads_deleted_by_check CHECK (deleted_by IN ('metrics', 'agent'));
UPDATE workloads SET deleted_by = 'metrics' WHERE deleted_at IS NOT NULL AND deleted_by IS NULL;

-- +goose Down
ALTER TABLE workloads DROP CONSTRAINT IF EXISTS workloads_deleted_by_check;
ALTER TABLE workloads DROP COLUMN IF EXISTS deleted_by;
ALTER TABLE clusters DROP COLUMN IF EXISTS agent_prev_pull_at;
ALTER TABLE clusters DROP COLUMN IF EXISTS agent_pull_at;
