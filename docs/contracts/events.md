# Events contract

Changing a type, reason or field here is a contract change (see [CONTRIBUTING.md](../../CONTRIBUTING.md#before-you-start)).

Events are the audit trail of what the agent decided and did. The agent sends them to the hub with
`POST /v1/clusters/{cluster}/events` (schema `Event` in `api/openapi/hub.yaml`); the hub stores them
in `audit_events` (`db/migrations/0001_init.sql`), counts them in `kerf_actions_total`
(`docs/contracts/metrics.md`) and serves them with `GET /v1/audit`.

## Delivery

- Each event has an agent-generated UUID `id`. The hub ignores an `id` it already stored, so the
  agent retries whole batches freely (at-least-once delivery, exactly-once storage).
- While the hub is unreachable the agent buffers events (bounded, oldest dropped first, with an
  `kerf_agent_events_dropped_total` counter) and sends them when the hub is back. Rollbacks never wait
  for delivery.
- The agent also records each event as a Kubernetes `Event` on the `RightsizingRecommendation`
  (`reason` = event reason, `type` = `Normal` except `failed`, `rolled_back`, `fallback` = `Warning`).

## Types

| Type | Emitted when | Typical reasons |
|---|---|---|
| `applied` | A change became live: resize accepted by the kubelet (in-place) or pod template patched and rollout started (rollout). One event per workload change, covering all changed containers. | `RecommendationApplied`, `ApprovedOnce` |
| `skipped` | The agent deliberately did not act and nothing is wrong: mode is not `auto-patch`, the change is too small, or the recommendation does not match the current policy. | `ModeOff`, `ModeAudit`, `NoSignificantChange`, `RecommendationStale` |
| `blocked` | A guardrail prevented a change the mode would allow. | `LearningPeriod`, `LowConfidence`, `Cooldown`, `HPACoupled`, `Throttled`, `OOMKilled`, `FreezeWindow`, `OutsideMaintenanceWindow`, `UnsupportedNode`, `RestartPolicyRequired`, `JVMFixedHeap`, `HubUnreachable`, `SingleReplicaUnsafe`, `PodDisruptionBudget`, `Paused` |
| `fallback` | An `in-place` change switched to `rollout`. | `ResizeInfeasible`, `ResizeDeferredTimeout` |
| `rolled_back` | The health watch restored the previous values. | `OOMKills`, `Restarts`, `Throttling` |
| `released` | The workload left `auto-patch`; field ownership was released so Argo CD restores Git values. | `ModeChanged` |
| `failed` | An API call needed for an action failed after retries. | `ApplyError`, `ResizeError`, `RollbackError`, `ReleaseError` |

`skipped` and `blocked` are emitted **only on change**: once per (workload, type, reason)
until the reason or the proposed values change (the hub's recommended values, by more than the
agent's tolerance, default 5 %), not on every loop iteration and not again just because the hub
issued a new recommendation id for the same values. A change of the workload's effective policy
(e.g. a RightsizingPolicy edit) also reports the current skip or block again, once. A change that is applied,
rolled back or released starts over, so the next skip or block is reported again. Per-resource
blocks inside an applied change (`Throttled`, `OOMKilled`, `RestartPolicyRequired`) follow the
same rule per resource.

Reasons are CamelCase and identical to the `Reason*` constants in `api/v1alpha1/wellknown.go`, to
the `metav1.Condition` reasons on `RightsizingRecommendation.status`, and to the `reason` label of
`kerf_actions_total`. Adding a reason is a contract change: add the constant and a row here.

### Reason meanings

| Reason | Meaning |
|---|---|
| `RecommendationApplied` | Values from the referenced recommendation are live. |
| `ApprovedOnce` | Values from the referenced recommendation are live because an operator approved it once in the hub; the workload's mode stays `audit`. `details.approvalId` is the approval id, `details.approvedBy` its actor. Guardrails applied as for `auto-patch`. |
| `ModeOff` / `ModeAudit` | Effective mode for the resource is `off` / `audit`. |
| `NoSignificantChange` | Recommended value is within the agent's tolerance of the current value. |
| `RecommendationStale` | The recommendation's `settings` differ from the current effective policy; waiting for the next hub run. |
| `LearningPeriod` | `dataCoverage.covered` < `guardrails.learningPeriod`. |
| `LowConfidence` | `confidence` < `guardrails.minConfidence`. |
| `Cooldown` | Last change or rollback was less than `guardrails.cooldown` ago. |
| `HPACoupled` | Workload has a CPU/memory-utilization HPA that is not coordinated: coordination is off in its policy, the HPA cannot be coordinated, or the new target would leave its bounds. |
| `Throttled` / `OOMKilled` | Decrease refused because the resource is under pressure. |
| `FreezeWindow` | Inside an `apply.freezeWindows` entry or the cluster-wide freeze: no change and no release of field ownership; health-watch rollbacks still run. |
| `OutsideMaintenanceWindow` | Strategy `deferred` and no maintenance window is open: the change is queued (phase `Deferred`, `status.nextWindow`) and applied as a rollout when the next window opens. |
| `UnsupportedNode` | In-place refused: node has swap or a static CPU/Memory Manager policy. |
| `RestartPolicyRequired` | Memory change on a JVM/Python container whose `resizePolicy` is not `RestartContainer` and strategy is `in-place`. |
| `JVMFixedHeap` | Memory change on a JVM container with a fixed maximum heap (`-Xmx` / `-XX:MaxHeapSize`, detected live or flagged `jvm-fixed-heap` by the hub); memory is audit-only. |
| `HubUnreachable` | No fresh recommendation because the hub cannot be reached; nothing new is applied. |
| `Paused` | Automation of the cluster (or of the whole fleet) is paused in the hub (`GET /v1/clusters/{cluster}/config`, `flags.paused`). Behaves like the cluster-wide freeze: no change and no release of field ownership; health-watch rollbacks still run. `details.pauseScope` is `cluster` or `fleet`. |
| `SingleReplicaUnsafe` | The workload is single-replica (Deployment/StatefulSet with `replicas: 1`, or a PodDisruptionBudget that allows 0 disruptions) and the change would leave it without a Ready pod: the in-place resize restarts a container (`resizePolicy: RestartContainer`), or the rollout has no surge (needs maxSurge ≥ 1 and maxUnavailable 0; Recreate and StatefulSets never qualify). Also when an in-place fallback to such a rollout is refused (`details.fallbackReason`). Opt in with the annotation `kerf.getkerf.dev/allow-disruption: "true"`. |
| `PodDisruptionBudget` | A PodDisruptionBudget refused (or the API server held) the eviction of an outdated pod of an `OnDelete` StatefulSet after its template was patched; the agent retries (`details.pod`). |
| `ResizeInfeasible` | Pod resize status `Infeasible`. |
| `ResizeDeferredTimeout` | Pod resize stayed `Deferred` longer than `apply.fallbackAfter`. |
| `OOMKills` / `Restarts` / `Throttling` | Rollback threshold from `guardrails.rollback` exceeded. |
| `ModeChanged` | Effective mode changed away from `auto-patch`. |
| `ApplyError` / `ResizeError` / `RollbackError` / `ReleaseError` | Kubernetes API error; `message` carries it. |

## Payload

```json
{
  "id": "6f1c2a8e-0d7b-4c55-a0b1-8f9e2f0d1c3a",
  "type": "rolled_back",
  "time": "2026-11-04T10:15:00Z",
  "workload": {"namespace": "payments-prod", "kind": "Deployment", "name": "checkout"},
  "recommendationID": "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b7c10",
  "strategy": "in-place",
  "resources": ["memory"],
  "containers": [
    {
      "name": "app",
      "from": {"requests": {"memory": "384Mi"}, "limits": {"memory": "384Mi"}},
      "to":   {"requests": {"memory": "1Gi"},   "limits": {"memory": "1Gi"}}
    }
  ],
  "reason": "OOMKills",
  "message": "2 OOM kills within 30m after change (threshold 1)",
  "details": {"oomKills": "2", "watchDuration": "30m"}
}
```

| Field | Required | Notes |
|---|---|---|
| `id` | yes | UUID, generated by the agent. |
| `type` | yes | One of the types above. |
| `time` | yes | When it happened in the cluster (not when it was sent). |
| `workload` | yes | `namespace`, `kind`, `name`. |
| `reason` | yes | CamelCase reason. |
| `recommendationID` | if known | Hub recommendation the event is about. |
| `strategy` | for applied/fallback/rolled_back/released | Strategy used (for `fallback`: the new strategy, `rollout`). |
| `resources` | if specific | `cpu`, `memory`, or both. Empty means the whole workload. |
| `containers` | for applied/rolled_back | `from` = values before, `to` = values after. Only changed resources are listed. |
| `message` | no | Human-readable, ≤ 1024 chars. |
| `details` | no | String map; well-known keys: `oomKills`, `restarts`, `throttlingRatio`, `watchDuration`, `resizeStatus`, `previousMode`, `newMode`, `error`; on `applied` with HPA coordination `hpa`, `hpaResource`, `hpaTargetFrom`, `hpaTargetTo` (percent); on `applied`/`ApprovedOnce` `approvalId`, `approvedBy`; on `blocked`/`Paused` `pauseScope`. |

The hub adds `cluster` (from the URL path) and `receivedAt` when it stores an event; both are returned
by `GET /v1/audit`.

## Notifications

The hub sends the events that matter to people to Slack, Teams and webhooks ([notifications](../notifications.md),
payload: [notifications contract](notifications.md)): `applied`, `rolled_back`, `failed` and `blocked` events of this
contract, plus `readiness_degraded` (a `ReadinessChanged` whose state went down) and `automation_paused` /
`automation_resumed` (`AutomationPaused` / `AutomationResumed`) from the configuration changes below. Nothing is
added to the event model.

## Configuration changes (`config_changed`)

The hub itself records every configuration change made through its API (admin area) in
the same audit log, with type `config_changed`. Agents never send this type (`POST .../events`
rejects it with `400`, code `invalid_item`, as any unknown type), and `GET /v1/audit` never returns it, because its
shape differs from a workload event: it is served by `GET /v1/audit/config` (schema
`ConfigChange` in `api/openapi/hub.yaml`) and, for changes that target one workload, in
`WorkloadHistory.configChanges`. Storage: `audit_events` rows with `type = 'config_changed'`
(`db/migrations/0009_*`), `reason` = the action below, `message` = the user's reason, plus
`actor`, `target_*`, `before`, `after`.

| Action (`ConfigChange.action`) | Target type | Recorded when |
|---|---|---|
| `SettingsUpdated` | `settings` | `PUT /v1/admin/settings/{recommender,pricing}` |
| `PolicyCreated` / `PolicyUpdated` / `PolicyDeleted` / `PolicyReverted` | `policy` | policy create, update, soft delete, revert |
| `OverrideSet` / `OverrideCleared` | `workload-override` | `PUT` / `DELETE /v1/workloads/{id}/override` |
| `WorkloadApproved` | `approval` | `POST /v1/workloads/{id}/approve` |
| `ClusterCreated` / `ClusterUpdated` / `ClusterDeleted` | `cluster` | cluster register, metadata change, retire |
| `AutomationPaused` / `AutomationResumed` | `automation` | pause / resume of a cluster (`id` = cluster) or the fleet (`id` = `*`); an auto-resume at `resumeAt` has actor type `system` |
| `TokenCreated` / `TokenRevoked` | `token` | API token create / revoke |
| `UserCreated` / `UserUpdated` / `UserDeleted` / `PasswordReset` | `user` | local user changes |
| `DatasourceCreated` / `DatasourceUpdated` / `DatasourceDeleted` | `datasource` | metrics datasource changes (never secrets) |
| `ReadinessChanged` | `readiness` (`id` = cluster) | the cluster's readiness state changed (actor type `system`, `before`/`after` = `{"state": …}`, `docs/contracts/readiness.md`) |
| `NotificationChannelCreated` / `NotificationChannelUpdated` / `NotificationChannelDeleted` | `notification` (`id` = channel id, `name` = channel name) | notification channel changes (the URL and the signing secret are never recorded: the masked URL only) |
| `NotificationRuleCreated` / `NotificationRuleUpdated` / `NotificationRuleDeleted` | `notification` (`id` = rule id) | notification rule changes |
| `NotificationDigestUpdated` | `notification` (`id` = `digest`) | the weekly digest's schedule or channels changed |
| `TeamCreated` / `TeamUpdated` / `TeamDeleted` | `team` (`id` = team id) | team changes ([teams.md](../teams.md)) |

`before`/`after` hold the target's API representation and never contain password hashes, passwords
or token values. When a user's team role (not a fleet-wide role) allowed the change, `actor.teams` lists that team:
the scope used ([teams.md](../teams.md)). Login attempts are not configuration changes and are not recorded here.
