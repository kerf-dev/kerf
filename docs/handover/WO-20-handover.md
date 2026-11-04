# WO-20 hand-over (admin area: hub)

Branch `wo-20-admin-hub` (from origin/main 59539ee). Contract: `docs/contracts/admin.md`, D-022..D-024.
No new migrations: 0007–0010 as merged by WO-19. Not deployed (the orchestrator deploys the hub).

## What the hub does now

**Auth (D-023)** — `internal/hub/auth` (bcrypt cost ≥ 12, random tokens, limiter, lockout, cookie)
and `internal/hub/authn.go` (middleware, login/logout/me/password, bootstrap).
- Every route declares the token roles it accepts and the lowest session role (`access` in
  `authn.go`). The session cookie `ro_session` wins over a bearer token; an unknown/expired
  session falls back to the bearer token (so the web nginx can keep injecting the `ui` token).
- Session writes need `X-CSRF-Token` (`403 csrf_invalid`). Sessions are stored as SHA-256 in
  `sessions`; the user's role, disabled and deleted state are read on every request, so a role
  change or disable takes effect at once (disable/delete/reset also revoke sessions).
- Login: `401 invalid_credentials` for wrong user or password (same timing); 5 consecutive failures
  lock the account for 15 min (`429 account_locked`, `Retry-After`); rate limit 10/min per
  username and 30/min per client address (`429 rate_limited`). Configurable under `auth.*`.
- `POST /v1/me/password` keeps the current session, revokes the others.
- Bootstrap admin: on a database without users, from `auth.bootstrapAdmin.passwordFile` (chart:
  Secret `<release>-bootstrap-admin` or `existingSecret`) or env `HUB_BOOTSTRAP_ADMIN_PASSWORD`.
- Dev users (values-dev.yaml, Secret `hub-dev-users`): `viewer` / `ro-dev-viewer-pass`,
  `operator` / `ro-dev-operator-pass`, `admin` / `ro-dev-admin-pass`. Created only when missing,
  `mustChangePassword: false`. Dev sets `auth.cookieSecure: false` (plain HTTP).
- `api_tokens.last_used_at` is updated at most once a minute; revocations clear the token cache.
- CORS: credentials for explicitly listed origins only (never for `*`), methods
  `GET POST PUT PATCH DELETE`, headers `If-Match`, `X-CSRF-Token`; exposes `ETag`, `Retry-After`.

**Settings** (`internal/hub/config`, `admin_settings.go`) — version 0 is the hub config file;
`PUT` needs `If-Match`, validates (all 3 profiles, interval 1m–7d, retention ≥ 24h, percentiles in
(0,1], margins in [0,10]) and stores a version. The job reloads both sections at the start of every
run (profiles, retention, pricing); a new interval resets the ticker at once.
- Pricing: a fixed rate replaces the metric/default rate, then the discount (cluster, else
  default) applies. `recommendations.pricing` / run pricing store the adjusted rates plus
  `adjustment {discountPercent, fixedCPU, fixedMemory, base*USD, settingsVersion}`; the API's
  `RecommendationPricing` shows the adjusted rates (no contract change). The pricing fallback uses
  the base rates, so adjustments never compound.

**Policies** (`admin_policies.go`, store `policies.go`) — create/update/delete(soft)/revert with
versions, `If-Match`, `validate.HubPolicy`, reserved names and bands set by the hub. Workload
overrides only through `PUT /v1/workloads/{id}/override` (POST with that type → 400). Updating a
deleted policy → `409 policy_deleted` (revert it instead).

**Effective policy and preview** (`internal/hub/admin`) — reuses the agent's `internal/agent/policy`
(`Matches`, `Winner`, built-in defaults); nothing was moved. It also requires `selector.workloads`
itself (AND), so it is right before and after WO-21 adds that to `policy.Matches`. Layers per
contract §2 (the chart `clusterDefaults` layer is unknown to the hub and omitted). Label selectors:
the hub has no labels; a label policy counts as matching only when the agent reports it as the
workload's policy (with a warning). Annotations are inferred from the reported mode sources. Git
policies come from the agents' `policyInventory` (`spec`). Clusters with `policySource: git` get
no hub policies, in the config and in the simulation. Preview is computed per cluster and merged.

**Agent config** (`GET /v1/clusters/{c}/config`) — global default, the cluster's default, policies
for all clusters or listing it, the cluster's workload overrides; pause flags (fleet wins); pending,
unexpired approvals whose recommendation is still the latest. `configVersion` = `c-` + 16 hex of
SHA-256 over the content without `generatedAt` (order-independent) → `ETag`, `304` on
`If-None-Match`. Pull time stored in `config_pulled_at`. Status batches store `configVersion` and,
when present, the full `policyInventory` (absent = unchanged); `WorkloadStatus.override` is stored
in `workload_status.settings` (like the JVM list).

**Approvals** — `409 recommendation_stale` unless the id is the latest recommendation, `409
not_in_audit` unless CPU or memory is in audit. A new approval supersedes the open one; a new
recommendation run supersedes stale ones; an agent `applied`/`ApprovedOnce` event with
`details.approvalId` consumes it. Default expiry 24 h, max 7 d.

**Automation** — pause by operators; resume of a `prod` cluster or the fleet by admins only
(`403 admin_required`); optional `If-Match`; `resumeAt` ends a pause lazily (config, cluster and
automation reads) with an `AutomationResumed` entry by actor `system`.

**Clusters / tokens / users** — register (optionally with an agent token shown once; a soft-deleted
cluster is revived), PATCH, soft DELETE (revokes its tokens, hides its workloads and the cluster
from lists and runs). Metrics binding, provider and region are stored (`cluster_metrics_bindings`;
the `local` datasource row is created if missing). Users: `409 last_admin` on demoting, disabling or
deleting the last enabled admin; reset returns a temporary password and revokes sessions.

**Audit** — every write records one `config_changed` row (actor, action, target, before/after,
reason, version) in the same transaction. `GET /v1/audit` and workload history exclude them;
`GET /v1/audit/config` serves them (keyset pagination bound to the query);
`WorkloadHistory.configChanges` lists a workload's override and approval changes.
`WorkloadDetail.approval/override` and `ClusterInfo.environment/owner/policySource/paused/
automation/configSync/readiness` are filled.

**D-025** — datasources, readiness, recheck, history and install answer `501 not_implemented`.
`ClusterAdmin.readiness` is the `cluster_readiness` row or `Registered`. No gating yet; an
operator sending `overrideReadiness` gets `403 admin_required` already.

## For other tracks
- **WO-21**: the hub imports `internal/agent/policy` (`Matches`, `Winner`, `Resolve` for
  built-in defaults). Keep those signatures, or tell the orchestrator; adding `selector.workloads`
  to `Matches` is expected. Events: `details.approvalId` consumes the approval.
- **WO-22**: login at `/v1/auth/login`, `credentials: 'include'`, `X-CSRF-Token` from the login/me
  response, ETags as `If-Match` (`"<version>"`). `POST /v1/workloads/{id}/approve` needs the
  `recommendation.id` the detail page shows.
- **WO-25**: replace the 501 stubs in `server.go`; add gating in `putWorkloadOverride`,
  `createPolicy`/`updatePolicy` and `approveRecommendation` (see `prodGuard`).

## Verification
```bash
make ci   # with RO_TEST_DATABASE_URL set
RO_TEST_DATABASE_URL=postgres://ro:ro@localhost:15432/postgres?sslmode=disable go test -race -cover ./internal/hub/... ./internal/recommender/...
```
Postgres tests: `internal/hub/admin_test.go` (auth/CSRF/lockout/rate limit, settings + hot reload +
pricing, policies/versions/revert/config/ETag/inventory/effective policy/preview, overrides/prod
guard/approvals/automation/resumeAt, clusters/tokens/users/last admin/bootstrap, 501 stubs).
Live check after deploy: log in as `admin`, change a setting, recompute, see the config ETag change.
