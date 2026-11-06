-- Recommender run bookkeeping and pricing provenance.

-- +goose Up

-- One row per recommender run (startup, interval, or a dev history backfill).
CREATE TABLE recommender_runs (
    id           uuid        PRIMARY KEY,
    started_at   timestamptz NOT NULL,
    finished_at  timestamptz,
    as_of        timestamptz NOT NULL,          -- end of the usage window
    backfill     boolean     NOT NULL DEFAULT false,
    status       text        NOT NULL CHECK (status IN ('running', 'succeeded', 'partial', 'failed')),
    error        text        NOT NULL DEFAULT ''
);
CREATE INDEX recommender_runs_as_of_idx ON recommender_runs (as_of);

-- Per-cluster outcome of a run. Totals cover the workloads live at as_of, so the savings trend
-- (GET /v1/savings/trend) is the latest row per cluster at each step.
CREATE TABLE recommender_run_clusters (
    run_id                      uuid        NOT NULL REFERENCES recommender_runs (id) ON DELETE CASCADE,
    cluster                     text        NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    as_of                       timestamptz NOT NULL,
    finished_at                 timestamptz NOT NULL,
    status                      text        NOT NULL CHECK (status IN ('succeeded', 'failed')),
    error                       text        NOT NULL DEFAULT '',
    workloads                   integer     NOT NULL DEFAULT 0,
    recommendations             integer     NOT NULL DEFAULT 0,
    monthly_savings_cpu_usd     double precision NOT NULL DEFAULT 0,
    monthly_savings_memory_usd  double precision NOT NULL DEFAULT 0,
    pricing                     jsonb       NOT NULL DEFAULT '{}'::jsonb,  -- rates and source used
    PRIMARY KEY (run_id, cluster)
);
CREATE INDEX recommender_run_clusters_cluster_as_of_idx ON recommender_run_clusters (cluster, as_of);

-- Rates used for the savings of each recommendation; source 'default' marks the fallback rate.
ALTER TABLE recommendations ADD COLUMN pricing jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX audit_events_workload_id_time_idx ON audit_events (workload_id, time);

-- +goose Down
DROP INDEX audit_events_workload_id_time_idx;
ALTER TABLE recommendations DROP COLUMN pricing;
DROP TABLE recommender_run_clusters;
DROP TABLE recommender_runs;
