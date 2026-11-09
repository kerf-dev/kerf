-- Datasource type dynatrace: Kubernetes metrics read from a Dynatrace environment with DQL
-- (docs/install/dynatrace.md). Its settings (ActiveGate URL) and the name of a mounted token
-- Secret live in auth_config like the other non-secret auth settings; a pasted token is in
-- auth_secret as for bearer auth.

-- +goose Up
ALTER TABLE datasources DROP CONSTRAINT IF EXISTS datasources_type_check;
ALTER TABLE datasources ADD CONSTRAINT datasources_type_check
    CHECK (type IN ('prometheus', 'victoriametrics', 'thanos', 'mimir', 'amp', 'dynatrace'));

-- +goose Down
ALTER TABLE datasources DROP CONSTRAINT IF EXISTS datasources_type_check;
ALTER TABLE datasources ADD CONSTRAINT datasources_type_check
    CHECK (type IN ('prometheus', 'victoriametrics', 'thanos', 'mimir', 'amp'));
