---
title: "Kerf vs Goldilocks - Kubernetes resource recommendations compared"
description: "Goldilocks shows VPA recommendations in a dashboard. Kerf prices, applies and rolls back rightsizing changes. A fair comparison of both open source tools and when to choose each."
---

# Kerf vs Goldilocks

[Goldilocks](https://github.com/FairwindsOps/goldilocks) from Fairwinds and Kerf both help you choose CPU and memory
requests for Kubernetes workloads, and both are open source under the Apache-2.0 license. They sit at different points
on the path from "what should these numbers be?" to "the cluster runs on them". Statements about Goldilocks come from
its [public documentation](https://goldilocks.docs.fairwinds.com/); statements about Kerf come from this
documentation and the repository code.

## What each one does

Goldilocks is a utility that gives you a starting point for requests and limits. It creates a Vertical Pod Autoscaler
(VPA) in recommendation mode for each workload in the namespaces you choose, reads those VPAs, and shows the results
in a dashboard. The recommendations are the VPA's. It does not have its own recommender, and it needs the VPA
installed. Its documentation does not describe a way to apply changes itself; the earlier [comparison table](../comparison.md)
lists applying as "through VPA, opt-in".

Kerf is a full system: a hub that computes recommendations from your Prometheus-compatible store, an agent in each
workload cluster that applies changes under a policy, a webhook that gives new pods the new values, and a web UI.

| | Goldilocks | Kerf |
|---|---|---|
| Source of recommendations | VPA recommendation objects | Kerf's own recommender over Prometheus, VictoriaMetrics, Thanos, Mimir or Amazon Managed Service for Prometheus |
| Requires | VPA installed in the cluster | PostgreSQL, a metrics store, one Helm chart |
| Presentation | Dashboard per cluster | UI for a fleet, CLI report, Grafana dashboards, hub API |
| Applies changes | Not described in its documentation; possible only through VPA | [Per policy](../concepts/modes.md): `off`, `audit` or `auto-patch`, CPU and memory separately |
| Savings in dollars | Not described in its documentation | Yes, priced per node type |
| Rollback on bad health | Not described | [Yes](../concepts/rollback.md) |
| HPA, JVM awareness | Not described | [HPA coordination](../concepts/hpa.md), [JVM-aware memory](../concepts/jvm.md) |
| Multi-cluster | Fairwinds Insights, a separate commercial platform | Built in |

## Where Goldilocks is the better fit

- **You want a look, not a system.** Label a namespace, open the dashboard, read the numbers. It is a quick way to see
  how far off your requests are, and the setup is small.
- **You already run the VPA** and want a friendlier view of its recommendations than `kubectl describe vpa`.
- **You prefer to change manifests yourself.** Goldilocks is a source of suggested values that you apply in Git. Kerf
  can do the same in `audit` mode, but it is more machinery if that is all you need.
- **You do not want a database or a metrics store to operate.** Kerf needs both.

## Where Kerf is the better fit

- **You want the numbers to reach the cluster safely.** In `auto-patch`, the agent checks a
  [learning period, confidence, step size and cooldown](../concepts/guardrails.md) before each change, applies it in
  place by default, and reverts it when OOM kills, restarts or throttling follow.
- **You need money, not millicores.** Each recommendation carries a monthly saving priced for the node type it runs
  on. [Realized savings](../realized-savings.md) shows what applied changes saved afterwards, separately from the
  estimate.
- **You have several clusters or several teams.** One UI, [sign-in through your identity provider](../auth.md),
  [team-scoped access](../teams.md) and [notifications](../notifications.md).
- **You want to see whether nodes will go away.** [Node savings](../node-savings.md) estimates the node effect and
  lists what blocks consolidation. Goldilocks does not look at nodes in the documentation we read.

## Where they overlap, and a path from one to the other

Both rest on the same idea: take observed usage, add headroom, and set requests to that. If you run Goldilocks today,
keep it while you try Kerf in `audit` mode: Kerf changes nothing until you turn on `auto-patch` for a namespace or
workload. Compare the two sets of numbers on a few workloads. The `kerf report` command ([CLI](../cli.md)) gives you
Kerf's numbers from your existing Prometheus without installing anything in the cluster. When you let Kerf apply
changes, do not let a VPA in `Auto` or `Recreate` mode manage the same workload. Goldilocks creates VPAs in
recommendation mode, so it does not conflict, but check that none of your VPAs apply changes.

## Things we could not verify

- Whether a current Goldilocks release offers any built-in way to apply recommendations. The documentation we read
  does not describe one.
- Which Goldilocks features are in Fairwinds Insights and which are in the open-source project beyond what the
  Goldilocks documentation states about Insights (multi-cluster, tracking over time, Slack, Datadog and Jira
  integrations).

## FAQ

### Does Kerf use Goldilocks or the VPA?

No. Kerf computes its own recommendations and does not create VPA objects.

### Can I run both?

Yes. Goldilocks only reads VPAs in recommendation mode. Kerf in `audit` mode only recommends. Pick one place for
changes to be applied.

### Is Goldilocks enough to reduce my cloud bill?

The recommendations are a start. Requests decide how many nodes you need, so lower requests save money only once the
node autoscaler removes nodes. The [guide on reducing Kubernetes costs](../guides/reduce-kubernetes-costs.md)
explains that step.

## Related

- [Kerf vs Kubernetes VPA](kerf-vs-vpa.md)
- [Kerf vs Kubecost and OpenCost](kerf-vs-kubecost.md)
- [How to rightsize Kubernetes resource requests](../guides/rightsize-kubernetes-resource-requests.md)
- [Comparison with other tools](../comparison.md)
