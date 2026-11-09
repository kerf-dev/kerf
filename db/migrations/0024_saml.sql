-- Sign in with SAML 2.0 (docs/auth.md). Users signed in through a SAML identity provider get a user
-- record (source 'saml') like the OpenID Connect ones, linked to the IdP's (entity ID, NameID) in
-- user_identities, now keyed by protocol too so the two kinds of identity never mix. Their sessions
-- carry that identity for the audit log. Additive for local and OIDC users.

-- +goose Up
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_source_check;
ALTER TABLE users ADD CONSTRAINT users_source_check CHECK (source IN ('local', 'oidc', 'saml'));

-- 'oidc': issuer and subject (sub) of the ID token. 'saml': the IdP's entity ID and the NameID.
ALTER TABLE user_identities ADD COLUMN IF NOT EXISTS protocol text NOT NULL DEFAULT 'oidc'
    CHECK (protocol IN ('oidc', 'saml'));
ALTER TABLE user_identities DROP CONSTRAINT IF EXISTS user_identities_pkey;
ALTER TABLE user_identities ADD PRIMARY KEY (protocol, issuer, subject);

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS saml_issuer  text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS saml_subject text;

-- Sign-ins in progress, at most 10 minutes: from the redirect to the IdP (request_id is the
-- AuthnRequest ID the response must answer), through the assertion consumer service, which stores the
-- verified identity, to the step that starts the session in the browser holding the kerf_saml cookie.
-- Keyed by the SHA-256 of the RelayState, which that cookie holds.
CREATE TABLE IF NOT EXISTS saml_logins (
    state_sha256  bytea       PRIMARY KEY CHECK (length(state_sha256) = 32),
    request_id    text        NOT NULL,
    next_path     text        NOT NULL DEFAULT '/',
    identity      jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS saml_logins_expires_at_idx ON saml_logins (expires_at);

-- Replay cache shared by every hub replica: the SHA-256 of each accepted assertion ID (with the IdP's
-- entity ID), kept until the assertion could no longer be accepted anyway.
CREATE TABLE IF NOT EXISTS saml_assertions (
    id_sha256   bytea       PRIMARY KEY CHECK (length(id_sha256) = 32),
    expires_at  timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS saml_assertions_expires_at_idx ON saml_assertions (expires_at);

-- +goose Down
DROP TABLE IF EXISTS saml_assertions;
DROP TABLE IF EXISTS saml_logins;
ALTER TABLE sessions DROP COLUMN IF EXISTS saml_subject;
ALTER TABLE sessions DROP COLUMN IF EXISTS saml_issuer;
DELETE FROM user_identities WHERE protocol = 'saml';
ALTER TABLE user_identities DROP CONSTRAINT IF EXISTS user_identities_pkey;
ALTER TABLE user_identities ADD PRIMARY KEY (issuer, subject);
ALTER TABLE user_identities DROP COLUMN IF EXISTS protocol;
UPDATE users SET source = 'oidc' WHERE source = 'saml';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_source_check;
ALTER TABLE users ADD CONSTRAINT users_source_check CHECK (source IN ('local', 'oidc'));
