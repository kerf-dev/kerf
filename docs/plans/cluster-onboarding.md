# Plan: Cluster onboarding, metrics sources and readiness checks

Status: **proposed → building** (extends `docs/plans/admin-area.md`, D-022..D-024). New decision D-025.

## 1. Today

- **One metrics source:** the hub and pricing service read one hard-coded Prometheus URL (`prometheusURL`, `--vm-url`). They expect every series to carry the label `cluster="<name>"`.
- **Adding a cluster:** a hub config/seed entry, a token in a Secret, an agent Helm install, and a vmagent remote-writing to the hub's VictoriaMetrics.
- **No feedback:** nothing tells you whether a cluster is set up correctly. Missing KSM label allowlists, missing RBAC, a missing webhook or short retention only show up later as empty charts, `low-confidence` or `default` pricing.

## 2. Model

### Datasource (hub-managed, admin page "Metrics sources")
A Prometheus-compatible query endpoint that one or more clusters' metrics come from.

| Field | Notes |
|---|---|
| name, type | `prometheus` · `victoriametrics` · `thanos` · `mimir` · `amp` (Amazon Managed Prometheus) |
| url | query base URL |
| auth | none · bearer token · basic · SigV4 (AMP, via IRSA/Pod Identity on EKS) · mTLS; secrets stored encrypted, write-only in the API |
| headers | e.g. `X-Scope-OrgID` for Mimir/Cortex tenants |
| timeout, max points | per-query limits; the recommender chunks to fit |

The built-in hub VictoriaMetrics is datasource `local` (the k3d default).

### Cluster metrics binding (per cluster)
| Field | Default | Why |
|---|---|---|
| datasource | `local` | where this cluster's metrics live |
| cluster label | `cluster` | orgs use `k8s_cluster`, `cluster_name`, `eks_cluster`… |
| cluster label value | the cluster name | the registered name may differ from the metrics label value |
| extra matchers | – | e.g. `env="prod"`, or a per-cluster Prometheus without a cluster label (then empty label = whole datasource) |
| job names | `kubelet`/`cadvisor`, `kube-state-metrics` | only used to give a precise diagnosis when series are missing |

Every PromQL query is built from this binding instead of the hard-coded `cluster=` matcher. Results are re-labelled to the registered cluster name, so everything downstream (DB, UI, dashboards) stays unchanged.

**Pricing:** the pricing service lists the datasources and bindings from the hub using a service token. Node discovery (`kube_node_labels`) runs per binding. Pricing still exports its rate series to the hub's own metrics store, where the recommender reads them.

## 3. Onboarding wizard (admin → Clusters → "Add cluster")

1. **Identify:**
   - fields: name, environment (prod / non-prod), owner team, provider/region (informational)
   - policy source: hub / git / both (D-022)
2. **Metrics:** pick or create a datasource, then set the cluster label key and value and any extra matchers. **Test** runs the metrics checks below right away against the datasource, before any agent exists, and shows what it found. For example: "found 1,284 cAdvisor container series and kube-state-metrics for 212 pods; node labels lack `instance_type`".
3. **Install the agent:**
   - The hub issues the agent token, shown once.
   - It renders install instructions in three flavours, all with the token pre-filled as a Secret reference and the hub URL: a Helm command, a values file, and an Argo CD `Application` manifest.
   - It also generates the Argo CD `ignoreDifferences` snippet (D-006) for the user's `argocd-cm`.
   - Optional: webhook install instructions (cert-manager or self-signed).
4. **Verify:** a live checklist. The agent's first status batch fills in the cluster-side checks. The cluster moves through states:

```
Registered → Agent connected → Metrics OK → Ready (audit) → Automation ready
            (first pull)      (metrics checks   (recommendations   (all write
                               pass)             produced)          capabilities OK)
```

Only **Automation ready** clusters can have `auto-patch` set (the UI blocks it and so does the hub API, unless an admin overrides with a reason). Until then, every workload stays in audit. This also covers clusters installed with `readOnly: true`.

## 4. Readiness checks

Each check has: id, category, severity (`blocking` · `warning` · `info`), status (`pass` · `warn` · `fail` · `unknown`), message, a **remediation** (exact command or values snippet), a docs link, and `checkedAt`.
- Metrics checks run on the hub at registration, on demand (**Re-check**) and every hour.
- Agent checks run at agent start, every 10 minutes and on demand (the hub sets a "recheck" flag in the config pull). Their results arrive in the status batch.

### Metrics (hub, via the cluster's datasource)
| Check | Severity | Detects |
|---|---|---|
| datasource reachable + auth | blocking | URL, TLS, credentials, tenant header |
| cluster label matches | blocking | wrong label key/value: zero series for the matcher (suggests the label values that do exist) |
| cAdvisor CPU + memory working set | blocking | kubelet/cAdvisor not scraped; `container` label missing |
| kube-state-metrics owners (`kube_pod_owner`, `kube_replicaset_owner`, `kube_job_owner`) | blocking | KSM missing → workloads cannot be resolved |
| KSM requests/limits (`kube_pod_container_resource_requests/limits`) | blocking | no "current" values → no savings |
| pod ↔ owner join coverage ≥ 95 % | warning | label mismatches between cAdvisor and KSM (`namespace`/`pod` relabeling) |
| scrape resolution ≤ 60 s | warning | coarse data → P95/P99 are unreliable |
| history ≥ profile window (7d / 14d / 28d) | warning (info while onboarding) | short retention → stays `low-confidence`; shows when the cluster will have enough data |
| throttling series (`container_cpu_cfs_*`) | warning | throttled guardrail is blind |
| restart/OOM series (KSM restarts, last terminated reason, `container_oom_events_total`) | warning | OOM/restart guardrails and rollbacks are blind |
| HPA series (`kube_horizontalpodautoscaler_*`) | warning | HPA coupling cannot be detected (D-011/WO-11) |
| node labels allowlisted (`instance-type`, `capacity-type`, `zone`, `region` in `kube_node_labels`) | warning | KSM `--metric-labels-allowlist` missing → pricing falls back to default rates |
| node allocatable (`kube_node_status_allocatable`) | warning | cost split per vCPU/GiB impossible |
| data freshness (last sample < 5 min) | blocking | scraping or remote-write stopped |
| pricing coverage (priced nodes / all nodes) | warning | from `ro_pricing_nodes{status="skipped",reason}` |

### Cluster (agent self-check, reported in status)
| Check | Severity | How |
|---|---|---|
| agent ↔ hub version compatibility | blocking | version handshake on pull |
| CRDs installed and current | blocking | discovery + stored version vs agent's expected |
| RBAC per capability | blocking for automation | `SelfSubjectAccessReview` for every verb the agent needs, grouped as capabilities: **read** (list/watch pods, workloads, HPAs, nodes), **resize** (`pods/resize` patch), **rollout** (patch deployments/statefulsets/daemonsets), **hpa** (patch HPAs), **evict** (`pods/eviction` create), **policies** (write `rightsizingpolicies`, D-022), **events** (create events). Each missing verb is listed with the ClusterRole rule to add. |
| in-place resize available | warning | server version ≥ 1.33 and `pods/resize` subresource in discovery; otherwise `in-place` falls back to `rollout` |
| webhook installed and healthy | warning | `MutatingWebhookConfiguration` present, `caBundle` set, service has ready endpoints, a dry-run pod create is mutated |
| cert-manager present (if the webhook uses it) | info | |
| nodes with swap / static CPU or memory manager | info | share of nodes where in-place memory is disabled (D-007) |
| Argo CD integration | warning / unknown | if Argo CD is installed and `argocd-cm` is readable: `ignoreDifferences.managedFieldsManagers` contains `resource-optimizer`. Otherwise `unknown`, with the snippet to apply |
| namespace coverage | info | namespaces excluded by policy or label |
| clock skew vs hub | warning | > 30 s breaks windows and cooldowns |

## 5. Where it shows up

- **Clusters list:** a readiness badge per cluster (✓ Automation ready · ● Ready (audit) · ▲ N warnings · ✕ N blocking), the state, last check time, and a "Fix" link.
- **Cluster detail → Readiness tab:** the checklist grouped by category. Each failed check is expanded with its remediation (copy button) and a docs link. There is a **Re-check** button. A history of check results shows when something broke.
- **Overview banner:** shown when any cluster has a blocking failure. For example: "ro-spoke-2: kube-state-metrics not found — recommendations are stale".
- **Workload rows and detail:** when a workload has no recommendation because of a cluster problem, the reason links to the failing check, instead of a bare "no data".
- **Grafana / metrics:** `ro_cluster_readiness{cluster,check,severity}`, with values 0/1, so existing alerting can page on it.
- **Audit:** state transitions (e.g. *Automation ready → Ready (audit)* because the `resize` RBAC was lost) are recorded as events.

## 6. Contract and work split

- **WO-19 (contract, running)** gains:
  - datasources CRUD + `POST /v1/admin/datasources/{id}/test`
  - the cluster metrics binding
  - `GET /v1/clusters/{c}/readiness` + `POST .../readiness/recheck`
  - readiness check schema and ids
  - agent status batch `capabilities[]` / `checks[]`
  - install-snippet endpoint `GET /v1/admin/clusters/{c}/install` (helm | values | argocd)
  - migration **0010** (datasources, bindings, readiness results)
- **WO-25 (new): onboarding and readiness.** It is part hub, part agent and part pricing, and builds on the WO-20 and WO-21 branches:
  - hub: datasource registry, binding-aware query builder, metrics checks, readiness state machine, install renderer, auto-patch gating
  - agent: self-checks
  - pricing: datasource-aware discovery
- **WO-22 (web)** gains the wizard, the readiness UI and the banners.
- **WO-24 (e2e)** gains:
  - register a new cluster and see the metrics checks
  - break KSM (scale it to 0) → blocking check + banner
  - remove a ClusterRole rule → *Ready (audit)* and auto-patch is refused

## D-025 (proposed with this plan)
Clusters are bound to a datasource with a configurable cluster label. Readiness is computed from hub-side metrics checks and agent self-checks. `auto-patch` requires the state *Automation ready*; an admin can override it with a reason, and the override is audited.
