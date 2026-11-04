# Admin area contract

Owner: WO-19. Decisions: D-022 (policies: UI + Git), D-023 (local users), D-024 (approve once).
Plan: `docs/plans/admin-area.md`. Schemas: `api/openapi/hub.yaml` (tags `auth`, `admin`, and the new
`ui`/`agent`/`audit` operations). Changing anything here is a contract change (see `CLAUDE.md`).

Everything is **additive**: agents and UIs that predate it keep working unchanged. Old agents never
pull `/config`, so they never see hub policies; the nginx of the web chart keeps injecting the `ui`
token, so the read-only UI keeps working without a login.

## 1. Flow: the hub stores intent, the agent materialises it

```
UI (session, role) ──PUT/POST──► hub ──► Postgres: policies + policy_versions, settings(+_versions),
                                              cluster_automation, workload_approvals, audit_events
agent ──GET /v1/clusters/{c}/config (ETag, every pull)──► hub
agent: make CRs labelled resource-optimizer.oz.io/managed-by=hub equal to config.policies
       (create / update / delete only those), apply flags.paused like a cluster freeze,
       apply approvals once
agent ──POST /v1/clusters/{c}/workload-status {configVersion, policyInventory[]}──► hub
UI ◄── GET /v1/admin/policy-inventory, /v1/admin/clusters/{c} (in sync? Git policies read-only)
```

1. A write (`POST/PUT/PATCH/DELETE`) is validated (`api/v1alpha1/validate`), stored as a new
   version, and recorded as a `config_changed` audit entry. The cluster's `configVersion` changes.
2. On its next pull (≤ the pull interval, 30 s in dev) the agent gets `200` with the new config
   instead of `304`. For each `config.policies[i]` it validates the spec with
   `validate.HubPolicy(type, name, &spec)` and creates or updates the CR:
   - `metadata.name` = `name`
   - label `resource-optimizer.oz.io/managed-by: hub`
   - annotations `resource-optimizer.oz.io/hub-policy-id: <id>`,
     `resource-optimizer.oz.io/hub-version: "<version>"`,
     `resource-optimizer.oz.io/hub-policy-type: <type>`
   - `spec` = `spec`

   It deletes hub-labelled CRs whose name is not in the list. It never creates, updates or deletes
   a CR without the label. If a CR with the same name but without the label exists (e.g. from
   Git), the hub policy is not materialised and is reported with `valid: false`,
   `message: "name taken by an unmanaged policy"`. An invalid hub policy is not applied either (an
   older valid CR of it stays) and is reported invalid.
3. The agent sends `configVersion` (the version it materialised) and, with every full batch, the
   complete `policyInventory` (every CR, hub and Git, plus hub policies it could not
   materialise). The hub shows sync state (`AgentConfigSync.inSync`) and Git policies read-only.
4. If the hub is unreachable the CRs stay, so the last settings keep applying; the agent keeps the
   last `flags` it received.

Constants (`api/v1alpha1/wellknown.go`): `LabelManagedBy`, `ManagedByHub`, `AnnotationHubPolicyID`,
`AnnotationHubVersion`, `AnnotationHubPolicyType`, `HubPolicyType*`, `GlobalDefaultsPolicyName`,
`ClusterDefaultsPolicyName`, `OverridePolicyNamePrefix`, `OverridePolicyName()`, `ReservedNamePrefix`,
`Priority*`, `ReasonPaused`, `ReasonApprovedOnce`.

## 2. Policy types, priority bands and precedence

| Type | CR name | Priority | Sent to | Selector |
|---|---|---|---|---|
| `global-default` | `ro-global-defaults` | 0 | every cluster | empty |
| `cluster-default` | `ro-cluster-defaults` | 10 | its one cluster | empty |
| `policy` | user's name, not starting with `ro-` | 1 – 899999 | `clusters` (empty = all) | any |
| `workload-override` | `ro-override-<16 hex of sha256("<ns>/<kind>/<name>")>` | 900000 | the workload's cluster | `{namespaces:[ns], kinds:[kind], workloads:[{kind,namespace,name}]}` |

**Layering (agent, WO-21).** The CRD rule "only the winning policy applies" stays for policies
(type `policy` and every Git CR). Hub defaults and overrides are *layers* instead, so they combine
with the winning policy field by field (unset = inherit):

```
built-in defaults < ro-global-defaults < chart clusterDefaults < ro-cluster-defaults
  < winning policy (highest priority among hub `policy` + Git CRs, ties by name)
  < ro-override-… (the workload's override) < workload annotations (D-005)
```

The agent recognises the layers by `hub-policy-type` and excludes them from the winner selection.
Mode sources reported in `WorkloadStatus.modes` stay within the existing enum: hub global default →
`global-default`, chart or hub cluster default → `cluster-default`, winning policy or override →
`policy` (the override's name goes in the new `WorkloadStatus.override`), annotation → `annotation`.

The priority bands make an agent that ignores `hub-policy-type` degrade safely: defaults lose against
every other policy and an override wins.

**`selector.workloads`** (new, additive): a workload matches if it equals any entry; combined with
the other criteria (AND). An agent older than this field drops it when it decodes the CR, which
would widen an override's selector. Hence the hub always also sets `namespaces` and `kinds` on an
override. **Before downgrading an agent below WO-21, delete the hub CRs**:
`kubectl delete rsp -l resource-optimizer.oz.io/managed-by=hub`.

**Validation** (`api/v1alpha1/validate`):
- `Spec`: every CRD rule (enums, bounds, patterns, the CEL rules) plus what the agent checks when
  evaluating a policy (cron and time zone of windows, positive window duration, end after start,
  non-negative durations and quantities, valid globs and labels).
- `HubPolicy`: `Spec` plus the table above.
- `OverrideSelector(ns, kind, name)` builds the override selector.

The hub validates every write and answers `400` (code `invalid_policy`, `detail` = the field errors)
on failure. For Git CRs the agent's `valid` is informational only: the agent keeps treating them as
before.

## 3. Identity, roles, CSRF

| Role | Can (each includes the ones above) |
|---|---|
| `viewer` | every read: what the read-only UI shows today, settings, policies, inventory, effective policy, config audit |
| `operator` | workload override (incl. "Exclude" = both modes `off`), approve-once, pause a cluster or the fleet, impact preview |
| `admin` | defaults and policies, recommender and pricing settings, clusters, tokens, users, resume a `prod` cluster or the fleet, `auto-patch` overrides on `prod` clusters |

Each operation's `security` lists `sessionAuth: [<lowest user role>]` next to the bearer roles it
accepts. For authorization a `ui` token counts as `viewer` and an `admin` token as `admin`
(`hubapi.TokenUserRole`, `hubapi.UserRoleAtLeast`). Machine tokens (`agent`, `ui`, `admin`, D-012)
are unchanged.

- **Login:** `POST /v1/auth/login` sets the cookie `ro_session` (HttpOnly, SameSite=Strict, Path=/,
  Secure behind TLS). The session token is stored as SHA-256 in `sessions`. The response carries
  `csrfToken`.
- **CSRF:** every session write needs `X-CSRF-Token: <csrfToken>`; bearer requests need none.
  Missing or wrong: `403` `csrf_invalid`.
- **Both credentials present:** the session wins. The web nginx can keep injecting the `ui` token:
  an anonymous browser is a `viewer`, a logged-in one has its role. WO-22 must pass the `Cookie`
  header through (today nginx clears it) and send `credentials: 'include'`. The hub's CORS must then
  allow credentials and the headers `X-CSRF-Token`, `If-Match` and the methods `PUT`, `PATCH`,
  `DELETE` (WO-20).
- **Lockout:** `401` `invalid_credentials` for wrong user or password (same answer for both).
  `429` `rate_limited` / `account_locked` with `Retry-After`.
- **Bootstrap:** the initial admin comes from a Secret (D-023). Dev seeds the users `viewer`,
  `operator` and `admin` with passwords from the chart's dev values (WO-20).

## 4. Writes: If-Match, reason, audit

- Every versioned object has `version` and its responses carry `ETag: "<version>"`.
- Changing or deleting it needs `If-Match: "<version>"`: missing → `428`
  `precondition_required`; stale → `412` `version_conflict`.
- Creates need none. Pausing / resuming and creating an override take an optional `If-Match`, so
  a pause is never held up by a stale page.
- Every write body has a required `reason` (1–1024 chars). For DELETE it is the body
  `{"reason": "…"}`.
- Each write records one `config_changed` entry (`docs/contracts/events.md`): `actor`, `action`,
  `target`, `before`, `after`, `reason`, `version`.
- Nothing is hard-deleted: policies, users and clusters get `deletedAt`, and their versions and
  audit stay.
- Until WO-20 implements an endpoint the hub answers `404 not_found` (no route) or `501
  not_implemented`.

## 5. Endpoints with examples

Headers in the examples: `C` = `Cookie: ro_session=…`, `X` = `X-CSRF-Token: …`. Errors use the
`Problem` schema.

### Auth

`POST /v1/auth/login` (no auth)
```json
→ {"username": "alice", "password": "correct horse battery"}
← 200  Set-Cookie: ro_session=…; HttpOnly; SameSite=Strict; Path=/
{"authMethod": "session", "role": "admin", "csrfToken": "q8…Zt",
 "sessionExpiresAt": "2026-11-05T02:00:00Z",
 "user": {"username": "alice", "displayName": "Alice", "role": "admin", "disabled": false,
          "mustChangePassword": false, "createdAt": "2026-11-04T09:00:00Z", "version": 3}}
```
`POST /v1/auth/logout` (C, X) → `204`, cookie cleared.

`GET /v1/me` (C, or a `ui`/`admin` bearer token) → `Me`. With a bearer `ui` token:
`{"authMethod": "token", "role": "viewer", "tokenId": "2"}`.

`POST /v1/me/password` (C, X) `{"currentPassword": "…", "newPassword": "…"}` → `204`.

### Settings (hub-only; never sent to clusters)

`GET /v1/admin/settings` (viewer)
```json
{"recommender": {"version": 2, "updatedAt": "2026-11-04T10:00:00Z",
   "updatedBy": {"type": "user", "name": "alice", "role": "admin"}, "reason": "longer prod window",
   "value": {"interval": "24h", "retention": "2160h",
     "profiles": [
       {"name": "dev", "window": "168h", "cpuPercentile": 0.9, "memoryPercentile": 0.95, "cpuHeadroom": 0.1, "memoryBuffer": 0.1},
       {"name": "production", "window": "336h", "cpuPercentile": 0.95, "memoryPercentile": 0.99, "cpuHeadroom": 0.15, "memoryBuffer": 0.15},
       {"name": "high-availability", "window": "672h", "cpuPercentile": 0.99, "memoryPercentile": 0.99, "cpuHeadroom": 0.3, "memoryBuffer": 0.25}]}},
 "pricing": {"version": 0, "value": {"clusters": []}}}
```
`GET /v1/admin/settings/recommender` and `.../pricing` (viewer) → the section with `ETag: "2"`.

`PUT /v1/admin/settings/recommender` (admin, If-Match) `{"value": RecommenderSettings, "reason": "…"}`
→ `200` with the new doc and `ETag: "3"`. Records `SettingsUpdated`. It takes effect on the next
run; `POST /v1/admin/recompute` starts one.

`PUT /v1/admin/settings/pricing` (admin, If-Match)
```json
→ {"value": {"defaultDiscountPercent": 0,
             "clusters": [{"cluster": "ro-spoke-1", "discountPercent": 28},
                          {"cluster": "ro-spoke-2", "cpuCoreHourUSD": 0.031, "memoryGiBHourUSD": 0.004}]},
   "reason": "EDP 2027"}
← 200 PricingSettingsDoc, ETag: "1"
```
A fixed rate replaces the metric or default rate. The discount then applies to whichever rate is
used.

`GET /v1/admin/settings/pricing/versions?limit=20` (viewer)
→ `{"items": [{"section": "pricing", "version": 1, "value": {…}, "createdAt": "…", "actor": {…}, "reason": "EDP 2027"}]}`

### Policies

`GET /v1/admin/policies?cluster=ro-spoke-1&type=policy` (viewer) → `{"items": [AdminPolicy…]}`.

`POST /v1/admin/policies` (admin)
```json
→ {"type": "policy", "name": "payments-cpu-auto", "description": "CPU auto-patch for payments",
   "clusters": ["ro-spoke-1"],
   "spec": {"priority": 100, "selector": {"namespaces": ["payments"]},
            "cpu": {"mode": "auto-patch"}, "apply": {"strategy": "in-place"}},
   "reason": "FinOps Q4 rollout"}
← 201 ETag: "1"
{"id": "0192f7c4-…", "type": "policy", "name": "payments-cpu-auto", "priority": 100,
 "clusters": ["ro-spoke-1"], "spec": {…}, "version": 1,
 "createdAt": "…", "createdBy": {"type": "user", "name": "alice", "role": "admin"},
 "updatedAt": "…", "updatedBy": {…}}
```
Global defaults: `{"type": "global-default", "spec": {"cpu": {"mode": "audit"}, "guardrails":
{"minConfidence": "0.8"}}, "reason": "…"}`. The hub sets the name `ro-global-defaults` and priority
0; a second one gives `409 already_exists`. A cluster default is the same with
`"type": "cluster-default", "clusters": ["ro-spoke-1"]`.

`GET /v1/admin/policies/{policyId}` (viewer) → `AdminPolicy`, `ETag: "1"`.

`PUT /v1/admin/policies/{policyId}` (admin, `If-Match: "1"`) with a `PolicyInput` body → `200`,
`ETag: "2"`. Changing `type` gives `400 type_immutable`.

`DELETE /v1/admin/policies/{policyId}` (admin, `If-Match: "2"`) `{"reason": "replaced by team policy"}`
→ `200` with `deletedAt` set (version 3, operation `delete`).

`GET /v1/admin/policies/{policyId}/versions` (viewer)
```json
{"items": [
  {"version": 3, "operation": "delete", "name": "payments-cpu-auto", "priority": 100, "clusters": ["ro-spoke-1"],
   "spec": {…}, "deleted": true, "createdAt": "…", "actor": {…}, "reason": "replaced by team policy"},
  {"version": 2, "operation": "update", …, "deleted": false, …},
  {"version": 1, "operation": "create", …}]}
```
`POST /v1/admin/policies/{policyId}/revert` (admin, `If-Match: "3"`) `{"version": 1, "reason": "rollback"}`
→ `200` version 4 (operation `revert`, `revertedFrom: 1`, undeleted). The CR follows on the next pull.

`GET /v1/admin/policy-inventory?cluster=ro-spoke-1` (viewer)
```json
{"items": [{"cluster": "ro-spoke-1", "reportedAt": "…", "configVersion": "c-41",
  "items": [
    {"name": "payments-cpu-auto", "source": "hub", "priority": 100, "generation": 4, "valid": true,
     "hubPolicyId": "0192f7c4-…", "hubVersion": 4, "hubPolicyType": "policy", "matchedWorkloads": 12, "spec": {…}},
    {"name": "team-defaults", "source": "git", "priority": 50, "generation": 1, "valid": true, "matchedWorkloads": 30, "spec": {…}}]}]}
```

`POST /v1/admin/preview` (operator; read-only, no If-Match, no reason, nothing recorded)
```json
→ {"changes": [{"op": "upsert", "policyId": "0192f7c4-…",
                "policy": {"type": "policy", "name": "payments-cpu-auto", "clusters": ["ro-spoke-1"],
                           "spec": {"priority": 100, "selector": {"namespaces": ["payments"]}, "cpu": {"mode": "auto-patch"}}}}],
   "limit": 50}
← 200
{"workloadsEvaluated": 214, "workloadsChanged": 12,
 "savingsIntoAutoPatchUSD": {"cpu": 412.5, "memory": 0, "total": 412.5},
 "savingsOutOfAutoPatchUSD": {"cpu": 0, "memory": 0, "total": 0},
 "items": [{"workloadId": "ro-spoke-1/payments/Deployment/checkout", "cluster": "ro-spoke-1",
            "namespace": "payments", "kind": "Deployment", "name": "checkout",
            "before": {"cpu": "audit", "memory": "audit", "cpuSource": "global-default", "memorySource": "global-default"},
            "after": {"cpu": "auto-patch", "memory": "audit", "cpuSource": "policy", "memorySource": "global-default"},
            "policyAfter": "payments-cpu-auto", "monthlySavingsUSD": {"cpu": 61.2, "memory": 8.1, "total": 69.3}}],
 "truncated": false, "warnings": []}
```
Bulk "Set mode…" in the opportunities table previews one `workload-override` draft per selected
workload (`policy.workloadId`).

### Workloads (operator writes)

`GET /v1/workloads/{id}/effective-policy` (viewer)
```json
{"workloadId": "ro-spoke-1/payments/Deployment/checkout",
 "layers": [
   {"source": "built-in", "applied": true},
   {"source": "global-default", "name": "ro-global-defaults", "origin": "hub", "policyId": "…", "priority": 0, "applied": true, "spec": {"guardrails": {"minConfidence": "0.8"}}},
   {"source": "policy", "name": "team-defaults", "origin": "git", "priority": 50, "applied": false, "note": "priority 50 < 100 of payments-cpu-auto"},
   {"source": "policy", "name": "payments-cpu-auto", "origin": "hub", "policyId": "…", "priority": 100, "applied": true, "spec": {"cpu": {"mode": "auto-patch"}}},
   {"source": "annotation", "name": "resource-optimizer.oz.io/memory-mode", "applied": true, "spec": {"memory": {"mode": "off"}}}],
 "fields": [
   {"path": "cpu.mode", "value": "auto-patch", "source": "policy", "name": "payments-cpu-auto"},
   {"path": "memory.mode", "value": "off", "source": "annotation", "name": "resource-optimizer.oz.io/memory-mode"},
   {"path": "guardrails.minConfidence", "value": "0.8", "source": "global-default", "name": "ro-global-defaults"},
   {"path": "apply.strategy", "value": "in-place", "source": "built-in"}],
 "modes": {"cpu": "auto-patch", "memory": "off", "cpuSource": "policy", "memorySource": "annotation"},
 "agentModes": {"cpu": "audit", "memory": "off", "cpuSource": "global-default", "memorySource": "annotation"},
 "agentPolicy": "", "warnings": ["agent has not applied config c-41 yet"]}
```

`GET /v1/workloads/{id}/override` (viewer) → `AdminPolicy` (type `workload-override`) or `404`.

`PUT /v1/workloads/{id}/override` (operator; If-Match only when one exists)
```json
→ {"spec": {"cpu": {"mode": "off"}, "memory": {"mode": "off"}}, "reason": "Exclude: batch job, owner asked"}
← 200 ETag: "1"  AdminPolicy {"type": "workload-override", "name": "ro-override-4f6e1e27aa6bac08",
   "priority": 900000, "clusters": ["ro-spoke-1"], "workloadId": "ro-spoke-1/payments/Deployment/checkout",
   "spec": {"priority": 900000, "selector": {"namespaces": ["payments"], "kinds": ["Deployment"],
            "workloads": [{"kind": "Deployment", "namespace": "payments", "name": "checkout"}]},
            "cpu": {"mode": "off"}, "memory": {"mode": "off"}}, …}
```
Sending `selector` or `priority` gives `400 invalid_override`. An `auto-patch` mode on a `prod`
cluster by an operator gives `403 admin_required`.

`DELETE /v1/workloads/{id}/override` (operator, If-Match) `{"reason": "back to team policy"}` → `204`.

`POST /v1/workloads/{id}/approve` (operator)
```json
→ {"recommendationID": "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b7c10", "reason": "reviewed with owner"}
← 201 {"id": "6c1d…", "workloadId": "ro-spoke-1/payments/Deployment/checkout",
       "recommendationID": "0b9b5c1e-…", "approvedBy": {"type": "user", "name": "bob", "role": "operator"},
       "approvedAt": "…", "expiresAt": "…+24h", "reason": "reviewed with owner", "state": "pending"}
```
An older recommendation id gives `409 recommendation_stale`. If no resource is in `audit`:
`409 not_in_audit`. The latest approval also appears in `WorkloadDetail.approval`, the override in
`WorkloadDetail.override`, and both changes in `WorkloadHistory.configChanges`.

### Clusters and automation

`POST /v1/admin/clusters` (admin)
```json
→ {"name": "eks-prod-eu1", "environment": "prod", "owner": "platform", "policySource": "both",
   "createAgentToken": true, "reason": "new region"}
← 201 {"cluster": ClusterAdmin, "agentToken": {"token": {"id": "17", "role": "agent", "cluster": "eks-prod-eu1",
        "description": "agent eks-prod-eu1", "createdAt": "…"}, "value": "ro-…shown-once…"}}
```
`GET /v1/admin/clusters/{cluster}` (viewer)
```json
{"name": "ro-spoke-1", "description": "", "environment": "prod", "owner": "payments", "policySource": "both",
 "version": 3, "automation": {"paused": false, "version": 4}, "paused": false,
 "agent": {"agentVersion": "dev-…", "lastHeartbeatAt": "…", "lastConfigPullAt": "…",
           "configVersion": "c-41", "appliedConfigVersion": "c-41", "inSync": true},
 "policyInventory": ClusterPolicyInventory}
```
`PATCH /v1/admin/clusters/{cluster}` (admin, If-Match) `{"environment": "prod", "reason": "…"}` → `ClusterAdmin`.
`DELETE /v1/admin/clusters/{cluster}` (admin, If-Match) `{"reason": "…"}` → `204` (soft delete, tokens revoked).

`PUT /v1/clusters/{cluster}/automation` (operator; If-Match optional)
```json
→ {"paused": true, "reason": "incident INC-4711", "resumeAt": "2026-11-04T18:00:00Z"}
← 200 ETag: "5" {"paused": true, "reason": "incident INC-4711", "by": {…}, "since": "…", "resumeAt": "…", "version": 5}
```
Resume `{"paused": false, "reason": "…"}`: on a `prod` cluster only an admin (`403 admin_required`).

`GET /v1/admin/automation` (viewer)
→ `{"fleet": Automation, "clusters": [{"cluster": "ro-spoke-1", "automation": Automation, "paused": true}]}`.
`PUT /v1/admin/automation` (operator to pause, admin to resume) → the fleet `Automation`.

`GET /v1/clusters` gains the optional fields `environment`, `owner`, `policySource`, `paused`,
`automation` and `configSync`.

### Agent

`GET /v1/clusters/ro-spoke-1/config` (agent token; `If-None-Match: "c-40"`)
```json
← 200 ETag: "c-41"
{"cluster": "ro-spoke-1", "configVersion": "c-41", "generatedAt": "…", "policySource": "both",
 "policies": [
   {"id": "…", "name": "ro-global-defaults", "type": "global-default", "version": 2, "priority": 0,
    "spec": {"priority": 0, "guardrails": {"minConfidence": "0.8"}}},
   {"id": "…", "name": "payments-cpu-auto", "type": "policy", "version": 4, "priority": 100,
    "spec": {"priority": 100, "selector": {"namespaces": ["payments"]}, "cpu": {"mode": "auto-patch"}}},
   {"id": "…", "name": "ro-override-4f6e1e27aa6bac08", "type": "workload-override", "version": 1, "priority": 900000, "spec": {…}}],
 "flags": {"paused": true, "pauseScope": "cluster", "pauseReason": "incident INC-4711",
           "pausedAt": "…", "resumeAt": "2026-11-04T18:00:00Z"},
 "approvals": [{"id": "6c1d…", "workload": {"namespace": "payments", "kind": "Deployment", "name": "checkout"},
                "recommendationID": "0b9b5c1e-…", "expiresAt": "…", "approvedBy": "bob", "approvedAt": "…"}]}
```
- `policySource: git` → `policies: []` (the agent deletes the hub CRs).
- With no change since the given ETag: `304`.

`POST /v1/clusters/ro-spoke-1/workload-status` (agent) gains
`{"agentVersion": "…", "configVersion": "c-41", "policyInventory": [PolicyInventoryItem…], "items": [...]}`.
- `policyInventory` absent = unchanged; `[]` = no policies.
- `WorkloadStatus.override` names the applied override.

Events from the agent:
- `blocked` / `Paused`, with `details.pauseScope`, while `flags.paused`. Rollbacks still run.
- `applied` / `ApprovedOnce`, with `details.approvalId` and `details.approvedBy`, for an approval
  it applied. The hub marks the approval consumed when it stores that event.

### Tokens and users (admin)

`GET /v1/admin/tokens?cluster=ro-spoke-1` → `{"items": [TokenInfo…]}` (never values; `lastUsedAt` at most per minute).
`POST /v1/admin/tokens` `{"role": "ui", "description": "grafana link", "expiresAt": "2027-01-01T00:00:00Z", "reason": "…"}`
→ `201 {"token": TokenInfo, "value": "ro-…"}` (value once).
`DELETE /v1/admin/tokens/{tokenId}` `{"reason": "leaked"}` → `200 TokenInfo` with `revokedAt`.

`GET /v1/admin/users` → `{"items": [User…]}`.
`POST /v1/admin/users` `{"username": "bob", "displayName": "Bob", "role": "operator", "password": "…", "reason": "…"}`
→ `201 User` (`mustChangePassword: true`).
`GET /v1/admin/users/{username}` → `User`, `ETag`.
`PATCH /v1/admin/users/{username}` (If-Match) `{"role": "admin", "reason": "…"}` → `User`. Demoting
or disabling the last enabled admin gives `409 last_admin`.
`DELETE /v1/admin/users/{username}` (If-Match) `{"reason": "left"}` → `204`.
`POST /v1/admin/users/{username}/reset-password` `{"reason": "forgot"}`
→ `{"temporaryPassword": "…", "mustChangePassword": true}`.

### Audit

`GET /v1/audit/config?targetType=policy&order=desc&limit=50` (viewer)
```json
{"items": [{"id": "0192f…", "type": "config_changed", "time": "…",
  "actor": {"type": "user", "name": "alice", "role": "admin"}, "action": "PolicyUpdated",
  "target": {"type": "policy", "id": "0192f7c4-…", "name": "payments-cpu-auto"},
  "reason": "raise min confidence", "version": 2,
  "before": {…AdminPolicy v1…}, "after": {…AdminPolicy v2…}}]}
```
`GET /v1/audit` (workload events) is unchanged and never returns `config_changed`.

## 6. Storage (migrations 0007 – 0009)

| Migration | Tables / columns |
|---|---|
| `0007_admin_policies_settings` | `settings`, `settings_versions`, `policies` (reserved names, priority bands, one live global default / cluster default per cluster / override per workload, unique live names), `policy_versions`, `clusters` + `environment, owner, policy_source, version, updated_at, updated_by, deleted_at, config_pulled_at, applied_config_version, policy_inventory, policy_inventory_at`, `cluster_automation` (`scope` = cluster or `*` for the fleet) |
| `0008_users_sessions` | `users` (bcrypt `password_hash`, role, disabled, must_change_password, failed_logins, locked_until, soft delete), `sessions` (`id_sha256`, `csrf_token`, expiry, revoke), `api_tokens` + `last_used_at, created_by, revoked_by` |
| `0009_approvals_config_events` | `workload_approvals` (one open per workload), `audit_events`: type `config_changed`; `cluster, namespace, workload_kind, workload_name` nullable for it only (`audit_events_shape_check`); + `actor, target_type, target_id, target_name, version, before, after` |

All are idempotent (`IF NOT EXISTS`, drop-then-add constraints) and have down migrations. **The
hub's existing audit queries must add `type <> 'config_changed'`** (WO-20) once it writes such rows.
