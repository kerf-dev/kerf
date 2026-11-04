-- WO-19 (contract for WO-25, D-025): metrics datasources, per-cluster metrics bindings, and
-- cluster readiness (current check results, state, history). docs/contracts/readiness.md.
-- Idempotent: safe to re-run on a database that already has it.

-- +goose Up

-- Prometheus-compatible query endpoints. id 'local' is the built-in hub VictoriaMetrics, upserted
-- by the hub at start-up from prometheusURL; other ids are UUIDs (as text). Secrets (bearer token,
-- basic password, mTLS client key) are only in auth_secret, encrypted by the hub (key from a
-- Secret), and never returned by the API. Soft delete.
CREATE TABLE IF NOT EXISTS datasources (
    id           text        PRIMARY KEY CHECK (id = 'local' OR id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    name         text        NOT NULL CHECK (name ~ '^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$'),
    type         text        NOT NULL CHECK (type IN ('prometheus', 'victoriametrics', 'thanos', 'mimir', 'amp')),
    url          text        NOT NULL,
    auth_type    text        NOT NULL DEFAULT 'none' CHECK (auth_type IN ('none', 'bearer', 'basic', 'sigv4', 'mtls')),
    auth_config  jsonb       NOT NULL DEFAULT '{}'::jsonb,  -- non-secret: username, sigv4, caCert, clientCert, insecureSkipVerify
    auth_secret  bytea,                                     -- encrypted secret material
    headers      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    timeout      text,                                      -- Go duration
    max_points   integer     CHECK (max_points >= 100),
    built_in     boolean     NOT NULL DEFAULT false,
    version      integer     NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at   timestamptz NOT NULL DEFAULT now(),
    created_by   jsonb,                                     -- Actor; NULL for 'local'
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   jsonb,
    deleted_at   timestamptz,
    CHECK (built_in = (id = 'local'))
);
CREATE UNIQUE INDEX IF NOT EXISTS datasources_name_live_idx ON datasources (name) WHERE deleted_at IS NULL;

-- Metrics binding per cluster (MetricsBinding). No row = datasource 'local', label 'cluster' =
-- the cluster name (behaviour before D-025). cluster_label '' = no cluster matcher.
CREATE TABLE IF NOT EXISTS cluster_metrics_bindings (
    cluster              text        PRIMARY KEY REFERENCES clusters (name) ON DELETE CASCADE,
    datasource_id        text        NOT NULL REFERENCES datasources (id),
    cluster_label        text        NOT NULL DEFAULT 'cluster' CHECK (cluster_label ~ '^([a-zA-Z_][a-zA-Z0-9_]*)?$'),
    cluster_label_value  text,                              -- NULL = the cluster name
    extra_matchers       text[]      NOT NULL DEFAULT '{}',
    job_names            jsonb       NOT NULL DEFAULT '{}'::jsonb,  -- MetricsJobNames
    updated_at           timestamptz NOT NULL DEFAULT now(),
    updated_by           jsonb
);

-- Informational cluster metadata from the onboarding wizard.
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT '';
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS region text NOT NULL DEFAULT '';

-- Readiness state per cluster, and the pending re-check (ClusterConfig.flags.recheck).
CREATE TABLE IF NOT EXISTS cluster_readiness (
    cluster               text        PRIMARY KEY REFERENCES clusters (name) ON DELETE CASCADE,
    state                 text        NOT NULL DEFAULT 'Registered'
                          CHECK (state IN ('Registered', 'AgentConnected', 'MetricsOK', 'ReadyAudit', 'AutomationReady')),
    state_since           timestamptz NOT NULL DEFAULT now(),
    checked_at            timestamptz,
    recheck_requested_at  timestamptz,
    capabilities          jsonb       NOT NULL DEFAULT '[]'::jsonb   -- []AgentCapability from the agent
);

-- Latest result per cluster and check (ReadinessCheck, API shape in columns).
CREATE TABLE IF NOT EXISTS readiness_checks (
    cluster           text        NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    check_id          text        NOT NULL CHECK (check_id ~ '^[a-z]+\.[a-z0-9-]+$'),
    source            text        NOT NULL CHECK (source IN ('hub', 'agent')),
    category          text        NOT NULL,
    severity          text        NOT NULL CHECK (severity IN ('blocking', 'warning', 'info')),
    status            text        NOT NULL CHECK (status IN ('pass', 'warn', 'fail', 'unknown')),
    message           text        NOT NULL DEFAULT '',
    remediation       text        NOT NULL DEFAULT '',
    remediation_type  text        CHECK (remediation_type IN ('command', 'yaml', 'text')),
    docs_url          text        NOT NULL DEFAULT '',
    details           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    checked_at        timestamptz NOT NULL,
    PRIMARY KEY (cluster, check_id)
);

-- Changes only: a state transition or a check's status change. Append-only.
CREATE TABLE IF NOT EXISTS readiness_history (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    cluster      text        NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    time         timestamptz NOT NULL DEFAULT now(),
    kind         text        NOT NULL CHECK (kind IN ('state', 'check')),
    from_state   text,
    to_state     text,
    check_id     text,
    from_status  text,
    to_status    text,
    message      text        NOT NULL DEFAULT '',
    CHECK ((kind = 'state') = (to_state IS NOT NULL)),
    CHECK ((kind = 'check') = (check_id IS NOT NULL AND to_status IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS readiness_history_cluster_time_idx ON readiness_history (cluster, time DESC);

-- Config audit: datasource changes and readiness transitions (docs/contracts/events.md).
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_target_type_check
    CHECK (target_type IN ('settings', 'policy', 'workload-override', 'approval', 'cluster', 'automation', 'token', 'user',
                           'datasource', 'readiness'));

-- +goose Down
DELETE FROM audit_events WHERE type = 'config_changed' AND target_type IN ('datasource', 'readiness');
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_target_type_check
    CHECK (target_type IN ('settings', 'policy', 'workload-override', 'approval', 'cluster', 'automation', 'token', 'user'));
DROP TABLE IF EXISTS readiness_history;
DROP TABLE IF EXISTS readiness_checks;
DROP TABLE IF EXISTS cluster_readiness;
ALTER TABLE clusters DROP COLUMN IF EXISTS region;
ALTER TABLE clusters DROP COLUMN IF EXISTS provider;
DROP TABLE IF EXISTS cluster_metrics_bindings;
DROP TABLE IF EXISTS datasources;
