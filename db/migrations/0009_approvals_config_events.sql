-- Admin area: approve-once grants, and configuration changes in the audit
-- log (type config_changed, docs/contracts/events.md).
-- Idempotent: safe to re-run on a database that already has it.

-- +goose Up

-- Approve-once grants (Approval / ConfigApproval). State: consumed_at set = consumed (by the
-- agent's applied/ApprovedOnce event), superseded_at set = replaced by a newer approval or a
-- newer recommendation, expires_at passed = expired, otherwise pending.
CREATE TABLE IF NOT EXISTS workload_approvals (
    id                 uuid        PRIMARY KEY,
    cluster            text        NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    namespace          text        NOT NULL,
    kind               text        NOT NULL CHECK (kind IN ('Deployment', 'StatefulSet', 'DaemonSet', 'CronJob')),
    name               text        NOT NULL,
    workload_id        bigint      REFERENCES workloads (id) ON DELETE SET NULL,
    recommendation_id  uuid        NOT NULL,       -- not a FK: history may be pruned
    approved_by        jsonb       NOT NULL,       -- Actor
    approved_at        timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    reason             text        NOT NULL,
    consumed_at        timestamptz,
    consumed_event_id  uuid,
    superseded_at      timestamptz,
    CHECK (expires_at > approved_at)
);
-- At most one open (not consumed, not superseded) approval per workload.
CREATE UNIQUE INDEX IF NOT EXISTS workload_approvals_open_idx
    ON workload_approvals (cluster, namespace, kind, name)
    WHERE consumed_at IS NULL AND superseded_at IS NULL;
CREATE INDEX IF NOT EXISTS workload_approvals_cluster_idx ON workload_approvals (cluster, expires_at);

-- audit_events: type config_changed (ConfigChange). For these rows reason = the action
-- (e.g. PolicyUpdated), message = the user's reason, cluster/namespace/workload_* are set when the
-- target belongs to a cluster / workload (NULL otherwise), plus actor, target and before/after.
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_type_check
    CHECK (type IN ('applied', 'skipped', 'blocked', 'fallback', 'rolled_back', 'released', 'failed', 'config_changed'));
ALTER TABLE audit_events ALTER COLUMN cluster DROP NOT NULL;
ALTER TABLE audit_events ALTER COLUMN namespace DROP NOT NULL;
ALTER TABLE audit_events ALTER COLUMN workload_kind DROP NOT NULL;
ALTER TABLE audit_events ALTER COLUMN workload_name DROP NOT NULL;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS actor jsonb;         -- Actor
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS target_type text;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS target_id text;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS target_name text;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS version integer;     -- target version after the change
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS before jsonb;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS after jsonb;
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_target_type_check
    CHECK (target_type IN ('settings', 'policy', 'workload-override', 'approval', 'cluster', 'automation', 'token', 'user'));
-- Workload events keep every workload column; config changes need actor and target.
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_shape_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_shape_check CHECK (
    (type = 'config_changed' AND actor IS NOT NULL AND target_type IS NOT NULL AND target_id IS NOT NULL)
    OR (type <> 'config_changed' AND cluster IS NOT NULL AND namespace IS NOT NULL
        AND workload_kind IS NOT NULL AND workload_name IS NOT NULL));
CREATE INDEX IF NOT EXISTS audit_events_config_time_idx
    ON audit_events (time) WHERE type = 'config_changed';
CREATE INDEX IF NOT EXISTS audit_events_config_target_idx
    ON audit_events (target_type, target_id, time) WHERE type = 'config_changed';

-- +goose Down
DROP INDEX IF EXISTS audit_events_config_target_idx;
DROP INDEX IF EXISTS audit_events_config_time_idx;
DELETE FROM audit_events WHERE type = 'config_changed';
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_shape_check;
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_type_check;
ALTER TABLE audit_events DROP COLUMN IF EXISTS after;
ALTER TABLE audit_events DROP COLUMN IF EXISTS before;
ALTER TABLE audit_events DROP COLUMN IF EXISTS version;
ALTER TABLE audit_events DROP COLUMN IF EXISTS target_name;
ALTER TABLE audit_events DROP COLUMN IF EXISTS target_id;
ALTER TABLE audit_events DROP COLUMN IF EXISTS target_type;
ALTER TABLE audit_events DROP COLUMN IF EXISTS actor;
ALTER TABLE audit_events ALTER COLUMN workload_name SET NOT NULL;
ALTER TABLE audit_events ALTER COLUMN workload_kind SET NOT NULL;
ALTER TABLE audit_events ALTER COLUMN namespace SET NOT NULL;
ALTER TABLE audit_events ALTER COLUMN cluster SET NOT NULL;
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_type_check
    CHECK (type IN ('applied', 'skipped', 'blocked', 'fallback', 'rolled_back', 'released', 'failed'));
DROP TABLE IF EXISTS workload_approvals;
