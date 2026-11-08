# Install on an existing cluster: one namespace, in-cluster PostgreSQL

For evaluating Kerf on a real cluster (EKS or any other) without RDS, an ALB, External Secrets or Secrets Manager.
Everything runs in one namespace of one cluster: `global.mode: all` installs the hub, web UI, pricing, agent and
webhook, and PostgreSQL runs as a pod next to them. It is a good way to look at your own workloads, not a production
layout: one database pod, no backup, no TLS inside the cluster. For production, use the [EKS guide](eks.md) or the
[any-cluster guide](any-cluster.md).

## Requirements

- Kubernetes 1.35 (tested), `kubectl` and Helm 3.17 or later, and cert-manager (the webhook's serving certificate).
- A Prometheus-compatible store (Prometheus, VictoriaMetrics, Thanos or Mimir) with cAdvisor and kube-state-metrics
  series of this cluster. [Scrape jobs and kube-state-metrics settings](#2-metrics) follow below.

## 1. Namespace, PostgreSQL and Secrets

```bash
kubectl create namespace kerf
read -rs DB_PW   # letters and digits only, so the URL needs no encoding
kubectl -n kerf create secret generic hub-db \
  --from-literal=POSTGRES_PASSWORD="$DB_PW" \
  --from-literal=DATABASE_URL="postgres://kerf:${DB_PW}@kerf-db:5432/kerf?sslmode=disable"
unset DB_PW
kubectl -n kerf create secret generic hub-bootstrap-admin --from-literal=password="$(openssl rand -base64 24)"
```

A minimal PostgreSQL (12 or later; the hub applies its schema itself). Save it as `postgres.yaml` and apply it with
`kubectl -n kerf apply -f postgres.yaml`. Set `storageClassName` if the cluster has no default class.

```yaml
apiVersion: v1
kind: Service
metadata: {name: kerf-db}
spec:
  selector: {app: kerf-db}
  ports: [{name: postgres, port: 5432}]
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: kerf-db}
spec:
  serviceName: kerf-db
  replicas: 1
  selector: {matchLabels: {app: kerf-db}}
  template:
    metadata: {labels: {app: kerf-db}}
    spec:
      securityContext: {runAsNonRoot: true, runAsUser: 70, runAsGroup: 70, fsGroup: 70}
      containers:
        - name: postgres
          image: postgres:18.6-alpine3.23
          env:
            - {name: POSTGRES_USER, value: kerf}
            - {name: POSTGRES_DB, value: kerf}
            - {name: PGDATA, value: /var/lib/postgresql/data/pgdata}
            - {name: POSTGRES_PASSWORD, valueFrom: {secretKeyRef: {name: hub-db, key: POSTGRES_PASSWORD}}}
          ports: [{name: postgres, containerPort: 5432}]
          readinessProbe: {exec: {command: [pg_isready, -U, kerf, -d, kerf]}, periodSeconds: 5}
          resources: {requests: {cpu: 100m, memory: 256Mi}, limits: {memory: 512Mi}}
          volumeMounts: [{name: data, mountPath: /var/lib/postgresql/data}]
  volumeClaimTemplates:
    - metadata: {name: data}
      spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 10Gi}}}
```

The hub reads the URL from the Secret named in `hub.database.existingSecret` (key `DATABASE_URL`). It connects
directly: no RDS Proxy and no transaction-mode pooler, because the hub elects its leader with an advisory lock.

**Check:** `kubectl -n kerf rollout status sts/kerf-db` prints `statefulset rolling update complete 1 pods at revision …`.

## 2. Metrics

Kerf reads the series below from the store behind `hub.config.prometheusURL`. The hub's readiness checks test each one.

| Scrape job | Series | Setting |
|---|---|---|
| `cadvisor` (the kubelets' `/metrics/cadvisor`) | `container_cpu_usage_seconds_total`, `container_memory_working_set_bytes`, `container_cpu_cfs_*` | `scrape_interval: 30s` (at most 60s); keep the `container`, `pod` and `namespace` labels |
| `kube-state-metrics` | `kube_pod_owner`, `kube_node_labels`, and more | the [collectors and node-label allowlist](any-cluster.md#kube-state-metrics-requirements) |

Every series needs the label `cluster=<global.clusterName>` (an external label on remote-write, or a relabel rule).
If your series carry no such label, set the label key and value on the cluster's page after the install (**Clusters**,
then the cluster, then *Edit*).

**Check:** from a pod or a port-forward that reaches the store:

```bash
curl -s "$METRICS_URL/api/v1/query" --data-urlencode 'query=count(kube_pod_owner)' | jq -r '.data.result[0].value[1]'
```

Expected: a number above 0.

## 3. Install

```yaml
# values.yaml
global: {mode: all, clusterName: lab, imageRegistry: ghcr.io/kerf-dev}   # a DNS label; the name the hub registers
hub:
  database: {existingSecret: hub-db}
  auth: {bootstrapAdmin: {existingSecret: hub-bootstrap-admin}}
  config:
    prometheusURL: http://vmsingle.monitoring.svc:8428   # the store from step 2
```

```bash
helm upgrade --install kerf oci://ghcr.io/kerf-dev/kerf/charts/kerf --version <version> \
  -n kerf -f values.yaml --wait --timeout 10m
```

In `mode: all` the chart creates the tokens that the local agent and the pricing service use, and `pricing.vmURL`
defaults to `hub.config.prometheusURL`: neither is in your values. Pricing uses the built-in static price table; on
EKS, `pricing.provider: aws` prices your node types from the AWS price list ([Pricing](../pricing.md)).

**Check:** `kubectl -n kerf get pods` shows the hub (two pods), web, pricing, agent (two) and webhook pods `Running`.

## 4. Sign in

```bash
kubectl -n kerf port-forward svc/web 8080:8080
kubectl -n kerf get secret hub-bootstrap-admin -o jsonpath='{.data.password}' | base64 -d; echo
```

Open <http://localhost:8080> and sign in as `admin` with that password. Use Chrome or Firefox: they accept the
session cookie over plain HTTP on `localhost`, and Safari does not. Change the password (user menu, *Change
password*), then create a user for each person who will look at Kerf under **Admin → Access → Users**
([Sign-in and access](../auth.md)).

## 5. Readiness

Open the cluster `lab` under **Clusters**: the state moves through *Agent connected* and *Metrics OK* to *Ready for
audit*. The hub runs its metrics checks when the cluster is registered, on **Re-check now** and then every
`hub.config.onboarding.checkInterval` (1 hour by default), and the recommender runs once a day. After you fix something in your
scrape configuration, re-check at once, and start a recommender run:

```bash
curl -sS -c cookies -H 'Content-Type: application/json' -d "$(jq -cn --arg p "$ADMIN_PW" '{username:"admin",password:$p}')" \
  http://localhost:8080/v1/auth/login | jq -r .csrfToken > csrf
curl -sS -b cookies -H "X-CSRF-Token: $(cat csrf)" -X POST http://localhost:8080/v1/clusters/lab/readiness/recheck
curl -sS -b cookies -H "X-CSRF-Token: $(cat csrf)" -X POST "http://localhost:8080/v1/admin/recompute?cluster=lab"
```

Set `ADMIN_PW` to the password you currently use. A recompute refreshes only `metrics.recommendations`, so use the
re-check for the metrics checks. Every failing check names its fix: [Cluster readiness](../operate/readiness.md).
Workloads stay in `audit` until you set a policy, so nothing changes in your cluster.

## Remove it

```bash
helm -n kerf uninstall kerf && kubectl delete namespace kerf
kubectl delete crd rightsizingpolicies.kerf.getkerf.dev rightsizingrecommendations.kerf.getkerf.dev
```

The CRDs stay after an uninstall on purpose, with every policy and recommendation. Delete them only if you want those gone.
