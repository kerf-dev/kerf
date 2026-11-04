# WO-19 hand-over (admin area contract)

Branch `wo-19-admin-contract` (pushed, no PR). The contract only: no hub, agent or web behaviour
changed. Full flow, rules and an example for every endpoint: **`docs/contracts/admin.md`**.
Schemas: `api/openapi/hub.yaml`.

## Deviations from the work-order text (decided here, please confirm)
1. **Auth paths are `/v1/auth/login` and `/v1/auth/logout`, not `/auth/*`**: the web nginx
   proxies only `/v1/`, and everything else in the API is versioned.
2. **Config changes are not `Event`s.** A `config_changed` row lives in `audit_events`, but the API
   serves it as `ConfigChange` from **`GET /v1/audit/config`** (and in
   `WorkloadHistory.configChanges`), not from `GET /v1/audit`. The `EventType` enum is
   unchanged. Reason: `Event.workload` is required and the deployed UI has exhaustive
   `Record<EventType,…>` maps, so widening either would break running agents or the UI.
3. **Hub defaults and overrides are layers, not competing policies.** CRD semantics say only the
   winning policy applies, which would make `ro-global-defaults` (prio 0) useless as a default. The
   agent layers them instead (`hub-policy-type` annotation), as listed below. The priority bands
   remain as a safe fallback for agents that ignore the annotation.
4. **`ModeSource` enum unchanged** (the web uses `Record<ModeSource,…>`). Overrides report `policy`,
   plus the new `WorkloadStatus.override` name.
5. Extras beyond the WO list:
   - `GET/PUT /v1/admin/automation` (fleet pause)
   - `GET /v1/admin/policy-inventory` (Git policies for the UI)
   - `GET /v1/workloads/{id}/override`
   - `GET /v1/admin/settings/{recommender,pricing}` and `.../{section}/versions`
   - `GET /v1/admin/users/{username}`
   - `POST /v1/me/password` (forced change after reset)
   - `PolicyInventoryItem.spec`, so the hub can show and simulate Git policies
6. **No 501 stub routes**: `internal/hub/server.go` is WO-20's, so I did not edit it. Unrouted new
   paths answer `404 not_found` today. WO-20 should register all new routes at once, as `501
   not_implemented` first if needed.
7. Custom recommender profiles are **not** in the contract (the `Profile` enum on the CRD and API
   would have to widen). Settings hold exactly the 3 built-in profiles.

## Everything added

**CRD (`api/v1alpha1`, regenerated `api/crds` + `deploy/charts/agent/crds`)**
- `RightsizingPolicy.spec.selector.workloads[] {kind, namespace, name}` (type `NamespacedWorkloadRef`,
  max 100). Additive. Old agents drop it, so overrides also set `namespaces` and `kinds`.
- `wellknown.go`:
  - `LabelManagedBy` = `resource-optimizer.oz.io/managed-by`, `ManagedByHub` = `hub`
  - `AnnotationHubPolicyID` / `AnnotationHubVersion` / `AnnotationHubPolicyType` (`hub-policy-id`, `hub-version`, `hub-policy-type`)
  - `HubPolicyType{GlobalDefault,ClusterDefault,Policy,WorkloadOverride}`
  - `GlobalDefaultsPolicyName` = `ro-global-defaults`, `ClusterDefaultsPolicyName` = `ro-cluster-defaults`
  - `OverridePolicyNamePrefix` = `ro-override-`, `OverridePolicyName(ns, kind, name)` (16 hex of sha256, pinned by a test)
  - `ReservedNamePrefix` = `ro-`
  - `PriorityGlobalDefault` 0, `PriorityClusterDefault` 10, `PriorityPolicyMin`/`Max` 1/899999,
    `PriorityWorkloadOverride` 900000, `PriorityMax` 1000000
  - `ReasonPaused` (blocked), `ReasonApprovedOnce` (applied), documented in `events.md`
- **Shared validator `api/v1alpha1/validate`**:
  - `Spec` (CRD rules plus the agent's semantic checks: cron, tz, windows, globs, labels, non-negative)
  - `HubPolicy(type, name, spec)` (plus reserved names, bands and the override selector shape)
  - `OverrideSelector`, `Selector`, `Window(s)`, `WorkloadRef`
  - Returns `field.ErrorList`. Table tests, plus a test that `api/examples` validate.

**OpenAPI** (lint clean). Security scheme `sessionAuth` (cookie `ro_session`, roles
viewer < operator < admin). Every existing `ui` operation also accepts `sessionAuth: [viewer]`;
recompute accepts `sessionAuth: [admin]`. Reusable components:
- `CSRFToken` header (`X-CSRF-Token`)
- `IfMatch` / `IfMatchOptional`
- header `ETag` (`"<version>"`)
- responses `Conflict` 409, `PreconditionFailed` 412, `PreconditionRequired` 428, `TooManyRequests` 429,
  `PolicyNotFound`, `UserNotFound`

| Operation | Path | Lowest role |
|---|---|---|
| getClusterConfig | GET /v1/clusters/{cluster}/config (ETag) | agent token |
| login / logout / getMe / changeOwnPassword | POST /v1/auth/login, POST /v1/auth/logout, GET /v1/me, POST /v1/me/password | – / viewer |
| getSettings, get/put Recommender/PricingSettings, listSettingsVersions | /v1/admin/settings[/recommender\|/pricing\|/{section}/versions] | viewer / admin (PUT) |
| listPolicies, createPolicy, getPolicy, updatePolicy, deletePolicy, listPolicyVersions, revertPolicy | /v1/admin/policies[/{policyId}[/versions\|/revert]] | viewer / admin |
| getPolicyInventory | GET /v1/admin/policy-inventory | viewer |
| previewPolicyChange | POST /v1/admin/preview | operator |
| createCluster, getClusterAdmin, updateCluster, deleteCluster | /v1/admin/clusters[/{cluster}] | viewer / admin |
| setClusterAutomation | PUT /v1/clusters/{cluster}/automation | operator (resume `prod`: admin) |
| getFleetAutomation, setFleetAutomation | /v1/admin/automation | viewer / operator (resume: admin) |
| getEffectivePolicy | GET /v1/workloads/{id}/effective-policy | viewer |
| get/put/deleteWorkloadOverride | /v1/workloads/{id}/override | viewer / operator (auto-patch on `prod`: admin) |
| approveRecommendation | POST /v1/workloads/{id}/approve | operator |
| listTokens, createToken, revokeToken | /v1/admin/tokens[/{tokenId}] | admin |
| listUsers, createUser, getUser, updateUser, deleteUser, resetUserPassword | /v1/admin/users[/{username}[/reset-password]] | admin |
| queryConfigAudit | GET /v1/audit/config | viewer |

Additive fields on existing schemas:
- `WorkloadStatusBatch.configVersion`, `.policyInventory[]`
- `WorkloadStatus.override`
- `ClusterInfo.environment/owner/policySource/paused/automation/configSync`
- `WorkloadDetail.approval/override`
- `WorkloadHistory.configChanges`

**Go types**
- `internal/hub/hubapi/admin.go`: every new schema, plus constants:
  - roles, `UserRoleAtLeast`, `TokenUserRole`
  - problem codes `Code*`, config actions `Action*`, targets `Target*`, layers `Layer*`
  - `SessionCookie`, `CSRFHeader`, `FleetScope` = `*`, `ApprovalDefaultTTL` / `ApprovalMaxTTL`
- New `internal/hub/hubapi/spec_test.go` checks every object schema against a Go type (fails on an
  unmapped new schema), the enums and the admin paths.
- `internal/agent/hubclient/types.go`:
  - `ClusterConfig`, `ConfigPolicy`, `ConfigFlags`, `ConfigApproval`, `PolicyInventoryItem`
  - the batch/status fields
  - `Detail*` keys
  - spec test extended to these and to the policy-spec schemas against the `v1alpha1` types

**Migrations** (goose, idempotent, with Down; verified up → re-up → down → up on postgres:18.6,
constraints negative-tested, and the existing hub + store DB tests pass on 0001–0009):
- `0007_admin_policies_settings.sql`: `settings`, `settings_versions`, `policies`,
  `policy_versions`, `clusters` metadata/sync columns, `cluster_automation` (`scope` `*` = fleet)
- `0008_users_sessions.sql`: `users`, `sessions`, `api_tokens.last_used_at/created_by/revoked_by`
- `0009_approvals_config_events.sql`: `workload_approvals`, `audit_events` `config_changed`
  (workload columns nullable only for that type, plus `actor, target_*, version, before, after`)

## Per track

**WO-20 hub**
- Register the routes and auth:
  - session cookie first, then bearer
  - CSRF on session writes
  - `If-Match` / `428` / `412`
  - CORS with credentials, the `PUT/PATCH/DELETE` methods and the `X-CSRF-Token, If-Match` headers
- Add `type <> 'config_changed'` to every existing `audit_events` query (queryAudit, workload
  history, `ro_actions_total`) before the first config row is written; old rows are unaffected.
- `configVersion` must change when policies, flags or approvals for the cluster change (e.g. a hash).
- Pause: an expired `resume_at` ends the pause and records `AutomationResumed` with actor `system`.
- Approvals:
  - consume on an `applied`/`ApprovedOnce` event (`details.approvalId`)
  - supersede on a new approval or a new recommendation
  - send only pending, unexpired ones whose recommendation is the latest
- Store `policy_inventory` and `applied_config_version` from status batches; an absent
  `policyInventory` means unchanged.
- Validate with `validate.HubPolicy`.

**WO-21 agent**
- Pull `/config` with ETag.
- Reconcile only CRs labelled `managed-by=hub` (name, label, the three annotations, spec). Never
  touch others. On a name clash with an unlabelled CR, report it invalid.
- Validate with `validate.HubPolicy` before materialising; report invalid ones.
- Layering: `ro-global-defaults` < chart `clusterDefaults` < `ro-cluster-defaults` < winner (hub
  `policy` + Git, excluding the hub default/override types) < override < annotations.
- Report `WorkloadStatus.override`.
- `flags.paused` = cluster freeze with reason `Paused` (`details.pauseScope`); keep the last flags
  while the hub is down; do not evaluate `resumeAt`.
- Approvals: apply once, guardrails intact, event `applied`/`ApprovedOnce` with `details.approvalId`
  and `approvedBy`.
- Send `configVersion` on every batch and `policyInventory` on full batches.
- RBAC: create/update/delete on `rightsizingpolicies`.
- Downgrade note: delete hub CRs first (see `admin.md` §2).

**WO-22 web**
- `npm run gen:api` is already done (`schema.d.ts` regenerated, typecheck passes).
- Use `/v1/auth/login` and `GET /v1/me` for the role and CSRF token; send `X-CSRF-Token` on writes
  and `If-Match` from the ETag.
- nginx must pass the `Cookie` header (today `proxy_set_header Cookie ""`); it may keep injecting
  the `ui` token, since a session wins over a bearer.
- `prod` confirmation via `ClusterInfo.environment`; config audit from `/v1/audit/config`.
- The mocks need handlers for the new paths.

## Verification (run in the worktree)
```
make ci                                          # build vet lint test verify-generate lint-openapi
cd web && npm run gen:api:check && npm run typecheck
RO_TEST_DATABASE_URL=postgres://… go test ./internal/hub/...   # existing DB tests on 0001-0009
```
