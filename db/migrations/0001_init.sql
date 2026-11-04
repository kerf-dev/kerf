-- resource-optimizer hub schema (WO-00 contract).
-- Migration tool: pressly/goose (SQL migrations, one file per version, Up and Down sections).
-- Later migrations (0002+) are owned by WO-04. Enumerations are text + CHECK so they can be
-- extended by a later migration without enum type gymnastics; values match api/openapi/hub.yaml.

-- +goose Up

-- Spoke clusters known to the hub. name equals the `cluster` metric label and the API path segment.
CREATE TABLE clusters (
    name           text        PRIMARY KEY CHECK (name ~ '^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$'),
    description    text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz,               -- last authenticated agent request
    agent_version  text
);

-- Bearer tokens (D-012). Only the SHA-256 of the token is stored.
-- role 'agent' is bound to one cluster; 'ui' (read-only) and 'admin' (everything) have cluster NULL.
CREATE TABLE api_tokens (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_sha256  bytea       NOT NULL UNIQUE CHECK (length(token_sha256) = 32),
    role          text        NOT NULL CHECK (role IN ('agent', 'ui', 'admin')),
    cluster       text        REFERENCES clusters (name) ON DELETE CASCADE,
    description   text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz,
    revoked_at    timestamptz,
    CHECK ((role = 'agent') = (cluster IS NOT NULL))
);

-- Workloads discovered from metrics (inventory, WO-04).
CREATE TABLE workloads (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    cluster        text        NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    namespace      text        NOT NULL,
    kind           text        NOT NULL CHECK (kind IN ('Deployment', 'StatefulSet', 'DaemonSet', 'CronJob')),
    name           text        NOT NULL,
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,                -- set when the workload disappears from metrics
    attributes     jsonb       NOT NULL DEFAULT '{}'::jsonb,  -- e.g. hpa, containers, labels
    UNIQUE (cluster, namespace, kind, name)
);

-- Recommendation history: one row per workload per recommender run. Never updated.
CREATE TABLE recommendations (
    id                          uuid        PRIMARY KEY,   -- WorkloadRecommendation.id
    workload_id                 bigint      NOT NULL REFERENCES workloads (id) ON DELETE CASCADE,
    run_id                      uuid        NOT NULL,      -- groups rows from one recommender run
    generated_at                timestamptz NOT NULL,
    strategy_name               text        NOT NULL,      -- e.g. 'percentile'
    settings                    jsonb       NOT NULL,      -- ValueSettings used (profile, cpuLimits, ...)
    containers                  jsonb       NOT NULL,      -- []ContainerRecommendation (API shape)
    confidence                  double precision NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    risk_flags                  text[]      NOT NULL DEFAULT '{}',
    data_coverage               jsonb       NOT NULL,      -- DataCoverage (API shape)
    average_replicas            double precision NOT NULL DEFAULT 0,
    monthly_savings_cpu_usd     double precision NOT NULL DEFAULT 0,
    monthly_savings_memory_usd  double precision NOT NULL DEFAULT 0,
    CHECK (risk_flags <@ ARRAY['throttled', 'oom-killed', 'restarting', 'hpa-coupled',
                               'sidecar', 'short-lived', 'low-confidence']::text[])
);
CREATE INDEX recommendations_workload_generated_idx ON recommendations (workload_id, generated_at DESC);
CREATE INDEX recommendations_run_idx ON recommendations (run_id);

-- Latest recommendation per workload (what GET /v1/clusters/{cluster}/recommendations serves).
CREATE VIEW latest_recommendations AS
SELECT DISTINCT ON (r.workload_id)
       w.cluster, w.namespace, w.kind, w.name, r.*
FROM recommendations r
JOIN workloads w ON w.id = r.workload_id
WHERE w.deleted_at IS NULL
ORDER BY r.workload_id, r.generated_at DESC;

-- Latest status reported by the agent per workload (POST .../workload-status). Upserted.
CREATE TABLE workload_status (
    workload_id         bigint      PRIMARY KEY REFERENCES workloads (id) ON DELETE CASCADE,
    recommendation_id   uuid,                      -- not a FK: history may be pruned
    policy              text        NOT NULL DEFAULT '',
    cpu_mode            text        NOT NULL CHECK (cpu_mode IN ('off', 'audit', 'auto-patch')),
    cpu_mode_source     text        CHECK (cpu_mode_source IN ('global-default', 'cluster-default', 'policy', 'annotation')),
    memory_mode         text        NOT NULL CHECK (memory_mode IN ('off', 'audit', 'auto-patch')),
    memory_mode_source  text        CHECK (memory_mode_source IN ('global-default', 'cluster-default', 'policy', 'annotation')),
    strategy            text        CHECK (strategy IN ('in-place', 'rollout', 'deferred')),
    settings            jsonb       NOT NULL DEFAULT '{}'::jsonb,  -- ValueSettings from the agent
    phase               text        NOT NULL CHECK (phase IN ('Pending', 'Applying', 'Applied', 'Blocked', 'RolledBack', 'Released')),
    reason              text        NOT NULL DEFAULT '',
    message             text        NOT NULL DEFAULT '',
    live                jsonb       NOT NULL DEFAULT '[]'::jsonb,  -- []ContainerResources
    applied_at          timestamptz,
    observed_at         timestamptz NOT NULL,      -- agent-side time
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Audit log (POST .../events, GET /v1/audit). Append-only.
-- Workload identity is denormalized so events survive workload deletion and arrive before inventory.
CREATE TABLE audit_events (
    id                 uuid        PRIMARY KEY,    -- Event.id from the agent (idempotency key)
    cluster            text        NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    namespace          text        NOT NULL,
    workload_kind      text        NOT NULL CHECK (workload_kind IN ('Deployment', 'StatefulSet', 'DaemonSet', 'CronJob')),
    workload_name      text        NOT NULL,
    workload_id        bigint      REFERENCES workloads (id) ON DELETE SET NULL,
    type               text        NOT NULL CHECK (type IN ('applied', 'skipped', 'blocked', 'fallback', 'rolled_back', 'released', 'failed')),
    reason             text        NOT NULL CHECK (reason ~ '^[A-Z][A-Za-z0-9]*$'),
    time               timestamptz NOT NULL,
    received_at        timestamptz NOT NULL DEFAULT now(),
    recommendation_id  uuid,
    strategy           text        CHECK (strategy IN ('in-place', 'rollout', 'deferred')),
    resources          text[]      NOT NULL DEFAULT '{}' CHECK (resources <@ ARRAY['cpu', 'memory']::text[]),
    containers         jsonb       NOT NULL DEFAULT '[]'::jsonb,  -- []ContainerChange
    message            text        NOT NULL DEFAULT '',
    details            jsonb       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX audit_events_cluster_time_idx  ON audit_events (cluster, time);
CREATE INDEX audit_events_workload_time_idx ON audit_events (cluster, namespace, workload_name, time);
CREATE INDEX audit_events_type_time_idx     ON audit_events (type, time);

-- +goose Down
DROP TABLE audit_events;
DROP TABLE workload_status;
DROP VIEW latest_recommendations;
DROP TABLE recommendations;
DROP TABLE workloads;
DROP TABLE api_tokens;
DROP TABLE clusters;
