-- Teams (docs/teams.md): a team owns the workloads its namespace selectors or the owner label pick.
-- Users may be bound to teams as viewer or operator; the hub then filters every list, count, total,
-- detail and write by the caller's teams. Additive: existing users keep their fleet-wide role.

-- +goose Up

-- A team. id is a DNS label chosen by the admin and never changes (role bindings and the IdP group
-- mappings refer to it); 'unassigned' is reserved for workloads in no team.
CREATE TABLE teams (
    id                text        PRIMARY KEY CHECK (id ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' AND id <> 'unassigned'),
    display_name      text        NOT NULL DEFAULT '',
    description       text        NOT NULL DEFAULT '',
    -- Namespace selectors: glob patterns of namespace names, namespace labels that must all match
    -- (matchLabels), and an optional restriction to clusters (globs). A team without namespaces and
    -- namespace_labels selects nothing by namespace (only by the owner label).
    namespaces        text[]      NOT NULL DEFAULT '{}',
    namespace_labels  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    clusters          text[]      NOT NULL DEFAULT '{}',
    -- Values of the owner label (hub config teams.ownerLabel, default "team") that mean this team,
    -- besides its id.
    owner_values      text[]      NOT NULL DEFAULT '{}',
    version           integer     NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at        timestamptz NOT NULL DEFAULT now(),
    created_by        text,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Workload labels and its namespace's labels, as the agent reports them, and the team the hub
-- resolved from them (NULL: Unassigned). The hub recomputes team when teams or labels change.
ALTER TABLE workloads ADD COLUMN labels           jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE workloads ADD COLUMN namespace_labels jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE workloads ADD COLUMN team             text;
CREATE INDEX workloads_team_idx ON workloads (team);

-- Role bindings. scope 'fleet': role applies to every workload, team_roles may raise it on some teams
-- (a fleet viewer who operates its own team). scope 'teams': the user sees and acts only on the teams
-- of team_roles ([{"team": id, "role": "viewer"|"operator"}]); role is then the highest of them. Admins
-- are always fleet-wide. IdP users get both from the group mappings at every sign-in and refresh.
ALTER TABLE users ADD COLUMN scope      text  NOT NULL DEFAULT 'fleet' CHECK (scope IN ('fleet', 'teams'));
ALTER TABLE users ADD COLUMN team_roles jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE users ADD CONSTRAINT users_admin_fleet_check CHECK (role <> 'admin' OR scope = 'fleet');

-- Estimated savings per team at the end of each run and cluster (team '' = Unassigned), the source
-- of GET /v1/savings/trend?groupBy=team and of the trend of team-scoped callers.
CREATE TABLE recommender_run_teams (
    run_id                      uuid             NOT NULL,
    cluster                     text             NOT NULL,
    team                        text             NOT NULL,
    monthly_savings_cpu_usd     double precision NOT NULL DEFAULT 0,
    monthly_savings_memory_usd  double precision NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, cluster, team),
    FOREIGN KEY (run_id, cluster) REFERENCES recommender_run_clusters (run_id, cluster) ON DELETE CASCADE
);

-- Config audit: team changes (docs/contracts/events.md).
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_target_type_check
    CHECK (target_type IN ('settings', 'policy', 'workload-override', 'approval', 'cluster', 'automation', 'token', 'user',
                           'datasource', 'readiness', 'notification', 'team'));

-- +goose Down
DELETE FROM audit_events WHERE type = 'config_changed' AND target_type = 'team';
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_type_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_target_type_check
    CHECK (target_type IN ('settings', 'policy', 'workload-override', 'approval', 'cluster', 'automation', 'token', 'user',
                           'datasource', 'readiness', 'notification'));
DROP TABLE IF EXISTS recommender_run_teams;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_admin_fleet_check;
ALTER TABLE users DROP COLUMN IF EXISTS team_roles;
ALTER TABLE users DROP COLUMN IF EXISTS scope;
DROP INDEX IF EXISTS workloads_team_idx;
ALTER TABLE workloads DROP COLUMN IF EXISTS team;
ALTER TABLE workloads DROP COLUMN IF EXISTS namespace_labels;
ALTER TABLE workloads DROP COLUMN IF EXISTS labels;
DROP TABLE IF EXISTS teams;
