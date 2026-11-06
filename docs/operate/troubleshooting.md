# Troubleshooting

Start from the symptom. The commands assume the release `kerf` in the namespace `kerf` and leave out `--context`:
run them against the cluster in question. The [EKS guide](../install/eks.md#10-troubleshooting) covers webhook
timeouts, a missing hub leader, an offline agent, stale metrics and External Secrets errors. This page adds the
rest and does not repeat it.

First look, on any cluster:

```bash
helm -n kerf status kerf
kubectl -n kerf get pods -l app.kubernetes.io/instance=kerf
```

Then open **Admin → Maintenance** for versions and the cluster's readiness page for failing checks
([readiness](readiness.md)).

## Agent not connecting

Readiness `agent.connected` fails, **Maintenance** shows the agent as `never` or `stale`, or the alert
`KerfAgentOffline` fires.

```bash
kubectl -n kerf get externalsecret kerf-agent-token       # with External Secrets: SecretSynced True?
kubectl -n kerf logs deploy/kerf-agent | grep -iE 'error|401|x509' | tail
kubectl -n kerf logs deploy/kerf-agent | grep -m1 'starting agent'
```

| Symptom | Cause | Fix |
|---|---|---|
| `401` in the agent log | The token is wrong, revoked, or the Secret `agent.hub.tokenSecret.name` (default `kerf-agent-token`) is missing. Without the Secret the agent runs but behaves as offline. | Create a new token with role `agent` for the cluster (**Admin → Access → Tokens**), update the Secret. The agent re-reads it on every request: no restart. |
| `x509: certificate signed by unknown authority` | The hub's certificate is from a private CA. | Set `agent.hub.caBundle` (`existingConfigMap`, `existingSecret` or `pem`). |
| Timeouts | No route to the hub, the hub's ingress allowlist, or the agent's NetworkPolicy blocks egress. | Test from the cluster with the `hub-probe` command in the [EKS guide](../install/eks.md#10-troubleshooting). Check `agent.networkPolicy.egress.ports`. |
| The `starting agent` line shows another `cluster` or `hub` | `global.clusterName` or `agent.hub.url` is wrong. A token belongs to one cluster. | Fix the values and run `helm upgrade`. The name must equal the cluster's name in the hub. |
| Connected, but readiness shows `agent.version` or `agent.crds` | Version skew, or old CRDs. | [Upgrade](upgrade.md#order-and-version-skew). |

## No recommendations

The cluster is below *Ready for audit*, `metrics.recommendations` fails, or workloads show nothing.

- **The metrics checks fail.** Fix them first: the recommender needs *Metrics OK*. [Readiness](readiness.md#metrics-ok).
- **No run yet.** The recommender runs once a day by default (`config.recommender.interval`), and the hub leader
  runs it. Start one as an admin: `POST /v1/admin/recompute?cluster=<cluster>`.
- **The run saw no workloads.** A new cluster needs a few minutes of metrics. If the log says `"workloads":0`, run
  it again after five minutes:

  ```bash
  kubectl -n kerf logs -l app.kubernetes.io/name=hub | grep "recommender run finished"
  ```

- **No leader.** `sum(kerf_hub_leader)` must be 1: [Hub HA and leader election](#hub-ha-and-leader-election).
- **Recommendations exist, but nothing changes.** In `audit` mode nothing should. Otherwise the agent reports why in a `skipped`
  or `blocked` event: [next section](#a-change-was-blocked-or-rolled-back). A workload with too little history
  gets `LearningPeriod` or `LowConfidence`, and `metrics.history` warns.
- **Namespaces are missing.** `policy.namespace-coverage` lists namespaces excluded by policy or label.

## Metrics source and pricing

Stale data and the cluster label are in the [EKS guide](../install/eks.md#10-troubleshooting). More:

- **`datasource.reachable` fails.** Use **Admin → Metrics sources → Test**: it runs the metrics checks against the
  datasource and shows `httpStatus` and `error`. Credentials are write-only: to change them, send a new value.
- **Pricing shows "skipped" nodes.** `kerf_pricing_nodes{status="skipped",reason}` names why. The log has the node:
  `kubectl -n kerf logs deploy/pricing`.

  | `reason` | Cause | Fix |
  |---|---|---|
  | `no-instance-type` | `kube_node_labels` has no `node.kubernetes.io/instance-type` | kube-state-metrics label allowlist |
  | `no-region` | No region or zone label | Label the nodes, or set `pricing.defaultRegion` |
  | `unknown-capacity-type` | Capacity type label other than on-demand or spot | Fix the label |
  | `no-allocatable` | No allocatable CPU or memory series | Enable the nodes collector |
  | `no-price` | The price table has no entry for that region, instance type and capacity type | Add it ([price table](../install/any-cluster.md#supply-a-price-table)) |
  | `provider-error` | The provider call failed | Read the pricing log |

- **The pricing pod does not start.** On a new hub it waits in `ContainerCreating` for its token Secret
  ([EKS guide, step 4.5](../install/eks.md#45-first-sign-in-and-the-pricing-token)). With a custom
  `pricing.prices`, an invalid file ends the process: the log says which instance type or region is wrong.

## Webhook and cert-manager

The webhook never denies a pod. On an error the pod starts with its template values, and the agent resizes it
later (`failurePolicy: Ignore`).

```bash
kubectl -n kerf get pods,certificate -l app.kubernetes.io/name=kerf-webhook
kubectl -n kerf logs deploy/webhook
kubectl get --raw "/api/v1/namespaces/kerf/services/https:webhook:443/proxy/healthz"
```

The last command should end in `the server could not find the requested resource` (the webhook answered 404, so
the port is open). A timeout means the control plane cannot reach the nodes on the webhook port (9443).

| Symptom | Cause | Fix |
|---|---|---|
| `webhook.cert-manager` fails, or the Certificate never becomes `Ready` | cert-manager is not installed or not working | Install cert-manager on the workload cluster. |
| `webhook.healthy`: "has no caBundle" | cert-manager's CA injector has not written the CA yet, or Argo CD reverts it | Wait for the Certificate to be `Ready`. With Argo CD, ignore the webhook's `caBundle` ([EKS guide, step 7](../install/eks.md#7-argo-cd-instead-of-helm)). |
| "no ready endpoints" | The webhook pods are not ready | Pod logs and events. |
| `KerfWebhookErrors` | `kerf_webhook_admissions_total{result="error"}` by `reason`: `decode`, `owner-lookup`, `recommendation-lookup`, `patch` | Webhook log and API server access. |
| Pods are not mutated, no errors | `result="skipped"`: for example `cache-not-synced` (`kerf_webhook_cache_synced` is 0), `no-recommendation`, `opt-out` | Wait for the caches to sync. A skip is not a fault. |

## A change was blocked or rolled back

Each decision of the agent is an event. See them in **Activity** and on the workload's page, with
`kubectl get rsr -A` (the `Phase` column), and with `kubectl -n <namespace> describe rsr <name>`: the agent
records its events there too ([events contract](../contracts/events.md)).

| Reason | What to do |
|---|---|
| `LearningPeriod`, `LowConfidence` | Wait for history, or lower `guardrails.learningPeriod` or `minConfidence` in the policy. |
| `Cooldown` | Wait. It also follows every rollback, for the decreases of that resource. |
| `HPACoupled` | The workload's HPA is not coordinated: [HPA](../concepts/hpa.md). |
| `Throttled`, `OOMKilled` | A decrease was refused because the resource is under pressure. Nothing is wrong. |
| `FreezeWindow`, `OutsideMaintenanceWindow`, `Paused` | A freeze window, a deferred strategy waiting for its window, or automation paused under **Admin → Automation**. |
| `UnsupportedNode`, `RestartPolicyRequired`, `JVMFixedHeap` | The node or container does not allow an in-place change: [apply strategies](../concepts/apply-strategies.md), [JVM](../concepts/jvm.md). |
| `SingleReplicaUnsafe` | The change would leave a single replica without a ready pod. Opt in with the annotation `kerf.getkerf.dev/allow-disruption: "true"`. |
| `HubUnreachable` | The agent cannot reach the hub: [Agent not connecting](#agent-not-connecting). |
| `rolled_back`: `OOMKills`, `Restarts`, `Throttling` | The health watch restored the old values. The thresholds are in `guardrails.rollback` ([rollback](../concepts/rollback.md)). Check the workload before you raise the cap. |

A throttling rollback needs the agent's metrics source: `agent.health.metricsURL`. Empty disables it.

## Hub HA and leader election

Two hub replicas serve the API. One of them, the leader, holds a Postgres advisory lock and runs the recommender,
the readiness checks and the sweeps.

```bash
for p in $(kubectl -n kerf get pods -l app.kubernetes.io/name=hub -o name); do
  kubectl get --raw "/api/v1/namespaces/kerf/pods/${p#pod/}:8080/proxy/metrics" | grep -E '^kerf_hub_leader(_transitions_total)? '
done
```

- **Sum of `kerf_hub_leader` is 0** (`KerfNoHubLeader`): see the EKS guide. The usual cause is RDS Proxy or a
  transaction-mode pooler between the hub and Postgres.
- **`kerf_hub_leader_transitions_total` keeps rising:** the leader loses its connection to the database. Check the
  database and the network between them.
- **`/readyz` answers `not ready`:** Postgres is unreachable, or migrations have not finished. The first pod
  applies them under a lock and the others wait for it, up to 10 minutes.
- **Replicas on different versions:** a rolling upgrade is in progress. If it is stuck, run
  `kubectl -n kerf rollout status deploy/hub`.

## Login problems

| Symptom | Cause | Fix |
|---|---|---|
| `401 invalid_credentials` | Wrong user or password. The answer is the same for both. | Check both. Another admin can reset the password (`POST /v1/admin/users/<username>/reset-password`). |
| `429 rate_limited` or `account_locked` | Too many failed sign-ins: 5 by default (`hub.auth.maxFailedLogins`), locked for 15 minutes (`hub.auth.lockoutDuration`). | Wait for the `Retry-After` time, or have an admin reset the password. |
| Signed out after a while | Sessions last 12 hours (`hub.auth.sessionTTL`). | Sign in again. |
| Signed in, but everything is read-only | The session cookie is not sent back, so requests use the read-only `ui` token. `GET /v1/me` shows `authMethod` `token`. | `hub.auth.cookieSecure` is `true`: serve the UI over HTTPS. |
| `403 csrf_invalid` (API clients) | A write with a session needs `X-CSRF-Token`, which login returns. | Send it. Bearer-token requests need none. |
| The `admin` password in the Secret does not work | The hub creates the bootstrap admin only when the database has no users. Later changes to the Secret do nothing. | Have another admin reset it. |

## Related

- [Install on Amazon EKS: troubleshooting](../install/eks.md#10-troubleshooting)
- [Cluster readiness](readiness.md)
- [Alerts and dashboards](alerts-dashboards.md)
- [Events contract](../contracts/events.md)
- [Admin area contract](../contracts/admin.md)
