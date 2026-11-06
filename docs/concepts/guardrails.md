# Guardrails

Guardrails limit what the agent may change. They apply to every change in `auto-patch`, to every strategy, and to a
change you approve once. They live under `spec.guardrails` of a policy.

## The guardrails

| Field | What it limits | Default |
|---|---|---|
| `learningPeriod` | The minimum time of usage data a workload needs before it may be changed | `168h` (7 days) |
| `minConfidence` | The minimum confidence of the recommendation, from 0 to 1 | `"0.7"` |
| `maxChangePercent` | How far one step may lower a value, in percent of the current value | `50` |
| `minCPU` | The lowest CPU request the agent will set | `10m` |
| `minMemory` | The lowest memory request the agent will set | `32Mi` |
| `cooldown` | The minimum time between two changes | `24h` |
| `rollback` | The health watch after a change: see [Rollback](rollback.md) | see below |

```yaml
spec:
  guardrails:
    learningPeriod: 336h
    minConfidence: "0.8"
    maxChangePercent: 30
    minCPU: 50m
    minMemory: 64Mi
    cooldown: 48h
```

- **Learning period.** The hub reports how much usage data covers a workload. Until that time reaches
  `learningPeriod`, the change is blocked with `LearningPeriod`.
- **Confidence.** The hub scores each recommendation from 0 to 1 from the coverage of the window, the number of
  samples and the stability of CPU usage. The workload's score is the lowest of its containers. Below
  `minConfidence` the change is blocked with `LowConfidence`.
- **Maximum change.** `maxChangePercent: 50` means a value can fall to half its current value in one step, and no
  lower. The agent rounds the limit up to a whole millicore or MiB. Increases are never capped. A larger drop takes
  several steps, each at least one `cooldown` apart.
- **Minimums.** The agent never sets a request below `minCPU` or `minMemory`. A limit is never set below its request.
- **Cooldown.** After a change, the agent makes no other change to the workload for `cooldown`. After a
  [rollback](rollback.md), it also blocks further decreases of the rolled-back resource until `cooldown` has passed
  since the rollback.
- **Tolerance.** A value that would change by less than 5 % stays as it is, and the agent records `skipped` with
  `NoSignificantChange`. This is an agent setting (`tolerance`), not a policy field.

A throttled container never gets a lower CPU request, and an OOM-killed container never gets a lower memory request.
The agent refuses that part of the change with `Throttled` or `OOMKilled`.

## Defaults

These are the values when no layer sets a field ([Policies](policies.md#layers)).

| Field | Default |
|---|---|
| `guardrails.learningPeriod` | `168h` |
| `guardrails.minConfidence` | `"0.7"` |
| `guardrails.maxChangePercent` | `50` |
| `guardrails.minCPU` | `10m` |
| `guardrails.minMemory` | `32Mi` |
| `guardrails.cooldown` | `24h` |
| `guardrails.rollback.watchDuration` | `30m` |
| `guardrails.rollback.oomKills` | `1` |
| `guardrails.rollback.restarts` | `3` |
| `guardrails.rollback.throttlingRatio` | `"0.25"` |
| `apply.fallbackAfter` | `10m` |

Set your own defaults for all clusters or for one cluster in the hub (**Admin → Defaults**), or for one cluster with
the `clusterDefaults` value of the agent chart.

## What "blocked" means

A change is *blocked* when a guardrail stops a change that the mode would allow. The workload keeps its current
values. Three things tell you:

- the `Blocked` condition of its `RightsizingRecommendation` is `True`, with the reason and a message. The phase is
  `Blocked` unless the values of an earlier change are still live;
- the hub records a `blocked` event with the reason, such as `LearningPeriod`, `LowConfidence`, `Cooldown`,
  `FreezeWindow`, `HPACoupled`, `JVMFixedHeap` or `SingleReplicaUnsafe`. The full list is in
  [events](../contracts/events.md#reason-meanings);
- the workload page and the **Activity** page show it.

A block is not permanent. The agent decides again on its next evaluation, and the change goes in once nothing holds
it back. Blocks differ from `skipped`: a skip means nothing is wrong (the mode is `audit`, or the change is too
small), a block means a guardrail intervened.

Kerf records a `blocked` or `skipped` event once per workload and reason, and again only when the reason or the
proposed values change, so the log stays quiet while a workload waits.

The agent checks in this order, and the first match decides: the mode, whether the hub is reachable, a pause,
recommendation settings that no longer match the policy, an uncoordinated HPA, the learning period, the confidence,
freeze windows, the per-resource limits above, the cooldown, single-replica safety, and last the maintenance window.

## Related

- [Rollback](rollback.md)
- [Apply strategies](apply-strategies.md)
- [Policies](policies.md)
- [Events contract](../contracts/events.md)
- [Custom resources](../reference/crds.md#rightsizingpolicy-spec)
