# Kerf compared with other rightsizing tools

Kerf takes ideas from the tools below. They are good tools: pick the one that fits.

| | License | Your data stays in your infrastructure | Applies changes | In-place resize | Rollback on bad health | Savings in $ | HPA and JVM aware | Alerts in chat | SSO | Realized savings |
|---|---|---|---|---|---|---|---|---|---|---|
| **Kerf** | Apache-2.0 | ✓ | ✓ policies, maintenance windows | ✓ | ✓ | ✓ | ✓ both | ✓ [Slack, Teams, webhook](notifications.md) | ✓ [OIDC](auth.md#sign-in-with-openid-connect) | ✓ [from applied changes](realized-savings.md) |
| [Vertical Pod Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler) | Apache-2.0 | ✓ | ✓ evict and webhook | ✓ `InPlaceOrRecreate` | – | – | not with an HPA on the same metric | – | – | – |
| [Goldilocks](https://github.com/FairwindsOps/goldilocks) | Apache-2.0 | ✓ | through VPA, opt-in | through VPA | – | – | – | – | – | – |
| [Robusta KRR](https://github.com/robusta-dev/krr) | MIT | ✓ CLI; the UI is SaaS | optional webhook, with a Robusta account | – | – | – | `--allow-hpa` | ✓ Slack (through Robusta), Teams | – | – |
| [IBM Kubecost](https://www.ibm.com/products/kubecost) | commercial, free tier | ✓ self-hosted option | Kubescaler (alpha) | – | – | ✓ | – | ✓ Slack, Teams, email | ✓ SAML, Enterprise | – |
| [StormForge](https://www.cloudbolt.io/stormforge/) | commercial | metrics go to SaaS | ✓ | ✓ | ✓ | ✓ | ✓ both | ✓ webhook, for example Slack | – | – |
| [PerfectScale](https://www.perfectscale.io/) | commercial, free tier | ✓ self-hosted option | ✓ maintenance windows | ✓ | – | ✓ | JVM; HPA advice | ✓ Slack, Teams | ✓ SAML, OIDC, Expert plan | – |
| [CAST AI](https://cast.ai/) | commercial | SaaS console | ✓ | ✓ | – | ✓ | ✓ both | ✓ Slack, Teams, webhook | ✓ OIDC | ✓ savings report |

✓ means documented. – means we did not find it in the tool's public docs on 2026-11-05 (2026-11-06 for the last three
columns), which is not proof that it is missing. "Alerts in chat" means messages to Slack, Teams or a webhook; "SSO" is
sign-in through your identity provider (SAML or OIDC); "Realized savings" is a figure measured after a change was
applied, kept apart from the estimate. Tools define it differently: see the [notes](branding/comparison.md#notes). Products change: check the source before you rely on a cell. The [sources for every cell](branding/comparison.md)
are listed on their own page, with notes on each tool. Corrections are welcome: open an issue or a pull request.

## Which one fits

- **One small cluster that you watch closely.** A Vertical Pod Autoscaler in recommendation mode, or Goldilocks on top
  of it, is enough. Kerf is more than you need.
- **A quick report from Prometheus.** Robusta KRR gives recommendations from the command line. So does
  [`kerf report`](cli.md), which needs no hub and prices the result per workload.
- **Automation as a commercial product with vendor support.** StormForge, PerfectScale and CAST AI automate most of
  what Kerf does.
- **That automation, open source, inside your own infrastructure.** This is what Kerf is for: hub, agents and UI
  run in your clusters, and usage data stays in your Prometheus and your PostgreSQL.
- **Allocating the whole cloud bill to teams.** Kubecost and OpenCost do that. Kerf groups only its own estimated and
  realized savings by [team](teams.md).
- **Try it first.** The [quickstart](quickstart.md) runs Kerf on a local kind or k3d cluster with demo workloads, for
  evaluation only.

## What Kerf adds to a VPA

A Vertical Pod Autoscaler recommends and can apply requests in one cluster. Kerf adds:

- savings in dollars per workload, namespace and cluster, priced per node type, with
  [effective rates from your AWS Cost and Usage Report](pricing.md#effective-rates-from-the-aws-cost-and-usage-report)
  when you turn it on;
- [realized savings](realized-savings.md): what the changes Kerf applied have saved since they were applied, kept apart
  from the estimate;
- one UI for a fleet of clusters, with [sign-in through your identity provider](auth.md#sign-in-with-openid-connect)
  (OIDC) and [teams](teams.md) that see and act on their own workloads and savings;
- [messages in Slack, Teams or a signed webhook](notifications.md) for rollbacks, blocked changes and a weekly
  digest;
- a [node estimate and the blockers that keep nodes from being removed](node-savings.md), and
  [visibility of idle and under-used GPUs](gpu.md);
- a mode per resource: CPU and memory are `off`, `audit` or `auto-patch` separately
  ([modes](concepts/modes.md));
- [maintenance and freeze windows](concepts/apply-strategies.md), and [guardrails](concepts/guardrails.md) that cap
  each decrease;
- [rollback](concepts/rollback.md) when a change is followed by OOM kills, restarts or CPU throttling;
- [HPA coordination](concepts/hpa.md) and [JVM-aware memory](concepts/jvm.md).

## Not a fit (yet)

- Node-level autoscaling or bin packing: use Karpenter or Cluster Autoscaler next to Kerf. Kerf [estimates the node
  effect](node-savings.md) and shows what blocks consolidation, but it never creates, drains or removes nodes.
- Changing GPU requests or partitions: Kerf's [GPU view](gpu.md) is visibility only.
- Splitting the cloud bill between teams (showback): use OpenCost or Kubecost.
- Teams that want a managed SaaS with vendor support.

## Related

- [FAQ](faq.md)
- [Concepts](concepts/README.md)
- [Architecture](architecture.md)
