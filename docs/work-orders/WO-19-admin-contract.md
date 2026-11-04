# WO-19 Admin area: contract

- **Phase / wave:** 4 / 0
- **Depends on:** –
- **Owns:** `api/openapi/hub.yaml`, `api/v1alpha1/` (selector.workloads, wellknown constants), `api/crds/` + `deploy/charts/agent/crds/` (regenerated), `db/migrations/0007_*`–`0009_*`, `docs/contracts/`, `internal/hub/hubapi/` types and `internal/agent/hubclient/` types (generated or hand-kept in sync, with the existing spec tests), `web/src/api/schema.d.ts` (regenerated)
- **Decisions:** D-004, D-005, D-022, D-023, D-024. Plan: `docs/plans/admin-area.md` (read it fully)

## Goal
Every interface the hub, agent and web tracks need for the admin area is fixed, documented, and validated, so that WO-20, WO-21 and WO-22 can run in parallel against it.

## Deliverables
1. **OpenAPI (`hub.yaml`)**, every addition from plan section 5:
   - **Auth:**
     - `POST /auth/login` (username/password → session cookie + CSRF token)
     - `POST /auth/logout`
     - `GET /v1/me` (user, role, csrf)
     - security scheme `session` (cookie) next to the existing bearer tokens
     - roles `viewer`, `operator`, `admin` for users; the machine tokens are unchanged
     - writes with the session scheme require the `X-CSRF-Token` header
   - **Admin read/write:**
     - settings sections: `recommender` (profiles, interval, retention) and `pricing` (per-cluster discount %, rate overrides)
     - policies CRUD + versions + revert, with `If-Match` and a required `reason`
     - `POST /v1/admin/preview` (impact dry-run for a policy/settings change: workloads whose effective mode changes, savings moving into auto-patch)
     - `GET /v1/workloads/{id}/effective-policy` (precedence chain: global default → cluster default → policies by priority → annotation, with the winner per field and the source of each)
     - `PUT/DELETE /v1/workloads/{id}/override`
     - `POST /v1/workloads/{id}/approve` (recommendation id in the body; 409 if it is stale)
     - `PUT /v1/clusters/{c}/automation` (paused, reason, resumeAt)
     - clusters `POST`/`PATCH`/`DELETE` (environment label `prod|non-prod`, owner, policySource `hub|git|both`)
     - API tokens create (value shown once) / list / revoke
     - users CRUD + reset-password
   - **Agent:**
     - `GET /v1/clusters/{c}/config` (ETag): hub-managed policies as `RightsizingPolicy` specs with name, priority, version; flags (paused, reason); approvals (workload ref + recommendation id + expiry)
     - status batch gains `policyInventory[]` (name, source hub|git, priority, generation, valid, message) and `configVersion`
   - **Events:** new audit type `config_changed` (actor, target, before/after JSON, reason); events `blocked/Paused` and `applied` with reason `ApprovedOnce`.
   - **Roles:** each operation's `security` lists the role(s) per plan section 3.
2. **CRD:** `RightsizingPolicy.spec.selector.workloads[] {kind, namespace, name}` (additive). Constants in `wellknown.go`: label `managed-by=hub`, annotations `hub-policy-id`, `hub-version`, the reserved priority bands (0 global, 10 cluster, ≥900000 workload override), reasons `Paused`, `ApprovedOnce`.
3. **Shared validation:** one Go package (e.g. `api/v1alpha1/validate` or `internal/policy/validate`) that validates a policy spec exactly like the CRD + agent do, used by both hub and agent. Table tests.
4. **Migrations:**
   - 0007: settings + versions, policies + policy_versions, cluster automation/metadata
   - 0008: users, sessions, api token role/expiry/last_used
   - 0009: workload approvals, `config_changed` event support

   All idempotent, with down migrations.
5. **Docs:** `docs/contracts/admin.md` describing the flow (hub stores intent → agent materialises CRs → inventory back), with examples for every endpoint.

## Definition of done
`make ci` is green with the regenerated code. `redocly lint` passes. The spec tests in hubclient/hubapi pass. The web schema is regenerated and `npm run typecheck` passes. There is no behaviour change yet: the new endpoints may return 501 from a stub router entry.

## Verification
```bash
make ci
cd web && npm run gen:api:check && npm run typecheck
```
