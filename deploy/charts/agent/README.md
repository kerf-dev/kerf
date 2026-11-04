# resource-optimizer agent chart (WO-06)

One release per workload cluster, namespace `resource-optimizer`:

```bash
make deploy-agent                       # build, push, helm upgrade --install on ro-spoke-1 and ro-spoke-2
make deploy-agent AGENT_HELM_ARGS='--set stubHub.enabled=true -f my-recs.yaml'   # with the stub hub
make test-agent                         # unit + envtest (downloads kube-apiserver/etcd 1.35 into ./bin/envtest)
```

## What it installs

- **CRDs** (`crds/`, copies of `api/crds/`, kept in sync by `make agent-sync-crds` and a test).
  Helm installs `crds/` only when the CRD does not exist yet and never deletes or upgrades them, so
  the install is idempotent and other charts (WO-07) may ship the same files. Upgrade CRDs with
  `make agent-crds` (server-side apply).
- Deployment (1 replica; `leaderElection: true` for more), ServiceAccount, ClusterRole/Binding,
  metrics Service (scraped by the spoke vmagent), ConfigMap with the cluster defaults.
- Optional: the hub token Secret (`hub.tokenSecret.create`, dev only) and a stub hub
  (`stubHub.enabled`) serving `stubHub.recommendations`.

## Hub connection

`hub.url` (default `http://hub-api.hub.svc:8080`), bearer token from Secret
`hub.tokenSecret.name` (default `resource-optimizer-agent-token`), key `token`, mounted as a file and
re-read on every request. The Secret is optional: without it the hub answers 401 and the agent is
"offline" (nothing new is applied, nothing is reverted, events are buffered).

## RBAC (ClusterRole)

| Resource | Verbs | Why |
|---|---|---|
| apps deployments, statefulsets, daemonsets; batch cronjobs | get list watch (+ patch) | policy resolution; rollout = SSA under field manager `resource-optimizer`, release = managedFields patch |
| apps replicasets, batch jobs | get list | owner chain to find a workload's pods |
| pods | get list watch (+ patch) | in-place status, health watch (restarts, OOM kills); patch only for the `previous-resources` / `recommendation-id` annotations |
| pods/resize | patch | in-place resize (D-007) |
| pods/eviction | create | OnDelete StatefulSets: outdated pods are evicted so PodDisruptionBudgets apply (WO-13) |
| policy poddisruptionbudgets | get list watch | single-replica protection: a PDB that allows 0 disruptions (WO-13) |
| nodes | get list watch | swap / unsupported-node detection |
| nodes/proxy | get (only with `health.cadvisorFallback`) | throttling from kubelet cAdvisor when the hub metrics are unreachable; reaches every kubelet API, hence off by default |
| autoscaling horizontalpodautoscalers | get list watch (+ patch unless readOnly) | HPA coupling (D-011); target set by HPA coordination (WO-11) |
| rightsizingpolicies | get list watch | policy resolution |
| rightsizingrecommendations (+ /status) | get list watch create update patch delete | the agent's record per workload |
| events (core and events.k8s.io) | create patch | Kubernetes Events on RightsizingRecommendations |
| leases (namespaced Role, only with leaderElection) | all | leader election |

The bracketed write verbs are dropped with `readOnly: true`, which also caps every mode at audit.

## Values

See `values.yaml`. `clusterDefaults` is a RightsizingPolicy spec (without selector/priority): the
cluster default level of D-005. Leave it empty to keep the global default (`audit`).
