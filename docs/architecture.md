# Architecture

How the parts of Kerf fit together and the main design choices behind them. The [README](../README.md#how-it-works)
has the short version with a diagram; the [contracts](README.md#concepts-and-contracts) have the exact interfaces.

## Components

| Component | Runs in | What it does |
|---|---|---|
| **Hub** | the central cluster | Reads usage history from Prometheus-compatible stores, computes recommendations, stores state and the audit log in PostgreSQL, serves the HTTP API (`api/openapi/hub.yaml`) |
| **Web UI** | the central cluster | A static React app served by nginx; `/api` is proxied to the hub |
| **Pricing** | the central cluster | Prices every node and exports per-vCPU-hour and per-GiB-hour rates as metrics |
| **Agent** | each workload cluster | Pulls recommendations and configuration from the hub, resolves the policy, enforces guardrails, applies changes, watches health and rolls back |
| **Webhook** | each workload cluster | A mutating admission webhook that gives new pods the recommended resources |

One Helm chart (`deploy/charts/kerf`) installs all of them; `global.mode: hub | spoke | all` picks the role of a
cluster.

## Design choices

**Hub and spoke, the agent pulls.** The hub produces recommendations for every workload; the agent in each
cluster pulls them and does the writing. The hub never holds write credentials to a workload cluster. Agents
authenticate to the hub with a per-cluster bearer token over HTTPS.

**PromQL only.** Usage comes from Prometheus or VictoriaMetrics (Thanos, Mimir and Amazon Managed Service for
Prometheus also work). Every query is plain PromQL, so no other monitoring product is a dependency. Each cluster
is bound to a metrics source with a configurable cluster label; source credentials stay inside the hub, and the
pricing service queries through a hub endpoint instead of holding them.

**Modes per workload and per resource.** `off`, `audit` or `auto-patch`, set separately for CPU and memory. The
effective mode comes from, lowest to highest: the global default (`audit`), the cluster default, the matching
`RightsizingPolicy` with the highest priority, and the workload's annotations. `auto-patch` needs the cluster to
pass its readiness checks; an admin can override that with a reason, and the override is audited.

**Policies from the UI and from Git.** Policies created in the UI are stored in the hub; each agent writes them
as `RightsizingPolicy` resources labelled `kerf.getkerf.dev/managed-by=hub` and only ever changes resources with
that label. Policies from Git keep working and show read-only in the UI.

**Apply strategies.**
- `in-place` (default) resizes running pods through the `resize` subresource; the webhook sets the same values
  on new pods. The pod template is not touched, so there is no rollout and no GitOps drift. When a resize is
  infeasible or stays deferred too long, the agent falls back to a rollout. In-place is not used on nodes with
  swap or a static CPU or Memory Manager policy, and memory changes on JVM or Python containers need
  `resizePolicy: RestartContainer` or a rollout.
- `rollout` patches the pod template (a rolling restart).
- `deferred` patches only inside a maintenance window.

**GitOps friendly.** The agent writes with server-side apply under its own field manager. Argo CD is configured
to ignore fields owned by that manager, so Git stays the source of truth for everything else. When a workload
leaves `auto-patch`, the agent releases its fields and Argo CD restores the Git values. After an approve-once
change the agent keeps ownership until a newer recommendation arrives, the workload is excluded, or the approval
is revoked, so a self-healing Argo CD does not revert an approved change.

**Approve once.** An operator can approve the current recommendation of a workload that stays in `audit`. The
agent applies it once, with all guardrails, and the workload stays in `audit`.

**HPA coordination.** For a workload scaled by a CPU or memory utilization HPA, the request change and a new HPA
utilization target (kept within 40–90 %) go in as one rollout, so the HPA keeps scaling at the same usage. HPAs on
external or custom metrics, or pinned at `maxReplicas`, stay in audit.

**Quiet audit log.** `skipped` and `blocked` events are sent once per workload, type and reason, and again only
when the reason or the proposed values change. Every action (`applied`, `rolled_back`, …) is one event.

**Pricing.** Nodes are priced from their instance type and capacity type labels, from the AWS price list
(on-demand) and EC2 spot price history, or from a static price table. Node cost is split into per-vCPU-hour and
per-GiB-hour rates.

**Sign-in.** The hub manages local users (bcrypt) with the roles `viewer`, `operator` and `admin`, using HttpOnly
session cookies with CSRF protection. The initial admin comes from a Secret. Machine tokens (`agent`, `ui`,
`admin`, `pricing`) are separate.

## Production packaging

- **Hub high availability:** two or more replicas. A PostgreSQL advisory-lock leader runs the background work
  (recommender, readiness, sweeps); every replica serves the API; migrations run under a session lock.
- **Secrets:** from AWS Secrets Manager through the External Secrets Operator, or an existing Secret. The values
  schema rejects inline secrets outside dev installs.
- **Database:** PostgreSQL (Amazon RDS on EKS) with `sslmode=verify-full`.
- **Exposure:** the hub API behind an internal load balancer; the web UI behind one whose scheme you choose.
- **AWS access for pricing:** EKS Pod Identity, with IRSA as the fallback.
- **CRDs:** shipped as chart templates with `helm.sh/resource-policy: keep`, so uninstalling the chart keeps them.
- **Images:** multi-arch (`linux/amd64`, `linux/arm64`), signed with cosign.
- **GitOps installs:** an Argo CD ApplicationSet with a cluster generator ([example](../deploy/examples/eks)).

## Out of scope

Cost allocation or showback per team, Argo Rollouts and canary awareness, and opening pull requests against
deployment repositories.
