-- Admin -> Maintenance. Versions of the components the hub hears from (hub replicas, the
-- pricing service, webhook pods reported by each agent; the agent's own version stays in
-- clusters.agent_version) and the stored result of the check for a newer Kerf release, run by the
-- leader (or "Check now" on any replica). Additive: nothing else changes.

-- +goose Up
CREATE TABLE component_versions (
    component  text        NOT NULL,                -- hub | pricing | webhook
    cluster    text        NOT NULL DEFAULT '',     -- workload cluster (webhook); '' for hub-side components
    instance   text        NOT NULL,                -- pod name
    version    text        NOT NULL,
    started_at timestamptz,                         -- hub replicas: process start
    seen_at    timestamptz NOT NULL,
    PRIMARY KEY (component, cluster, instance)
);
CREATE INDEX component_versions_seen_at_idx ON component_versions (seen_at);

-- One row: the last update check.
CREATE TABLE update_check (
    id              smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    checked_at      timestamptz NOT NULL,
    current_version text        NOT NULL,           -- hub version the check ran for
    latest_version  text        NOT NULL DEFAULT '', -- kept from the last success when a check fails
    release_name    text        NOT NULL DEFAULT '',
    release_url     text        NOT NULL DEFAULT '',
    published_at    timestamptz,
    error           text        NOT NULL DEFAULT '', -- '' = the last check succeeded
    etag            text        NOT NULL DEFAULT '', -- conditional request (304 is free of rate limit)
    last_success_at timestamptz
);

-- +goose Down
DROP TABLE update_check;
DROP TABLE component_versions;
