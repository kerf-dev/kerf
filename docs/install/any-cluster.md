# Install Kerf on any Kubernetes cluster

This guide installs Kerf on a conformant Kubernetes cluster that is not Amazon EKS. The difference from the
[EKS guide](eks.md) is prices: instead of the AWS price list, the pricing service reads a price table you supply.

> **Just evaluating?** [Install on an existing cluster](single-cluster.md) needs one namespace and an in-cluster
> PostgreSQL, and nothing else from AWS.

> **Note.** EKS is the tested platform. The agent, webhook and recommender use only the Kubernetes API, PromQL and
> PostgreSQL, but Kerf's tests do not cover other distributions (the local development setup uses k3d).

The chart is the same: `global.mode` is `hub` on the hub cluster (hub, web UI, pricing), `spoke` on each workload
cluster (agent, webhook, CRDs) and `all` for everything in one cluster.

## Requirements

| What | Notes |
|---|---|
| Kubernetes | 1.35 is the tested version. In-place resize needs 1.33 or later; before that Kerf falls back to `rollout`. |
| PostgreSQL | 12 or later, for the hub. Connect directly or through a session-mode pooler, never a transaction-mode one: the hub elects its leader with an advisory lock. |
| Metrics store | Prometheus, VictoriaMetrics, Thanos or Mimir, holding cAdvisor and kube-state-metrics series of every workload cluster ([what Kerf reads](#kube-state-metrics-requirements)). Simplest: every cluster remote-writes to one store with the label `cluster=<cluster name>`, which `hub.config.prometheusURL` points at. Other sources: **Admin → Metrics sources**. |
| cert-manager | On every workload cluster, for the webhook's serving certificate. |
| Network | Workload clusters reach the hub API over HTTPS. The API server reaches the nodes on port 9443 (the webhook). NetworkPolicies are off by default (`networkPolicy.enabled`). |

## 1. Hub cluster

Create the Secrets: outside development installs the chart rejects inline secrets. The database and its user
must exist; the hub applies the schema itself.

```bash
kubectl create namespace kerf
read -rs DB_PW   # URL-encode it if it holds special characters
kubectl -n kerf create secret generic hub-db \
  --from-literal=DATABASE_URL="postgres://kerf:${DB_PW}@db.example.com:5432/kerf?sslmode=verify-full"
unset DB_PW
kubectl -n kerf create secret generic hub-bootstrap-admin --from-literal=password="$(openssl rand -base64 24)"
```

For a private database CA, mount it with `hub.database.caBundle` and add `sslrootcert=/etc/hub/db-ca/ca.pem` to
the URL. The chart generates the key that encrypts datasource credentials and keeps it on uninstall: [back it up](../operate/backup.md). Write `values-hub.yaml`:

```yaml
global: {mode: hub, clusterName: hub-cluster, imageRegistry: ghcr.io/kerf-dev}
hub:
  database: {existingSecret: hub-db}
  auth: {bootstrapAdmin: {existingSecret: hub-bootstrap-admin}}
  config:
    prometheusURL: http://prometheus.monitoring.svc:9090    # the built-in datasource `local`
    onboarding:
      hubURL: https://hub.example.com                       # the hub as agents reach it
      chart: oci://ghcr.io/kerf-dev/kerf/charts/kerf
      imageRegistry: ghcr.io/kerf-dev                       # for the wizard's install snippets
  ingress:
    enabled: true
    hosts: [{host: hub.example.com, paths: [{path: /, pathType: Prefix}]}]
    tls: [{hosts: [hub.example.com], secretName: hub-tls}]
web:
  ingress:
    enabled: true
    hosts: [{host: kerf.example.com, paths: [{path: /, pathType: Prefix}]}]
    tls: [{hosts: [kerf.example.com], secretName: kerf-ui-tls}]
pricing:
  provider: static
  vmURL: http://prometheus.monitoring.svc:9090    # holds kube_node_labels of the workload clusters
  hub: {tokenSecret: {name: kerf-pricing-token}}
```

```bash
helm upgrade --install kerf oci://ghcr.io/kerf-dev/kerf/charts/kerf --version <version> \
  -n kerf -f values-hub.yaml
kubectl -n kerf rollout status deploy/hub
```

Released charts pull the images of the same release from `ghcr.io/kerf-dev/kerf`. The `pricing` token does not
exist yet, so `pricing` waits for its Secret. Sign in at `https://kerf.example.com` as `admin` with the password in
the Secret `hub-bootstrap-admin`, change it, and create a token with role `pricing` under
**Admin → Access → Tokens**. A token is shown once. Everyone who uses Kerf signs in with a user of their own or
through your identity provider ([Sign-in and access](../auth.md)); the UI holds no token. If you used an older
guide that created a `ui` token, [remove it](../auth.md#upgrading-from-anonymous-read-access).

```bash
kubectl -n kerf create secret generic kerf-pricing-token --from-literal=token="<pricing token>"
```

**Check:** `curl -sS https://hub.example.com/readyz` prints `ok`.

## kube-state-metrics requirements

Kerf reads these kube-state-metrics series. The readiness checks `metrics.ksm-owners`, `metrics.ksm-resources`,
`metrics.node-labels`, `metrics.hpa` and `metrics.node-allocatable` test them, and
[Cluster readiness](../operate/readiness.md) lists the fixes. Enable these collectors (the `--resources` flag):

```
nodes,pods,replicasets,deployments,statefulsets,daemonsets,jobs,cronjobs,horizontalpodautoscalers
```

| Collector | Series Kerf reads |
|---|---|
| `pods` | `kube_pod_owner`, `kube_pod_container_resource_requests`, `kube_pod_container_resource_limits`, `kube_pod_container_status_restarts_total`, `kube_pod_container_status_last_terminated_reason`, `kube_pod_container_info` |
| `replicasets`, `jobs` | `kube_replicaset_owner`, `kube_job_owner`: they resolve a pod to its Deployment or CronJob |
| `nodes` | `kube_node_labels`, `kube_node_status_allocatable` |
| `horizontalpodautoscalers` | `kube_horizontalpodautoscaler_info`, `kube_horizontalpodautoscaler_spec_target_metric` |
| `deployments`, `statefulsets`, `daemonsets`, `cronjobs` | replica counts and owners, for the workload views |

Pricing and the node estimate also need the node labels below in `kube_node_labels`. Allow them in kube-state-metrics:

```
nodes=[node.kubernetes.io/instance-type,karpenter.sh/capacity-type,eks.amazonaws.com/capacityType,topology.kubernetes.io/zone,topology.kubernetes.io/region]
```

With the **kube-state-metrics Helm chart** (`prometheus-community/kube-state-metrics`), in its values:

```yaml
collectors: [nodes, pods, replicasets, deployments, statefulsets, daemonsets, jobs, cronjobs, horizontalpodautoscalers]
metricLabelsAllowlist:
  - nodes=[node.kubernetes.io/instance-type,karpenter.sh/capacity-type,eks.amazonaws.com/capacityType,topology.kubernetes.io/zone,topology.kubernetes.io/region]
```

With **kube-prometheus-stack**, the same keys sit under `kube-state-metrics:`:

```yaml
kube-state-metrics:
  collectors: [nodes, pods, replicasets, deployments, statefulsets, daemonsets, jobs, cronjobs, horizontalpodautoscalers]
  metricLabelsAllowlist:
    - nodes=[node.kubernetes.io/instance-type,karpenter.sh/capacity-type,eks.amazonaws.com/capacityType,topology.kubernetes.io/zone,topology.kubernetes.io/region]
```

A kube-state-metrics you run yourself takes the same lists as `--resources=…` and `--metric-labels-allowlist=…`.
Do not drop these series with `metric_relabel_configs` or a remote-write filter. Kubernetes labels turn into
`label_<name>` with dots and slashes as underscores, so `eks.amazonaws.com/capacityType` arrives as
`label_eks_amazonaws_com_capacity_type`. After a change, re-check readiness
([how](single-cluster.md#5-readiness)); the hub repeats its metrics checks every hour.

## Supply a price table

The chart's default provider is `static`. Without `pricing.prices` it uses the file in its image: approximate AWS
list prices for development, which your nodes will not match. Set your own table, in USD per hour per node:

```yaml
pricing:
  provider: static
  prices: |
    instanceTypes:                   # the shape of every type that has a price
      std-4x16: {vcpu: 4, memoryGiB: 16}
      big-16x64: {vcpu: 16, memoryGiB: 64}
    regions:
      on-prem-1:                     # region -> instance type -> prices
        std-4x16: {onDemand: 0.15}
        big-16x64: {onDemand: 0.60, spot: 0.25, spotByZone: {on-prem-1a: 0.22}}
```

`spot` applies to zones not listed in `spotByZone`. A price of 0, or none, counts as no price. The service exits on
a table with an unknown instance type or a negative price: read `kubectl -n kerf logs deploy/pricing`. It matches
each node, from `kube_node_labels`, to the table:

| Node label | Used as | If missing |
|---|---|---|
| `node.kubernetes.io/instance-type` | the instance type | node skipped (`no-instance-type`) |
| `topology.kubernetes.io/region`, else `topology.kubernetes.io/zone` | the region; a zone `on-prem-1a` gives `on-prem-1` | `pricing.defaultRegion`, else skipped (`no-region`) |
| `karpenter.sh/capacity-type` or `eks.amazonaws.com/capacityType` | `on-demand` or `spot` | on-demand |

Nodes without these labels: choose your own instance type names, use them in the table, and label the nodes
(`kubectl label node <node> node.kubernetes.io/instance-type=std-4x16 topology.kubernetes.io/region=on-prem-1`),
then allowlist the labels in kube-state-metrics ([`metrics.node-labels`](../operate/readiness.md#checks-that-do-not-gate)).
The service splits a node's price into a CPU and a memory rate. `pricing.cpuMemoryWeight` sets the weight of one
vCPU in GiB: `"0"` derives it per instance family and falls back to 9, as it does for names of your own.

A skipped node uses a default rate in savings; **Admin → Pricing** can set a fixed rate for a cluster instead.
Check `kerf_pricing_nodes` and the readiness check `pricing.coverage`.

## 2. Each workload cluster

Register the cluster: **Clusters → Add cluster**. Its name is a DNS label and must equal `global.clusterName`. The
wizard shows the agent token once.

```bash
kubectl create namespace kerf
read -rs AGENT_TOKEN && kubectl -n kerf create secret generic kerf-agent-token --from-literal=token="$AGENT_TOKEN"; unset AGENT_TOKEN
```

```yaml
global: {mode: spoke, clusterName: prod-eu1, imageRegistry: ghcr.io/kerf-dev}
agent:
  hub:
    url: https://hub.example.com
  health:
    metricsURL: http://prometheus-operated.monitoring.svc:9090   # this cluster's Prometheus
    clusterLabel: ""                                             # its series carry no cluster label
```

```bash
helm upgrade --install kerf oci://ghcr.io/kerf-dev/kerf/charts/kerf --version <version> \
  -n kerf -f values-spoke.yaml --wait --timeout 10m
kubectl get --raw "/api/v1/namespaces/kerf/services/https:webhook:443/proxy/healthz"
```

The last command should end in `the server could not find the requested resource`: the webhook answered, so 9443 is open.
A hub certificate from a private CA needs `agent.hub.caBundle`.

## Readiness

The cluster should reach *Metrics OK*, then *Ready for audit* once the recommender has run (daily by default; an admin
can start a run with `POST /v1/admin/recompute?cluster=<cluster>`), then *Automation ready*. Every check and its fix:
[Cluster readiness](../operate/readiness.md). The hub repeats its metrics checks every hour and on a re-check
(`POST /v1/clusters/<cluster>/readiness/recheck`): a recompute alone does not refresh them. Workloads stay in `audit` until you set a policy.

## Related

- [Install on an existing cluster: one namespace, in-cluster PostgreSQL](single-cluster.md)
- [Install on Amazon EKS](eks.md)
- [Cluster readiness](../operate/readiness.md), [chart values](../reference/values/README.md), [pricing values](../reference/values/pricing.md)
