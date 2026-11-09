---
title: "Kerf vs Kubernetes VPA - an open source VPA alternative"
description: "Kerf and the Vertical Pod Autoscaler compared: what each recommends, how changes are applied, HPA handling, rollback, savings in dollars and when the plain VPA is the better choice."
---

# Kerf vs the Kubernetes Vertical Pod Autoscaler (VPA)

The Vertical Pod Autoscaler (VPA) is the standard Kubernetes answer to oversized CPU and memory requests. Kerf is an
open-source, self-hosted rightsizing system that covers the same ground and adds pricing, guardrails and rollback.
This page compares them so you can pick one, or run the VPA first and move later. Every statement about the VPA comes
from its [public documentation](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler/docs);
every statement about Kerf comes from this documentation and the code in this repository.

## The short version

| | Vertical Pod Autoscaler | Kerf |
|---|---|---|
| What it is | A Kubernetes autoscaler project (recommender, updater, admission controller) | A hub, per-cluster agents, a webhook and a web UI, installed with one Helm chart |
| Recommends | CPU and memory requests per container | CPU and memory requests and limits per container, from a [profile](../concepts/policies.md#profiles) |
| Applies changes | Evicts pods and mutates new pods; in-place updates with `InPlaceOrRecreate` | Resizes pods in place, rolls out the template, or waits for a [maintenance window](../concepts/apply-strategies.md) |
| Default behavior | You choose the update mode per VPA object | Every workload starts in [`audit`](../concepts/modes.md): recommend, change nothing |
| Rollback after a bad change | Not described in the VPA docs | [Automatic](../concepts/rollback.md) on OOM kills, restarts or CPU throttling |
| Savings in dollars | Not described in the VPA docs | Yes, priced per node type ([pricing](../pricing.md)) |
| Many clusters, one view | One VPA install per cluster | One hub and UI for the fleet |
| Footprint | Three small controllers | Hub, PostgreSQL, a Prometheus-compatible store, an agent and webhook per cluster |

## How each one decides a value

The VPA recommender builds its recommendation from current usage samples and history. The history comes from its own
checkpoints or from Prometheus (`--storage=prometheus`). The updater compares the recommendation with what pods
request and evicts pods that differ enough, respecting PodDisruptionBudgets. The admission controller then applies the
recommended values to new pods. We did not find the exact percentile and safety margin the recommender uses in the
documentation pages we read, so we do not quote them here. See the VPA flag reference before you rely on a number.

Kerf reads usage from a Prometheus-compatible store you already run (Prometheus, VictoriaMetrics, Thanos, Mimir or
Amazon Managed Service for Prometheus). The hub computes CPU as a percentile of usage plus headroom, and memory as the
higher of a percentile and the peak, plus a buffer. The built-in `production` profile reads 14 days, uses the 95th
percentile for CPU with 15 % headroom, and the 99th percentile for memory with a 15 % buffer. The `dev` and
`high-availability` profiles use shorter and longer windows, and an admin can change all of them. A container that was
recently throttled or OOM-killed never gets a decrease for that resource. For the underlying method, read
[How to rightsize Kubernetes resource requests](../guides/rightsize-kubernetes-resource-requests.md).

## Applying changes

The VPA offers update modes per object. `InPlaceOrRecreate` tries an in-place resize first and falls back to
recreating the pod when the resize is infeasible, deferred for more than five minutes, or would change the QoS class.
Its documentation lists it as alpha in VPA 1.4.0, beta in 1.5.0 and GA in 1.6.0, and it needs Kubernetes 1.33 or
newer. An `InPlace` mode that never evicts is alpha in 1.7.0. The VPA updates all containers of a pod together.

Kerf's default strategy is `in-place`: the agent resizes running pods through the `resize` subresource and leaves the
pod template alone, so GitOps tools such as Argo CD see no drift ([GitOps](../concepts/gitops.md)). If the node cannot
fit the new values, the agent falls back to a rollout. Two other strategies exist: `rollout`, and `deferred`, which
holds the change until a maintenance window opens. [Freeze windows](../concepts/apply-strategies.md#freeze-windows)
block every strategy. Single-replica workloads are protected: a change that would leave no Ready pod is blocked.

## Guardrails and rollback

A VPA object can set minimum and maximum allowed values per container. Beyond that, the VPA documentation we read
describes no health check after a change and no automatic revert.

Kerf puts [guardrails](../concepts/guardrails.md) in front of every change: a learning period (7 days by default), a
minimum confidence, a cap on how far one step may lower a value (50 % by default), minimum requests, and a cooldown
between changes. After a change the agent watches the workload for 30 minutes by default. One OOM kill (memory
changes), three restarts, or a CPU throttling ratio of 0.25 (CPU changes) triggers a [rollback](../concepts/rollback.md)
to the previous values, and decreases of that resource stay blocked until the cooldown has passed. All thresholds are
policy fields.

## Next to a Horizontal Pod Autoscaler

An HPA that scales on CPU or memory utilization divides usage by requests, so changing requests changes when it
scales. The VPA FAQ describes one way around it: let the VPA manage only memory (`controlledResources`) and the HPA
scale on CPU. The broader guidance in the VPA documentation is to avoid both on the same metric.

Kerf's answer is [HPA coordination](../concepts/hpa.md): it changes the requests and the HPA's utilization target in
one step, keeping the product constant so the replica count stays the same at the same usage. It is off by default
and uses a rollout. Without it, Kerf leaves HPA-coupled workloads in `audit` and records why.

## Money, teams and fleets

The VPA works in units of CPU and memory. Kerf also prices every recommendation per node type, can use effective rates
from your AWS Cost and Usage Report, and keeps [realized savings](../realized-savings.md) (what applied changes
saved) apart from estimates. It adds a UI with [sign-in through your identity provider](../auth.md), [teams](../teams.md),
[Slack, Teams and webhook notifications](../notifications.md), and a [node estimate](../node-savings.md). The VPA
project does not provide a UI or these features in the documentation we read.

## When the VPA is the better fit

- You run one or two clusters that you watch closely, and a recommendation you read in `kubectl` is enough.
- You want the smallest footprint and nothing outside the Kubernetes project to operate.
- You want the VPA to act on a workload's requests as a native Kubernetes object, with no hub or database.
- You have no Prometheus-compatible store and no wish to run one. The VPA runs from the Metrics API.

Kerf needs PostgreSQL and a metrics store, and it is version 0.x: until 1.0.0 a minor release may change chart values
or custom resources ([FAQ](../faq.md#is-it-ready-for-production)).

## When Kerf is the better fit

- You need to explain savings in dollars, per team or per cluster.
- You want automatic changes with a limit on step size, maintenance windows and a revert when a workload turns
  unhealthy.
- You have many clusters and want one place to review, approve and audit changes.
- You run HPAs on CPU or memory and still want requests lowered.

## Trying Kerf without dropping the VPA

Run [`kerf report`](../cli.md) against your existing Prometheus. It needs nothing installed in the cluster, only
reads, and prints workloads sorted by monthly saving. Compare its numbers with your VPA recommendations. For the
full system on a laptop, use the [quickstart](../quickstart.md). See also the [comparison table](../comparison.md)
with other tools, and [Kerf vs Goldilocks](kerf-vs-goldilocks.md).

## FAQ

### Is Kerf a replacement for the VPA?

For rightsizing requests, yes. Kerf does not use VPA objects. Do not let both manage the same workload.

### Does Kerf need the VPA installed?

No. Kerf has its own recommender, agent and webhook.

### Does Kerf support in-place pod resize?

Yes, it is the default apply strategy. In-place resize is GA in Kubernetes 1.35; see [apply strategies](../concepts/apply-strategies.md).

### Does the VPA work with GitOps?

The VPA docs describe eviction and mutation of new pods. How that sits with your GitOps tool depends on your setup,
and we have not verified it. With Kerf's default in-place strategy the pod template does not change.

## Related

- [Guide: How to rightsize Kubernetes resource requests](../guides/rightsize-kubernetes-resource-requests.md)
- [Guide: Reduce Kubernetes costs with rightsizing](../guides/reduce-kubernetes-costs.md)
- [Concepts](../concepts/README.md)
