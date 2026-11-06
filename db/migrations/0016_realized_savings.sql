-- Realized savings (docs/realized-savings.md): what the changes Kerf applied actually released,
-- materialised per workload and hour by the hub leader from the stored audit events (applied,
-- rolled_back, released), the replicas that ran and the rates of the time. Kept apart from the
-- estimated savings of the recommendations. Plus the per-cluster node cost trend drawn next to it.
-- Additive: nothing else changes.

-- +goose Up

-- One row per workload and UTC hour in which some request was released (or raised). Workload
-- identity is denormalized like audit_events, so the history survives the workload's deletion.
CREATE TABLE realized_savings_hourly (
    cluster              text             NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    namespace            text             NOT NULL,
    workload_kind        text             NOT NULL CHECK (workload_kind IN ('Deployment', 'StatefulSet', 'DaemonSet', 'CronJob')),
    workload_name        text             NOT NULL,
    hour                 timestamptz      NOT NULL CHECK (extract(epoch FROM hour) % 3600 = 0),
    -- Requests released per replica, summed over containers, time-weighted over the hour.
    -- Negative when Kerf raised them above where they were before its first change.
    cpu_cores            double precision NOT NULL,
    memory_bytes         double precision NOT NULL,
    replicas             double precision NOT NULL CHECK (replicas >= 0),
    replicas_source      text             NOT NULL CHECK (replicas_source IN ('metrics', 'recommendation', 'none')),
    cpu_core_hour_usd    double precision NOT NULL,
    memory_gib_hour_usd  double precision NOT NULL,
    pricing_source       text             NOT NULL CHECK (pricing_source IN ('metrics', 'default')),
    cpu_usd              double precision NOT NULL,  -- cpu_cores x replicas x cpu_core_hour_usd
    memory_usd           double precision NOT NULL,  -- memory_bytes / 2^30 x replicas x memory_gib_hour_usd
    PRIMARY KEY (cluster, namespace, workload_kind, workload_name, hour)
);
CREATE INDEX realized_savings_hourly_cluster_hour_idx ON realized_savings_hourly (cluster, hour);

-- Per cluster and UTC hour: cost of the priced nodes and requested / allocatable capacity, hourly
-- averages. NULL when the metrics had no data for that hour.
CREATE TABLE node_cost_hourly (
    cluster                 text             NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    hour                    timestamptz      NOT NULL CHECK (extract(epoch FROM hour) % 3600 = 0),
    node_cost_usd           double precision,  -- sum of kerf_node_hourly_cost_usd (USD for the hour)
    nodes                   double precision,
    cpu_requested_ratio     double precision,  -- scheduled pods' CPU requests / allocatable CPU
    memory_requested_ratio  double precision,
    PRIMARY KEY (cluster, hour)
);

-- Progress of the leader's incremental computation per cluster.
CREATE TABLE realized_savings_state (
    cluster             text        PRIMARY KEY REFERENCES clusters (name) ON DELETE CASCADE,
    computed_through    timestamptz,           -- realized_savings_hourly is complete before this hour
    events_through      timestamptz NOT NULL,  -- audit events received up to here are reflected
    node_cost_through   timestamptz,           -- node_cost_hourly is complete before this hour
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- The ledger reads a cluster's request-changing events in time order.
CREATE INDEX audit_events_ledger_idx ON audit_events (cluster, time)
    WHERE type IN ('applied', 'rolled_back', 'released');

-- +goose Down
DROP INDEX IF EXISTS audit_events_ledger_idx;
DROP TABLE realized_savings_state;
DROP TABLE node_cost_hourly;
DROP TABLE realized_savings_hourly;
