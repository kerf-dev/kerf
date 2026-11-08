# Kerf hub chart

The Kerf hub: the HTTP API (`api/openapi/hub.yaml`), the recommender scheduler and the
`kerf_recommendation_*` / `kerf_workload_*` metrics. It applies the goose migrations in `db/migrations`
(baked into the image at `/migrations`) on every start.

## Deploy to the shared dev environment

```bash
make deploy-hub                   # build, push to localhost:5005, helm upgrade --install into ro-hub
# equivalent:
docker build -f cmd/hub/Dockerfile -t localhost:5005/kerf/hub:dev-1 . && docker push localhost:5005/kerf/hub:dev-1
helm --kube-context k3d-ro-hub upgrade --install hub deploy/charts/hub -n kerf --create-namespace \
  -f deploy/charts/hub/values-dev.yaml --set image.tag=dev-1 --wait
```

| What | Where |
|---|---|
| API from the host | `http://localhost:18080` (NodePort 30080 on ro-hub) |
| API from a spoke | `http://hub-api.hub.svc:8080` (selector-less Service → hub NodePort 30080) |
| API inside ro-hub | `http://hub.kerf.svc:8080` |
| Metrics | `/metrics` on the same port; the Service carries `prometheus.io/scrape: "true"` so the hub vmagent scrapes it (`honor_labels`, the hub sets `cluster` itself) |
| Health | `/healthz` (liveness), `/readyz` (migrations done and Postgres reachable) |
| Postgres | `postgres.postgres.svc:5432`, database `kerf` (Secret `hub-db`) |
| VictoriaMetrics | `http://victoria-metrics.monitoring.svc:8428` |
| CORS | `http://localhost:5173` (UI dev server) |

On the host (no image): `make run-hub` (= `go run ./cmd/hub --config dev/hub.yaml`, port 8080, same
database and tokens). Do not run it while the in-cluster hub is up: both would run the recommender.

## Dev tokens (Secret `hub-dev-tokens`, namespace `kerf`, ro-hub)

`devSeed.enabled: true` (values-dev.yaml only) registers the clusters `ro-spoke-1`, `ro-spoke-2`,
`synth-a`, `synth-b` and stores the SHA-256 of these tokens. **Development only** — they are in Git.

| Key in the Secret | Role | Scope | Token |
|---|---|---|---|
| `ro-spoke-1` | agent | cluster ro-spoke-1 | `ro-dev-rospoke1-2b8dbdc041b28d5865e7caf7` |
| `ro-spoke-2` | agent | cluster ro-spoke-2 | `ro-dev-rospoke2-22690fedaab92db2f9f3b91b` |
| `synth-a` | agent | cluster synth-a | `ro-dev-syntha-624ec11020f8aadc7458d7a6` |
| `synth-b` | agent | cluster synth-b | `ro-dev-synthb-a06443f1dbc00f61be2e0900` |
| `ui` | ui | read-only, all clusters | `ro-dev-ui-97446cd3a095035d6290abc5` |
| `admin` | admin | everything | `ro-dev-admin-1b71e51dc42ec5deb56e7f06` |

```bash
kubectl --context k3d-ro-hub -n kerf get secret hub-dev-tokens -o jsonpath='{.data.ui}' | base64 -d
curl -s -H "Authorization: Bearer ro-dev-ui-97446cd3a095035d6290abc5" localhost:18080/v1/summary | jq .fleet
curl -s -H "Authorization: Bearer ro-dev-rospoke1-2b8dbdc041b28d5865e7caf7" localhost:18080/v1/clusters/ro-spoke-1/recommendations | jq '.items | length'
```

The agent on a spoke reads its token from its own Secret; copy the value for its cluster from
the table above.

## Values

| Value | Default | Meaning |
|---|---|---|
| `image.repository` / `image.tag` | `localhost:5005/kerf/hub` / `dev` | image (dev: `k3d-ro-registry:5005/...`) |
| `service.type` / `service.nodePort` | `ClusterIP` / – | dev: `NodePort` 30080 |
| `database.url` or `database.existingSecret` | – | Postgres URL, passed as `HUB_DATABASE_URL` |
| `devSeed.enabled`, `.clusters`, `.tokens` | `false` | dev seed and Secret `hub-dev-tokens` |
| `devSeed.users` | `[]` | local users created when missing (Secret `hub-dev-users`); dev: `viewer`, `operator`, `admin` with the passwords in values-dev.yaml |
| `auth.cookieSecure`, `.sessionTTL`, `.maxFailedLogins`, `.lockoutDuration` | `true`, `12h`, `5`, `15m` | browser sessions; dev: `cookieSecure: false` (plain HTTP) |
| `auth.bootstrapAdmin.username`, `.password` or `.existingSecret`/`.existingSecretKey` | `admin`, – | initial admin, created only on a database without users |
| `auth.bootstrapAdmin.generate` | `true` | no password set (and no devSeed): the chart generates one (Secret `<release>-bootstrap-admin`, kept) and the admin must change it at first sign-in |
| `localTokens.enabled` | `true` | umbrella chart, `global.mode` hub or all: generate the pricing token and (mode all) the local agent's token in Secret `<release>-local-tokens`; the hub accepts them at start-up |
| `config` | see values.yaml | the hub config file (`internal/hub/config.go`): `prometheusURL`, `corsOrigins`, `agentStaleAfter`, `recommender.*`, `history.*` |
| `database.externalSecret` (`source: rds\|url`), `.host`, `.port`, `.name`, `.sslmode`, `.params`, `.caBundle` | off, –, 5432, `kerf`, `verify-full` | External Secrets Operator builds Secret `<release>-db` from an RDS-managed secret (JSON username/password) plus host/port/name, `sslmode` and `sslrootcert` = the mounted CA bundle (`/etc/hub/db-ca/ca.pem`) |
| `auth.bootstrapAdmin.externalSecret`, `onboarding.secretsKey.externalSecret` | off | the same for the first admin's password and the datasource encryption key (kept on uninstall) |
| `replicaCount`, `updateStrategy`, `podDisruptionBudget`, `autoscaling`, `topologySpread` | 2, RollingUpdate, on (>1 pod), off, on (>1 pod) | replicas elect a leader on Postgres: only the leader runs the recommender, readiness checks and sweeps; every replica serves the API |
| `serviceAccount.{create,name,annotations,automountServiceAccountToken}` | true, release name, –, false | IRSA / Pod Identity annotations |
| `startupProbe.{enabled,periodSeconds,failureThreshold}` | on, 5, 120 | migrations run before the hub listens (10 minutes) |
| `ingress`, `networkPolicy`, `serviceMonitor`, `prometheusRule`, `dashboards` | off | ALB example in values.yaml; alerts (agent offline, readiness blocking, recommender failing / stale, no leader, webhook errors, pricing stale, update available); Grafana dashboard ConfigMaps (`grafana_dashboard: "1"`) |
| `updateCheck.enabled`, `.interval`, `.url` | `true`, `24h`, GitHub releases API of kerf-dev/kerf | the leader checks for a newer stable Kerf release (Admin → Maintenance, `kerf_update_available`); an unauthenticated GET that sends nothing about the installation. `false` for air-gapped sites; a proxy via `extraEnv` `HTTPS_PROXY` |
| `notifications.baseURL`, `.rateLimitPerMinute`, `.allowInsecureHTTP`, `.channels` | `""`, `20`, `false`, `[]` | Slack, Teams and signed webhook notifications ([docs/notifications.md](../../../docs/notifications.md)): `baseURL` is the web UI address the messages link to; `channels` define read-only channels `{name, type: slack\|teams\|webhook, existingSecret, urlKey, secretKey, enabled}` whose URL (and a webhook's signing secret) come from a Secret. Rules and the digest are set in the UI. Alert `KerfNotificationDeliveryFailing` (`prometheusRule.alerts.notificationFailing`). The hub needs egress to the destinations. |

Every production feature is switched on in `ci/values-prod.yaml` (`make charts-lint`). Names shared by
all component charts: [deploy/charts/README.md](../README.md#value-names-shared-by-the-component-charts).

Recommender settings under `config.recommender` (all optional; defaults in `internal/recommender/config.go`):
`interval` (production 24h, dev 5m), `backfill.runs`/`every` (dev: 29 × 24h of history before the
first regular run, computed from the stored metrics and filled in between regular runs), `incompleteMetricsGrace` (default 30m: how long a run whose inventory suddenly halves is
refused as incomplete metrics), `step`, `rateWindow`, `riskWindow`, `inventoryLookback`, `throttleThreshold`,
`restartThreshold`, `throttleIncrease`, `oomIncrease`, `minCPU`, `minMemory`, `lowConfidence`,
`defaultProfile`, `defaultCPUCoreHourUSD`, `defaultMemoryGiBHourUSD` (fallback when the cluster has no
`kerf_*_hourly_cost_usd` series) and `profiles.<dev|production|high-availability>.{window, cpuPercentile,
memoryPercentile, memoryPeakWindow, cpuHeadroom, memoryBuffer}`.
History: `history.fullResolution` keeps every run, older recommendations are thinned to one per
workload and day, `history.retention` deletes the rest (the latest per workload is always kept).

Settings stored through the admin API (`PUT /v1/admin/settings/recommender|pricing`) override
`config.recommender.interval`, the profiles and `history.retention` from the next run on (the
interval at once); without stored settings the config file applies.

## Log in (admin area)

```bash
curl -s -c jar -H 'Content-Type: application/json' -d '{"username":"admin","password":"ro-dev-admin-pass"}' localhost:18080/v1/auth/login
# writes need the csrfToken of the login response:
curl -s -b jar -H "X-CSRF-Token: <csrfToken>" -H 'If-Match: "0"' -H 'Content-Type: application/json' -X PUT \
  -d '{"value": {...}, "reason": "..."}' localhost:18080/v1/admin/settings/recommender
```

## Recompute now

`POST /v1/admin/recompute[?cluster=<name>]` (admin token) queues a recommender run and returns `202`
with its `runId`; completion shows as `lastRecommenderRunAt` in `GET /v1/clusters`. The run uses data
up to the last full 5-minute step.

```bash
curl -s -X POST -H "Authorization: Bearer $(kubectl --context k3d-ro-hub -n kerf get secret hub-dev-tokens -o jsonpath='{.data.admin}' | base64 -d)" \
  'http://localhost:18080/v1/admin/recompute?cluster=ro-spoke-1'
```
