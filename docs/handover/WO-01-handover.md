# WO-01 hand-over (local k3d dev environment)

Branch `wo-01-dev-env` (commit fff202a), pushed to origin. No PR (gh not installed). Clusters LEFT RUNNING.
Full docs: `dev/README.md`. Until WO-00's root Makefile lands: `make -f make/dev.mk dev-up|dev-down|dev-reset|dev-verify|dev-status`.

## Endpoints (host)
- VictoriaMetrics http://localhost:18428 (query, /api/v1/import, /api/v1/write) — also port-forward svc/victoria-metrics 8428 on k3d-ro-hub, ns monitoring
- Grafana http://localhost:13000 (anonymous admin; admin/admin); dashboards from repo `dashboards/**/*.json` → folder resource-optimizer; default datasource uid `victoriametrics` (Prometheus type)
- Postgres postgres://ro:ro@localhost:15432/resource_optimizer?sslmode=disable (in-cluster postgres.postgres.svc:5432; Secret postgres/postgres has DATABASE_URL)
- Argo CD (spoke-1) http://localhost:18090 admin/admin123; git server git://localhost:19418/workloads.git
- Registry: push localhost:5005/<repo>:<tag>, pull k3d-ro-registry:5005/<repo>:<tag>
- Contexts k3d-ro-hub :16443, k3d-ro-spoke-1 :16444, k3d-ro-spoke-2 :16445 (current context is NOT switched)
- Reserved hub NodePorts: 30080 → localhost:18080 (hub API, WO-04), 30090 → localhost:19090 (web UI, WO-14; pricing is ClusterIP only)

## Conventions other tracks should follow
- Spokes reach the hub via selector-less Services: `http://victoria-metrics.hub.svc:8428`, `http://hub-api.hub.svc:8080` (→ hub NodePort 30080). WO-04: expose the hub API as NodePort 30080 on ro-hub.
- Scraping: annotate your Service `prometheus.io/scrape: "true"`, `prometheus.io/port`, optional `/path`, `/scheme`. Hub vmagent (no external cluster label) and spoke vmagents (external label cluster=<name>) both have this job with honor_labels: true. Discovery labels are kubernetes_namespace/kubernetes_service/kubernetes_pod (never `namespace`).
- Tilt: resources hub, pricing (ro-hub), agent, webhook (both spokes); skipped until deploy/charts/<name>/ exists. Dockerfile lookup: cmd/<name>/Dockerfile, build/<name>/Dockerfile, build/<name>.Dockerfile, or root Dockerfile with --build-arg COMPONENT. Sets image.repository, image.tag, clusterName; uses values-dev.yaml / values-<cluster>.yaml; namespace resource-optimizer.
- Workloads: namespace default, label app=<name>: overprovisioned, throttled, memory-leak (OOMs ~every 4 min — intended), hpa-cpu, jvm-maxram, jvm-xmx, single-replica, statefulset-ondelete, cronjob-batch, daemonset-agent.
- Node labels: spoke-1 m6i.xlarge/on-demand/eu-west-1a, spoke-2 c6a.2xlarge/spot/eu-west-1b (in kube_node_labels as label_node_kubernetes_io_instance_type etc.). WO-03's `dev/prices.yaml` (path in its verification) is NOT created — WO-03 owns its format; it needs prices for these two types in eu-west-1.
- D-006 smoke-tested: SSA by field manager resource-optimizer (needs --force-conflicts, argocd-controller owns the fields) survives hard refresh + explicit sync; app stays Synced.
- VM retention 35d (VM rejects samples older than retention; WO-02 backfills 30d).
