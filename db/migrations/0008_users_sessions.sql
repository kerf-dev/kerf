-- Admin area (D-023): local users, browser sessions, and API token bookkeeping.
-- Idempotent: safe to re-run on a database that already has it.

-- +goose Up

-- Local users. password_hash is bcrypt. Never hard-deleted (deleted_at), so usernames in the audit
-- trail stay unambiguous. The initial admin is bootstrapped by the hub from a Secret.
CREATE TABLE IF NOT EXISTS users (
    username              text        PRIMARY KEY CHECK (username ~ '^[a-z0-9][a-z0-9._-]{1,62}$'),
    display_name          text        NOT NULL DEFAULT '',
    role                  text        NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    password_hash         text        NOT NULL,
    disabled              boolean     NOT NULL DEFAULT false,
    must_change_password  boolean     NOT NULL DEFAULT true,
    failed_logins         integer     NOT NULL DEFAULT 0 CHECK (failed_logins >= 0),
    locked_until          timestamptz,
    last_login_at         timestamptz,
    password_changed_at   timestamptz NOT NULL DEFAULT now(),
    created_at            timestamptz NOT NULL DEFAULT now(),
    created_by            text,                  -- actor name; NULL for the bootstrap admin
    updated_at            timestamptz NOT NULL DEFAULT now(),
    version               integer     NOT NULL DEFAULT 1 CHECK (version >= 1),
    deleted_at            timestamptz
);

-- Browser sessions (cookie kerf_session). Only the SHA-256 of the session token is stored. The CSRF
-- token is returned by login and GET /v1/me, so it is kept as is (it is useless without the cookie).
CREATE TABLE IF NOT EXISTS sessions (
    id_sha256     bytea       PRIMARY KEY CHECK (length(id_sha256) = 32),
    username      text        NOT NULL REFERENCES users (username) ON DELETE CASCADE,
    csrf_token    text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    user_agent    text        NOT NULL DEFAULT '',
    remote_addr   text        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_username_idx ON sessions (username);
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions (expires_at);

-- API tokens (TokenInfo): role and expires_at exist since 0001.
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS last_used_at timestamptz;  -- updated at most once a minute
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS created_by text;           -- actor name; NULL when seeded
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS revoked_by text;           -- actor name

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN IF EXISTS revoked_by;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS created_by;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS last_used_at;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
