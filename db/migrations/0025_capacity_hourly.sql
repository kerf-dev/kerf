-- Capacity history: one row per cluster, node pool and hour with the node count, the pod requests
-- and the allocatable CPU and memory taken from the cluster's latest capacity report. The row with
-- node_pool = '' is the cluster total (nodes and pods outside any pool count only there). Every pool
-- the report lists gets a row, also with zero nodes, so a pool scaled to zero is a line at 0 and
-- not a gap. Read for the capacity trend of a cluster. Additive: nothing else changes.

-- +goose Up
CREATE TABLE capacity_hourly (
    cluster          text             NOT NULL REFERENCES clusters (name) ON DELETE CASCADE,
    node_pool        text             NOT NULL,  -- '' = cluster total
    hour             timestamptz      NOT NULL CHECK (extract(epoch FROM hour) % 3600 = 0),
    nodes            integer          NOT NULL,
    cpu_requested    double precision NOT NULL,  -- cores
    cpu_allocatable  double precision NOT NULL,  -- cores
    mem_requested    double precision NOT NULL,  -- bytes
    mem_allocatable  double precision NOT NULL,  -- bytes
    truncated        boolean          NOT NULL DEFAULT false,  -- the sampled report was truncated
    PRIMARY KEY (cluster, node_pool, hour)
);

CREATE INDEX capacity_hourly_cluster_hour_idx ON capacity_hourly (cluster, hour);

-- +goose Down
DROP TABLE capacity_hourly;
