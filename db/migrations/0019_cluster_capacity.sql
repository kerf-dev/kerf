-- Node capacity: the latest capacity report of each cluster's agent (nodes, node pools, pod
-- requests grouped by shape, consolidation blockers), read by GET /v1/clusters/{cluster}/capacity
-- to estimate the nodes needed after the open recommendations. One row per cluster, replaced on
-- every report. Additive: nothing else changes.

-- +goose Up
CREATE TABLE cluster_capacity (
    cluster     text        PRIMARY KEY REFERENCES clusters (name) ON DELETE CASCADE,
    reported_at timestamptz NOT NULL,   -- when the hub received the report
    observed_at timestamptz NOT NULL,   -- when the agent built it
    report      jsonb       NOT NULL    -- hub API CapacityReport
);

-- +goose Down
DROP TABLE cluster_capacity;
