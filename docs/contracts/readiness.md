# Cluster onboarding and readiness contract

Contract for per-cluster metrics sources and readiness. Changing anything here is
a contract change (see [CONTRIBUTING.md](../../CONTRIBUTING.md#before-you-start)).
Schemas: `api/openapi/hub.yaml` (`Datasource*`, `MetricsBinding`, `ClusterReadiness`, `ReadinessCheck`,
`AgentCapability`, `InstallSnippet`). Check catalog: `api/v1alpha1/readiness` (a test keeps the table
below and the catalog in sync). Storage: `db/migrations/0010_onboarding_readiness.sql`.

## 1. Datasources and the cluster metrics binding

A **datasource** is a Prometheus-compatible query endpoint (`prometheus`, `victoriametrics`, `thanos`,
`mimir`, `amp`).

**Auth** is one of `none`, `bearer`, `basic`, `sigv4` (AMP; credentials from IRSA / Pod Identity, so only
region, service and an optional role ARN), or `mtls`. Secret material is **write-only**:
- It is sent in `DatasourceInput.auth` (`token`, `password`, `tls.clientKey`).
- It is stored encrypted by the hub, with a key from a Secret.
- It is never returned. `Datasource.auth` (`DatasourceAuthInfo`) only says `secretConfigured: true`.
- On update, omitting a secret field keeps the stored one. `clearSecrets: true` removes them.

**Headers** (e.g. `X-Scope-OrgID`) are not secret and are returned.

The hub's own VictoriaMetrics is the built-in datasource `local` (`builtIn: true`, created by the hub at
start-up from `prometheusURL`). It cannot be deleted. Without any configuration every cluster is bound to
`local` with label `cluster` = its name, which is today's behaviour.

A cluster's **metrics binding** (`ClusterAdmin.metrics`, set by `POST /v1/admin/clusters` and `PATCH
/v1/admin/clusters/{c}`):

| Field | Default | Meaning |
|---|---|---|
| `datasourceId` | `local` | where the metrics are |
| `clusterLabel` | `cluster` | label key; `""` = no cluster matcher (a per-cluster Prometheus) |
| `clusterLabelValue` | the cluster name | label value |
| `extraMatchers` | `[]` | PromQL label matchers added to every selector, e.g. `env="prod"` |
| `jobNames.cadvisor` / `.kubeStateMetrics` | `[kubelet, cadvisor]` / `[kube-state-metrics]` | only to make diagnoses precise |

Every query the hub builds uses `{<clusterLabel>="<clusterLabelValue>", <extraMatchers>…}`. Results are
re-labelled to the registered cluster name, so nothing downstream changes.

## 2. Readiness states

```
Registered → AgentConnected → MetricsOK → ReadyAudit → AutomationReady
```

The state is the highest one for which every **blocking** check gating it, and every earlier state,
has status `pass` (`readiness.ComputeState`). A blocking check without a result counts as not passing.
`warning` and `info` checks never gate. Transitions are stored in the history and recorded as a
`config_changed` audit entry with action `ReadinessChanged` (actor `system`, target `readiness`).

**Auto-patch gating.** These requests answer **`409`, code `cluster_not_automation_ready`**, when a
target cluster is not `AutomationReady`:
- a policy write (create, update or revert of policies, defaults, overrides) that newly enables `auto-patch` (it did not
  set it before, or it now reaches more clusters), for any target cluster;
- an approve-once for a workload on such a cluster.

`detail` lists the clusters and their states. An **admin** can send `"overrideReadiness": true` (with
the usual `reason`) on `PolicyInput`, `PolicyRevert`, `WorkloadOverrideInput` or `ApprovalRequest`. The override is
recorded in the `config_changed` entry (`after.overrideReadiness`). An operator sending it gets `403
admin_required`. Existing `auto-patch` policies are not changed when a cluster drops out of
`AutomationReady`. The agent itself still refuses writes it has no RBAC for.

## 3. Checks

Every result (`ReadinessCheck`) has:
- `id`, `category`, `severity` (`blocking` · `warning` · `info`), `source` (`hub` · `agent`), `gates`
  (blocking checks only)
- `status` (`pass` · `warn` · `fail` · `unknown`), `message`
- `remediation` (exact command or values snippet; `remediationType` `command` · `yaml` · `text`)
- `docsURL` (`<hub docs base>/readiness#<id>`), `checkedAt`, optional `details` (string map)

When they run:
- **Hub checks** run at registration, on `POST .../readiness/recheck`, every hour, and from
  `POST /v1/admin/datasources/{id}/test`.
- **Agent checks** run at agent start, every 10 minutes, and when the config pull carries
  `flags.recheck` (`recheckRequestedAt` newer than the agent's last run). Their results go in the
  status batch (`checks[]`, `capabilities[]`).

| Check id | Source | Severity | Gates | Detects / remediation |
|---|---|---|---|---|
| `agent.connected` | hub | blocking | AgentConnected | The agent has pulled `/config` at least once and within 3 pull intervals. Remediation: the install snippet. |
| `agent.version` | agent | blocking | AgentConnected | Agent version below `ClusterConfig.minAgentVersion`, or newer than the hub supports. Remediation: helm upgrade command. |
| `agent.crds` | agent | blocking | AgentConnected | CRDs missing or older than the agent expects. Remediation: `kubectl apply --server-side -f` CRD URLs. |
| `agent.clock-skew` | agent | warning | – | Skew > 30 s versus the hub's `Date` header (windows, cooldowns). |
| `datasource.reachable` | hub | blocking | MetricsOK | URL, TLS, credentials, tenant header. `details.httpStatus`, `details.error`. |
| `metrics.cluster-label` | hub | blocking | MetricsOK | Zero series for the binding's matchers; `details.suggestions` lists existing values of the label. |
| `metrics.cadvisor` | hub | blocking | MetricsOK | `container_cpu_usage_seconds_total` / `container_memory_working_set_bytes` missing or without `container`. |
| `metrics.ksm-owners` | hub | blocking | MetricsOK | `kube_pod_owner`, `kube_replicaset_owner`, `kube_job_owner` missing. |
| `metrics.ksm-resources` | hub | blocking | MetricsOK | `kube_pod_container_resource_requests/limits` missing. |
| `metrics.freshness` | hub | blocking | MetricsOK | Last sample older than 5 min. |
| `metrics.owner-join` | hub | warning | – | Pod ↔ owner join coverage < 95 % (relabelling mismatch). |
| `metrics.scrape-interval` | hub | warning | – | Resolution coarser than 60 s. |
| `metrics.history` | hub | warning | – | History shorter than the profile window; `details.enoughDataAt`. Status `warn` while onboarding. |
| `metrics.throttling` | hub | warning | – | `container_cpu_cfs_*` missing. |
| `metrics.restarts-oom` | hub | warning | – | KSM restarts / last terminated reason / `container_oom_events_total` missing. |
| `metrics.hpa` | hub | warning | – | `kube_horizontalpodautoscaler_*` missing. |
| `metrics.node-labels` | hub | warning | – | `kube_node_labels` lacks instance type, capacity type, zone or region (KSM `--metric-labels-allowlist`). |
| `metrics.node-allocatable` | hub | warning | – | `kube_node_status_allocatable` missing. |
| `metrics.dcgm` | hub | warning | – | Only for a cluster with GPU nodes (`kube_node_status_allocatable` of `nvidia_com_gpu`) or pods that request GPUs: the NVIDIA DCGM exporter series `DCGM_FI_DEV_GPU_UTIL`, `DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_FB_FREE` are missing (`details.missing` names them), or none carries the namespace and pod of a pod that requests GPUs (the exporter's Kubernetes pod mapping). `pass` when the cluster has no GPUs. See [GPU visibility](../gpu.md). |
| `pricing.coverage` | hub | warning | – | Priced nodes / all nodes below 100 % (`kerf_pricing_nodes{status="skipped"}`), or the AWS Cost and Usage Report reader's last read failed (`kerf_pricing_cur_last_read_ok` 0). `details`: `priced`, `nodes`, `source.<cur\|list\|spot\|static>` (nodes per rate source), `curLastReadOK` (when the reader is on). |
| `metrics.recommendations` | hub | blocking | ReadyAudit | No recommendation produced yet for the cluster's live workloads. |
| `rbac.read` | agent | blocking | AgentConnected | Missing list/watch on pods, workloads, HPAs, nodes. |
| `rbac.resize` | agent | blocking | AutomationReady | Missing `patch pods/resize`. |
| `rbac.rollout` | agent | blocking | AutomationReady | Missing `patch` on deployments, statefulsets, daemonsets. |
| `rbac.hpa` | agent | blocking | AutomationReady | Missing `patch horizontalpodautoscalers`. |
| `rbac.evict` | agent | blocking | AutomationReady | Missing `create pods/eviction`. |
| `rbac.policies` | agent | blocking | AutomationReady | Missing create/update/delete on `rightsizingpolicies`. |
| `rbac.events` | agent | blocking | AutomationReady | Missing `create events`. |
| `apply.in-place-resize` | agent | warning | – | Server < 1.33 or no `pods/resize` in discovery: `in-place` falls back to `rollout`. |
| `webhook.healthy` | agent | warning | – | MutatingWebhookConfiguration missing, no `caBundle`, no ready endpoints, or a dry-run pod is not mutated. |
| `webhook.cert-manager` | agent | info | – | cert-manager present when the webhook uses it. |
| `nodes.in-place-memory` | agent | info | – | Share of nodes with swap or static CPU/memory manager; `details.nodes`. |
| `integration.argocd` | agent | warning | – | Argo CD installed and `argocd-cm` lacks `managedFieldsManagers: [kerf]`; `unknown` if unreadable. Remediation: the snippet. |
| `policy.namespace-coverage` | agent | info | – | Namespaces excluded by policy or label. |

Each `rbac.*` check also arrives as an `AgentCapability {name, granted, missing[], message}`. `missing` lists
the ClusterRole rules to add, e.g. `{"apiGroups":[""],"resources":["pods/resize"],"verbs":["patch"]}`.

## 4. Endpoints (examples)

`GET /v1/admin/datasources` (viewer) → `{"items": [Datasource…]}`.

`POST /v1/admin/datasources` (admin)
```json
→ {"name": "amp-eu1", "type": "amp", "url": "https://aps-workspaces.eu-west-1.amazonaws.com/workspaces/ws-1/",
   "auth": {"type": "sigv4", "sigv4": {"region": "eu-west-1", "roleARN": "arn:aws:iam::123:role/ro-hub"}},
   "timeout": "30s", "maxPoints": 11000, "reason": "EKS prod metrics"}
← 201 ETag: "1" {"id": "…", "name": "amp-eu1", "type": "amp", "url": "…", "builtIn": false,
   "auth": {"type": "sigv4", "sigv4": {"region": "eu-west-1", "service": "aps", "roleARN": "…"}, "secretConfigured": false},
   "headers": {}, "timeout": "30s", "maxPoints": 11000, "usedBy": [], "version": 1, "createdAt": "…", "createdBy": {…}, "updatedAt": "…", "updatedBy": {…}}
```
A bearer datasource sends `"auth": {"type": "bearer", "token": "…"}`. The response shows
`"secretConfigured": true` and never the token.

`GET /v1/admin/datasources/{id}` (viewer) → `Datasource`.
`PUT /v1/admin/datasources/{id}` (admin, If-Match) → `Datasource`.
`DELETE /v1/admin/datasources/{id}` (admin, If-Match, `{"reason"}`) → `204`. It answers `409
datasource_in_use` while a cluster is bound to it, and `409 builtin` for `local`.

`POST /v1/admin/datasources/{id}/test` (admin; nothing stored)
```json
→ {"binding": {"datasourceId": "…", "clusterLabel": "k8s_cluster", "clusterLabelValue": "prod-eu1"}}
← 200 {"ok": false, "latencyMs": 84,
   "found": {"containerSeries": 1284, "pods": 212, "nodes": 9},
   "labelValues": ["prod-eu1", "prod-eu2"],
   "checks": [{"id": "metrics.node-labels", "category": "metrics", "severity": "warning", "source": "hub", "status": "warn",
               "message": "kube_node_labels has no label_node_kubernetes_io_instance_type",
               "remediation": "kube-state-metrics:\n  metricLabelsAllowlist:\n    - nodes=[node.kubernetes.io/instance-type,karpenter.sh/capacity-type,topology.kubernetes.io/zone,topology.kubernetes.io/region]",
               "remediationType": "yaml", "docsURL": "…/readiness#metrics.node-labels", "checkedAt": "…"}]}
```
Without `binding`, only `datasource.reachable` runs, plus `labelValues` for `clusterLabel` `cluster`.

`GET /v1/clusters/{c}/readiness` (viewer)
```json
{"cluster": "ro-spoke-2", "state": "ReadyAudit", "stateSince": "…", "checkedAt": "…",
 "summary": {"blocking": 1, "warnings": 2, "unknown": 0},
 "nextState": "AutomationReady", "blockedBy": ["rbac.resize"],
 "capabilities": [{"name": "resize", "granted": false,
                   "missing": ["{\"apiGroups\":[\"\"],\"resources\":[\"pods/resize\"],\"verbs\":[\"patch\"]}"],
                   "message": "SelfSubjectAccessReview denied patch pods/resize"}],
 "recheckRequestedAt": null,
 "checks": [ReadinessCheck…]}
```
`POST /v1/clusters/{c}/readiness/recheck` (operator) → `202 {"requestedAt": "…", "hubChecks": true, "agentRecheck": true}`.
`GET /v1/clusters/{c}/readiness/history?from&to&limit` (viewer)
→ `{"items": [{"time": "…", "kind": "state", "fromState": "AutomationReady", "toState": "ReadyAudit"},
{"time": "…", "kind": "check", "checkId": "rbac.resize", "fromStatus": "pass", "toStatus": "fail", "message": "…"}]}`.

`GET /v1/admin/clusters/{c}/install?format=helm&webhook=true` (admin)
→ `InstallSnippet {"format": "helm", "content": "kubectl create secret … \nhelm upgrade --install …",
"filename": "install.sh", "argocdIgnoreDifferences": "resource.customizations.ignoreDifferences.all: |\n  managedFieldsManagers:\n  - kerf\n", "notes": […]}`.
- `format=values` returns a `values.yaml`; `format=argocd` returns an Argo CD `Application` manifest.
- The token is never in the snippet. The snippet references the Secret `kerf-agent-token`
  (key `token`), and the value comes from cluster creation or `POST /v1/admin/tokens` (shown once).

Lists and status batch:
- `ClusterInfo` (in `GET /v1/clusters`) and `ClusterAdmin` gain `readiness: ReadinessSummary`.
- `WorkloadStatusBatch` gains `capabilities[]` and `checks[]` (absent = unchanged; full set when present).
- `ClusterConfig` gains `minAgentVersion`, `hubVersion`, and `flags.recheck` / `flags.recheckRequestedAt`.

## 5. Metrics

`kerf_cluster_readiness{cluster,check,severity}` (gauge) and `kerf_cluster_readiness_state{cluster,state}`
(gauge) are documented in `docs/contracts/metrics.md`.
