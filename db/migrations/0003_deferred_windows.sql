-- WO-10: deferred strategy. New workload phase Deferred and the next maintenance window.

-- +goose Up
ALTER TABLE workload_status DROP CONSTRAINT IF EXISTS workload_status_phase_check;
ALTER TABLE workload_status ADD CONSTRAINT workload_status_phase_check
    CHECK (phase IN ('Pending', 'Applying', 'Applied', 'Blocked', 'RolledBack', 'Released', 'Deferred'));
ALTER TABLE workload_status ADD COLUMN next_window timestamptz;

-- +goose Down
ALTER TABLE workload_status DROP COLUMN next_window;
UPDATE workload_status SET phase = 'Blocked' WHERE phase = 'Deferred';
ALTER TABLE workload_status DROP CONSTRAINT IF EXISTS workload_status_phase_check;
ALTER TABLE workload_status ADD CONSTRAINT workload_status_phase_check
    CHECK (phase IN ('Pending', 'Applying', 'Applied', 'Blocked', 'RolledBack', 'Released'));
