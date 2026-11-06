# Modes

A mode says what Kerf does with the recommendation for one resource of one workload. There are three modes, and
CPU and memory each have their own.

## The three modes

| Mode | What Kerf does |
|---|---|
| `off` | Nothing. The recommendation is not acted on or reported as actionable. Use it to exclude a workload. |
| `audit` | Computes and reports the recommendation and its savings. Changes nothing. This is the global default. |
| `auto-patch` | The agent applies the recommendation, subject to the [guardrails](guardrails.md), with the [apply strategy](apply-strategies.md) in effect. |

In `audit` and `off`, the agent records a `skipped` event with the reason `ModeAudit` or `ModeOff`, once, until the
reason changes.

## CPU and memory separately

The two resources do not depend on each other. A common start is CPU in `auto-patch` and memory in `audit`: Kerf
resizes CPU and still shows the memory recommendation without touching it.

```yaml
spec:
  cpu:
    mode: auto-patch
  memory:
    mode: audit
```

Memory for JVM containers has extra rules, see [JVM-aware memory](jvm.md).

## Where the effective mode comes from

The effective mode of each resource comes from the highest level that sets it. From lowest to highest:

| Level | `ModeSource` value | Set by |
|---|---|---|
| Global default | `global-default` | Built-in `audit`, or the hub's global default policy (`ro-global-defaults`) |
| Cluster default | `cluster-default` | The agent chart value `clusterDefaults`, or the hub's cluster default policy (`ro-cluster-defaults`) |
| Policy | `policy` | The winning [`RightsizingPolicy`](policies.md), or the hub override of one workload |
| Annotation | `annotation` | An annotation on the workload object |

A level that leaves a mode unset inherits it from the level below. The workload page in the UI and the status of
its `RightsizingRecommendation` (`status.effective.cpuModeSource` and `memoryModeSource`) show which level won.

Annotations on the Deployment, StatefulSet, DaemonSet or CronJob (not the pod template) win over everything:

| Annotation | Sets |
|---|---|
| `kerf.getkerf.dev/mode` | CPU and memory |
| `kerf.getkerf.dev/cpu-mode` | CPU, and overrides `mode` for CPU |
| `kerf.getkerf.dev/memory-mode` | Memory, and overrides `mode` for memory |

```bash
kubectl annotate deployment checkout -n payments \
  kerf.getkerf.dev/cpu-mode=auto-patch kerf.getkerf.dev/memory-mode=audit
```

An invalid value is ignored and the level below applies.

## Changing a mode

- **Many workloads:** write a policy in the UI (**Admin → Policies**), or commit a `RightsizingPolicy` to Git.
- **One workload:** use the workload page in the UI. An override sets the modes for that workload, and **Exclude**
  sets both to `off`. Operators and admins can do this.
- **A whole cluster:** set `clusterDefaults` in the agent chart, or a cluster default in the hub.

The hub refuses a policy, override or approval that newly enables `auto-patch` on a cluster that has not reached
`AutomationReady` (`409`, code `cluster_not_automation_ready`). An admin can override this with a reason, and the
override is audited. An operator cannot enable `auto-patch` in a workload override on a cluster whose environment is
`prod`: that needs an admin.

## Limits on `auto-patch`

- An agent installed with `readOnly: true` caps every mode at `audit` and gets no write permissions on workloads.
- A paused cluster or fleet (**Admin → Automation**) behaves like a freeze: nothing new is applied, and health-watch
  rollbacks still run. See [apply strategies](apply-strategies.md#freeze-windows).
- A workload in `audit` can still receive one approved change: see [Approve once](approve-once.md).

## Related

- [Policies](policies.md)
- [Guardrails](guardrails.md)
- [Approve once](approve-once.md)
- [Custom resources](../reference/crds.md#rightsizingpolicy-spec)
- [Turning on auto-patch on EKS](../install/eks.md#6-turning-on-auto-patch)
