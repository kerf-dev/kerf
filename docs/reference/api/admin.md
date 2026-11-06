<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Admin endpoints

Administration (admin area). Roles per operation.

| Endpoint | Summary |
|---|---|
| [`POST /v1/admin/recompute`](#recompute) | Run the recommender now |
| [`GET /v1/admin/settings`](#getSettings) | Hub-only settings (recommender, pricing) |
| [`GET /v1/admin/settings/recommender`](#getRecommenderSettings) | Recommender settings |
| [`PUT /v1/admin/settings/recommender`](#putRecommenderSettings) | Replace the recommender settings |
| [`GET /v1/admin/settings/pricing`](#getPricingSettings) | Pricing adjustments |
| [`PUT /v1/admin/settings/pricing`](#putPricingSettings) | Replace the pricing adjustments |
| [`GET /v1/admin/settings/{section}/versions`](#listSettingsVersions) | Version history of a settings section |
| [`GET /v1/admin/policies`](#listPolicies) | Hub-managed policies |
| [`POST /v1/admin/policies`](#createPolicy) | Create a hub-managed policy |
| [`GET /v1/admin/policies/{policyId}`](#getPolicy) | One hub-managed policy |
| [`PUT /v1/admin/policies/{policyId}`](#updatePolicy) | Replace a hub-managed policy |
| [`DELETE /v1/admin/policies/{policyId}`](#deletePolicy) | Soft-delete a hub-managed policy |
| [`GET /v1/admin/policies/{policyId}/versions`](#listPolicyVersions) | Version history of a policy |
| [`POST /v1/admin/policies/{policyId}/revert`](#revertPolicy) | Restore an earlier version |
| [`GET /v1/admin/policy-inventory`](#getPolicyInventory) | RightsizingPolicy CRs reported by the agents |
| [`POST /v1/admin/preview`](#previewPolicyChange) | Impact dry-run of policy changes |
| [`POST /v1/admin/clusters`](#createCluster) | Register a cluster |
| [`GET /v1/admin/clusters/{cluster}`](#getClusterAdmin) | Cluster metadata, automation state and agent config sync |
| [`DELETE /v1/admin/clusters/{cluster}`](#deleteCluster) | Retire a cluster |
| [`PATCH /v1/admin/clusters/{cluster}`](#updateCluster) | Change cluster metadata |
| [`PUT /v1/clusters/{cluster}/automation`](#setClusterAutomation) | Pause or resume automation of a cluster |
| [`GET /v1/admin/automation`](#getFleetAutomation) | Fleet-wide and per-cluster pause state |
| [`PUT /v1/admin/automation`](#setFleetAutomation) | Pause or resume automation of the whole fleet |
| [`GET /v1/admin/tokens`](#listTokens) | API tokens (never their values) |
| [`POST /v1/admin/tokens`](#createToken) | Create an API token |
| [`DELETE /v1/admin/tokens/{tokenId}`](#revokeToken) | Revoke an API token |
| [`GET /v1/admin/teams`](#listTeams) | Teams with their workloads and estimated savings |
| [`POST /v1/admin/teams`](#createTeam) | Create a team |
| [`GET /v1/admin/teams/{teamId}`](#getTeam) | One team |
| [`PUT /v1/admin/teams/{teamId}`](#updateTeam) | Replace a team's definition |
| [`DELETE /v1/admin/teams/{teamId}`](#deleteTeam) | Delete a team |
| [`GET /v1/admin/users`](#listUsers) | Local users |
| [`POST /v1/admin/users`](#createUser) | Create a local user |
| [`GET /v1/admin/users/{username}`](#getUser) | One local user |
| [`DELETE /v1/admin/users/{username}`](#deleteUser) | Delete a local user |
| [`PATCH /v1/admin/users/{username}`](#updateUser) | Change a user's display name, role or disabled flag |
| [`POST /v1/admin/users/{username}/reset-password`](#resetUserPassword) | Set a new password for a user |
| [`GET /v1/admin/datasources`](#listDatasources) | Metrics datasources |
| [`POST /v1/admin/datasources`](#createDatasource) | Add a metrics datasource |
| [`GET /v1/admin/datasources/{datasourceId}`](#getDatasource) | One datasource |
| [`PUT /v1/admin/datasources/{datasourceId}`](#updateDatasource) | Replace a datasource |
| [`DELETE /v1/admin/datasources/{datasourceId}`](#deleteDatasource) | Delete a datasource |
| [`POST /v1/admin/datasources/{datasourceId}/test`](#testDatasource) | Run the metrics checks against a datasource now |
| [`GET /v1/admin/clusters/{cluster}/install`](#getClusterInstall) | Agent install instructions for a cluster |
| [`GET /v1/admin/maintenance`](#getMaintenance) | Component versions and the update check |
| [`POST /v1/admin/maintenance/update-check`](#checkForUpdate) | Check for a newer Kerf release now |
| [`GET /v1/admin/notifications/status`](#getNotificationStatus) | Notification settings the page needs |
| [`GET /v1/admin/notifications/channels`](#listNotificationChannels) | Notification channels |
| [`POST /v1/admin/notifications/channels`](#createNotificationChannel) | Add a notification channel |
| [`GET /v1/admin/notifications/channels/{channelId}`](#getNotificationChannel) | One notification channel |
| [`PUT /v1/admin/notifications/channels/{channelId}`](#updateNotificationChannel) | Replace a notification channel |
| [`DELETE /v1/admin/notifications/channels/{channelId}`](#deleteNotificationChannel) | Delete a notification channel |
| [`POST /v1/admin/notifications/channels/{channelId}/test`](#testNotificationChannel) | Send a test message |
| [`GET /v1/admin/notifications/rules`](#listNotificationRules) | Notification rules |
| [`POST /v1/admin/notifications/rules`](#createNotificationRule) | Add a notification rule |
| [`PUT /v1/admin/notifications/rules/{ruleId}`](#updateNotificationRule) | Replace a notification rule |
| [`DELETE /v1/admin/notifications/rules/{ruleId}`](#deleteNotificationRule) | Delete a notification rule |
| [`GET /v1/admin/notifications/digest`](#getNotificationDigest) | Weekly digest settings |
| [`PUT /v1/admin/notifications/digest`](#updateNotificationDigest) | Change the weekly digest |
| [`POST /v1/admin/notifications/digest/send`](#sendNotificationDigest) | Send the digest now |
| [`GET /v1/admin/notifications/deliveries`](#listNotificationDeliveries) | Delivery history |

### Run the recommender now { #recompute }

`POST /v1/admin/recompute`

Accepts: bearer token with role `admin` or session of a user with role `admin` or higher.

Queues a recommender run for one cluster (`cluster`) or for all registered
clusters, and returns at once with the run id. The run starts as soon as no other run is
in progress; it uses usage data up to the last full recommender step (5 minutes by
default) before it starts, like a scheduled run. A request for the same scope that
arrives while an earlier one is still queued gets that run's id (`coalesced: true`)
instead of queuing another.

Completion is visible through the existing endpoints: `lastRecommenderRunAt` of the
cluster in `GET /v1/clusters` moves past `requestedAt`, and the agent endpoint returns
the new recommendations.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | string | Only this cluster. Default all clusters. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `202` | Run queued. | [RecomputeAccepted](schemas.md#recomputeaccepted) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Hub-only settings (recommender, pricing) { #getSettings }

`GET /v1/admin/settings`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Settings that stay in the hub and never reach clusters. A section never saved holds the
values from the hub's configuration file with `version` 0.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Settings. | [Settings](schemas.md#settings) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Recommender settings { #getRecommenderSettings }

`GET /v1/admin/settings/recommender`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Recommender settings. | [RecommenderSettingsDoc](schemas.md#recommendersettingsdoc) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Replace the recommender settings { #putRecommenderSettings }

`PUT /v1/admin/settings/recommender`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Stores a new version. Takes effect on the next recommender run (`POST /v1/admin/recompute`
starts one). Records a `config_changed` audit event (`SettingsUpdated`).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [RecommenderSettingsUpdate](schemas.md#recommendersettingsupdate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [RecommenderSettingsDoc](schemas.md#recommendersettingsdoc) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Pricing adjustments { #getPricingSettings }

`GET /v1/admin/settings/pricing`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Pricing settings. | [PricingSettingsDoc](schemas.md#pricingsettingsdoc) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Replace the pricing adjustments { #putPricingSettings }

`PUT /v1/admin/settings/pricing`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Stores a new version. Savings of recommendations computed afterwards use the adjusted rates.
Records a `config_changed` audit event (`SettingsUpdated`).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [PricingSettingsUpdate](schemas.md#pricingsettingsupdate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [PricingSettingsDoc](schemas.md#pricingsettingsdoc) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Version history of a settings section { #listSettingsVersions }

`GET /v1/admin/settings/{section}/versions`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Newest first.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `section` | path | SettingsSection (enum), required | one of `recommender`, `pricing` |
| `limit` | query | integer | min 1; max 1000; default `100` |
| `continue` | query | string | Token from a previous page's `continue` field. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Versions. | [SettingsVersionList](schemas.md#settingsversionlist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Hub-managed policies { #listPolicies }

`GET /v1/admin/policies`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Hub-managed policies (all types, including workload overrides), ordered by type and then
priority descending. Policies from Git are in `GET /v1/admin/policy-inventory`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | string | Only policies sent to this cluster (fleet-wide policies included). pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `type` | query | array of HubPolicyType (enum) |  |
| `includeDeleted` | query | boolean | default `False` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Policies. | [AdminPolicyList](schemas.md#adminpolicylist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Create a hub-managed policy { #createPolicy }

`POST /v1/admin/policies`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Types `global-default`, `cluster-default` and `policy`; workload overrides use
`PUT /v1/workloads/{id}/override`. The hub sets the name and priority of the default types
(`ro-global-defaults` / 0, `ro-cluster-defaults` / 10); at most one global default and one
cluster default per cluster exist (`409`, code `already_exists`). A `policy` needs a name
that does not start with `ro-` and a priority from 1 to 899999. The spec is validated like
the CRD (code `invalid_policy`, `detail` lists the fields). Records a `config_changed`
audit event (`PolicyCreated`).
Setting a mode to `auto-patch` for a cluster that is not `AutomationReady` gives `409`, code
`cluster_not_automation_ready`, unless an admin sends `overrideReadiness: true`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [PolicyInput](schemas.md#policyinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Created. | [AdminPolicy](schemas.md#adminpolicy) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### One hub-managed policy { #getPolicy }

`GET /v1/admin/policies/{policyId}`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Soft-deleted policies are returned too (with `deletedAt`).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `policyId` | path | string (uuid), required |  |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Policy. | [AdminPolicy](schemas.md#adminpolicy) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown policy id. | [Problem](schemas.md#problem) |

### Replace a hub-managed policy { #updatePolicy }

`PUT /v1/admin/policies/{policyId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Stores a new version; `type` cannot change (`400`, code `type_immutable`). Agents pick the
change up on their next config pull. Records `PolicyUpdated`.
Setting a mode to `auto-patch` for a cluster that is not `AutomationReady` gives `409`, code
`cluster_not_automation_ready`, unless an admin sends `overrideReadiness: true`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `policyId` | path | string (uuid), required |  |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [PolicyInput](schemas.md#policyinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [AdminPolicy](schemas.md#adminpolicy) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown policy id. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Soft-delete a hub-managed policy { #deletePolicy }

`DELETE /v1/admin/policies/{policyId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Sets `deletedAt` and stores a version with operation `delete`; the versions are kept and
`revert` restores it. Agents delete the CR on their next pull. Records `PolicyDeleted`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `policyId` | path | string (uuid), required |  |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Deleted. | [AdminPolicy](schemas.md#adminpolicy) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown policy id. | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Version history of a policy { #listPolicyVersions }

`GET /v1/admin/policies/{policyId}/versions`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Newest first. Every create, update, delete and revert is one version.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `policyId` | path | string (uuid), required |  |
| `limit` | query | integer | min 1; max 1000; default `100` |
| `continue` | query | string | Token from a previous page's `continue` field. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Versions. | [PolicyVersionList](schemas.md#policyversionlist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown policy id. | [Problem](schemas.md#problem) |

### Restore an earlier version { #revertPolicy }

`POST /v1/admin/policies/{policyId}/revert`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Stores a new version whose content equals version `version` (operation `revert`); reverting
to a version of a deleted policy undeletes it. Records `PolicyReverted`.
A revert that newly enables `auto-patch` on a cluster that is not `AutomationReady` answers
`409` `cluster_not_automation_ready`, unless an admin sends `overrideReadiness: true`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `policyId` | path | string (uuid), required |  |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [PolicyRevert](schemas.md#policyrevert)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Reverted. | [AdminPolicy](schemas.md#adminpolicy) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown policy id. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### RightsizingPolicy CRs reported by the agents { #getPolicyInventory }

`GET /v1/admin/policy-inventory`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Every policy CR on each cluster as last reported by its agent (`policyInventory` of the
status batch), hub-managed and from Git. The UI shows Git policies read-only from here.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | array of string |  |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Inventory per cluster. | [PolicyInventoryList](schemas.md#policyinventorylist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Impact dry-run of policy changes { #previewPolicyChange }

`POST /v1/admin/preview`

Accepts: session of a user with role `operator` or higher or bearer token with role `admin`.

Applies the given hypothetical policy changes (global and cluster defaults are policies
too) to the current state without saving them, and returns the workloads whose effective
CPU or memory mode changes and the estimated monthly savings that move into (or out of)
`auto-patch`. Read-only: needs no `If-Match` or reason and records nothing.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [PreviewRequest](schemas.md#previewrequest)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Impact. | [ImpactPreview](schemas.md#impactpreview) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Register a cluster { #createCluster }

`POST /v1/admin/clusters`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Creates the cluster and, with `createAgentToken`, an `agent` token for it whose value is
returned once in `agentToken`. Records `ClusterCreated` (and `TokenCreated`).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ClusterCreate](schemas.md#clustercreate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Created. | [ClusterCreated](schemas.md#clustercreated) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### Cluster metadata, automation state and agent config sync { #getClusterAdmin }

`GET /v1/admin/clusters/{cluster}`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Cluster. | [ClusterAdmin](schemas.md#clusteradmin) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Retire a cluster { #deleteCluster }

`DELETE /v1/admin/clusters/{cluster}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Soft delete: sets `deletedAt`, revokes the cluster's tokens and hides it from the lists;
its history is kept. Records `ClusterDeleted`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Deleted. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Change cluster metadata { #updateCluster }

`PATCH /v1/admin/clusters/{cluster}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Only the given fields change (JSON merge semantics). Records `ClusterUpdated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ClusterUpdate](schemas.md#clusterupdate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [ClusterAdmin](schemas.md#clusteradmin) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Pause or resume automation of a cluster { #setClusterAutomation }

`PUT /v1/clusters/{cluster}/automation`

Accepts: session of a user with role `operator` or higher or bearer token with role `admin`.

Pausing is always allowed for an operator. Resuming a cluster with environment `prod`
needs an admin (`403`, code `admin_required`). `If-Match` is optional here, so a pause is
never held up by a stale page; when given it is checked. With `resumeAt` the hub stops
reporting the pause at that time (the agent does not evaluate it). Records
`AutomationPaused` or `AutomationResumed`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `If-Match` | header | string | ETag (`"<version>"`) of the object being changed; checked when given (`412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [AutomationUpdate](schemas.md#automationupdate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [Automation](schemas.md#automation) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |

### Fleet-wide and per-cluster pause state { #getFleetAutomation }

`GET /v1/admin/automation`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Automation state. | [FleetAutomation](schemas.md#fleetautomation) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Pause or resume automation of the whole fleet { #setFleetAutomation }

`PUT /v1/admin/automation`

Accepts: session of a user with role `operator` or higher or bearer token with role `admin`.

A fleet pause applies to every cluster in addition to its own pause (a cluster is paused
if either is). Resuming the fleet needs an admin. `If-Match` (the `fleet.version`) is
optional, as for a cluster. Records `AutomationPaused` or `AutomationResumed`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `If-Match` | header | string | ETag (`"<version>"`) of the object being changed; checked when given (`412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [AutomationUpdate](schemas.md#automationupdate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [Automation](schemas.md#automation) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |

### API tokens (never their values) { #listTokens }

`GET /v1/admin/tokens`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | query | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `includeRevoked` | query | boolean | default `False` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Tokens, newest first. | [TokenList](schemas.md#tokenlist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Create an API token { #createToken }

`POST /v1/admin/tokens`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

The value is returned once, in `value`; the hub stores only its SHA-256. Role `agent` needs
`cluster`; `ui` and `admin` must not have one. Records `TokenCreated` (without the value).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [TokenCreate](schemas.md#tokencreate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Created. | [TokenCreated](schemas.md#tokencreated) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Revoke an API token { #revokeToken }

`DELETE /v1/admin/tokens/{tokenId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Sets `revokedAt`; the token stops working at once (the hub's token cache is invalidated).
Revoking the token used for this request is allowed. Records `TokenRevoked`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `tokenId` | path | string, required | pattern `^[0-9]+$` |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Revoked. | [TokenInfo](schemas.md#tokeninfo) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Teams with their workloads and estimated savings { #listTeams }

`GET /v1/admin/teams`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

Teams own workloads (docs/teams.md). A team-scoped caller gets only its teams and no
`unassigned` counts. Counts are the caller's live workloads now.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Teams, ordered by id. | [TeamList](schemas.md#teamlist) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Create a team { #createTeam }

`POST /v1/admin/teams`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

The workloads are reassigned at once: the owner label on a workload, then on its namespace,
then the first team (by id) whose namespace selector matches. Records `TeamCreated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [TeamInput](schemas.md#teaminput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Created. | [Team](schemas.md#team) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### One team { #getTeam }

`GET /v1/admin/teams/{teamId}`

Accepts: bearer token with role `ui` or session of a user with role `viewer` or higher.

A team outside a team-scoped caller's teams is not found.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `teamId` | path | string, required | pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`; example `payments` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Team. | [Team](schemas.md#team) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown team, or a team outside the caller's teams. | [Problem](schemas.md#problem) |

### Replace a team's definition { #updateTeam }

`PUT /v1/admin/teams/{teamId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

The id never changes (bindings and IdP group mappings refer to it). The workloads are
reassigned at once. Records `TeamUpdated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `teamId` | path | string, required | pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`; example `payments` |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [TeamInput](schemas.md#teaminput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [Team](schemas.md#team) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown team, or a team outside the caller's teams. | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Delete a team { #deleteTeam }

`DELETE /v1/admin/teams/{teamId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Its workloads go to another matching team or to Unassigned, and the users' bindings to it
are removed. Records `TeamDeleted`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `teamId` | path | string, required | pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`; example `payments` |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Deleted. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown team, or a team outside the caller's teams. | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Local users { #listUsers }

`GET /v1/admin/users`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `includeDeleted` | query | boolean | default `False` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Users, ordered by username. | [UserList](schemas.md#userlist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Create a local user { #createUser }

`POST /v1/admin/users`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

The password is stored as a bcrypt hash. The user must change it at first login
(`mustChangePassword`) unless `mustChangePassword: false` is sent. Records `UserCreated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [UserCreate](schemas.md#usercreate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Created. | [User](schemas.md#user) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### One local user { #getUser }

`GET /v1/admin/users/{username}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `username` | path | string, required | pattern `^[a-z0-9][a-z0-9._@+-]{1,126}$`; example `alice`, `alice@example.com` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | User. | [User](schemas.md#user) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown username. | [Problem](schemas.md#problem) |

### Delete a local user { #deleteUser }

`DELETE /v1/admin/users/{username}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Soft delete (`deletedAt`, sessions revoked); the username stays reserved so the audit
trail stays unambiguous. Not for the last enabled admin (`409`, code `last_admin`).
Records `UserDeleted`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `username` | path | string, required | pattern `^[a-z0-9][a-z0-9._@+-]{1,126}$`; example `alice`, `alice@example.com` |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Deleted. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown username. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Change a user's display name, role or disabled flag { #updateUser }

`PATCH /v1/admin/users/{username}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Only the given fields change. Disabling a user or changing the role revokes the user's
sessions. The last enabled admin cannot be demoted or disabled (`409`, code
`last_admin`). Records `UserUpdated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `username` | path | string, required | pattern `^[a-z0-9][a-z0-9._@+-]{1,126}$`; example `alice`, `alice@example.com` |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [UserUpdate](schemas.md#userupdate)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [User](schemas.md#user) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown username. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Set a new password for a user { #resetUserPassword }

`POST /v1/admin/users/{username}/reset-password`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Sets `password`, or a generated one returned once in `temporaryPassword` when `password`
is omitted. Sets `mustChangePassword`, clears the lockout and revokes the user's sessions.
Records `PasswordReset` (without the password).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `username` | path | string, required | pattern `^[a-z0-9][a-z0-9._@+-]{1,126}$`; example `alice`, `alice@example.com` |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [PasswordReset](schemas.md#passwordreset)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Reset. | [PasswordResetResult](schemas.md#passwordresetresult) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `404` | Unknown username. | [Problem](schemas.md#problem) |

### Metrics datasources { #listDatasources }

`GET /v1/admin/datasources`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Includes the built-in `local` datasource. Secrets are never returned.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Datasources, ordered by name. | [DatasourceList](schemas.md#datasourcelist) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Add a metrics datasource { #createDatasource }

`POST /v1/admin/datasources`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Secret auth fields are write-only: stored encrypted, never returned. Records
`DatasourceCreated` (without secrets).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [DatasourceInput](schemas.md#datasourceinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Created. | [Datasource](schemas.md#datasource) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### One datasource { #getDatasource }

`GET /v1/admin/datasources/{datasourceId}`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `datasourceId` | path | string, required | `local` or a UUID. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Datasource. | [Datasource](schemas.md#datasource) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown datasource id. | [Problem](schemas.md#problem) |

### Replace a datasource { #updateDatasource }

`PUT /v1/admin/datasources/{datasourceId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Omitted secret fields keep their stored values; `clearSecrets: true` removes them. The
built-in `local` datasource only accepts `timeout` and `maxPoints` changes (`400`, code
`builtin`). Records `DatasourceUpdated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `datasourceId` | path | string, required | `local` or a UUID. |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [DatasourceInput](schemas.md#datasourceinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [Datasource](schemas.md#datasource) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown datasource id. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Delete a datasource { #deleteDatasource }

`DELETE /v1/admin/datasources/{datasourceId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Soft delete. `409` with code `datasource_in_use` while a cluster is bound to it, `builtin`
for `local`. Records `DatasourceDeleted`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `datasourceId` | path | string, required | `local` or a UUID. |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Deleted. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown datasource id. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Run the metrics checks against a datasource now { #testDatasource }

`POST /v1/admin/datasources/{datasourceId}/test`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Runs `datasource.reachable` and, with `binding`, every hub metrics check for that binding
(before any agent exists; the onboarding wizard's "Test"). Nothing is stored or audited.
Returns `200` also when checks fail; `ok` is true when no blocking check failed.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `datasourceId` | path | string, required | `local` or a UUID. |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`): [DatasourceTestRequest](schemas.md#datasourcetestrequest)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Check results. | [DatasourceTestResult](schemas.md#datasourcetestresult) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown datasource id. | [Problem](schemas.md#problem) |

### Agent install instructions for a cluster { #getClusterInstall }

`GET /v1/admin/clusters/{cluster}/install`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Rendered for this cluster and hub URL. The token value is never included: the snippet
references the Secret `kerf-agent-token` (key `token`); the value is shown
once when the token is created. Also returns the Argo CD `ignoreDifferences` snippet for
`argocd-cm`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `cluster` | path | string, required | Cluster name. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |
| `format` | query | InstallFormat (enum), required | one of `helm`, `values`, `argocd` |
| `webhook` | query | boolean | Include the webhook install. default `False` |
| `certManager` | query | boolean | Webhook certificates from cert-manager (otherwise self-signed). default `False` |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Install instructions. | [InstallSnippet](schemas.md#installsnippet) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Component versions and the update check { #getMaintenance }

`GET /v1/admin/maintenance`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

The versions of every Kerf component the hub knows of: each hub replica (seen in the
last 10 minutes), the pricing service (reported with its requests to the hub, seen in the last
hour), and per registered cluster the agent (status batch) and webhook pods (agent self-check).
`compat` compares each with the hub version of the replica that answered. `update` is the
stored result of the check for a newer stable Kerf release (`hub.updateCheck` chart values).
The web UI knows its own version (build-time constant) and compares it client-side.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Versions and update status. | [Maintenance](schemas.md#maintenance) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Check for a newer Kerf release now { #checkForUpdate }

`POST /v1/admin/maintenance/update-check`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Reads the public GitHub releases of kerf-dev/kerf now (plain unauthenticated GET; nothing about
the installation is sent), stores the result and returns it. Returns the stored result without
a request when the last check is less than 30 seconds old. `200` also when the check failed
(`state: failed`, `error`). `409 update_check_disabled` when the chart value
`hub.updateCheck.enabled` is false.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | The update status after the check. | [UpdateStatus](schemas.md#updatestatus) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### Notification settings the page needs { #getNotificationStatus }

`GET /v1/admin/notifications/status`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Whether webhook URLs can be stored (the secrets encryption key), whether messages link to the UI, and the delivery limits.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Status. | [NotificationStatus](schemas.md#notificationstatus) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Notification channels { #listNotificationChannels }

`GET /v1/admin/notifications/channels`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Slack, Microsoft Teams and generic webhook channels, ordered by name, with the outcome of the last delivery. Webhook URLs are masked and secrets are never returned.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Channels. | [NotificationChannelList](schemas.md#notificationchannellist) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Add a notification channel { #createNotificationChannel }

`POST /v1/admin/notifications/channels`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

A Slack incoming webhook, a Microsoft Teams Workflows webhook or a generic webhook (optionally signed with an HMAC secret, `docs/contracts/notifications.md`). The URL and the secret are write-only: stored encrypted, never returned. `https` only (`http` when the hub allows it, `notifications.allowInsecureHTTP`). `409 secrets_key_missing` when the hub has no secrets encryption key, `409 already_exists` for a taken name. Records `NotificationChannelCreated` (without the URL or secret).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [NotificationChannelInput](schemas.md#notificationchannelinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Created. | [NotificationChannel](schemas.md#notificationchannel) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### One notification channel { #getNotificationChannel }

`GET /v1/admin/notifications/channels/{channelId}`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `channelId` | path | string (uuid), required |  |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Channel. | [NotificationChannel](schemas.md#notificationchannel) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Replace a notification channel { #updateNotificationChannel }

`PUT /v1/admin/notifications/channels/{channelId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Omitted `url` and `secret` keep their stored values; `clearSecret: true` removes the signing secret. The `type` cannot change (`400`). Channels defined in the chart values (`source: config`) are read-only (`409 channel_read_only`). Records `NotificationChannelUpdated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `channelId` | path | string (uuid), required |  |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [NotificationChannelInput](schemas.md#notificationchannelinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [NotificationChannel](schemas.md#notificationchannel) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Delete a notification channel { #deleteNotificationChannel }

`DELETE /v1/admin/notifications/channels/{channelId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Soft delete; queued deliveries of the channel are dropped. `409 channel_in_use` while a rule or the enabled digest sends to it, `channel_read_only` for chart-defined channels. Records `NotificationChannelDeleted`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `channelId` | path | string (uuid), required |  |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Done. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Send a test message { #testNotificationChannel }

`POST /v1/admin/notifications/channels/{channelId}/test`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Sends a test message to the channel now, from the replica that answers (not queued, not retried), and returns the outcome. `200` also when the destination refused it (`ok: false`, `error`; the error never contains the URL). Test messages appear in the delivery history.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `channelId` | path | string (uuid), required |  |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | The outcome. | [NotificationTestResult](schemas.md#notificationtestresult) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |

### Notification rules { #listNotificationRules }

`GET /v1/admin/notifications/rules`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Rules, ordered by name. | [NotificationRuleList](schemas.md#notificationrulelist) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Add a notification rule { #createNotificationRule }

`POST /v1/admin/notifications/rules`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

A rule sends the listed event types to the listed channels. `clusters` and `namespaces` are glob patterns (`prod-*`); empty matches everything. An event without a namespace (readiness, automation) never matches a rule that lists namespaces. Rule names are unique (`409 already_exists`); every channel must exist. Records `NotificationRuleCreated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [NotificationRuleInput](schemas.md#notificationruleinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `201` | Created. | [NotificationRule](schemas.md#notificationrule) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### Replace a notification rule { #updateNotificationRule }

`PUT /v1/admin/notifications/rules/{ruleId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Records `NotificationRuleUpdated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `ruleId` | path | string (uuid), required |  |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [NotificationRuleInput](schemas.md#notificationruleinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [NotificationRule](schemas.md#notificationrule) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Delete a notification rule { #deleteNotificationRule }

`DELETE /v1/admin/notifications/rules/{ruleId}`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Soft delete. Records `NotificationRuleDeleted`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `ruleId` | path | string (uuid), required |  |
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [ChangeReason](schemas.md#changereason)

**Responses**

| Status | Description | Body |
|---|---|---|
| `204` | Done. |  |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `404` | Unknown cluster. | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Weekly digest settings { #getNotificationDigest }

`GET /v1/admin/notifications/digest`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Settings. | [NotificationDigest](schemas.md#notificationdigest) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |

### Change the weekly digest { #updateNotificationDigest }

`PUT /v1/admin/notifications/digest`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Day (0 = Sunday), time of day and IANA time zone of the digest and the channels it goes to; enabling it needs at least one channel (`400`). Records `NotificationDigestUpdated`.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `If-Match` | header | string, required | ETag (`"<version>"`) of the object being changed (`428` if missing, `412` if stale). |
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Request body** (`application/json`, required): [NotificationDigestInput](schemas.md#notificationdigestinput)

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Saved. | [NotificationDigest](schemas.md#notificationdigest) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `412` | `If-Match` does not match the current version (code `version_conflict`). | [Problem](schemas.md#problem) |
| `428` | `If-Match` is required (code `precondition_required`). | [Problem](schemas.md#problem) |

### Send the digest now { #sendNotificationDigest }

`POST /v1/admin/notifications/digest/send`

Accepts: session of a user with role `admin` or higher or bearer token with role `admin`.

Queues the digest for the configured, enabled channels now (the leader sends it). `409 no_channels` when there is none.

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `X-CSRF-Token` | header | string | Required when the request is authenticated with the session cookie. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `202` | Queued. | [NotificationDigestQueued](schemas.md#notificationdigestqueued) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
| `409` | The change conflicts with the current state (`code` says how). | [Problem](schemas.md#problem) |

### Delivery history { #listNotificationDeliveries }

`GET /v1/admin/notifications/deliveries`

Accepts: session of a user with role `viewer` or higher or bearer token with role `ui`.

Queued, sent and failed deliveries, newest first. Deliveries are kept for 30 days. A failing delivery is retried with exponential backoff for one hour, then marked `failed` (also exported as `kerf_notification_deliveries_total{result="failed"}`).

**Parameters**

| Name | In | Type | Description |
|---|---|---|---|
| `channelId` | query | string (uuid) |  |
| `status` | query | NotificationDeliveryStatus (enum) | one of `pending`, `sent`, `failed` |
| `limit` | query | integer | Maximum number of items in this page. min 1; max 5000 |
| `continue` | query | string | Token from a previous page's `continue` field. |

**Responses**

| Status | Description | Body |
|---|---|---|
| `200` | Deliveries. | [NotificationDeliveryList](schemas.md#notificationdeliverylist) |
| `400` | The request is malformed or fails validation. | [Problem](schemas.md#problem) |
| `401` | Missing or invalid bearer token. | [Problem](schemas.md#problem) |
| `403` | The token is not allowed to access this cluster, the role is too low (`role_forbidden`), or the caller's team bindings do not cover the endpoint or the workload (`scope_forbidden`, docs/teams.md). | [Problem](schemas.md#problem) |
