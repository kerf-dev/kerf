# Kerf agent chart (WO-06)

One release per workload cluster, namespace `kerf`:

```bash
make deploy-agent                       # build, push, helm upgrade --install on ro-spoke-1 and ro-spoke-2
make deploy-agent AGENT_HELM_ARGS='--set stubHub.enabled=true -f my-recs.yaml'   # with the stub hub
make test-agent                         # unit + envtest (downloads kube-apiserver/etcd 1.35 into ./bin/envtest)
```

## What it installs

- **CRDs** as templates (`files/crds/` = copies of `api/crds/`, kept in sync by `make charts-sync`
  and `deploy/charts/sync_test.go`), annotated `helm.sh/resource-policy: keep` (D-028): `helm upgrade`
  upgrades them, `helm uninstall` leaves them and every custom resource in place. Exactly one release
  per cluster renders them: this chart (`crds.install: true`); the webhook chart has
  `crds.install: false`. **Upgrading a release installed from the old `crds/` chart** (0.1.0 before
  WO-18b): the first upgrade needs `--take-ownership` (Helm ≥ 3.17), because the existing CRDs carry
  no Helm ownership metadata: `make deploy-agent AGENT_HELM_ARGS=--take-ownership`.
- Deployment (1 replica; `leaderElection: true` for more), ServiceAccount, ClusterRole/Binding,
  metrics Service (scraped by the spoke vmagent), ConfigMap with the cluster defaults.
- Optional: the hub token Secret (`hub.tokenSecret.create`, dev only) and a stub hub
  (`stubHub.enabled`) serving `stubHub.recommendations`.

## Hub connection

`hub.url` (default `http://hub-api.hub.svc:8080`), bearer token from Secret
`hub.tokenSecret.name` (default `kerf-agent-token`), key `token`, mounted as a file and
re-read on every request. The Secret is optional: without it the hub answers 401 and the agent is
"offline" (nothing new is applied, nothing is reverted, events are buffered).

## RBAC (ClusterRole)

| Resource | Verbs | Why |
|---|---|---|
| apps deployments, statefulsets, daemonsets; batch cronjobs | get list watch (+ patch) | policy resolution; rollout = SSA under field manager `kerf`, release = managedFields patch |
| apps replicasets, batch jobs | get list | owner chain to find a workload's pods |
| pods | get list watch (+ patch) | in-place status, health watch (restarts, OOM kills); patch only for the `previous-resources` / `recommendation-id` annotations |
| pods/resize | patch | in-place resize (D-007) |
| pods/eviction | create | OnDelete StatefulSets: outdated pods are evicted so PodDisruptionBudgets apply (WO-13) |
| policy poddisruptionbudgets | get list watch | single-replica protection: a PDB that allows 0 disruptions (WO-13) |
| nodes | get list watch | swap / unsupported-node detection |
| nodes/proxy | get (only with `health.cadvisorFallback`) | throttling from kubelet cAdvisor when the hub metrics are unreachable; reaches every kubelet API, hence off by default |
| autoscaling horizontalpodautoscalers | get list watch (+ patch unless readOnly) | HPA coupling (D-011); target set by HPA coordination (WO-11) |
| rightsizingpolicies | get list watch (+ create update patch delete unless readOnly) | policy resolution; writes only CRs labelled `kerf.getkerf.dev/managed-by=hub` (hub config, D-022) |
| rightsizingrecommendations (+ /status) | get list watch create update patch delete | the agent's record per workload |
| events (core and events.k8s.io) | create patch | Kubernetes Events on RightsizingRecommendations |
| leases (namespaced Role, only with leaderElection) | all | leader election |
| configmaps (namespaced Role, only with configSync) | get update (`<fullname>-config-state` only), create | the last hub config: pause flag and approvals survive a restart without the hub |

The bracketed write verbs are dropped with `readOnly: true`, which also caps every mode at audit.

## Hub-managed configuration (WO-21, D-022/D-024)

With `configSync.enabled` (default) the agent pulls `GET /v1/clusters/{cluster}/config` on every
pull (ETag) and:
- makes the `RightsizingPolicy` CRs labelled `kerf.getkerf.dev/managed-by=hub` equal to the
  hub's policies (server-side apply, field manager `kerf`). CRs without the label
  (Git) are never written; a hub policy whose name is taken by one is reported invalid.
- layers `ro-global-defaults` / `ro-cluster-defaults` / `ro-override-…` around the winning policy
  (`docs/contracts/admin.md` §2),
- treats `flags.paused` like a cluster-wide freeze (`blocked/Paused`), and
- applies approve-once grants (`applied/ApprovedOnce`).

The last config is kept in the ConfigMap `<fullname>-config-state`. A hub without the endpoint
(404) changes nothing. **Before downgrading the agent below WO-21, delete the hub CRs:**
`kubectl delete rsp -l kerf.getkerf.dev/managed-by=hub`.

## Values

See `values.yaml`; every production feature is switched on in `ci/values-prod.yaml` (`make charts-lint`).
Shared names across the component charts: `docs/handover/WO-18b-handover.md`. `clusterDefaults` is a RightsizingPolicy spec (without selector/priority): the
cluster default level of D-005. Leave it empty to keep the global default (`audit`).
