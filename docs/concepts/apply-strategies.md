# Apply strategies

The apply strategy is how the agent puts a change onto a workload in `auto-patch`. Set it with
`spec.apply.strategy` in a policy, or per workload with the annotation `kerf.getkerf.dev/strategy`.

| Strategy | What the agent does | Restarts pods |
|---|---|---|
| `in-place` (default) | Resizes the running pods through the `resize` subresource. The webhook gives new pods the same values. The pod template is not touched. | Only if the container's `resizePolicy` says so |
| `rollout` | Patches the pod template, which starts a rolling restart. | Yes |
| `deferred` | Holds the change until a maintenance window opens, then applies it as a rollout. | Yes, inside the window |

## In-place

The agent patches each pod's resources and waits for the kubelet to apply them. Because the pod template stays as it
is, there is no rollout and no drift for GitOps ([GitOps](gitops.md)).

A resize can be refused or stall. The agent then falls back to a rollout:

- **Infeasible:** the node can never fit the new values. The fallback is immediate.
- **Deferred by the kubelet:** the node cannot fit them yet. The fallback happens after `spec.apply.fallbackAfter`
  (default `10m`).

A fallback sets the strategy of the `RightsizingRecommendation` to `rollout`, records a `fallback` event, and sticks
until the configured strategy changes. In-place is not used where it cannot work:

- A node with swap blocks in-place memory changes, and a node you list in the agent's
  `unsupportedNodeSelectors` (for example a static CPU or Memory Manager policy) blocks every in-place change. The
  change is blocked with the reason `UnsupportedNode`.
- Memory of a JVM or Python container needs `resizePolicy: RestartContainer` or a rollout
  ([JVM-aware memory](jvm.md)).
- An in-place resize cannot remove a limit. A removal reaches new pods through the webhook, or a rollout.

## Rollout

The agent writes the pod template with server-side apply under the field manager `kerf`. It owns only the CPU and
memory fields of the resources in `auto-patch`. When a workload leaves `auto-patch`, the agent releases them. HPA
coordination always uses a rollout ([HPA coordination](hpa.md)).

## Deferred and maintenance windows

With `deferred`, a change that passes the guardrails waits for a maintenance window. The status of the
`RightsizingRecommendation` shows phase `Deferred` and `status.nextWindow`, and a `blocked` event with the reason
`OutsideMaintenanceWindow` is recorded. When the window opens, the agent checks the guardrails again and applies the
change as a rollout. With no maintenance window configured, the change waits.

```yaml
spec:
  apply:
    strategy: deferred
    maintenanceWindows:
      - schedule: "0 2 * * 0"      # Sundays at 02:00 ...
        duration: 3h               # ... for three hours
        timeZone: Europe/Berlin
      - start: "2026-12-20T00:00:00Z"
        end: "2026-12-21T00:00:00Z"
```

A window is either recurring (`schedule`, a 5-field cron expression, plus `duration`; `timeZone` defaults to UTC)
or one-off (`start` and `end`). A policy can list up to 16. An invalid maintenance window counts as closed.

## Freeze windows

A freeze window blocks every strategy. While one is open, the agent applies nothing, does not release field
ownership, and records `blocked` with the reason `FreezeWindow`. Health-watch [rollbacks](rollback.md) still run, and
an in-place resize that was already submitted is watched to the end, but no new resize starts.

```yaml
spec:
  apply:
    freezeWindows:
      - schedule: "0 18 * * 5"     # Fridays at 18:00 ...
        duration: 62h              # ... until Monday 08:00
        timeZone: Europe/Berlin
```

Freeze windows use the same format as maintenance windows. An invalid freeze window counts as open, so it errs on the
side of not changing anything. Two things freeze in addition to the policy and cannot be overridden by it:

- the cluster-wide `freeze` of the agent chart (`freeze.now: true` or `freeze.windows`);
- a pause in **Admin → Automation**, for one cluster or the whole fleet (reason `Paused`).

## Which strategy to use

| Situation | Use |
|---|---|
| Most workloads on a cluster that supports in-place resize | `in-place` |
| A workload that must change with a restart, or a node that cannot resize in place | `rollout` |
| A workload that may only be restarted in an agreed window | `deferred` with `maintenanceWindows` |
| No change at all during a period | `freezeWindows`, on top of any strategy |

## Single replicas and `OnDelete` StatefulSets

A change must not leave a single-replica workload without a Ready pod. A workload counts as single-replica if it is
a Deployment or StatefulSet with one replica, or if a PodDisruptionBudget allows no disruption of its pods.

- An in-place resize that does not restart a container is safe, so it goes in.
- An in-place resize that restarts a container (`resizePolicy: RestartContainer`) is blocked.
- A rollout is safe only for a Deployment or DaemonSet with `maxSurge` of at least 1 and `maxUnavailable` of 0. A
  `Recreate` Deployment and a StatefulSet never qualify.
- A blocked change records `SingleReplicaUnsafe`. To accept a short disruption, annotate the workload with
  `kerf.getkerf.dev/allow-disruption: "true"`.

A StatefulSet with `updateStrategy: OnDelete` only changes a pod when the pod is deleted. After a rollout patches
the template, the agent replaces the outdated pods itself:

- highest ordinal first, one pod at a time by default (`onDelete.maxPodDeletesPerRun`);
- only when every pod is Ready and has been for 30 seconds (`onDelete.settle`), and the health watch reports no
  breach;
- through the eviction API, so PodDisruptionBudgets apply. A refusal is recorded as `PodDisruptionBudget`, and the
  agent retries.

## Related

- [Guardrails](guardrails.md)
- [Rollback](rollback.md)
- [GitOps and Argo CD](gitops.md)
- [Agent chart values](../reference/values/agent.md)
- [Custom resources](../reference/crds.md#rightsizingpolicy-spec)
