-- Pricing service: API tokens of role pricing (the pricing service: metrics bindings and the
-- datasource query proxy). Like ui and admin tokens they have no cluster.
-- Idempotent: safe to re-run on a database that already has it.

-- +goose Up
ALTER TABLE api_tokens DROP CONSTRAINT IF EXISTS api_tokens_role_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_role_check CHECK (role IN ('agent', 'ui', 'admin', 'pricing'));

-- +goose Down
DELETE FROM api_tokens WHERE role = 'pricing';
ALTER TABLE api_tokens DROP CONSTRAINT IF EXISTS api_tokens_role_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_role_check CHECK (role IN ('agent', 'ui', 'admin'));
