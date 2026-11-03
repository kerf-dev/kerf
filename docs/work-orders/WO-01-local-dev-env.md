# WO-01 Local dev environment (k3d)

- **Phase / wave:** 1 / 1
- **Depends on:** – (standard Kubernetes metric names only; runs in parallel with WO-00)
- **Owns:** `dev/`, `Tiltfile`, `make/dev.mk`
- **Decisions:** D-003, D-010

## Goal
One command brings up a hub cluster and two spoke clusters that look like EKS as far as our components care.

## Deliverables
- `make dev-up` / `make dev-down` / `make dev-reset` creating three k3d clusters (`ro-hub`, `ro-spoke-1`, `ro-spoke-2`) on k3s v1.35.x, on one shared Docker network. Disable Traefik and other unused k3s add-ons.
- **Hub:** VictoriaMetrics single-node (with `/api/v1/import` reachable), Grafana provisioned from `dashboards/`, Postgres.
- **Each spoke:** kube-state-metrics, vmagent scraping kubelet/cAdvisor and KSM with `externalLabels: {cluster: <name>}` remote-writing to the hub, cert-manager.
- **spoke-1 only:** Argo CD, with the `managedFieldsManagers` / `RespectIgnoreDifferences` config from D-006, and one Application syncing `dev/workloads/` from a local Git server or file path.
- Fake node labels on spokes: `node.kubernetes.io/instance-type` and `karpenter.sh/capacity-type`.
- **Sample workloads** in `dev/workloads/`: overprovisioned (stress-ng ~10% of request), throttled, memory leak until OOM, CPU-utilization HPA, JVM with `MaxRAMPercentage`, JVM with fixed `-Xmx`, single-replica Deployment, StatefulSet with `OnDelete`, CronJob, DaemonSet.
- `Tiltfile` with a resource per component that is skipped if its chart in `deploy/charts/<name>` does not exist yet.
- `dev/README.md`: prerequisites (Docker resources), commands, ports.

## Definition of done
From a clean machine with Docker, k3d, kubectl, helm and tilt: `make dev-up` completes in under 10 minutes and the hub sees metrics from both spokes.

## Verification
```bash
make dev-down; make dev-up
kubectl --context k3d-ro-hub -n monitoring port-forward svc/victoria-metrics 8428 &
curl -s 'localhost:8428/api/v1/query?query=count by (cluster)(container_cpu_usage_seconds_total)'   # both spokes present
curl -s 'localhost:8428/api/v1/query?query=count by (cluster)(kube_pod_container_resource_requests)'
kubectl --context k3d-ro-spoke-1 get pods -A | grep -E 'Error|CrashLoop' && exit 1 || true   # except the intended OOM workload
```
