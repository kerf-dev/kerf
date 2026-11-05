# Kerf and other rightsizing tools: details and sources

The table in the [README](../../README.md#how-kerf-compares) is a summary of this page. Every fact about another tool comes
from that tool's public docs, repository or pricing page, read on **2026-11-05**. "Not stated" means we did not
find it in their docs. It does not mean the tool lacks it. Products change: check the source before you rely on a
cell, and open an issue or a pull request when something here is wrong or out of date.

## Summary

| | Kerf | VPA | Goldilocks | Robusta KRR | IBM Kubecost | StormForge | PerfectScale | CAST AI |
|---|---|---|---|---|---|---|---|---|
| **License** | Apache-2.0 | Apache-2.0 | Apache-2.0 | MIT (CLI) | commercial; free tier up to 250 cores | commercial; 30-day trial | commercial; free tier (300 vCPU) | commercial |
| **Where it runs** | your clusters only | your cluster | your cluster | CLI on your machine; optional SaaS UI | self-hosted, or managed backend | agent in-cluster, metrics sent to SaaS | SaaS, or self-hosted | in-cluster agent, SaaS console |
| **Recommends** | requests and limits | requests (limits keep their ratio) | requests and limits (via VPA) | requests and limits | requests | requests and limits | requests and limits | requests |
| **Data source** | Prometheus, VictoriaMetrics, Thanos, Mimir, AMP | Metrics API, history in checkpoints or Prometheus | VPA | Prometheus, Thanos, VictoriaMetrics, Mimir, managed Prometheus | Prometheus (2.x); own agent (3.0) | own forwarder | own exporter | metrics-server and own exporter |
| **Applies changes** | agent, per policy: in-place, rollout, deferred | eviction and admission webhook | opt-in, through VPA ("not recommended") | optional enforcer webhook, needs a Robusta UI account | Kubescaler (alpha) | applier, or patches for CI/CD | eviction and webhook; GitOps notes | immediate or deferred |
| **In-place pod resize** | yes (default strategy) | `InPlaceOrRecreate` (GA in VPA 1.6.0) | through VPA | not stated | not stated | yes (applier 2.12.0) | yes (default since agent 1.0.26) | yes |
| **Next to an HPA on CPU/memory** | yes: requests and HPA target change together | docs warn against it on the same metric | not stated | `--allow-hpa` flag | not stated | yes (vendor docs) | HPA recommendations; docs advise against two vertical tools | yes |
| **Fleet UI** | yes | no UI | per cluster; multi-cluster in Fairwinds Insights | through the Robusta UI | Enterprise | yes | yes | yes |
| **Savings in dollars** | yes, per node type | not stated | not stated | not stated | yes | yes | yes | yes |
| **Rollback on bad health** | yes: OOM kills, restarts, CPU throttling | not stated | not stated | not stated | not stated | yes | not stated | not stated |
| **Maintenance windows** | yes, plus freeze windows | not stated | not stated | not stated | schedule settings | yes | yes | not stated |
| **JVM heap aware** | yes | not stated | not stated | not stated | not stated | yes | yes | yes |

## Notes

- **VPA.** In-place pod resize is GA in Kubernetes 1.35. VPA's `InPlaceOrRecreate` mode was alpha in 1.4.0, beta in
  1.5.0 and GA in 1.6.0; an `InPlace` mode without eviction is alpha in 1.7.0. `Auto` is deprecated and behaves
  like `Recreate`.
- **Goldilocks** creates VPA objects in `Off` mode and shows their recommendations; it inherits VPA's behaviour.
- **KRR.** The README's own comparison lists autoscaling as future work, while the `enforcer/` directory documents
  a mutating webhook. We describe the enforcer.
- **Kubecost** was acquired by IBM (announced 2024-09-17). Kubecost created OpenCost (CNCF, Apache-2.0). Kubecost 3.0
  no longer needs Prometheus.
- **StormForge** was acquired by CloudBolt (announced 2025-03-31); the docs stay at docs.stormforge.io.
- **PerfectScale** was acquired by DoiT (announced 2025-02-04). Its pricing page and a cached copy differ on the free
  tier (300 or 200 vCPU).
- **CAST AI.** The JVM feature's status (early access or GA) isn't stated on the docs page.
- The HPA, JVM and in-place claims for the commercial tools come from their documentation, not from our testing.

## Kerf's own claims

Each Kerf cell is backed by this repository: modes and strategies in `api/v1alpha1/common_types.go`, guardrails and
rollback thresholds in `api/v1alpha1/rightsizingpolicy_types.go`, HPA coordination in `docs/handover/WO-11-handover.md`,
pricing in `DECISIONS.md` (D-008), metrics sources in `docs/contracts/readiness.md`.

## Sources

1. VPA: [features](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/features.md),
   [quick start](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/quickstart.md),
   [known limitations](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/known-limitations.md),
   [releases](https://github.com/kubernetes/autoscaler/releases),
   [Kubernetes 1.35: in-place pod resize GA](https://kubernetes.io/blog/2025/12/19/kubernetes-v1-35-in-place-pod-resize-ga/)
2. Goldilocks: [repository](https://github.com/FairwindsOps/goldilocks), [docs](https://goldilocks.docs.fairwinds.com/),
   [advanced usage](https://goldilocks.docs.fairwinds.com/advanced/)
3. Robusta KRR: [repository](https://github.com/robusta-dev/krr), [enforcer](https://github.com/robusta-dev/krr/tree/main/enforcer)
4. IBM Kubecost: [pricing](https://www.ibm.com/products/kubecost/pricing),
   [Kubescaler](https://www.ibm.com/docs/en/SSW0JQG_2.x/install-and-configure/advanced-configuration/controller/kubescaler.html),
   [OpenCost](https://www.opencost.io/),
   [acquisition](https://newsroom.ibm.com/blog-ibm-acquires-kubecost-to-broaden-hybrid-cloud-cost-management-capabilities)
5. StormForge: [architecture](https://docs.stormforge.io/docs/concepts/architecture/),
   [applier events](https://docs.stormforge.io/docs/reference/applier-events/),
   [applier 2.12.0](https://docs.stormforge.io/docs/releases/stormforge/v2.12.0-applier/),
   [schedule](https://docs.stormforge.io/docs/configure/settings/schedule/),
   [product page](https://www.cloudbolt.io/stormforge/kubernetes-rightsizing/)
6. PerfectScale: [in-place rightsizing](https://help.doit.com/docs/perfectscale-for-kubernetes/self-hosted/enable-automation/in-place-workload-right-sizing),
   [automation CRD](https://docs.perfectscale.io/enable-automation/configuring-automation-with-crd/automation-customization-with-crd.md),
   [Java containers](https://docs.perfectscale.io/visibility-and-optimization/java-containers),
   [HPA](https://docs.perfectscale.io/visibility-and-optimization/combining-vertical-podfit-and-horizontal-hpa-optimization.md),
   [pricing](https://c.perfectscale.io/pricing)
7. CAST AI: [workload autoscaling](https://docs.cast.ai/docs/workload-autoscaling-overview),
   [in-place resizing](https://docs.cast.ai/docs/woop-in-place-resizing),
   [JVM](https://docs.cast.ai/docs/woop-jvm-optimization),
   [savings report](https://docs.cast.ai/docs/organizational-savings-report),
   [pricing](https://cast.ai/pricing/)
