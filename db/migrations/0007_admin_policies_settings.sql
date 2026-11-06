-- Admin area: hub-only settings with versions, hub-managed policies with versions,
-- cluster metadata, automation (pause) and the agents' config sync and policy inventory.
-- API shapes: api/openapi/hub.yaml; flow: docs/contracts/admin.md.
-- Idempotent: safe to re-run on a database that already has it.

-- +goose Up

-- Current value of each hub-only settings section (RecommenderSettings / PricingSettings, API
-- shape). A section without a row uses the hub configuration file (API version 0).
CREATE TABLE IF NOT EXISTS settings (
    section     text        PRIMARY KEY CHECK (section IN ('recommender', 'pricing')),
    version     integer     NOT NULL CHECK (version >= 1),
    value       jsonb       NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  jsonb       NOT NULL,             -- Actor
    reason      text        NOT NULL DEFAULT ''
);

-- Every saved version of a settings section (append-only; the latest equals settings).
CREATE TABLE IF NOT EXISTS settings_versions (
    section     text        NOT NULL CHECK (section IN ('recommender', 'pricing')),
    version     integer     NOT NULL CHECK (version >= 1),
    value       jsonb       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    actor       jsonb       NOT NULL,             -- Actor
    reason      text        NOT NULL DEFAULT '',
    PRIMARY KEY (section, version)
);

-- Hub-managed policies (AdminPolicy). Never hard-deleted: deleted_at marks a soft delete.
-- spec is RightsizingPolicySpec (API/CRD shape) including selector and priority; the priority
-- column duplicates spec.priority for ordering. clusters empty = every cluster.
CREATE TABLE IF NOT EXISTS policies (
    id                  uuid        PRIMARY KEY,
    type                text        NOT NULL CHECK (type IN ('global-default', 'cluster-default', 'policy', 'workload-override')),
    name                text        NOT NULL CHECK (length(name) <= 253 AND name ~ '^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$'),
    description         text        NOT NULL DEFAULT '',
    clusters            text[]      NOT NULL DEFAULT '{}',
    -- the workload of a workload-override (denormalized, like audit_events)
    workload_cluster    text,
    workload_namespace  text,
    workload_kind       text        CHECK (workload_kind IN ('Deployment', 'StatefulSet', 'DaemonSet', 'CronJob')),
    workload_name       text,
    priority            integer     NOT NULL CHECK (priority BETWEEN 0 AND 1000000),
    spec                jsonb       NOT NULL,
    version             integer     NOT NULL CHECK (version >= 1),
    created_at          timestamptz NOT NULL DEFAULT now(),
    created_by          jsonb       NOT NULL,     -- Actor
    updated_at          timestamptz NOT NULL DEFAULT now(),
    updated_by          jsonb       NOT NULL,     -- Actor
    deleted_at          timestamptz,
    -- reserved names and priority bands (api/v1alpha1/wellknown.go)
    CHECK (type <> 'global-default'    OR (name = 'ro-global-defaults' AND priority = 0 AND clusters = '{}')),
    CHECK (type <> 'cluster-default'   OR (name = 'ro-cluster-defaults' AND priority = 10 AND cardinality(clusters) = 1)),
    CHECK (type <> 'policy'            OR (priority BETWEEN 1 AND 899999 AND name !~ '^ro-')),
    CHECK (type <> 'workload-override' OR (priority = 900000 AND name ~ '^ro-override-[0-9a-f]{16}$')),
    CHECK ((type = 'workload-override') = (workload_name IS NOT NULL)),
    CHECK (type <> 'workload-override' OR (workload_cluster IS NOT NULL AND workload_namespace IS NOT NULL
                                          AND workload_kind IS NOT NULL AND clusters = ARRAY[workload_cluster]))
);
-- At most one live global default, one live cluster default per cluster, one live override per
-- workload, and unique live policy names.
CREATE UNIQUE INDEX IF NOT EXISTS policies_global_default_live_idx
    ON policies (type) WHERE type = 'global-default' AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS policies_cluster_default_live_idx
    ON policies ((clusters[1])) WHERE type = 'cluster-default' AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS policies_override_live_idx
    ON policies (workload_cluster, workload_namespace, workload_kind, workload_name)
    WHERE type = 'workload-override' AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS policies_name_live_idx
    ON policies (name) WHERE type = 'policy' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS policies_clusters_idx ON policies USING gin (clusters);

-- Every version of a policy (append-only). A delete is a version with deleted = true.
CREATE TABLE IF NOT EXISTS policy_versions (
    policy_id      uuid        NOT NULL REFERENCES policies (id) ON DELETE CASCADE,
    version        integer     NOT NULL CHECK (version >= 1),
    operation      text        NOT NULL CHECK (operation IN ('create', 'update', 'delete', 'revert')),
    reverted_from  integer,
    name           text        NOT NULL,
    description    text        NOT NULL DEFAULT '',
    clusters       text[]      NOT NULL DEFAULT '{}',
    priority       integer     NOT NULL,
    spec           jsonb       NOT NULL,
    deleted        boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    actor          jsonb       NOT NULL,          -- Actor
    reason         text        NOT NULL DEFAULT '',
    PRIMARY KEY (policy_id, version),
    CHECK ((operation = 'revert') = (reverted_from IS NOT NULL))
);

-- Cluster metadata (ClusterAdmin) and the agent's config sync / policy inventory.
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS environment text CHECK (environment IN ('prod', 'non-prod'));
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS owner text NOT NULL DEFAULT '';
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS policy_source text NOT NULL DEFAULT 'both'
    CHECK (policy_source IN ('hub', 'git', 'both'));
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS updated_at timestamptz;
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS updated_by jsonb;             -- Actor
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS deleted_at timestamptz;       -- soft delete
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS config_pulled_at timestamptz; -- last GET .../config
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS applied_config_version text;  -- WorkloadStatusBatch.configVersion
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS policy_inventory jsonb;       -- []PolicyInventoryItem
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS policy_inventory_at timestamptz;

-- Pause state per cluster (scope = cluster name) and fleet-wide (scope = '*'). No row = not paused.
-- No foreign key, so the fleet row fits; rows of retired clusters are harmless.
CREATE TABLE IF NOT EXISTS cluster_automation (
    scope       text        PRIMARY KEY CHECK (scope = '*' OR scope ~ '^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$'),
    paused      boolean     NOT NULL DEFAULT false,
    reason      text        NOT NULL DEFAULT '',
    changed_by  jsonb,                            -- Actor
    changed_at  timestamptz NOT NULL DEFAULT now(),
    resume_at   timestamptz,
    version     integer     NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (resume_at IS NULL OR paused)
);

-- +goose Down
DROP TABLE IF EXISTS cluster_automation;
ALTER TABLE clusters DROP COLUMN IF EXISTS policy_inventory_at;
ALTER TABLE clusters DROP COLUMN IF EXISTS policy_inventory;
ALTER TABLE clusters DROP COLUMN IF EXISTS applied_config_version;
ALTER TABLE clusters DROP COLUMN IF EXISTS config_pulled_at;
ALTER TABLE clusters DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE clusters DROP COLUMN IF EXISTS updated_by;
ALTER TABLE clusters DROP COLUMN IF EXISTS updated_at;
ALTER TABLE clusters DROP COLUMN IF EXISTS version;
ALTER TABLE clusters DROP COLUMN IF EXISTS policy_source;
ALTER TABLE clusters DROP COLUMN IF EXISTS owner;
ALTER TABLE clusters DROP COLUMN IF EXISTS environment;
DROP TABLE IF EXISTS policy_versions;
DROP TABLE IF EXISTS policies;
DROP TABLE IF EXISTS settings_versions;
DROP TABLE IF EXISTS settings;
