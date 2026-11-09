---
title: "Reduce Kubernetes costs with rightsizing"
description: "How to cut Kubernetes compute spend by lowering oversized CPU and memory requests: find the waste, price it, change requests safely, and make sure nodes actually go away. With an open source tool."
---

# Reduce Kubernetes costs with rightsizing

The largest line on most Kubernetes bills is compute, and the biggest driver of compute is the sum of container
requests, because requests decide how many nodes the scheduler needs. Rightsizing lowers requests to what workloads
use. This guide shows how that turns into savings, and where the savings stop short.

## Why requests drive the bill

The scheduler places pods by requests. A node with 16 vCPU allocatable can host pods requesting 16 vCPU in total, even
if they use 3. You pay for the node. So when a team requests 4 CPU for a service that uses 1, the other 3 cost money
as long as they keep a node in the pool.

That makes oversized requests the common form of Kubernetes waste: invisible to the application, visible on the
invoice. [How to rightsize Kubernetes resource requests](rightsize-kubernetes-resource-requests.md) explains how to
pick correct values. This page is about turning them into dollars.

## Step 1: find the gap

Compare requested CPU and memory with what containers use, per workload, over a window that covers peaks. Prometheus
has both. Sort by the size of the gap, not by percentage: a 90 % gap on a tiny service is small money.

Kerf shows this as a list of workloads ranked by estimated monthly saving, with current, recommended and used values.
The quickest way to see it for your own cluster is [`kerf report`](../cli.md), which reads your existing Prometheus,
installs nothing in the cluster and sends no telemetry:

```bash
kerf report --prometheus-url https://prometheus.example.com \
  --cluster-label cluster=prod-eu-1 --profile production --format table
```

## Step 2: put a price on it

A gap in millicores does not convince anyone who approves changes. A dollar figure does. Kerf prices recommendations
with an hourly rate per vCPU and per GiB, derived from the nodes your workloads run on: effective rates from your AWS
Cost and Usage Report (with Savings Plans and Reserved Instances amortised), AWS list or spot prices, or a static
price file for other clusters ([pricing](../pricing.md)). Without any of them, `kerf report` uses default rates and says
so at the top of the report.

Treat the figure as an estimate: it is the difference between current and recommended requests at those rates, "if it
were applied". Kerf keeps it apart from [realized savings](../realized-savings.md), computed after changes are applied.
The two are never added together.

## Step 3: lower requests, starting where it is safe

Order the work by saving and by risk:

1. Start with CPU. It is compressible: a too-low request slows a container, it does not kill it.
2. Pick stateless workloads with several replicas and a history of stable usage.
3. Leave single-replica workloads, HPA-coupled workloads and fixed-heap JVMs for later, or use the features that
   handle them ([HPA coordination](../concepts/hpa.md), [JVM-aware memory](../concepts/jvm.md)).
4. Move in steps. Kerf's default is at most a 50 % decrease per change, with 24 hours between changes.

In Kerf, every workload begins in [`audit`](../concepts/modes.md): nothing changes until you set `auto-patch` per
namespace, per workload, and per resource. The [guardrails](../concepts/guardrails.md) check the length of history,
the confidence of the recommendation, and the size of the step before each change. A [rollback](../concepts/rollback.md)
restores the old values if the workload shows OOM kills, restarts or CPU throttling after a change.

## Step 4: make the nodes go away

This is the step that is easiest to skip. Lower requests free capacity on nodes. You only save money when nodes are
removed or replaced by smaller ones. That is the job of a node autoscaler such as Karpenter or the Cluster Autoscaler,
and several things can stop it:

- PodDisruptionBudgets that allow no disruption
- pods or nodes annotated against disruption (`karpenter.sh/do-not-disrupt`, the Cluster Autoscaler's scale-down
  annotation)
- local storage, affinity and topology rules
- a node pool at its minimum size

Kerf never creates, drains or deletes nodes. What it provides is the [node estimate](../node-savings.md): how many
nodes each pool would need after the recommendations and what they would cost, and a list of consolidation blockers
with the object that causes each one. The estimate is an upper bound of what consolidation can remove, because it does
not model affinity, topology spread and some other constraints. Treat it as a pointer to where to look.

## Step 5: prove it

Savings that nobody measures disappear in the next budget meeting. After changes are applied, compare what the cluster
costs with what it would have cost. Kerf's realized savings adds up the requests that applied changes released, hour by
hour, priced at that hour's rates, with a [documented method](../realized-savings.md#method) you can reproduce with
SQL and PromQL. It counts only changes Kerf applied, and only requests.

## Step 6: keep it from creeping back

Requests grow again when new services are copied from old ones. Keep recommendations on: Kerf's recommender runs daily,
and policies in Git ([GitOps](../concepts/gitops.md)) make your defaults reviewable. A weekly digest in Slack, Teams or
a signed webhook ([notifications](../notifications.md)) keeps the savings visible.

## What rightsizing does not do

- It does not split the bill between teams. For showback and chargeback, use a cost allocation tool; see
  [Kerf vs Kubecost and OpenCost](../compare/kerf-vs-kubecost.md).
- It does not change instance types, buy commitments or pick Spot capacity.
- It does not shrink storage, network or load balancer costs.
- It does not change GPU requests. Kerf's [GPU view](../gpu.md) shows idle GPUs but changes nothing.

## A realistic starting plan

1. Run `kerf report` and note the top ten workloads by estimated saving.
2. Install Kerf with the [quickstart](../quickstart.md) to learn the UI, then on a real cluster
   ([EKS](../install/eks.md), [any cluster](../install/any-cluster.md)). Leave everything in `audit`.
3. Review the top recommendations with the owning teams. Use the workload page's "why this recommendation" view.
4. Turn on `auto-patch` for CPU in one namespace. Watch the activity log for a week.
5. Widen to memory and more namespaces. Check the Capacity tab for the node effect.
6. Review realized savings monthly.

## FAQ

### How much can rightsizing save on Kubernetes?

It depends on how far requests are from usage and on whether nodes are removed. We do not quote a number, because a
figure from another cluster says little about yours. `kerf report` gives you a figure for yours, as an estimate.

### Will rightsizing reduce my bill right away?

Only when the node autoscaler removes or shrinks nodes. Check consolidation blockers.

### Is it safe to automate?

Done in steps with guardrails and a rollback, it is controlled, but it is a change to production workloads. Start in
`audit`, then one namespace. Kerf is a 0.x release ([FAQ](../faq.md#is-it-ready-for-production)).

### Does it work with the Horizontal Pod Autoscaler?

Yes, with care. See [HPA coordination](../concepts/hpa.md).

### Is there an open source tool for this?

Kerf is open source under Apache-2.0 and self-hosted. See [how it compares](../comparison.md) with the VPA, Goldilocks
and commercial tools.

## Related

- [How to rightsize Kubernetes resource requests](rightsize-kubernetes-resource-requests.md)
- [Kerf vs Kubernetes VPA](../compare/kerf-vs-vpa.md)
- [Kerf vs Kubecost and OpenCost](../compare/kerf-vs-kubecost.md)
- [Concepts](../concepts/README.md)
