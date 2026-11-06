-- Notifications: Slack, Microsoft Teams and signed webhook channels, the rules that route events to
-- them, the weekly digest settings and the delivery queue (with its history). Additive: nothing
-- else changes, except that configuration changes may now target 'notification' objects in the
-- audit log (docs/contracts/events.md).

-- +goose Up

-- A destination. The webhook URL (a secret for Slack and Teams) and the HMAC signing secret of a
-- generic webhook are sealed together in secret_enc (AES-256-GCM, the datasource key); url_hint is
-- the masked URL the API returns.
CREATE TABLE notification_channels (
    id               uuid        PRIMARY KEY,
    name             text        NOT NULL,
    type             text        NOT NULL CHECK (type IN ('slack', 'teams', 'webhook')),
    source           text        NOT NULL DEFAULT 'api' CHECK (source IN ('api', 'config')),
    enabled          boolean     NOT NULL DEFAULT true,
    secret_enc       bytea       NOT NULL,
    url_hint         text        NOT NULL,
    has_secret       boolean     NOT NULL DEFAULT false,
    version          integer     NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    created_by       jsonb,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    updated_by       jsonb,
    deleted_at       timestamptz,
    -- Last delivery attempt of any kind (event, digest, test), for the channel list.
    last_status      text        CHECK (last_status IN ('sent', 'failed', 'retrying')),
    last_attempt_at  timestamptz,
    last_error       text        NOT NULL DEFAULT '',
    last_success_at  timestamptz
);
CREATE UNIQUE INDEX notification_channels_name_live_idx ON notification_channels (name) WHERE deleted_at IS NULL;

-- Which events go to which channels. Empty clusters / namespaces match everything; entries are
-- glob patterns ('prod-*'). An event without a namespace (readiness, automation) never matches a
-- rule that lists namespaces.
CREATE TABLE notification_rules (
    id           uuid        PRIMARY KEY,
    name         text        NOT NULL,
    enabled      boolean     NOT NULL DEFAULT true,
    event_types  text[]      NOT NULL CHECK (cardinality(event_types) > 0 AND event_types <@ ARRAY[
                     'applied', 'rolled_back', 'failed', 'blocked', 'readiness_degraded', 'automation_paused', 'automation_resumed']::text[]),
    clusters     text[]      NOT NULL DEFAULT '{}',
    namespaces   text[]      NOT NULL DEFAULT '{}',
    channel_ids  uuid[]      NOT NULL CHECK (cardinality(channel_ids) > 0),
    version      integer     NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    created_by   jsonb,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   jsonb,
    deleted_at   timestamptz
);
CREATE UNIQUE INDEX notification_rules_name_live_idx ON notification_rules (name) WHERE deleted_at IS NULL;

-- The weekly digest: one row.
CREATE TABLE notification_digest (
    id           smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled      boolean     NOT NULL DEFAULT false,
    weekday      smallint    NOT NULL DEFAULT 1 CHECK (weekday BETWEEN 0 AND 6),   -- 0 = Sunday
    time_of_day  text        NOT NULL DEFAULT '09:00' CHECK (time_of_day ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    timezone     text        NOT NULL DEFAULT 'UTC',
    channel_ids  uuid[]      NOT NULL DEFAULT '{}',
    version      integer     NOT NULL DEFAULT 1,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   jsonb,
    -- The scheduled time (UTC) of the last digest that was queued: it is never queued twice.
    last_period  timestamptz
);
INSERT INTO notification_digest (id) VALUES (1);

-- The delivery queue and its history. payload is the neutral, secret-free message
-- (internal/hub/notify); it is rendered for the channel type on every attempt.
CREATE TABLE notification_deliveries (
    id               uuid        PRIMARY KEY,
    channel_id       uuid        NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    kind             text        NOT NULL CHECK (kind IN ('event', 'digest', 'test')),
    event_id         uuid,                       -- audit_events.id of an event delivery
    event_type       text        NOT NULL DEFAULT '',
    dedupe_key       text        NOT NULL DEFAULT '',
    summary          text        NOT NULL DEFAULT '',
    payload          jsonb       NOT NULL,
    status           text        NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts         integer     NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL,
    last_error       text        NOT NULL DEFAULT '',
    last_http_status integer,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_attempt_at  timestamptz,
    sent_at          timestamptz
);
-- One delivery per event and channel, however many rules match or how often the event is seen.
CREATE UNIQUE INDEX notification_deliveries_event_idx ON notification_deliveries (channel_id, event_id) WHERE event_id IS NOT NULL;
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX notification_deliveries_channel_idx ON notification_deliveries (channel_id, created_at DESC);
CREATE INDEX notification_deliveries_dedupe_idx ON notification_deliveries (channel_id, dedupe_key, created_at DESC) WHERE dedupe_key <> '';

-- Where the leader stopped reading audit_events (received_at), and the floor below which it never
-- reads: events older than the moment notifications were first started (or the last moment no rule
-- existed) are not sent.
CREATE TABLE notification_cursor (
    id          smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    received_at timestamptz NOT NULL,
    floor_at    timestamptz NOT NULL
);

-- The leader reads new events by when the hub stored them.
CREATE INDEX audit_events_received_at_idx ON audit_events (received_at);

-- Config audit: notification changes (docs/contracts/events.md).
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_target_type_check
    CHECK (target_type IN ('settings', 'policy', 'workload-override', 'approval', 'cluster', 'automation', 'token', 'user',
                           'datasource', 'readiness', 'notification'));

-- +goose Down
DELETE FROM audit_events WHERE type = 'config_changed' AND target_type = 'notification';
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_target_type_check
    CHECK (target_type IN ('settings', 'policy', 'workload-override', 'approval', 'cluster', 'automation', 'token', 'user',
                           'datasource', 'readiness'));
DROP INDEX IF EXISTS audit_events_received_at_idx;
DROP TABLE IF EXISTS notification_cursor;
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_digest;
DROP TABLE IF EXISTS notification_rules;
DROP TABLE IF EXISTS notification_channels;
