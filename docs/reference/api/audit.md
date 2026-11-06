<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Audit endpoints

Audit log queries.

| Endpoint | Summary |
|---|---|
| [`GET /v1/audit`](#queryAudit) | Query the audit log |
| [`GET /v1/audit/config`](#queryConfigAudit) | Configuration changes (audit type `config_changed`) |

### Query the audit log { #queryAudit }

`GET /v1/audit`

Accepts: bearer token with role `agent` or bearer token with role `ui` or session of a user with role `viewer` or higher.

Returns audit events matching all given filters, ordered by event `time` (ascending by
default). An agent token implicitly filters on its own cluster.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `namespace` | query | string |  |
| `kind` | query | WorkloadKind (enum) | one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob` |
| `workload` | query | string | Workload name. |
| `type` | query | array of EventType (enum) | Event types; repeat the parameter for several. |
| `from` | query | string (date-time) | Inclusive lower bound on event time. |
| `to` | query | string (date-time) | Exclusive upper bound on event time. |
| `order` | query | string | one of `asc`, `desc`; default `asc` |
| `limit` | query | integer | min 1; max 1000; default `100` |
| `continue` | query | string | Token from a previous page's `continue` field. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Audit events. | [AuditEventList](schemas.md#auditeventlist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |

### Configuration changes (audit type `config_changed`) { #queryConfigAudit }

`GET /v1/audit/config`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Every configuration change made through the hub API, with actor, target, before/after and
reason, ordered by time. Stored in the audit log with type `config_changed`; served here
and not by `GET /v1/audit`, whose items are workload events. All filters are combined with
AND.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | string | Changes whose target belongs to this cluster. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `targetType` | query | array of ConfigTargetType (enum) |  |
| `targetId` | query | string |  |
| `workloadId` | query | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` |
| `actor` | query | string | Actor name (username or `token:<id>`). |
| `action` | query | array of ConfigAction (enum) |  |
| `from` | query | string (date-time) | Inclusive lower bound on time. |
| `to` | query | string (date-time) | Exclusive upper bound on time. |
| `order` | query | string | one of `asc`, `desc`; default `desc` |
| `limit` | query | integer | min 1; max 1000; default `100` |
| `continue` | query | string | Token from a previous page's `continue` field. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Changes. | [ConfigChangeList](schemas.md#configchangelist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster. | [Problem](schemas.md#problem) |
