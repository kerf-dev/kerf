# Local dev environment (k3d)

Three k3s v1.35 clusters on one Docker network (`ro-dev`) that stand in for the EKS fleet (D-010):

| Cluster | Role | Runs |
|---|---|---|
| `ro-hub` | central hub | VictoriaMetrics single-node, Grafana, Postgres, vmagent for hub components; later the hub and pricing charts (Tilt) |
| `ro-spoke-1` | workload cluster | kube-state-metrics, vmagent → hub, cert-manager, metrics-server, **Argo CD** + in-cluster git server, sample workloads synced by Argo CD; later agent + webhook |
| `ro-spoke-2` | workload cluster | kube-state-metrics, vmagent → hub, cert-manager, metrics-server, sample workloads applied with kubectl; later agent + webhook |

Each cluster is a single k3s server node (no agents, no load balancer). Traefik, ServiceLB and the Helm
controller are disabled everywhere; metrics-server is disabled on the hub (the spokes keep it for the HPA
workload). Every chart and image version is pinned in [`versions.env`](versions.env).

## Prerequisites

- Docker Desktop with **at least 8 GB memory and 4 CPUs** (the whole fleet uses 5.1–5.7 GB; see
  "Footprint" below) and ~10 GB free disk.
- `k3d` ≥ 5.9, `kubectl` ≥ 1.35, `helm` ≥ 4 (3.x works), `tilt` ≥ 0.37 for the inner loop.
  The scripts add `~/.local/bin` to `PATH`.
- Host ports `5005`, `13000`, `15432`, `16443-16445`, `18080`, `18090`, `18428`, `19090`, `19418` free on 127.0.0.1.

## Commands

The root `Makefile` includes `make/*.mk`; until it exists use `make -f make/dev.mk <target>`.

| Command | What it does |
|---|---|
| `make dev-up` | Create/converge everything (idempotent). Waits until Argo CD is synced, the workloads are up and the hub sees metrics from both spokes. ~2.5 min from scratch (measured 161 s with an empty image cache), ~30 s when the clusters exist. |
| `make dev-down` | Delete the three clusters and the image registry. The docker.io pull cache is kept. |
| `make dev-reset` | `dev-down` + `dev-up`. |
| `make dev-purge` | `dev-down` and also delete the docker.io pull cache and the `ro-dev` network. |
| `make dev-status` | Pod counts per cluster and the URLs below. |
| `make dev-verify` | WO-01 checks: both spokes' cAdvisor/KSM series in the hub, no failing pods (except `memory-leak`), Argo CD synced, Grafana healthy. |
| `make dev-dashboards` | Re-provision Grafana from `dashboards/` (Tilt resource `dashboards` does this on change). |
| `make dev-workloads` | Push `dev/workloads/` to the spokes (Tilt resource `workloads` does this on change). |
| `tilt up [resource...]` | Inner loop, see below. |

## Access

| What | From the host | In-cluster |
|---|---|---|
| VictoriaMetrics (query, `/api/v1/import`, `/api/v1/write`) | http://localhost:18428 | `victoria-metrics.monitoring.svc:8428` (hub), `victoria-metrics.hub.svc:8428` (spokes) |
| Grafana | http://localhost:13000 (anonymous admin, or `admin`/`admin`) | `grafana.monitoring.svc:80` (hub) |
| Postgres | `postgres://ro:ro@localhost:15432/resource_optimizer?sslmode=disable` | `postgres.postgres.svc:5432` (hub), Secret `postgres/postgres` has `DATABASE_URL` |
| Hub API (reserved for WO-04, NodePort 30080 on the hub) | http://localhost:18080 | `hub-api.hub.svc:8080` (spokes) |
| Web UI (WO-14, NodePort 30090 on the hub) | http://localhost:19090 | – |
| Argo CD (spoke-1) | http://localhost:18090 (`admin`/`admin123`) | `argocd-server.argocd.svc` |
| Git server (spoke-1) | `git://localhost:19418/workloads.git` | `git://git-server.git-server.svc.cluster.local/workloads.git` |
| Image registry | push `localhost:5005/<repo>:<tag>` | pull `k3d-ro-registry:5005/<repo>:<tag>` |
| Kube API | contexts `k3d-ro-hub` (:16443), `k3d-ro-spoke-1` (:16444), `k3d-ro-spoke-2` (:16445) | – |

`make dev-up` adds the three contexts to `~/.kube/config` without switching the current context; always
pass `--context`. The WO verification also works through a port-forward:
`kubectl --context k3d-ro-hub -n monitoring port-forward svc/victoria-metrics 8428`.

### How the spokes reach the hub

Every spoke has a namespace `hub` with selector-less Services (`victoria-metrics`, `hub-api`) whose
EndpointSlices point at the hub node's IP on `ro-dev` (k3d keeps node IPs stable across restarts;
`make dev-up` refreshes them). Spoke components use these stable names the way they would use a real
hub endpoint in EKS. Spoke → hub only; the hub never talks to the spokes' API servers (D-004).

## Metrics

vmagent on each spoke scrapes every 30 s and remote-writes to the hub with the external label
`cluster=<cluster name>` (`ro-spoke-1`, `ro-spoke-2`):

- `kubelet` – kubelet `/metrics`
- `cadvisor` – kubelet `/metrics/cadvisor` (`container_cpu_usage_seconds_total`, `container_memory_working_set_bytes`, `container_cpu_cfs_throttled_periods_total`, `container_oom_events_total`, …)
- `kube-state-metrics` – including `kube_node_labels` with `node.kubernetes.io/instance-type`,
  `karpenter.sh/capacity-type`, `topology.kubernetes.io/region|zone`, and the `app` label on pods and workloads.
- `annotated-services` – any Service annotated `prometheus.io/scrape: "true"` (+ `prometheus.io/port`,
  optional `prometheus.io/path`, `prometheus.io/scheme`), with `honor_labels: true`. This is how the agent
  and webhook (WO-06/07) get scraped: annotate the chart's Service, nothing to change in `dev/`.

The hub runs its own vmagent with only the `annotated-services` job and **no** external `cluster` label,
writing to the local VictoriaMetrics. Pricing and the hub (WO-03/04) get scraped the same way, and
`honor_labels: true` keeps the `cluster` label their series carry (`docs/contracts/metrics.md`).
VictoriaMetrics' own Service is annotated, so `up{job="annotated-services"}` is non-empty from the start.
Discovery metadata goes to `kubernetes_namespace` / `kubernetes_service` / `kubernetes_pod`, never to
`namespace`, so it cannot clobber the workload labels in `ro_*` series.

Fake EKS/Karpenter node labels: spoke-1 `m6i.xlarge` / `on-demand` / `eu-west-1a`, spoke-2 `c6a.2xlarge` / `spot` / `eu-west-1b`.

VictoriaMetrics retention is 35 days: it rejects samples older than the retention period, and the
synthetic backfill (WO-02) writes 30 days.

## Grafana dashboards

Every `*.json` under the repo's `dashboards/` (nested paths flattened to `dir_file.json`) is packed into
ConfigMap `monitoring/grafana-dashboards` and file-provisioned into folder **resource-optimizer**, with
VictoriaMetrics (Prometheus type, uid `victoriametrics`) as the default datasource. Use
`${datasource}` or uid `victoriametrics` in dashboard JSON. Changes appear ~1 minute after
`make dev-dashboards` (or immediately via Tilt's `dashboards` resource plus the ConfigMap refresh).

## Argo CD and the git server (spoke-1)

Argo CD is installed without dex, notifications or the ApplicationSet controller. Per D-006 it ignores
fields owned by field manager `resource-optimizer`, globally (`resource.customizations.ignoreDifferences.all`
in `argocd-cm`) and on the Application `argocd/workloads` (`ignoreDifferences.managedFieldsManagers`
plus `RespectIgnoreDifferences=true`, automated sync with prune and self-heal, 60 s reconciliation).

The Application syncs `git://git-server.git-server.svc.cluster.local/workloads.git` (branch `main`),
a bare repo that the git-server pod seeds from `dev/workloads/` on start. No GitHub access or credentials
are needed. To simulate a Git change:

```bash
make dev-workloads                       # re-seed from dev/workloads/ (one new commit) + hard refresh
# or push a commit directly:
git clone git://localhost:19418/workloads.git /tmp/workloads && cd /tmp/workloads
# edit, commit, then: git push origin main
```

Pushed commits live only until the git-server pod restarts.

## Sample workloads (`dev/workloads/`, namespace `default`, label `app=<name>`)

| Name | Kind | Behaviour |
|---|---|---|
| `overprovisioned` | Deployment ×2 | requests 500m/256Mi, uses ~50m (10%) and ~24Mi; in-place `resizePolicy: NotRequired` |
| `throttled` | Deployment | wants a full core, limit 200m → heavily CFS-throttled |
| `memory-leak` | Deployment | grows ~512 KiB/s to its 128Mi limit, OOMKilled every ~4 min, then CrashLoopBackOff (intended) |
| `hpa-cpu` | Deployment + HPA | CPU-utilization HPA (target 60%, 2–4 replicas, ~40% actual); no `spec.replicas` in Git |
| `jvm-maxram` | Deployment | JVM with `-XX:MaxRAMPercentage=75` in `JAVA_TOOL_OPTIONS`; memory `resizePolicy: RestartContainer` |
| `jvm-xmx` | Deployment | JVM with `-Xmx128m` on the command line, request 512Mi |
| `single-replica` | Deployment ×1 + PDB `minAvailable: 1` | single replica that cannot be evicted voluntarily |
| `statefulset-ondelete` | StatefulSet ×2 | `updateStrategy: OnDelete`, small PVC |
| `cronjob-batch` | CronJob | every 5 min, ~60 s at half a core |
| `daemonset-agent` | DaemonSet | light per-node agent |

The JVM program is in `jvm-app-configmap.yaml` (run with JDK single-file launch). Workloads are identical
on both spokes; spoke-1 gets them through Argo CD, spoke-2 through `kubectl apply`.

## Tilt

`tilt up` (or `tilt up agent webhook`) runs one resource per component — `hub`, `pricing` (on `ro-hub`),
`agent`, `webhook` (on both spokes) — each skipped until `deploy/charts/<name>/` exists. A resource builds
the image (first of `cmd/<name>/Dockerfile`, `build/<name>/Dockerfile`, `build/<name>.Dockerfile`, or a
root `Dockerfile` with `--build-arg COMPONENT=<name>`), pushes it to `localhost:5005`, and
`helm upgrade --install`s the chart into namespace `resource-optimizer` with `image.repository`,
`image.tag` and `clusterName` set, plus `values-dev.yaml` / `values-<cluster>.yaml` if the chart has them.
See [`scripts/tilt-deploy.sh`](scripts/tilt-deploy.sh). The `dashboards` and `workloads` resources
re-sync on file changes. Tilt's own kube context does not matter: every resource passes `--kube-context`
explicitly and gets your full `KUBECONFIG`. `tilt ci` exits 0 against a running fleet.

## Footprint

All requests and limits are set. `docker stats --no-stream` with the full fleet running (2026-11-04):

| Container | Memory |
|---|---|
| `k3d-ro-hub-server-0` | ~1.2 GiB |
| `k3d-ro-spoke-1-server-0` | 2.2–2.7 GiB (Argo CD, two JVMs ≈ 360 MiB, memory-leak up to 128 MiB) |
| `k3d-ro-spoke-2-server-0` | ~1.6 GiB |
| registries + k3d tools containers | ~100 MiB |
| **Total** | **5.1–5.7 GiB** (highest reading 5,700 MiB) |

About 0.6–0.9 GiB per node is k3s itself plus containerd shims; the rest is pods. Biggest pods:
`jvm-maxram` ~200 MiB, `jvm-xmx` ~160 MiB, `argocd-application-controller` ~135 MiB, vmagent.
CPU: about 0.7 cores per spoke (mostly the `throttled` and `overprovisioned` stress loads).

## Troubleshooting

- `make dev-up` is safe to re-run after any failure; it converges.
- Images come from Docker Hub through the `k3d-ro-dockerio-cache` pull-through cache (kept across
  `dev-down`), so repeated resets do not hit Docker Hub rate limits. Other registries (quay.io, ghcr.io,
  registry.k8s.io) are pulled directly.
- After a Docker Desktop restart, run `make dev-up` again: it starts the stopped node containers and
  refreshes the hub link.
