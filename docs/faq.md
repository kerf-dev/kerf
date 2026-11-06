# Frequently asked questions

## What is Kubernetes rightsizing?

Setting each container's CPU and memory requests and limits to what it actually uses, plus headroom. Requests decide
how many pods fit on a node, so over-requested pods waste nodes you pay for, and under-requested pods get throttled or
OOM-killed.

## Does Kerf change my workloads on its own?

Only where you allow it. Every workload starts in `audit`: Kerf recommends and prices, and changes nothing. You turn
on `auto-patch` per namespace or workload, and CPU and memory can be set separately, so you can start with CPU. See
[modes](concepts/modes.md). Every change is in the activity log, and a change that makes a workload unhealthy is
rolled back ([rollback](concepts/rollback.md)).

## VPA already does this. Why Kerf?

A Vertical Pod Autoscaler recommends and can apply requests in one cluster. Kerf adds dollars, a fleet UI,
per-resource modes, maintenance windows, rollback on bad health and HPA coordination. A VPA is a fine choice for one
cluster you watch closely. See the [comparison](comparison.md).

## Can I use Kerf with the Horizontal Pod Autoscaler?

Yes. When an HPA scales a workload on CPU or memory utilization, Kerf changes the requests and the HPA's utilization
target in one change, so the HPA keeps scaling at the same usage as before. See [HPA coordination](concepts/hpa.md).

## Does Kerf send data anywhere?

No usage data leaves your infrastructure. Kerf reads metrics from your Prometheus-compatible store and keeps its
state in your PostgreSQL. Two kinds of calls go outside your clusters:

- the AWS Pricing and EC2 APIs, only when you choose AWS prices;
- a daily check of the public GitHub releases of `kerf-dev/kerf` for a newer version. It sends nothing about your
  installation. Turn it off with `hub.updateCheck.enabled: false` ([maintenance](maintenance.md)).

## Does it work outside EKS?

The recommender, agent and webhook use standard Kubernetes APIs. EKS 1.35 is the tested platform. On other clusters,
prices come from a static table instead of the AWS price list. See [install on any cluster](install/any-cluster.md).

## What does it need to run?

One Helm chart. The hub needs PostgreSQL and a Prometheus-compatible metrics endpoint (Prometheus, VictoriaMetrics,
Thanos, Mimir or Amazon Managed Service for Prometheus) that you probably have already. Workload clusters need
cert-manager for the webhook. See [Install on Amazon EKS](install/eks.md).

## Does it work with Argo CD?

Yes. An in-place resize does not touch the pod template, so Argo CD sees no drift. Policies can live in Git as
`RightsizingPolicy` resources. See [GitOps and Argo CD](concepts/gitops.md).

## A recommendation or a change did not happen. Why?

The workload's mode, a guardrail, a maintenance or freeze window, or the cluster's readiness may be holding it back.
Start with [troubleshooting](operate/troubleshooting.md) and the [readiness checks](operate/readiness.md).

## Is it ready for production?

Kerf is new (0.x). Run it in `audit` first, then turn on `auto-patch` for one namespace and watch the activity log.
Until 1.0.0, a minor version may change the chart values or the custom resources; the
[changelog](https://github.com/kerf-dev/kerf/blob/main/CHANGELOG.md) says how to upgrade.

## What does it cost?

Nothing. Kerf is open source under the Apache License 2.0, with no paid tier and no hosted service. You pay for the
compute it runs on.

## Related

- [Comparison with other tools](comparison.md)
- [Concepts](concepts/README.md)
- [Operate](operate/README.md)
