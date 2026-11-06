# Teams

Teams let application teams see and act on their own workloads without operator rights on the whole fleet, and
route the savings to the team that owns them. A team owns workloads; users are bound to teams as `viewer` or
`operator`; the hub filters every list, count, total, detail and write by what the caller may see.

Kerf does not split the cloud bill between teams (OpenCost and Kubecost do that). It groups its own estimated and
realized savings by team, so each team has its own list of opportunities.

## Which team owns a workload

Admins define teams in **Admin → Teams** (or `POST /v1/admin/teams`). A team has an id (a DNS label such as
`payments`, fixed once created), a display name, and two ways to claim workloads:

- **The owner label.** A label on the workload or on its namespace whose value is the team's id, or one of the
  team's *owner values* (aliases such as `pay` for `payments`). The label key is `team` by default; set it with
  the chart value `hub.config.teams.ownerLabel` (for example `app.kubernetes.io/part-of` or
  `example.com/owner`).
- **A namespace selector.** Namespace names or globs (`payments`, `payments-*`), namespace labels that must all
  match (`tier: frontend`), and optionally the clusters it applies to (globs; empty means every cluster).

The hub resolves each workload in this order, and the first rule that names a team wins:

1. the owner label on the **workload**;
2. the owner label on its **namespace**;
3. the first team, by id, whose **namespace selector** matches.

An owner label value that names no team falls through to the next rule. A workload no rule matches is
**Unassigned**: it shows up as such in lists, filters and groupings, and only fleet-wide users see it.

The agent reports the labels of each workload and of its namespace with the workload's status (it needs `get`,
`list`, `watch` on namespaces; the agent chart grants it). The hub reassigns workloads whenever a team changes or
new labels arrive, and at start-up (the owner label key may have changed). Until an agent of this version reports,
only namespace names select teams.

## Who sees and does what

A user has a **scope**:

| Scope | What the user sees | What the user may do |
|---|---|---|
| `fleet` (default) | every workload, Unassigned included | its role everywhere; a **team role** raises it on that team (a fleet `viewer` who is `operator` on `payments`) |
| `teams` | only the workloads of its teams | its role on each team: `viewer` or `operator` |

`admin` is always fleet-wide and has no team roles. A user may hold several team roles. Local users get their
scope and team roles in **Admin → Access**.

Team-scoped users use Overview, Opportunities, the workload pages, Activity and **Admin → Teams** (their own teams).
Everything else, such as policies, settings changes, clusters, datasources, users, tokens, node capacity and the
node cost trend, covers the whole fleet and needs a fleet-wide role: the API answers `403` with code
`scope_forbidden`, and the UI hides these pages.

### Enforced by the hub

- **Reads.** The scope is applied where the hub reads its database, not per endpoint: workload lists, facets,
  `GET /v1/summary` (counts, totals and the clusters listed), `GET /v1/clusters` (counts), the savings trend, realized
  savings, the audit log and the configuration audit only ever contain the caller's teams. A workload of another
  team answers `404`, as if it did not exist.
- **Writes.** Approving, overriding or excluding a workload needs `operator` on the workload's team (or fleet-wide):
  otherwise `403 scope_forbidden`. A fleet viewer with `operator` on `payments` can approve `payments` workloads and
  nothing else.
- **Audit.** Each action records the scope used: when a team role allowed it, the configuration audit entry's actor
  has `teams: ["payments"]` next to its name and role. Fleet-wide actions have no `teams`.
- **Tests.** A table test calls every endpoint a team-scoped user may use as a viewer of one team and fails if any
  response contains another team's workload; another checks that writes outside the scope answer `403`.

## Sign-in with an identity provider

With [OpenID Connect sign-in](auth.md#sign-in-with-openid-connect), a group mapping can name teams. The mapping then
gives its role on those teams only:

```yaml
hub:
  auth:
    oidc:
      roleMappings:
        - {group: platform, role: operator}                       # fleet-wide
        - {group: engineering, role: viewer}                      # fleet-wide
        - {group: payments-devs, role: operator, teams: [payments]}
        - {group: search-devs, role: operator, teams: [search, ranking]}
```

- A member of `engineering` and `payments-devs` is a fleet viewer and an operator on `payments`.
- A member of `payments-devs` only is team-scoped: it sees `payments` and nothing else.
- The highest role per team wins; `defaultRole` applies only when no mapping matches at all.
- Team mappings give `viewer` or `operator`. `admin` cannot have `teams`: the hub refuses to start.
- The groups are re-read at every refresh (`refreshInterval`), so losing a group removes the team role within that
  interval. A mapping may name a team that does not exist yet; it takes effect once the team is created.

IdP users' scope and team roles come from their groups, so Admin → Access shows them read-only.

## Savings by team

- **Overview** has a *Savings by team* card: the estimated monthly savings of each team, largest first, with
  Unassigned last. Each row opens Opportunities filtered to the team.
- **Opportunities** filters by team (`?team=payments`, `?team=unassigned`) and can group the rows by team.
- **API.**
  - `GET /v1/summary` adds `teams`.
  - `GET /v1/workloads` and `/v1/workloads/facets` take `team=` and return each row's `team`.
  - `GET /v1/savings/trend` takes `team=` and `groupBy=team`.
  - `GET /v1/savings/realized` takes `team=`.
- **History.** The recommender records the estimated savings per team at every run since teams exist. A trend by
  team, or the trend of a team-scoped user, starts at the first run after the upgrade. Realized savings use the
  workload's current team for its whole history.
- **Weekly digest.** When teams exist, the [digest](notifications.md) has a *Savings by team* section, and the
  webhook payload has `digest.teams`.

## Limits

- One team per workload. When several selectors match, the first team by id wins, so prefer the owner label for
  shared namespaces.
- Policies, defaults and settings stay an admin task. There is no per-team policy editing.
- Bearer tokens (`ui`, `admin`) are fleet-wide.
