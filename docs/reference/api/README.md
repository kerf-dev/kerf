<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Hub API

The hub computes recommendations for every workload in every cluster; each cluster's agent
pulls them, applies them according to its local policy and reports status and events back.
The hub never calls the agents. The web UI reads through the `ui` endpoints.

**Authentication**: every request carries `Authorization: Bearer <token>`. Each token
has one role; the roles an operation accepts are listed in its `security` requirement.
- `agent`: bound to exactly one cluster. It may only use the `/v1/clusters/{cluster}/...`
  endpoints of that cluster (otherwise `403`), and `GET /v1/audit` restricted to that cluster.
- `ui`: read-only, all clusters. May use the `ui`-tagged endpoints and `GET /v1/audit`.
- `admin`: may use every endpoint for every cluster (satisfies every role).
A token without an accepted role gets `403`. The hub stores only a SHA-256 hash of each token.

**Sessions** (admin area `docs/contracts/admin.md`): a local user logs in with
`POST /v1/auth/login` and gets the HttpOnly cookie `kerf_session` (`sessionAuth`). Users have one
role, `viewer` < `operator` < `admin`; each includes the ones before it. An operation lists the
lowest user role it accepts as `sessionAuth: [role]`, next to the token roles it accepts. For
authorization a `ui` token counts as `viewer` and an `admin` token as `admin`. Every write
(POST, PUT, PATCH, DELETE) made with a session needs the header `X-CSRF-Token` with the token
from the login or `GET /v1/me` response (`403`, code `csrf_invalid`); bearer requests need none.
When a request carries both a valid session cookie and a bearer token, the session is used.

**Admin writes** take a `reason` and record a `config_changed` audit entry
(`GET /v1/audit/config`). Changes to an existing versioned object need `If-Match` with its
ETag `"<version>"`: missing gives `428` (code `precondition_required`), stale gives `412` (code
`version_conflict`). Until the hub implements an endpoint it answers `501` (code
`not_implemented`).

**Conventions**
- `cluster` is the cluster name, identical to the `cluster` label on all metrics
  (`docs/contracts/metrics.md`).
- Quantities use Kubernetes quantity syntax (`250m`, `512Mi`).
- Durations use Go duration syntax (`336h0m0s`, `10m`).
- Timestamps are RFC 3339 in UTC.
- Workload ids are `<cluster>/<namespace>/<kind>/<name>`, e.g.
  `ro-spoke-1/payments/Deployment/checkout`. In a path they are one segment, URL-encoded
  (`encodeURIComponent`, so `/` becomes `%2F`). Reverse proxies in front of the hub must pass
  `%2F` through undecoded.
- Numbers in time series and totals use base units: cores, bytes, USD.
- Errors use `application/problem+json` (RFC 9457).
- Unknown fields must be ignored by readers, so fields can be added without a new version.

## Endpoints by area

| Area | Endpoints | What it covers |
|---|---|---|
| [Agent](agent.md) | 4 | Endpoints used by the per-cluster agent. |
| [Web UI](ui.md) | 20 | Read-only endpoints for the web UI. |
| [Audit](audit.md) | 2 | Audit log queries. |
| [Admin](admin.md) | 61 | Administration (admin area). Roles per operation. |
| [Authentication](auth.md) | 7 | Login, logout and the caller's identity. |
| [Internal](internal.md) | 6 | Service endpoints for the pricing service (token role `pricing`). |

The request and response objects are described on the [schemas](schemas.md) page. The machine-readable contract is [api/openapi/hub.yaml](https://github.com/kerf-dev/kerf/blob/main/api/openapi/hub.yaml) (OpenAPI 3.1.0).
