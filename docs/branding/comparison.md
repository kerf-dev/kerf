# Kerf and other rightsizing tools: details and sources

The table in the [README](../../README.md#how-kerf-compares) is a summary of this page. Every fact about another tool comes
from that tool's public docs, repository or pricing page, read on **2026-11-05**; the rows "Alerts in chat", "SSO" and "Realized savings" were added and checked on **2026-11-06**. "Not stated" means we did not
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
| **Alerts in chat** | yes: Slack, Teams, signed webhook | not stated | not stated (Slack only in Fairwinds Insights, a separate product) | Slack (through a Robusta install), Teams (report export) | Slack, Teams, email | webhook after each apply; Slack incoming webhook as an example | Slack, Teams | Slack, Teams, webhook |
| **SSO** | OIDC and SAML 2.0 | not stated | not stated | not stated | SAML (Enterprise plans) | not stated | SAML and OpenID (Expert plan) | OIDC |
| **Realized savings** | yes: from applied changes, apart from the estimate | not stated | not stated | not stated | not stated | not stated | not stated | yes: projected minus actual cost, from a baseline |

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
- **Alerts in chat.** KRR sends Slack messages through a Robusta playbook (a weekly scan to a Slack sink) and Teams cards
  when it exports a report to Azure Blob Storage. StormForge's applier posts an HTTP request after each successful
  apply; its docs show a Slack incoming webhook, ServiceNow and Amazon SNS as destinations, and a vendor blog post
  (2026-05-18) walks through the Slack case. Neither is a rules engine like Kerf's, which filters events by type,
  cluster and namespace; CAST AI filters by category, severity and cluster.
- **SSO.** Kerf signs in through OpenID Connect or SAML 2.0, with the same group-to-role mappings. Kubecost's docs
  describe SAML only. PerfectScale's SSO is a paid-plan feature and lists SAML and OpenID providers. CAST AI's docs
  describe OpenID Connect; its docs say multi-tenant SSO and group sync are Enterprise features. For StormForge we
  found SSO only in a [2022 press release](https://stormforge.io/press-releases/stormforge-optimize-live/), not in the docs.
  VPA, Goldilocks and KRR have no sign-in of their own in their docs, so "not stated" there is expected.
- **Realized savings.** The word means different things. Kerf adds up the requests its applied changes released, per
  hour, priced at that hour's rates, and keeps the sum apart from the estimate; it never adds the two. CAST AI's report
  compares a projected cost with the actual cost against a baseline, which is mainly the effect of its node
  autoscaler, and it counts workload-autoscaler savings from the reduction in requests while that autoscaler is active.
  We did not find a comparable figure in the other tools' docs. Third-party reviews that mention one were not counted.
- The HPA, JVM and in-place claims for the commercial tools come from their documentation, not from our testing.

## Kerf's own claims

Each Kerf cell is backed by this repository: chat alerts in [docs/notifications.md](../notifications.md), sign-in in
[docs/auth.md](../auth.md#sign-in-with-openid-connect), realized savings in
[docs/realized-savings.md](../realized-savings.md), modes and strategies in `api/v1alpha1/common_types.go`, guardrails and
rollback thresholds in `api/v1alpha1/rightsizingpolicy_types.go`, HPA coordination and
pricing in [docs/architecture.md](../architecture.md#design-choices), metrics sources in `docs/contracts/readiness.md`.

## Sources

1. VPA: [features](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/features.md),
   [quick start](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/quickstart.md),
   [known limitations](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/known-limitations.md),
   [releases](https://github.com/kubernetes/autoscaler/releases),
   [Kubernetes 1.35: in-place pod resize GA](https://kubernetes.io/blog/2025/12/19/kubernetes-v1-35-in-place-pod-resize-ga/)
   (the three new rows: the features page lists no notifications, sign-in or savings tracking, 2026-11-06)
2. Goldilocks: [repository](https://github.com/FairwindsOps/goldilocks), [docs](https://goldilocks.docs.fairwinds.com/),
   [advanced usage](https://goldilocks.docs.fairwinds.com/advanced/) (the three new rows: Slack appears only for Fairwinds
   Insights, a separate platform; 2026-11-06)
3. Robusta KRR: [repository](https://github.com/robusta-dev/krr), [enforcer](https://github.com/robusta-dev/krr/tree/main/enforcer); alerts: the README sections "Slack Notification" and
   "Azure Blob Storage Export with Microsoft Teams Notifications" (2026-11-06)
4. IBM Kubecost: [pricing](https://www.ibm.com/products/kubecost/pricing),
   [Kubescaler](https://www.ibm.com/docs/en/SSW0JQG_2.x/install-and-configure/advanced-configuration/controller/kubescaler.html),
   [OpenCost](https://www.opencost.io/),
   [acquisition](https://newsroom.ibm.com/blog-ibm-acquires-kubecost-to-broaden-hybrid-cloud-cost-management-capabilities);
   alerts: [Slack, Teams and email](https://www.ibm.com/docs/en/SSW0JQG_2.x/using-kubecost/navigating-the-kubecost-ui/alerts.html);
   SSO: [User management (SAML)](https://docs.kubecost.com/install-and-configure/advanced-configuration/user-management-saml)
   ("only officially supported on Kubecost Enterprise plans"); all read 2026-11-06
5. StormForge: [architecture](https://docs.stormforge.io/docs/concepts/architecture/),
   [applier events](https://docs.stormforge.io/docs/reference/applier-events/),
   [applier 2.12.0](https://docs.stormforge.io/docs/releases/stormforge/v2.12.0-applier/),
   [schedule](https://docs.stormforge.io/docs/configure/settings/schedule/),
   [product page](https://www.cloudbolt.io/stormforge/kubernetes-rightsizing/); alerts:
   [applier alerts](https://docs.stormforge.io/docs/guides/applier-alerts/), and the vendor blog post
   [Slack notifications when StormForge applies recommendations](https://www.cloudbolt.io/blog/how-to-get-slack-notifications-when-stormforge-applies-recommendations/)
   (2026-05-18); SSO and realized savings: nothing found in the docs index or the recommendations page; all read 2026-11-06
6. PerfectScale: [in-place rightsizing](https://help.doit.com/docs/perfectscale-for-kubernetes/self-hosted/enable-automation/in-place-workload-right-sizing),
   [automation CRD](https://docs.perfectscale.io/enable-automation/configuring-automation-with-crd/automation-customization-with-crd.md),
   [Java containers](https://docs.perfectscale.io/visibility-and-optimization/java-containers),
   [HPA](https://docs.perfectscale.io/visibility-and-optimization/combining-vertical-podfit-and-horizontal-hpa-optimization.md),
   [pricing](https://c.perfectscale.io/pricing); alerts: [Slack](https://docs.perfectscale.io/customize-workflow/communication-and-messaging/slack-integration)
   and the Teams integration in the [workflow docs](https://docs.perfectscale.io/customize-workflow);
   SSO: [SSO setup](https://docs.perfectscale.io/administration/security/sso) ("only with the EXPERT package"); realized savings:
   nothing found in the docs; all read 2026-11-06
7. CAST AI: [workload autoscaling](https://docs.cast.ai/docs/workload-autoscaling-overview),
   [in-place resizing](https://docs.cast.ai/docs/woop-in-place-resizing),
   [JVM](https://docs.cast.ai/docs/woop-jvm-optimization),
   [savings report](https://docs.cast.ai/docs/savings-report),
   [pricing](https://cast.ai/pricing/); alerts: [Slack](https://docs.cast.ai/docs/observability-tutorial-set-up-slack-notifications),
   [Teams and webhooks](https://docs.cast.ai/docs/examples); SSO: [OpenID Connect](https://docs.cast.ai/docs/sso);
   realized savings: the same [savings report](https://docs.cast.ai/docs/savings-report); all read 2026-11-06
