<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Agent endpoints

Endpoints used by the per-cluster agent.

| Endpoint | Summary |
|---|---|
| [`GET /v1/clusters/{cluster}/recommendations`](#listRecommendations) | Latest recommendation for every workload of a cluster |
| [`POST /v1/clusters/{cluster}/workload-status`](#reportWorkloadStatus) | Report the status of a batch of workloads |
| [`POST /v1/clusters/{cluster}/events`](#reportEvents) | Append audit events |
| [`GET /v1/clusters/{cluster}/config`](#getClusterConfig) | Hub-managed configuration of a cluster |

### Latest recommendation for every workload of a cluster { #listRecommendations }

`GET /v1/clusters/{cluster}/recommendations`

Accepts: bearer token with role `agent`.

Returns the latest recommendation per workload. Workloads without a recommendation
(e.g. unknown to the hub) are omitted. Supports conditional requests: the response carries
an `ETag`; sending it back in `If-None-Match` returns `304` when nothing changed.
Without `limit` all items are returned in one page.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `namespace` | query | string | Only workloads in this namespace. |
| `limit` | query | integer | Maximum number of items in this page. min 1; max 5000 |
| `continue` | query | string | Token from a previous page's `continue` field. |
| `If-None-Match` | header | string | ETag of a previous response. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Recommendations. | [RecommendationList](schemas.md#recommendationlist) |
| `304` | Not modified since the given ETag. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Report the status of a batch of workloads { #reportWorkloadStatus }

`POST /v1/clusters/{cluster}/workload-status`

Accepts: bearer token with role `agent`.

Upserts the status of each listed workload (keyed by namespace, kind and name). Workloads
not listed keep their previous status. The agent sends a full batch at least once per
recommendation pull, and a partial batch on every phase change. The hub uses
`settings` as input for the next recommendation run of that workload.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |

**Request body** (`application/json`, required): [WorkloadStatusBatch](schemas.md#workloadstatusbatch)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Batch accepted. | [BatchResult](schemas.md#batchresult) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `413` | Batch too large (more than 1000 items or 8 MiB). | [Problem](schemas.md#problem) |

### Append audit events { #reportEvents }

`POST /v1/clusters/{cluster}/events`

Accepts: bearer token with role `agent`.

Appends events to the audit log (`docs/contracts/events.md`). Idempotent by event `id`:
re-sending an event that was already stored is counted as a duplicate, not an error, so
the agent can retry a whole batch after a timeout.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |

**Request body** (`application/json`, required): [EventBatch](schemas.md#eventbatch)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Batch accepted. | [BatchResult](schemas.md#batchresult) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `413` | Batch too large (more than 1000 items or 8 MiB). | [Problem](schemas.md#problem) |

### Hub-managed configuration of a cluster { #getClusterConfig }

`GET /v1/clusters/{cluster}/config`

Accepts: bearer token with role `agent`.

What the agent materialises on its cluster (`docs/contracts/admin.md`):
- `policies`: every hub-managed policy for this cluster, as `RightsizingPolicy` specs. The
  agent creates, updates and deletes `RightsizingPolicy` CRs labelled
  `kerf.getkerf.dev/managed-by=hub` so that exactly these exist (name = `name`,
  annotations `hub-policy-id`, `hub-version`, `hub-policy-type`). CRs without the label are
  never touched. With `policySource: git` the list is empty.
- `flags`: pause. While `paused`, the agent applies nothing new (event `blocked`, reason
  `Paused`); rollbacks still run.
- `approvals`: approve-once grants.

The agent pulls it on every pull interval. Conditional requests: the response carries an
`ETag` (equal to `configVersion` in quotes); `If-None-Match` returns `304` when nothing
changed. If the hub is unreachable the agent keeps the CRs and the last flags it received.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `If-None-Match` | header | string | ETag of a previous response. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Cluster configuration. | [ClusterConfig](schemas.md#clusterconfig) |
| `304` | Not modified since the given ETag. |  |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
