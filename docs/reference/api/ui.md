<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Web UI endpoints

Read-only endpoints for the web UI.

| Endpoint | Summary |
|---|---|
| [`GET /v1/summary`](#getSummary) | Fleet and per-cluster totals |
| [`GET /v1/clusters`](#listClusters) | Clusters with agent heartbeat and counts |
| [`GET /v1/workloads`](#listWorkloads) | Workloads with their latest recommendation |
| [`GET /v1/workloads/facets`](#getWorkloadFacets) | Filter values with workload counts |
| [`GET /v1/workloads/{id}`](#getWorkload) | Workload detail with per-container explanation |
| [`GET /v1/workloads/{id}/usage`](#getWorkloadUsage) | Usage, request, limit and recommendation time series |
| [`GET /v1/workloads/{id}/history`](#getWorkloadHistory) | Past recommendations and audit events of a workload |
| [`GET /v1/savings/trend`](#getSavingsTrend) | Estimated monthly savings over time |
| [`GET /v1/workloads/{id}/effective-policy`](#getEffectivePolicy) | Precedence chain and winning value per field |
| [`GET /v1/workloads/{id}/override`](#getWorkloadOverride) | The workload's hub override policy |
| [`PUT /v1/workloads/{id}/override`](#putWorkloadOverride) | Create or replace the workload's override |
| [`DELETE /v1/workloads/{id}/override`](#deleteWorkloadOverride) | Clear the workload's override |
| [`POST /v1/workloads/{id}/approve`](#approveRecommendation) | Approve the current recommendation once |
| [`GET /v1/clusters/{cluster}/readiness`](#getClusterReadiness) | Readiness state and check results of a cluster |
| [`POST /v1/clusters/{cluster}/readiness/recheck`](#recheckClusterReadiness) | Re-run the readiness checks now |
| [`GET /v1/clusters/{cluster}/readiness/history`](#getClusterReadinessHistory) | State transitions and check status changes |

### Fleet and per-cluster totals { #getSummary }

`GET /v1/summary`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

Totals over the latest recommendation of every live workload. Workloads the agent never
reported count as `audit` / `Pending` (global default).

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Summary. | [FleetSummary](schemas.md#fleetsummary) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |

### Clusters with agent heartbeat and counts { #listClusters }

`GET /v1/clusters`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Clusters, ordered by name. | [ClusterList](schemas.md#clusterlist) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |

### Workloads with their latest recommendation { #listWorkloads }

`GET /v1/workloads`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

One row per live workload, including workloads that have no recommendation yet (their
recommendation fields are absent). All filters are combined with AND; repeatable filters
match any of their values.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | array of string |  |
| `namespace` | query | array of string |  |
| `kind` | query | array of WorkloadKind (enum) |  |
| `mode` | query | array of Mode (enum) | Matches if the CPU or the memory mode is one of the values. |
| `riskFlag` | query | array of RiskFlag (enum) | Matches if the workload has any of the flags. |
| `minSavings` | query | number | Minimum total estimated monthly savings in USD. |
| `q` | query | string | Case-insensitive substring match on namespace and workload name. max length 253 |
| `sort` | query | string | one of `savings`, `confidence`, `name`; default `savings` |
| `order` | query | string | Default `desc` for savings and confidence, `asc` for name. one of `asc`, `desc` |
| `limit` | query | integer | min 1; max 500; default `50` |
| `continue` | query | string | Token from a previous page's `continue` field. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Workloads. | [WorkloadList](schemas.md#workloadlist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |

### Filter values with workload counts { #getWorkloadFacets }

`GET /v1/workloads/facets`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

Counts of live workloads per filter value, for building the filters of the workload list.
Takes the same filters as `GET /v1/workloads`. Each facet is counted with every filter
applied except its own (so a multi-select keeps showing the other values of that facet);
`total` applies all filters and equals `WorkloadList.total`. A workload counts once per
distinct value: in `modes` under both its CPU and its memory mode, in `riskFlags` under each
of its flags. Values with zero workloads are omitted. Items are sorted by value.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | array of string |  |
| `namespace` | query | array of string |  |
| `kind` | query | array of WorkloadKind (enum) |  |
| `mode` | query | array of Mode (enum) | Matches if the CPU or the memory mode is one of the values. |
| `riskFlag` | query | array of RiskFlag (enum) | Matches if the workload has any of the flags. |
| `minSavings` | query | number | Minimum total estimated monthly savings in USD. |
| `q` | query | string | Case-insensitive substring match on namespace and workload name. max length 253 |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Facets. | [WorkloadFacets](schemas.md#workloadfacets) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |

### Workload detail with per-container explanation { #getWorkload }

`GET /v1/workloads/{id}`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `id` | path | string, required | Workload id `<cluster>/<namespace>/<kind>/<name>`, URL-encoded as one segment. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Workload detail. | [WorkloadDetail](schemas.md#workloaddetail) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown workload id. | [Problem](schemas.md#problem) |

### Usage, request, limit and recommendation time series { #getWorkloadUsage }

`GET /v1/workloads/{id}/usage`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

Per container. `usage` is the maximum across the workload's pods at each step (what a
per-pod request has to cover); `usageAvg` is the mean across pods. `request`, `limit` and
`recommendation` (recommended request) are step functions. Values are cores for `cpu` and
bytes for `memory`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `id` | path | string, required | Workload id `<cluster>/<namespace>/<kind>/<name>`, URL-encoded as one segment. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |
| `resource` | query | ResourceName (enum), required | one of `cpu`, `memory` |
| `container` | query | string | Only this container. |
| `from` | query | string (date-time) | Inclusive start. Default `to` minus 7 days. |
| `to` | query | string (date-time) | Exclusive end. Default now. |
| `step` | query | string | Resolution (Go duration). Default chosen so the result has at most 1000 points. example `5m`, `1h` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Time series. | [UsageSeries](schemas.md#usageseries) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown workload id. | [Problem](schemas.md#problem) |

### Past recommendations and audit events of a workload { #getWorkloadHistory }

`GET /v1/workloads/{id}/history`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

Both lists are newest first and capped at `limit` entries each.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `id` | path | string, required | Workload id `<cluster>/<namespace>/<kind>/<name>`, URL-encoded as one segment. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |
| `from` | query | string (date-time) | Inclusive start. Default `to` minus 7 days. |
| `to` | query | string (date-time) | Exclusive end. Default now. |
| `limit` | query | integer | min 1; max 1000; default `100` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | History. | [WorkloadHistory](schemas.md#workloadhistory) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown workload id. | [Problem](schemas.md#problem) |

### Estimated monthly savings over time { #getSavingsTrend }

`GET /v1/savings/trend`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

At each step: the sum of the estimated monthly savings of the recommendation that was
latest at that time, over the workloads live at that time. Estimated, not realized
(realized savings are). Points are aligned to multiples of `step` (UTC); a point at
time `t` holds the value at the end of its bucket, `min(t + step, to)`, so with the default
`to` (now) the last point equals the current `GET /v1/summary` totals.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | array of string |  |
| `groupBy` | query | string | `none` returns one fleet series; `cluster` one series per cluster. one of `none`, `cluster`; default `none` |
| `from` | query | string (date-time) | Inclusive start. Default `to` minus 7 days. |
| `to` | query | string (date-time) | Exclusive end. Default now. |
| `step` | query | string | Resolution (Go duration). Default chosen so the result has at most 1000 points. example `5m`, `1h` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Savings trend. | [SavingsTrend](schemas.md#savingstrend) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |

### Precedence chain and winning value per field { #getEffectivePolicy }

`GET /v1/workloads/{id}/effective-policy`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Computed by the hub from the hub-managed policies, the cluster's reported policy inventory
(Git policies), the agent's reported cluster defaults and the workload's annotations and
labels as last reported. Layers from lowest to highest precedence: built-in defaults →
global default → cluster default (chart value) → cluster default (hub) → the winning
policy (highest priority, ties by name) → workload override (hub) → annotations.
`fields` lists every field with its value and the layer that set it. `agentModes` is what
the agent last reported, so the UI can show a difference (e.g. before the next pull).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `id` | path | string, required | Workload id `<cluster>/<namespace>/<kind>/<name>`, URL-encoded as one segment. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Effective policy. | [EffectivePolicy](schemas.md#effectivepolicy) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown workload id. | [Problem](schemas.md#problem) |

### The workload's hub override policy { #getWorkloadOverride }

`GET /v1/workloads/{id}/override`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `id` | path | string, required | Workload id `<cluster>/<namespace>/<kind>/<name>`, URL-encoded as one segment. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Override. | [AdminPolicy](schemas.md#adminpolicy) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown workload id. | [Problem](schemas.md#problem) |

### Create or replace the workload's override { #putWorkloadOverride }

`PUT /v1/workloads/{id}/override`

Accepts: session of a user with role `operator` or higher or bearer token with role `admin`.

Stores a hub policy of type `workload-override` named
`ro-override-<16 hex of sha256("<namespace>/<kind>/<name>")>`, priority 900000, sent only to
the workload's cluster, with selector `{namespaces: [ns], kinds: [kind], workloads: [ref]}`.
Creating one needs no `If-Match`; replacing an existing one does (`428` without it).
Setting a mode to `auto-patch` on a cluster with environment `prod` needs an admin (`403`,
code `admin_required`). "Exclude" is an override with both modes `off`. Records
`OverrideSet`.
Setting a mode to `auto-patch` for a cluster that is not `AutomationReady` gives `409`, code
`cluster_not_automation_ready`, unless an admin sends `overrideReadiness: true`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `id` | path | string, required | Workload id `<cluster>/<namespace>/<kind>/<name>`, URL-encoded as one segment. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |
| `If-Match` | header | string | ETag (`"<version>"`) of the object being changed; checked when given (`412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [WorkloadOverrideInput](schemas.md#workloadoverrideinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [AdminPolicy](schemas.md#adminpolicy) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown workload id. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Clear the workload's override { #deleteWorkloadOverride }

`DELETE /v1/workloads/{id}/override`

Accepts: session of a user with role `operator` or higher or bearer token with role `admin`.

Soft-deletes the override policy; the workload inherits again. Records `OverrideCleared`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `id` | path | string, required | Workload id `<cluster>/<namespace>/<kind>/<name>`, URL-encoded as one segment. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Cleared. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown workload id. | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Approve the current recommendation once { #approveRecommendation }

`POST /v1/workloads/{id}/approve`

Accepts: session of a user with role `operator` or higher or bearer token with role `admin`.

Records an approval for `recommendationID`, which must be the workload's latest
recommendation (`409`, code `recommendation_stale`). Only for a workload whose effective
mode for at least one resource is `audit` (`409`, code `not_in_audit`). The agent applies
that recommendation once, with all guardrails, as if the `audit` resources were
`auto-patch`, and emits `applied` with reason `ApprovedOnce`; the workload stays in
`audit`. A new approval replaces an earlier unconsumed one. Records `WorkloadApproved`.
On a cluster that is not `AutomationReady`: `409`, code `cluster_not_automation_ready`,
unless an admin sends `overrideReadiness: true`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `id` | path | string, required | Workload id `<cluster>/<namespace>/<kind>/<name>`, URL-encoded as one segment. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ApprovalRequest](schemas.md#approvalrequest)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Approved. | [Approval](schemas.md#approval) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown workload id. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### Readiness state and check results of a cluster { #getClusterReadiness }

`GET /v1/clusters/{cluster}/readiness`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

The latest result of every check (hub metrics checks and agent self-checks), the state they
imply and what blocks the next state. Checks never run yet have status `unknown`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Readiness. | [ClusterReadiness](schemas.md#clusterreadiness) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Re-run the readiness checks now { #recheckClusterReadiness }

`POST /v1/clusters/{cluster}/readiness/recheck`

Accepts: session of a user with role `operator` or higher or bearer token with role `admin`.

Queues the hub metrics checks and sets the recheck flag in the cluster's config, so the
agent re-runs its self-checks on its next pull. Results appear in
`GET /v1/clusters/{cluster}/readiness` as they arrive. Not audited.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `202` | Re-check queued. | [RecheckAccepted](schemas.md#recheckaccepted) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### State transitions and check status changes { #getClusterReadinessHistory }

`GET /v1/clusters/{cluster}/readiness/history`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Only changes are stored (a check's status or the state), newest first.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `from` | query | string (date-time) | Inclusive start. Default `to` minus 7 days. |
| `to` | query | string (date-time) | Exclusive end. Default now. |
| `limit` | query | integer | min 1; max 1000; default `100` |
| `continue` | query | string | Token from a previous page's `continue` field. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | History. | [ReadinessHistory](schemas.md#readinesshistory) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
