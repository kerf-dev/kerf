---
title: "Kerf vs Kubecost and OpenCost - rightsizing and cost allocation"
description: "Kubecost and OpenCost allocate Kubernetes spend to teams and workloads. Kerf recommends and applies right-sized requests with guardrails and rollback. How they differ, and how they work together."
---

# Kerf vs Kubecost and OpenCost

Kubecost, OpenCost and Kerf all put dollars on Kubernetes, but they start from different questions. Kubecost and
OpenCost answer "where does the money go?" Kerf answers "which CPU and memory requests are too high, and can we lower
them safely?" Many teams need both. This page explains where each fits. Statements about the other tools come from
their public pages and are listed with their limits below; statements about Kerf come from this documentation and the
repository code.

## What each project is

**OpenCost** describes itself as a vendor-neutral open-source project for measuring and allocating cloud
infrastructure and container costs, aimed at showback and chargeback ([documentation](https://www.opencost.io/docs/)).
Its pages we read describe monitoring, allocation and a UI for cost views. They do not describe recommending or
changing resource requests. Our [comparison sources](../branding/comparison.md) record that Kubecost created OpenCost
and that it is a CNCF project under Apache-2.0; we did not re-confirm the license on the documentation page we read
for this article.

**Kubecost** is now an [IBM product](https://www.ibm.com/products/kubecost). Its pricing page lists a free
"Foundations" tier (unlimited clusters up to 250 cores, 15-day metric retention, community support) and Enterprise
plans with a unified multi-cluster view, unlimited retention, role-based access control and a bring-your-own identity
provider option. The page also lists cost optimization insights and automation. We did not find on that page which
tier includes request sizing. Our earlier research found Kubescaler, Kubecost's automated request sizing, documented
as alpha ([comparison notes](../branding/comparison.md)).

**Kerf** is open source under Apache-2.0 with no paid tier and no hosted service. It reads usage from a
Prometheus-compatible store, computes request recommendations, prices them and applies them under
[policies](../concepts/policies.md).

## Side by side

| | OpenCost | Kubecost | Kerf |
|---|---|---|---|
| Main job | Measure and allocate cost | Cost visibility, allocation and optimization insights | Rightsize requests and limits, price the savings |
| Allocation of the full cloud bill to teams | Yes (its stated purpose) | Yes | No. [Teams](../teams.md) group only Kerf's own savings |
| Request recommendations | Not described on the pages we read | Yes, tier details not verified | Yes, per container, with confidence and risk flags |
| Applies changes | Not described | Kubescaler (alpha, per our earlier notes) | Agent, per policy; [in-place resize](../concepts/apply-strategies.md) by default |
| Rollback on bad health | Not described | Not stated in the pages we read | [Yes](../concepts/rollback.md) |
| Cost data source | Prometheus and cloud billing integrations | Not re-verified for this page | Your metrics store plus [pricing sources](../pricing.md): AWS Cost and Usage Report, list, spot or a static file |
| Realized savings | Not described | Not stated | [Yes, from applied changes](../realized-savings.md) |
| License | Open source | Commercial with a free tier | Apache-2.0 |

## The overlap

Both kinds of tool turn "we use X CPU and request Y" into money, and both can point at waste. In Kubecost the waste
usually appears as an efficiency or savings view; in Kerf it appears as a list of workloads ranked by estimated monthly
saving, each with current, recommended and used values.

## Where Kubecost or OpenCost is the better fit

- **Showback and chargeback.** If finance wants the whole bill split by team, namespace, label and cloud service,
  including storage, network and load balancers, use OpenCost or Kubecost. Kerf prices only the CPU and memory side
  of requests and does not allocate the bill.
- **Cloud bill reconciliation.** Kubecost's pricing page lists reconciliation with your cloud provider bill. Kerf's
  [pricing](../pricing.md) can use effective rates from an AWS Cost and Usage Report, but it is not a billing tool.
- **A vendor to call.** Kubecost has Enterprise support and professional services. Kerf is a community project.
- **Non-AWS price data.** Kerf's price list integration is for AWS; on other clusters it uses a static price table
  ([install on any cluster](../install/any-cluster.md)). If you need provider price lists beyond that, check what
  the other tools support for your cloud.

## Where Kerf is the better fit

- **Acting on the recommendation with safety rails.** Guardrails limit each step, maintenance and freeze windows
  control when, and the [health watch](../concepts/rollback.md) reverts a change followed by OOM kills, restarts or
  CPU throttling.
- **Workload-specific handling.** [HPA coordination](../concepts/hpa.md) changes requests and the HPA target
  together. [JVM-aware memory](../concepts/jvm.md) keeps the heap safe.
- **Evidence after the change.** Realized savings is computed from the requests Kerf's applied changes released and is
  kept apart from the estimate.
- **A self-hosted open-source tool end to end.** No usage data leaves your infrastructure. The only outbound calls
  are the AWS pricing APIs if you choose AWS prices, and a daily check for a new Kerf release you can turn off
  ([FAQ](../faq.md#does-kerf-send-data-anywhere)).

## Using them together

The two do not compete for the same job. A common arrangement: OpenCost or Kubecost for allocation and reporting to
finance, Kerf for the engineering work of lowering requests. Kerf's savings estimate is deliberately narrow: it is
the difference between current and recommended requests, priced per node type, and
[the node estimate](../node-savings.md) shows whether the node autoscaler can turn that into fewer nodes. Where
your cost tool reports waste, you can read the same workload in Kerf and see the recommended values.

## Things we could not verify

- Which Kubecost tier includes request sizing and its automation, and its current status.
- The OpenCost license and CNCF status from the documentation page we read for this article; our earlier research
  recorded them.
- Whether OpenCost or Kubecost reverts a change on bad health. The pages we read do not describe it.

## FAQ

### Does Kerf replace Kubecost?

No, not for allocating the bill. Kerf does not split cloud costs between teams.

### Does Kerf need OpenCost or Kubecost installed?

No. Kerf has its own pricing service and recommender.

### Can lower requests reduce my bill without Kerf?

Yes. Any process that lowers requests and lets the node autoscaler remove nodes does. See
[Reduce Kubernetes costs with rightsizing](../guides/reduce-kubernetes-costs.md).

## Related

- [Kerf vs Kubernetes VPA](kerf-vs-vpa.md)
- [Kerf vs Goldilocks](kerf-vs-goldilocks.md)
- [Comparison with other tools](../comparison.md)
- [Pricing](../pricing.md)
