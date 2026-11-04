-- WO-10: deferred strategy. New workload phase Deferred and the next maintenance window.
-- Idempotent: safe to re-run on a database that already has it.

-- +goose Up
ALTER TABLE workload_status DROP CONSTRAINT IF EXISTS workload_status_phase_check;
ALTER TABLE workload_status ADD CONSTRAINT workload_status_phase_check
    CHECK (phase IN ('Pending', 'Applying', 'Applied', 'Blocked', 'RolledBack', 'Released', 'Deferred'));
ALTER TABLE workload_status ADD COLUMN IF NOT EXISTS next_window timestamptz;

-- +goose Down
ALTER TABLE workload_status DROP COLUMN IF EXISTS next_window;
UPDATE workload_status SET phase = 'Blocked' WHERE phase = 'Deferred';
ALTER TABLE workload_status DROP CONSTRAINT IF EXISTS workload_status_phase_check;
ALTER TABLE workload_status ADD CONSTRAINT workload_status_phase_check
    CHECK (phase IN ('Pending', 'Applying', 'Applied', 'Blocked', 'RolledBack', 'Released'));
