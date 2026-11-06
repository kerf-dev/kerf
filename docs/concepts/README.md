# Concepts

How Kerf turns usage history into a change on a workload, and what stops or reverses that change. Read this page
first. Each page below goes deep on one part.

## The model in one page

```text
usage history -> recommendation -> policy -> guardrails -> apply -> health watch -> rollback
   (hub)            (hub)         (agent)     (agent)     (agent)     (agent)       (agent)
```

1. **Usage history.** The hub reads each container's CPU and memory usage from your Prometheus-compatible store.
2. **Recommendation.** The hub computes requests and limits per container from a percentile of that history plus
   headroom, using a profile (`dev`, `production` or `high-availability`). It runs once a day by default. A
   recommendation also carries a confidence, risk flags and the savings in dollars.
3. **Policy.** The agent in each workload cluster resolves the `RightsizingPolicy` that applies to a workload. The
   policy decides the mode of CPU and memory (`off`, `audit` or `auto-patch`), the apply strategy and the
   guardrails.
4. **Guardrails.** Before a change goes in, the agent checks the history, the confidence, the size of the step, the
   cooldown, freeze windows and the workload's own risks. A guardrail that stops a change *blocks* it.
5. **Apply.** In `auto-patch`, the agent applies the change as an in-place resize, a rollout, or a deferred patch
   inside a maintenance window.
6. **Health watch.** After each change the agent watches the workload for OOM kills, restarts and CPU throttling.
7. **Rollback.** If a threshold is crossed, the agent restores the previous values and blocks further decreases of
   that resource for the cooldown.

Every workload starts in `audit`: Kerf recommends and changes nothing until you turn on `auto-patch`. Every decision
is an event in the audit log ([events](../contracts/events.md)).

## Pages

| Page | What it explains |
|---|---|
| [Modes](modes.md) | `off`, `audit` and `auto-patch` per resource, and where the effective mode comes from |
| [Policies](policies.md) | `RightsizingPolicy`: selectors, priority, layers, the effective policy and the profiles |
| [Apply strategies](apply-strategies.md) | In-place, rollout and deferred; windows; single-replica and `OnDelete` handling |
| [Guardrails](guardrails.md) | The limits on each change, their defaults, and what "blocked" means |
| [Rollback](rollback.md) | The health watch after a change and what a rollback does |
| [HPA coordination](hpa.md) | Changing requests and the HPA target together |
| [JVM-aware memory](jvm.md) | Heap detection, heap-aware sizing and fixed-heap containers |
| [Approve once](approve-once.md) | Applying one recommendation to a workload that stays in `audit` |
| [GitOps and Argo CD](gitops.md) | No drift from in-place resizes, policies in Git, the `ignoreDifferences` entry |

## Terms

- **Hub.** The central service that computes recommendations and stores state. It holds no write credentials for a workload cluster.
- **Agent.** Runs in each workload cluster, pulls recommendations from the hub, and does all the writing.
- **Webhook.** Gives new pods the recommended resources, so a restart or a scale-up keeps the new values.
- **Workload.** A Deployment, StatefulSet, DaemonSet or CronJob.
- **`RightsizingRecommendation`.** The agent's record of the change for one workload. See the
  [custom resources](../reference/crds.md#rightsizingrecommendation-spec).

## Related

- [Architecture](../architecture.md): how the components fit together.
- [Install on Amazon EKS](../install/eks.md): including [turning on auto-patch](../install/eks.md#6-turning-on-auto-patch).
- [Custom resources](../reference/crds.md): every field of `RightsizingPolicy` and `RightsizingRecommendation`.
- [Frequently asked questions](../faq.md)
