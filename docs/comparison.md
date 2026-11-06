# Kerf compared with other rightsizing tools

Kerf takes ideas from the tools below. They are good tools: pick the one that fits.

| | License | Your data stays in your infrastructure | Applies changes | In-place resize | Rollback on bad health | Savings in $ | HPA and JVM aware |
|---|---|---|---|---|---|---|---|
| **Kerf** | Apache-2.0 | ✓ | ✓ policies, maintenance windows | ✓ | ✓ | ✓ | ✓ both |
| [Vertical Pod Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler) | Apache-2.0 | ✓ | ✓ evict and webhook | ✓ `InPlaceOrRecreate` | – | – | not with an HPA on the same metric |
| [Goldilocks](https://github.com/FairwindsOps/goldilocks) | Apache-2.0 | ✓ | through VPA, opt-in | through VPA | – | – | – |
| [Robusta KRR](https://github.com/robusta-dev/krr) | MIT | ✓ CLI; the UI is SaaS | optional webhook, with a Robusta account | – | – | – | `--allow-hpa` |
| [IBM Kubecost](https://www.ibm.com/products/kubecost) | commercial, free tier | ✓ self-hosted option | Kubescaler (alpha) | – | – | ✓ | – |
| [StormForge](https://www.cloudbolt.io/stormforge/) | commercial | metrics go to SaaS | ✓ | ✓ | ✓ | ✓ | ✓ both |
| [PerfectScale](https://www.perfectscale.io/) | commercial, free tier | ✓ self-hosted option | ✓ maintenance windows | ✓ | – | ✓ | JVM; HPA advice |
| [CAST AI](https://cast.ai/) | commercial | SaaS console | ✓ | ✓ | – | ✓ | ✓ both |

✓ means documented. – means we did not find it in the tool's public docs on 2026-11-05, which is not proof that it
is missing. Products change: check the source before you rely on a cell. The [sources for every cell](branding/comparison.md)
are listed on their own page, with notes on each tool. Corrections are welcome: open an issue or a pull request.

## Which one fits

- **One small cluster that you watch closely.** A Vertical Pod Autoscaler in recommendation mode, or Goldilocks on top
  of it, is enough. Kerf is more than you need.
- **A quick report from Prometheus.** Robusta KRR gives recommendations from the command line.
- **Automation as a commercial product with vendor support.** StormForge, PerfectScale and CAST AI automate most of
  what Kerf does.
- **That automation, open source, inside your own infrastructure.** This is what Kerf is for: hub, agents and UI
  run in your clusters, and usage data stays in your Prometheus and your PostgreSQL.

## What Kerf adds to a VPA

A Vertical Pod Autoscaler recommends and can apply requests in one cluster. Kerf adds:

- savings in dollars per workload, namespace and cluster, priced per node type;
- one UI for a fleet of clusters;
- a mode per resource: CPU and memory are `off`, `audit` or `auto-patch` separately
  ([modes](concepts/modes.md));
- [maintenance and freeze windows](concepts/apply-strategies.md), and [guardrails](concepts/guardrails.md) that cap
  each decrease;
- [rollback](concepts/rollback.md) when a change is followed by OOM kills, restarts or CPU throttling;
- [HPA coordination](concepts/hpa.md) and [JVM-aware memory](concepts/jvm.md).

## Not a fit (yet)

- Node-level autoscaling or bin packing: use Karpenter or Cluster Autoscaler next to Kerf.
- Teams that want a managed SaaS with vendor support.

## Related

- [FAQ](faq.md)
- [Concepts](concepts/README.md)
- [Architecture](architecture.md)
