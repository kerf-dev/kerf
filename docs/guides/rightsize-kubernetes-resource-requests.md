---
title: "How to rightsize Kubernetes resource requests"
description: "A practical guide to Kubernetes rightsizing: requests vs limits, which usage percentile to use, how much headroom, in-place pod resize, HPA and JVM traps, and rollback. With an open source tool to automate it."
---

# How to rightsize Kubernetes resource requests

Rightsizing means setting each container's CPU and memory requests to what it needs, plus a margin. Most clusters
start with guesses, copied manifests or a default that someone picked years ago. This guide covers the concepts you
need to do it well, by hand or with a tool, and where each step goes wrong.

## Requests and limits do different things

A **request** is what the scheduler reserves for a container. A node can host pods whose requests add up to its
allocatable CPU and memory, whatever those pods actually use. Requests are the number that decides how many nodes you
need, and so what you pay.

A **limit** is a ceiling enforced at run time, and the two resources behave differently when they hit it:

- **CPU** over the limit is throttled. The container keeps running, slower. Throttling shows up as latency, not as a
  failure.
- **Memory** over the limit is killed by the kernel (OOM kill) and the container restarts.

Two consequences. First, an oversized request costs money without hurting the application, which is why it goes
unnoticed. Second, an undersized memory request or limit causes outages, which is why people are careful with it.
The Kubernetes documentation describes how requests and limits map to quality-of-service classes (Guaranteed,
Burstable, BestEffort), which decide eviction order under node pressure. Check how your changes affect the class.

## Step 1: measure usage over a representative window

You need per-container usage over time, not a snapshot. Prometheus and compatible stores (VictoriaMetrics, Thanos,
Mimir, Amazon Managed Service for Prometheus) hold it as cAdvisor series such as
`container_cpu_usage_seconds_total` and `container_memory_working_set_bytes`. Kerf's
[metrics contract](../contracts/metrics.md) lists the series it reads.

The window has to include what your workload really does: the weekly peak, the end-of-month job, the Monday-morning
rush. Kerf's built-in profiles read 7 days (`dev`), 14 days (`production`) or 28 days (`high-availability`). A window
shorter than your longest cycle will miss the peak, and the result looks fine until that day.

## Step 2: pick a percentile

Taking the maximum usage makes every request as large as your worst spike. Taking the average makes half of the time
a problem. A high percentile sits between.

CPU and memory deserve different treatment:

- **CPU is compressible.** Missing a short burst slows requests for a moment. A percentile around the 90th to the 95th
  is common.
- **Memory is not.** Missing a peak kills the container. Use a higher percentile or the peak.

Kerf's `production` profile uses the 95th percentile for CPU and, for memory, the higher of the 99th percentile and the
observed peak. `dev` uses 0.90 and 0.95, `high-availability` uses 0.99 for both. These are the built-in values
([profiles](../concepts/policies.md#profiles)); an admin can change them.

## Step 3: add headroom

The percentile describes the past. Headroom covers growth and variation. The built-in profiles add 10 %, 15 % or 30 %
of CPU and 10 %, 15 % or 25 % of memory. More headroom costs more and protects more. Choose by the cost of a failure:
a batch job that can rerun tolerates less than a payment service.

Two more rules prevent self-inflicted damage:

- **Never lower a resource that is already hurting.** A container that was throttled needs its CPU request
  left alone, and one that was OOM-killed needs its memory request left alone, until the cause is understood. Kerf
  never recommends a decrease in those cases.
- **Set a floor.** Tiny requests make containers fragile. Kerf's default minimums are 10m CPU and 32Mi memory.

## Step 4: decide what to do with limits

The two common positions are a memory limit equal to the request (the usual choice, because memory usage is not
negotiable) and no CPU limit, or a generous one, to avoid throttling. Kerf's defaults are
`memory.limitEqualsRequest: true` and `cpu.limits: keep`, with `remove` and `ratio` as alternatives. Whichever you
choose, be explicit: a limit that follows the request means the limit moves whenever the request does.

## Step 5: watch for the traps

- **Horizontal Pod Autoscaler (HPA).** An HPA scaling on CPU or memory utilization divides usage by requests. Lower the
  requests and utilization rises, so the HPA adds replicas, and you pay for more pods. Either scale the HPA on another
  metric, or change the requests and the HPA target together. Kerf's [HPA coordination](../concepts/hpa.md) does the
  second.
- **JVM applications.** The heap is sized at startup, from `-Xmx` or as a share of the memory limit. A request that
  ignores the heap gets the container killed. See [JVM-aware memory](../concepts/jvm.md).
- **Single replicas.** A change that restarts the only pod of a workload is an outage. Prefer a change that does not
  restart containers, or schedule it ([single replicas](../concepts/apply-strategies.md#single-replicas-and-ondelete-statefulsets)).
- **Seasonal workloads.** A workload that is quiet in the measurement window and busy later will be undersized. Use a
  longer window or leave it in recommendation mode.

## Step 6: apply the change

There are three ways, in increasing order of automation.

1. **Edit the manifest.** Change the values in Git and let your pipeline roll them out. Pods restart. This is the
   safest to reason about and the slowest.
2. **In-place pod resize.** Kubernetes can change a running pod's requests and limits without recreating it. The
   feature is GA in Kubernetes 1.35. Whether a container restarts depends on its `resizePolicy`: CPU usually resizes
   without a restart; memory may need one, and decreasing a memory limit below current usage can cause an OOM kill.
   A resize can also be refused or deferred by the node. A tool that uses it needs a fallback for that case.
3. **A controller.** The VPA and Kerf both apply changes on their own. See
   [Kerf vs VPA](../compare/kerf-vs-vpa.md).

## Step 7: change in small steps and be able to roll back

Even correct numbers should go in gradually. Cap how far one step may lower a value, wait, then take the next step.
Kerf's default cap is 50 % per step with a 24-hour cooldown between changes.

After a change, watch the workload for OOM kills, restarts and CPU throttling, and have the old values ready. A manual
version is a dashboard and a runbook. Kerf's agent does it for you: it watches for 30 minutes by default, and one OOM
kill, three restarts or a throttling ratio of 0.25 (for the resource it changed) restores the previous values. Decreases
of that resource stay blocked for the cooldown ([rollback](../concepts/rollback.md)).

## Doing this at scale with Kerf

The seven steps above are the process Kerf automates:

| Step | Where Kerf does it |
|---|---|
| Measure usage | The hub reads your Prometheus-compatible store daily |
| Percentile and headroom | [Profiles](../concepts/policies.md#profiles) and per-policy settings |
| Limits | `cpu.limits` and `memory.limitEqualsRequest` in a policy |
| HPA and JVM traps | [HPA coordination](../concepts/hpa.md), [JVM-aware memory](../concepts/jvm.md) |
| Apply | [In-place](../concepts/apply-strategies.md), rollout or deferred to a maintenance window |
| Small steps | [Guardrails](../concepts/guardrails.md) |
| Rollback | [Health watch](../concepts/rollback.md) |

Every workload starts in `audit`: Kerf recommends and prices, and changes nothing. Turn on `auto-patch` for one
namespace, one resource (CPU first is a common choice), and watch the activity log.

To see what it finds before installing anything in the cluster, run the [`kerf report`](../cli.md) command against your
Prometheus. For the full system on a laptop, follow the [quickstart](../quickstart.md); for a real cluster, see
[Install on Amazon EKS](../install/eks.md) or [on any cluster](../install/any-cluster.md).

Once requests are lower, the savings still depend on your nodes. Read
[Reduce Kubernetes costs with rightsizing](reduce-kubernetes-costs.md).

## FAQ

### What is the right percentile for Kubernetes resource requests?

There is no single answer. A high percentile (around 95th for CPU) with headroom is a common start, and memory uses a
higher one or the peak, because running out of memory kills the container.

### Should I set CPU limits?

It depends on how you value predictability against throttling. Many teams keep requests and drop CPU limits, so a
container can use idle CPU. Kerf keeps existing CPU limits by default and can remove them or set a ratio.

### How long should I observe a workload before rightsizing it?

At least the length of its longest cycle. Kerf's default learning period before it will change a workload is 7 days,
and its production profile reads 14 days.

### Does lowering requests risk outages?

Lowering memory below real peak does. That is why you use a high percentile, headroom, small steps and a rollback.

## Related

- [Concepts](../concepts/README.md)
- [Kerf vs Kubernetes VPA](../compare/kerf-vs-vpa.md)
- [Kerf vs Goldilocks](../compare/kerf-vs-goldilocks.md)
- [FAQ](../faq.md)
