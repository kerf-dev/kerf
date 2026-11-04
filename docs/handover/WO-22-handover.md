# WO-22 hand-over (admin area: web)

Branch `wo-22-admin-web` (from origin/main `59539ee`, WO-19 merged), pushed. **Not redeployed**: release
`web` still runs `wo14d-2` until the hub supports login (WO-20). Spec: `docs/work-orders/WO-22-admin-web.md`,
plans `docs/plans/admin-area.md` §3–4 and `docs/plans/cluster-onboarding.md` §3 and §5.

## What is in it

**Sign-in (D-023)**
- `/login`: username/password; handles `401 invalid_credentials`, `429 account_locked` (`Retry-After`),
  and hubs without the endpoint.
- `GET /v1/me` drives the role and the CSRF token. `X-CSRF-Token` is added to every write by a client
  middleware (`src/api/client.ts`, `src/api/auth.ts`).
- Forced password change after a reset (`mustChangePassword`). User menu with change password and sign out.
  A `401` on any request re-checks `/v1/me`.
- Role-aware navigation, "Administration" section:
  - viewer: nothing
  - operator: Automation, Policies (read-only), Audit
  - admin: everything
- Writes need a session; the anonymous `ui` token is read-only even though it maps to `viewer`.

**nginx / chart / dev proxy** (`web/nginx`, `deploy/charts/web`, `vite.config.ts`)
- `/v1/` forwards `GET HEAD POST PUT PATCH DELETE`. `X-CSRF-Token` and `If-Match` pass through as sent.
- With a `ro_session` cookie: only that cookie is forwarded, and **no bearer token**.
- Without one: `Authorization: Bearer <ui token>` when `auth.anonymousRead: true` (default, as
  admin.md §3 describes: anonymous = viewer). With `false`, nothing is sent and the UI requires a login.
- The browser's own `Authorization` header is never forwarded.
- The Vite live proxy does the same with `RO_HUB_TOKEN`.

**Admin pages** (plan §4)

| Page | Route | Notes |
|---|---|---|
| Automation | `/admin` | fleet + per-cluster pause/resume (reason, auto-resume presets), agent version, last pull, sync, readiness, workloads by mode. Resume of `prod` / fleet disabled for operators. |
| Defaults | `/admin/defaults` | full spec form; every field shows the inherited value ("Inherits 168h from built-in"); impact preview before save; YAML export. |
| Clusters | `/clusters`, `/clusters/:name`, `/clusters/new` | readiness badge and "Fix" link on cards. Detail tabs: Overview, Readiness, Cluster defaults, Agent and install. The wizard is described below. |
| Policies | `/admin/policies`, `/admin/policies/new`, `/admin/policies/:id` | details below. |
| Recommender | `/admin/recommender` | 3 profiles (percent inputs), interval, retention, change list, Recompute now. |
| Pricing | `/admin/pricing` | default discount, per-cluster discount and fixed rates, change list. |
| Metrics sources | `/admin/datasources` | CRUD for all auth types; secrets are write-only ("stored, leave blank to keep"); per-datasource Test. |
| Access | `/admin/access` | users (create, change role, disable, delete, reset password with a temporary password shown once) and API tokens (create with role/expiry, value shown once, revoke). |
| Audit | `/admin/audit` | `GET /v1/audit/config` with type and actor filters and a before/after diff per change. |

Clusters, in more detail:
- **Overview tab:** metadata, metrics binding, agent sync, policy inventory with Hub/Git badges.
- **Readiness tab:** stepper, checklist grouped by category (problems first, expanded, with remediation,
  copy button, ClusterRole rules and docs link), Re-check with polling, history timeline.
- **Agent and install tab:** create/rotate/revoke agent tokens (value shown once, plus the `kubectl create
  secret` command), and Helm / values / Argo CD snippets with webhook and cert-manager toggles, plus the
  `argocd-cm` ignoreDifferences snippet.
- **Wizard (`/clusters/new`):** Identify → Metrics (pick or create a datasource, cluster label/value,
  extra matchers, **Test** before any agent, label-value suggestions) → Install (register; token once;
  snippets) → Verify (live checklist).

Policies, in more detail:
- **Table:** Hub + Git rows from `policy-inventory`, with name, source badge, clusters, selector summary,
  priority, matched workloads, last change, and an invalid-on-cluster marker. Views: Policies, Defaults,
  Overrides, All.
- **Editor:** name, priority band, clusters, selector builder (namespace globs, excludes, kinds, labels,
  specific workloads) with a live "Matches N workloads" list. The list is matched client-side over
  `/v1/workloads`; labels are evaluated by the agent, so it notes that.
- **History tab:** versions with a line diff and Revert. **YAML tab:** the manifest, plus a download.
- **Simulator tab:** effective-policy precedence chain and field sources for any workload.
- Git policies open read-only as YAML.

**Write actions in existing pages**
- **Workload Automation card:**
  - shows the active override and the approval state
  - Override (CPU/memory mode, strategy, profile), with an impact preview
  - Exclude, Clear override, Approve once
  - "Why these modes?", which opens the effective-policy dialog
- The workload history includes config changes (`WorkloadHistory.configChanges`).
- **Opportunities:** checkbox selection (operator+), page select-all, sticky bulk bar with count and
  $/mo, and **Set mode… / Exclude** with the impact preview. Each workload's existing override is merged
  (GET, then PUT with `If-Match`). Partial failures are reported and the dialog retries only the failed
  workloads.
- **Overview:** readiness banner (one line per cluster with a blocking failure: message and effect, links
  to the checklist; dismissible until the set of failures changes).
- **Workload without a recommendation:** names the failing readiness check when the cluster is below
  Ready (audit).

**Safety and UX for every write** (`src/components/admin/write-dialog.tsx`)
- A reason is required (stored in the audit log).
- `prod` clusters: type the cluster name, for each affected prod cluster. For defaults and policies, the
  affected prod clusters come from the preview, so the confirmation appears where the change actually
  lands.
- `412` shows "Someone else changed this in the meantime" with **Reload**. `409
  cluster_not_automation_ready` explains the block and shows admins an audited "Override readiness"
  checkbox. `403 admin_required` and `csrf_invalid` get explained too.
- Unsaved-changes guard on forms (in-app navigation and tab close).
- Client-side validation mirrors `api/v1alpha1/validate` (`src/lib/validate.ts`, with tests): durations,
  quantities, fractions, limitRatio iff ratio, bands, reserved `ro-`, globs, label keys, 5-field cron,
  IANA time zones, windows.
- Empty, loading and error states throughout; light/dark; tablet width.

**Mocks (`web/src/mocks/**`)**
- Every new endpoint, with session, roles, CSRF, `If-Match` (428/412), reason, readiness gating, preview
  evaluation, approvals, recheck and onboarding. All of it is contract-validated (`admin.test.ts`).
- Mock users: `admin`, `operator`, `viewer`, `alice`, `bob`, password `dev-password`. `sam` must change
  the password; `dana` is disabled.
- The **Mock API** menu toggles "login required".

**Also fixed:** ECharts `ScatterChart` was not registered, so WO-14d's recommendation-change dots never
rendered (console error). It is registered now.

## Verification (all exit 0)
See "Verification" in the report: `npm ci`, lint (0 warnings), prettier, typecheck, vitest (8 files),
build, Playwright (11 smoke + 14 admin flows), `npm audit`, docker image + nginx config test.

Admin Playwright flows (`web/e2e/admin.spec.ts`):
- **Roles:** login as admin (full nav, sign out), operator (limited nav, Defaults refused), viewer (no admin
  area); wrong password; login-required mode.
- **Admin writes:** change defaults with preview; create a policy with the selector builder; revert to v1;
  `412` conflict with Reload; onboard a cluster.
- **Operator writes:** pause a cluster (resume disabled for prod); approve once; exclude then clear an
  override; bulk exclude.
- **Readiness:** banner leads to the RBAC remediation.

Screenshots: `docs/screenshots/` (`make web-screenshots`). New captures:
- **login and admin pages, light and dark:** login, admin-automation, admin-policies, admin-policy-editor,
  admin-policy-history, admin-simulator, admin-defaults, admin-recommender, admin-pricing,
  admin-datasources, admin-access, admin-audit
- **clusters and workloads, light and dark:** cluster-readiness, cluster-agent, cluster-wizard,
  workload-automation
- **light only:** admin-defaults-preview, opportunities-bulk
- **tablet:** admin-automation, admin-policy-editor, cluster-readiness

## For the hub (WO-20 / WO-25) and the switch to the live hub
- The UI needs no switch. It calls the contract paths. Against today's hub, `/v1/me` answers 404, so the
  UI stays read-only and hides Sign in and the admin area. Once WO-20 serves `/v1/me` and
  `/v1/auth/login`, the UI lights up.
- **Deploying:** set the chart's `auth.anonymousRead` as wanted; the default keeps today's anonymous
  dashboards.
- **Cookie:** the hub must set `ro_session` with `Path=/` and `SameSite=Strict`, and add `Secure` only
  behind TLS (`X-Forwarded-Proto`; dev is plain HTTP on :19090). The web nginx sends
  `X-Forwarded-Proto $scheme`.
- **Order of checks** is not specified: the mock answers `409 cluster_not_automation_ready` before `412`.
  Either works in the UI.
- **Codes the mock invented** (the contract leaves them open; the UI only shows their `detail`):
  `invalid_settings`, `invalid_reason`, `*_not_found`. A wrong current password on `/v1/me/password`
  gives `400 invalid_credentials` in the mock; the UI also accepts 401/403.
- A **preview draft** of type `workload-override` without `policyId` is assumed to replace that
  workload's existing override (bulk preview relies on it).

## Not done / deviations
- **Audit** is its own page (`/admin/audit`) instead of a `config` filter inside Activity: config
  changes are a different API (`/v1/audit/config`, WO-19 deviation 2), and the page shows diffs.
- **Pricing:** "current rates per cluster and their source" is not shown; there is no endpoint for it
  (rates appear per recommendation on the workload page).
- **Policy source `git`:** the UI does not yet switch the admin pages to read-only for Git-only clusters
  (the hub sends no policies to them, D-022). Cluster editing shows the setting; YAML export exists on
  every policy and on the defaults.
- **Custom recommender profiles:** not in the contract (WO-19 deviation 7).
- **Not redeployed** (as asked). When the hub supports login, run `make deploy-web` with a new tag.

## WO-22b (admin polish)

Branch `wo-22b-admin-polish` from origin/main `9325962`; deployed as `web:wo22b-59ff5d7`.
- **Mode controls:** the CPU/memory segmented controls and the form grids use container queries (the
  form column is a `@container`), so they stack by the width they actually get. Playwright checks
  this at a 1060 px viewport (~800 px content).
- **`/admin/clusters[/:name]`:** redirects to `/clusters[/:name]`. Nothing in the UI links to it.
- **Git-only clusters (`policySource: git`):**
  - Cluster defaults are read-only, with an explanation.
  - The policy editor cannot target them.
  - Defaults and policy editors say which clusters a change will not reach.
  - The workload card hides override/exclude; Approve once stays, since approvals are not policies.
  - Bulk actions skip those workloads.
  - In the mock, `dr-eu-central-1` is Git-only.
- **Pricing rates per cluster:** not added. No existing endpoint provides them; rates exist only per
  recommendation (`WorkloadDetail.recommendation.pricing`).
- **"Policy sync: Syncing"** is the hub's own `AgentConfigSync.inSync` flag, so the UI does not compare
  versions itself. Live, both spokes settled to `inSync: true` (10:48 UTC). Two hub/agent oddities seen
  read-only on ro-spoke-1:
  1. `policyInventory` (reportedAt 10:46:48) still lists `orch-live-check`, which was deleted at
     10:46:57. Inventory is only refreshed on full batches.
  2. The hub's `configVersion` is `c-bd98dd27a0127536` both now and in that report, when the policy
     still existed. Either the version does not cover that policy (it had no target clusters), or
     the agent reported before materialising it. Worth a look in WO-20/WO-21.
