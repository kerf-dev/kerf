# Plan: Admin area and write actions (phase 4, WO-14b)

Status: **accepted** (2026-11-04). The user decided D1–D3: UI + Git policies (D-022), local users first with OIDC later (D-023), approve-once included (D-024).

## 1. Problem

Today the UI is read-only. Every setting that controls behaviour is in one of these places, and none of them is editable in the UI:

| Setting | Where it lives today | Who can change it |
|---|---|---|
| Modes, strategy, profile, limits, guardrails, rollback, HPA, JVM, windows | `RightsizingPolicy` CRs on each spoke, plus the `clusterDefaults` chart value | kubectl or Git |
| Per-workload overrides | workload annotations | kubectl or Git |
| Cluster-wide freeze ("kill switch") | agent chart value `freeze` | helm upgrade |
| Recommender profiles (window, percentiles, headroom, buffer) and schedule | hub config / chart values (`DefaultProfiles`) | hub redeploy |
| Pricing (discounts, overrides) | `dev/prices.yaml`, pricing flags | redeploy |
| Clusters and agent/UI tokens | `hub-dev-tokens` Secret / dev seed | redeploy |

Platform and FinOps teams need to manage all of this from the UI, with an audit trail. Two constraints still apply:
- The hub never gets write credentials to a cluster (D-004).
- Teams that keep policies in Git must keep working (D-006).

## 2. Core design: the hub stores intent, the agent materialises it

```
UI ──(OIDC user, role operator/admin)──► Hub API ──► Postgres (settings, policies, versions, audit)
                                              ▲
             agent pulls GET /v1/clusters/{c}/config (ETag), every pull interval
                                              │
Spoke: agent reconciles RightsizingPolicy CRs labelled
       resource-optimizer.oz.io/managed-by=hub (create / update / delete only those)
       + applies cluster flags (pause, freeze) in memory
```

How this works:
- **Pull model and reuse.** The pull model stays, and the hub still has no cluster credentials. On the cluster, the policy engine keeps working on CRs. The existing precedence (D-005) and all phase 2–3 logic are reused unchanged.
- **CRs from both sources coexist.**
  - Hub-managed CRs carry `managed-by=hub` and a `hub-version` annotation.
  - CRs from Git (no label) are never touched by the agent.
  - The agent reports every policy CR on the cluster back to the hub, so the UI can show Git policies read-only, marked with a "Git" badge.
- **Offline and hub-down behaviour.** CRs persist on the cluster, so the last known settings keep applying. The pause flag defaults to the last value the agent received. If the hub is unreachable, nothing new is applied anyway.
- **Global defaults** become a hub-managed policy named `ro-global-defaults`: priority 0, empty selector, sent to every cluster.
- **Cluster defaults** become `ro-cluster-defaults`: priority 10, sent to one cluster only. The chart value `clusterDefaults` stays as a fallback for installs without the admin feature.
- **Workload overrides** from the UI become one policy per workload in a reserved priority band (≥ 900000). This needs one additive selector field: `selector.workloads: [{kind, namespace, name}]`. A Git annotation on the workload still wins (D-005).
- **Pause automation** works per cluster or fleet-wide. It is a flag in the config response, and the agent treats it like a cluster-wide freeze: no new changes, rollbacks still allowed. It takes effect on the next pull (≤ 30 s), with no CR and no redeploy.
- **Hub-only settings never reach clusters:**
  - recommender profiles and interval
  - pricing adjustments
  - tokens
  - retention

  They are stored as versioned rows. A recommender change takes effect on the next run; a "Recompute now" button triggers one.

✅ **D1 – Policy source of truth: UI + Git (D-022).**
- *Recommended:* `hub + git` as above. A per-installation setting `policySource: hub | git | both` exists anyway.
- In `git` mode, the admin pages are read-only and offer "Export YAML" so teams commit the policy themselves.

## 3. Identity and roles

Writes need a real user for the audit trail ("who set payments to auto-patch?").

| Role | Can |
|---|---|
| `viewer` | everything the UI shows today |
| `operator` | workload actions (mode, exclude, approve-once) and pausing a cluster |
| `admin` | defaults, policies, recommender, pricing, clusters, tokens, users |

Mechanics:
- **Login:** a username/password form handled by the hub (D-023). The session cookie is HttpOnly, SameSite=Strict, with CSRF protection on writes. There is login rate limiting and lockout, and admins can reset passwords. OIDC comes later and maps IdP groups to the same roles.
- **Roles:** stored per user in the hub.
- **Machine tokens:** agent, ui and admin tokens stay as they are.
- **Local dev (k3d):** seeded users `viewer`, `operator` and `admin`, with passwords from the chart's dev values.

✅ **D2 – Local users first (D-023).** Users and passwords (bcrypt) live in the hub. An initial admin is bootstrapped from a Secret on first start. OIDC is added later behind the same session and role model. The original options were:
- *Recommended:* generic OIDC, so any IdP works (Azure AD/Entra, Okta, Keycloak).
- Name the one you use, and I'll make it the documented example.
- Alternative for now: local users stored in the hub (bcrypt), with OIDC later.

## 4. Pages (`/admin`, shown in the nav only for admin and operator)

1. **Automation overview.** For each cluster:
   - agent version, last pull, last policy sync
   - policy source, and paused/frozen state
   - workloads by effective mode

   Plus a big **Pause / Resume** switch for each cluster and for the whole fleet, with a reason and an optional auto-resume time.
2. **Defaults** (global). One form for:
   - modes CPU/memory, strategy, profile
   - CPU limits (keep / remove / ratio), memory limit = request
   - guardrails (learning period, min confidence, max change %, min CPU/memory, cooldown)
   - rollback thresholds
   - HPA coordination and bounds
   - JVM detection
   - maintenance and freeze windows, with a cron builder and a timezone

   Each field shows its built-in default. Saving shows an **impact preview** before confirming:
   - workloads whose effective mode changes
   - savings that move into auto-patch
3. **Clusters.**
   - **List:** environment label (prod / non-prod), owner team, and status.
   - **Detail:** cluster overrides (the same form, showing the difference from global), pause, windows.
   - **Agent token:** create, rotate or revoke. The value is shown once, together with a ready-to-copy `helm install` snippet.
   - **Register new cluster** wizard.
4. **Policies.**
   - **Table:** name, source (Hub/Git), clusters, selector summary, priority, matched workloads, last change.
   - **Editor:** a selector builder (namespaces with globs and excludes, labels, kinds, workloads) with a live "matches N workloads" list.
   - **Effective-policy simulator:** pick a workload and see the precedence chain and which setting won.
   - **History:** per-policy version history with diff and one-click revert.
   - **Export:** YAML.
5. **Recommender.**
   - Profiles table (dev / production / high-availability, plus custom profiles): window, CPU and memory percentile, headroom, buffer, peak window.
   - Run interval, history retention, and a **Recompute now** button.
6. **Pricing.**
   - Current rates per cluster and their source.
   - Per-cluster discount % (Savings Plans / EDP until CUR arrives in WO-16), and optional fixed rate overrides.
7. **Access.**
   - Local users: create, set the role, disable, reset the password.
   - API tokens: create with a role and expiry, revoke, last used.
8. **Audit.** All config changes (actor, time, before/after diff, reason). This is the existing Activity log with a `config` type filter.

**Write actions in existing pages:**
- **Workload detail:** an "Automation" card.
  - Set the CPU and memory mode, strategy and profile for this workload (creates or edits its override).
  - **Exclude** (mode off).
  - **Approve once** (see D3).
  - "Clear override" to return to the inherited settings.
- **Opportunities table:** row selection with bulk "Set mode…" or "Exclude". It always shows a confirmation dialog with the count and the savings impact.
- **Production confirmation:** every write to a cluster labelled `prod` asks the user to type the cluster name.

✅ **D3 – "Approve once": included (D-024).**
- *Recommended:* include it. An audit workload can be applied once for the current recommendation, with guardrails still enforced.
- The hub records the approval for that recommendation id, and the agent applies it as if the mode were auto-patch for that one change.
- This gives FinOps a safe review-and-approve flow without turning on automation.

## 5. API and contract changes (one contract work-order first)

- **Hub, read:**
  - `GET /v1/admin/settings`
  - `GET /v1/admin/policies`
  - `GET /v1/admin/policies/{id}/versions`
  - `GET /v1/admin/clusters/{c}`
  - `GET /v1/admin/tokens`
  - `GET /v1/workloads/{id}/effective-policy`
- **Hub, write:**
  - All writes take `If-Match` (optimistic locking) and a `reason`, and record an `audit_events` row of type `config_changed`.
  - `PUT /v1/admin/settings/{section}`
  - Policies: `POST`, `PUT`, `DELETE /v1/admin/policies[/{id}]`, and `POST .../revert`
  - `POST /v1/admin/preview` (impact dry-run)
  - `PUT /v1/clusters/{c}/automation` (pause / resume)
  - Workload override: `PUT` and `DELETE /v1/workloads/{id}/override`
  - `POST /v1/workloads/{id}/approve`
  - Tokens: `POST` and `DELETE`
  - Clusters: `POST`, `PATCH`, `DELETE`
- **Agent:**
  - `GET /v1/clusters/{c}/config` returns policies, flags and approvals, with an ETag.
  - The status batch gains `policyInventory[]` (name, source, priority, generation) and `configVersion`.
- **Auth:** `POST /auth/login`, `POST /auth/logout`, `GET /v1/me`. Users: `GET`, `POST`, `PATCH`, `DELETE /v1/admin/users`, and `POST .../reset-password`.
- **CRD:**
  - `RightsizingPolicy.spec.selector.workloads[]` (additive).
  - Label and annotation constants in `wellknown.go`.
- **DB** (migrations 0007+, numbers assigned by the orchestrator):
  - `settings` (versioned)
  - `policies` and `policy_versions`
  - `cluster_automation`
  - `workload_approvals`
  - `users` (bcrypt hash, role, disabled, failed-login counters)
  - `sessions`
  - `api_tokens.role/expiry/last_used`

## 6. Safety rules

- **Validation:** every write is validated server-side with the same rules as the CRD (one shared Go validator). The agent rejects and reports an invalid policy rather than applying it.
- **Rate of change:** the agent applies at most N config changes per pull, and a policy change never bypasses guardrails, cooldown or learning period.
- **Pause:** pause is always allowed for an operator. Resume and enabling auto-patch on a `prod` cluster require admin.
- **History:** nothing is deleted. Policies are soft-deleted and their versions are kept.
- **Tokens:** shown once and stored hashed (as today).

## 7. Work breakdown (parallel after the contract)

| WO | Track | Depends on |
|---|---|---|
| WO-19 | **Contract**: hub.yaml admin/config/auth endpoints, CRD selector.workloads, migrations 0007–0009, events | – |
| WO-20 | **Hub**: settings/policies store + versions, admin API, preview/effective-policy, config endpoint for agents, local users + sessions + roles, audit | WO-19 |
| WO-21 | **Agent**: config pull, CR materialisation (hub-labelled only), policy inventory, pause flag, approve-once | WO-19 |
| WO-22 | **Web**: login, role-aware nav, the `/admin` pages, workload Automation card, bulk actions, confirmations | WO-19 (mock API first) |
| WO-24 | **E2E**: UI change → CR on the spoke → applied; pause blocks; approve-once; Git CR untouched; revert | WO-20, 21, 22 |

Estimate: the contract first (one session), then WO-20, 21 and 22 in parallel, then WO-24. Similar in size to phase 2.

## 8. Definition of done

On k3d, an admin logs in with a seeded local user and does the following:
1. Sets namespace `payments` on `ro-spoke-1` to CPU auto-patch, sees the impact preview, and saves. Within one pull, a hub-labelled `RightsizingPolicy` appears on the spoke, workloads apply, and the change shows in Audit with the actor.
2. Pauses `ro-spoke-1` from the overview. Applies stop (a `blocked/Paused` event); rollbacks still work.
3. As operator, approves one audit workload once. It is applied once, and the next recommendation is not.
4. Reverts the policy to its previous version. The CR follows.
5. Sees a Git-managed policy as read-only with its Git badge. The agent never modifies it.
