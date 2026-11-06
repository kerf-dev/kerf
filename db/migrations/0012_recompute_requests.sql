-- Hub HA (D-028): "Recompute now" requests (POST /v1/admin/recompute) are stored here instead of in
-- the memory of the replica that got the request, so any hub replica can accept one and the leader
-- runs it. Additive: nothing else changes.

-- +goose Up
CREATE TABLE recompute_requests (
    id           uuid        PRIMARY KEY,               -- also the id of the recommender run that serves it
    cluster      text        NOT NULL DEFAULT '',       -- '' = every cluster
    requested_at timestamptz NOT NULL,
    requested_by text        NOT NULL DEFAULT '',
    started_at   timestamptz,                           -- set by the leader when the run starts
    finished_at  timestamptz,
    error        text        NOT NULL DEFAULT ''
);
-- At most one queued (not started) request per scope: a second request coalesces into it.
CREATE UNIQUE INDEX recompute_requests_queued_idx ON recompute_requests (cluster) WHERE started_at IS NULL;
CREATE INDEX recompute_requests_requested_at_idx ON recompute_requests (requested_at);

-- +goose Down
DROP TABLE recompute_requests;
