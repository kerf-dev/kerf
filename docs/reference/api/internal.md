<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Internal endpoints

Service endpoints for the pricing service (token role `pricing`).

| Endpoint | Summary |
|---|---|
| [`GET /v1/internal/metrics-bindings`](#listMetricsBindings) | Metrics binding of every registered cluster (pricing service) |
| [`GET /v1/internal/datasources/{datasourceId}/api/v1/query`](#proxyInstantQuery) | Instant PromQL query against a datasource (pricing service) |
| [`POST /v1/internal/datasources/{datasourceId}/api/v1/query`](#proxyInstantQueryPost) | Instant PromQL query against a datasource (form body) |
| [`GET /v1/internal/datasources/{datasourceId}/api/v1/query_range`](#proxyRangeQuery) | Range PromQL query against a datasource (pricing service) |
| [`POST /v1/internal/datasources/{datasourceId}/api/v1/query_range`](#proxyRangeQueryPost) | Range PromQL query against a datasource (form body) |
| [`GET /v1/internal/clusters/{cluster}/nodes`](#getClusterNodeInventory) | Node inventory of a cluster bound to a Dynatrace datasource (pricing service) |

### Metrics binding of every registered cluster (pricing service) { #listMetricsBindings }

`GET /v1/internal/metrics-bindings`

Accepts: bearer token with role `pricing`.

The binding of every registered, not deleted cluster. `bound` is false for a
cluster without a stored binding: its metrics are in datasource `local` with label `cluster`
= its name. Never contains datasource secrets. An `admin` token may call it too.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Bindings by cluster. | [MetricsBindingList](schemas.md#metricsbindinglist) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Instant PromQL query against a datasource (pricing service) { #proxyInstantQuery }

`GET /v1/internal/datasources/{datasourceId}/api/v1/query`

Accepts: bearer token with role `pricing`.

Runs the query against the datasource with the datasource's auth and headers, which never
leave the hub, and returns the datasource's response unchanged (the Prometheus
HTTP API `/api/v1/query` format, including its errors and status code). Only `query`,
`time` and `timeout` are forwarded. POST with a form body is accepted too.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `datasourceId` | path | string, required | `local` or a UUID. |
| `query` | query | string, required | PromQL expression. |
| `time` | query | string |  |
| `timeout` | query | string |  |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | The datasource's Prometheus HTTP API response, unchanged (also for its own errors). | [PromQueryResponse](schemas.md#promqueryresponse) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown datasource id. | [Problem](schemas.md#problem) |
| `502` | The datasource could not be reached. | [Problem](schemas.md#problem) |

### Instant PromQL query against a datasource (form body) { #proxyInstantQueryPost }

`POST /v1/internal/datasources/{datasourceId}/api/v1/query`

Accepts: bearer token with role `pricing`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `datasourceId` | path | string, required | `local` or a UUID. |

**Request body** (`application/x-www-form-urlencoded`, required): [PromQueryForm](schemas.md#promqueryform)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | The datasource's Prometheus HTTP API response, unchanged (also for its own errors). | [PromQueryResponse](schemas.md#promqueryresponse) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown datasource id. | [Problem](schemas.md#problem) |
| `502` | The datasource could not be reached. | [Problem](schemas.md#problem) |

### Range PromQL query against a datasource (pricing service) { #proxyRangeQuery }

`GET /v1/internal/datasources/{datasourceId}/api/v1/query_range`

Accepts: bearer token with role `pricing`.

As the instant query, for `/api/v1/query_range`: `query`, `start`, `end`, `step` and
`timeout` are forwarded.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `datasourceId` | path | string, required | `local` or a UUID. |
| `query` | query | string, required | PromQL expression. |
| `start` | query | string |  |
| `end` | query | string |  |
| `step` | query | string |  |
| `timeout` | query | string |  |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | The datasource's Prometheus HTTP API response, unchanged (also for its own errors). | [PromQueryResponse](schemas.md#promqueryresponse) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown datasource id. | [Problem](schemas.md#problem) |
| `502` | The datasource could not be reached. | [Problem](schemas.md#problem) |

### Range PromQL query against a datasource (form body) { #proxyRangeQueryPost }

`POST /v1/internal/datasources/{datasourceId}/api/v1/query_range`

Accepts: bearer token with role `pricing`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `datasourceId` | path | string, required | `local` or a UUID. |

**Request body** (`application/x-www-form-urlencoded`, required): [PromQueryForm](schemas.md#promqueryform)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | The datasource's Prometheus HTTP API response, unchanged (also for its own errors). | [PromQueryResponse](schemas.md#promqueryresponse) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown datasource id. | [Problem](schemas.md#problem) |
| `502` | The datasource could not be reached. | [Problem](schemas.md#problem) |

### Node inventory of a cluster bound to a Dynatrace datasource (pricing service) { #getClusterNodeInventory }

`GET /v1/internal/clusters/{cluster}/nodes`

Accepts: bearer token with role `pricing`.

The cluster's current nodes with the labels pricing needs and their allocatable capacity,
read by the hub from the cluster's datasource. Only for clusters bound to a datasource that
is not queried with PromQL (`dynatrace`, see `ClusterMetricsBinding.datasourceType`):
`409` with code `not_supported` for the others, which the pricing service queries through
the query proxy. An `admin` token may call it too.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Nodes, ordered by name. | [NodeInventory](schemas.md#nodeinventory) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `502` | The datasource could not be reached. | [Problem](schemas.md#problem) |
