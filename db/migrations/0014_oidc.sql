-- Sign in with OpenID Connect. Users signed in through the identity provider get a user record
-- (source 'oidc') named after their e-mail address, linked to the provider's (issuer, subject).
-- Their sessions keep the refresh token (encrypted) so the role is re-read from the provider.
-- Additive for local users and sessions.

-- +goose Up

-- IdP usernames are e-mail addresses ('@', '+'); local usernames keep their stricter API rule.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_username_check;
ALTER TABLE users ADD CONSTRAINT users_username_check CHECK (username ~ '^[a-z0-9][a-z0-9._@+-]{1,126}$');
ALTER TABLE users ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'local' CHECK (source IN ('local', 'oidc'));
ALTER TABLE users ADD COLUMN IF NOT EXISTS email text;

-- The provider identity of an 'oidc' user. One user per (issuer, subject).
CREATE TABLE IF NOT EXISTS user_identities (
    issuer         text        NOT NULL,
    subject        text        NOT NULL,
    username       text        NOT NULL UNIQUE REFERENCES users (username) ON DELETE CASCADE,
    email          text        NOT NULL DEFAULT '',
    groups         text[]      NOT NULL DEFAULT '{}',   -- from the last sign-in or refresh
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_login_at  timestamptz,
    PRIMARY KEY (issuer, subject)
);

-- OIDC sessions: the refresh token (AES-GCM, key derived from the client secret, the session id hash
-- as associated data), when the role was last re-read, and a short lease so one request refreshes.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS oidc_issuer         text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS oidc_subject        text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS oidc_refresh_token  bytea;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS oidc_refreshed_at   timestamptz;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS oidc_refresh_lease  timestamptz;

-- Sign-ins in progress (between the redirect to the provider and its callback), at most 10 minutes.
-- Keyed by the SHA-256 of the state; the browser holds the state in the kerf_oidc cookie.
CREATE TABLE IF NOT EXISTS oidc_logins (
    state_sha256   bytea       PRIMARY KEY CHECK (length(state_sha256) = 32),
    nonce          text        NOT NULL,
    code_verifier  bytea       NOT NULL,                -- encrypted like the refresh token
    next_path      text        NOT NULL DEFAULT '/',
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS oidc_logins_expires_at_idx ON oidc_logins (expires_at);

-- +goose Down
DROP TABLE IF EXISTS oidc_logins;
ALTER TABLE sessions DROP COLUMN IF EXISTS oidc_refresh_lease;
ALTER TABLE sessions DROP COLUMN IF EXISTS oidc_refreshed_at;
ALTER TABLE sessions DROP COLUMN IF EXISTS oidc_refresh_token;
ALTER TABLE sessions DROP COLUMN IF EXISTS oidc_subject;
ALTER TABLE sessions DROP COLUMN IF EXISTS oidc_issuer;
DROP TABLE IF EXISTS user_identities;
ALTER TABLE users DROP COLUMN IF EXISTS email;
ALTER TABLE users DROP COLUMN IF EXISTS source;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_username_check;
ALTER TABLE users ADD CONSTRAINT users_username_check CHECK (username ~ '^[a-z0-9][a-z0-9._-]{1,62}$');
