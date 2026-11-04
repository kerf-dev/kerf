# hub chart (WO-04)

The resource-optimizer hub: the HTTP API (`api/openapi/hub.yaml`), the recommender scheduler and the
`ro_recommendation_*` / `ro_workload_*` metrics. It applies the goose migrations in `db/migrations`
(baked into the image at `/migrations`) on every start.

## Deploy to the shared dev environment

```bash
make deploy-hub                   # build, push to localhost:5005, helm upgrade --install into ro-hub
# equivalent:
docker build -f cmd/hub/Dockerfile -t localhost:5005/resource-optimizer/hub:dev-1 . && docker push localhost:5005/resource-optimizer/hub:dev-1
helm --kube-context k3d-ro-hub upgrade --install hub deploy/charts/hub -n resource-optimizer --create-namespace \
  -f deploy/charts/hub/values-dev.yaml --set image.tag=dev-1 --wait
```

| What | Where |
|---|---|
| API from the host | `http://localhost:18080` (NodePort 30080 on ro-hub) |
| API from a spoke | `http://hub-api.hub.svc:8080` (selector-less Service → hub NodePort 30080, WO-01) |
| API inside ro-hub | `http://hub.resource-optimizer.svc:8080` |
| Metrics | `/metrics` on the same port; the Service carries `prometheus.io/scrape: "true"` so the hub vmagent scrapes it (`honor_labels`, the hub sets `cluster` itself) |
| Health | `/healthz` (liveness), `/readyz` (migrations done and Postgres reachable) |
| Postgres | `postgres.postgres.svc:5432`, database `resource_optimizer` (Secret `hub-db`) |
| VictoriaMetrics | `http://victoria-metrics.monitoring.svc:8428` |
| CORS | `http://localhost:5173` (UI dev server, WO-14) |

On the host (no image): `make run-hub` (= `go run ./cmd/hub --config dev/hub.yaml`, port 8080, same
database and tokens). Do not run it while the in-cluster hub is up: both would run the recommender.

## Dev tokens (Secret `hub-dev-tokens`, namespace `resource-optimizer`, ro-hub)

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
kubectl --context k3d-ro-hub -n resource-optimizer get secret hub-dev-tokens -o jsonpath='{.data.ui}' | base64 -d
curl -s -H "Authorization: Bearer ro-dev-ui-97446cd3a095035d6290abc5" localhost:18080/v1/summary | jq .fleet
curl -s -H "Authorization: Bearer ro-dev-rospoke1-2b8dbdc041b28d5865e7caf7" localhost:18080/v1/clusters/ro-spoke-1/recommendations | jq '.items | length'
```

The agent (WO-06) on a spoke reads its token from its own Secret; copy the value for its cluster from
the table above.

## Values

| Value | Default | Meaning |
|---|---|---|
| `image.repository` / `image.tag` | `localhost:5005/resource-optimizer/hub` / `dev` | image (dev: `k3d-ro-registry:5005/...`) |
| `service.type` / `service.nodePort` | `ClusterIP` / – | dev: `NodePort` 30080 |
| `database.url` or `database.existingSecret` | – | Postgres URL, passed as `HUB_DATABASE_URL` |
| `devSeed.enabled`, `.clusters`, `.tokens` | `false` | dev seed and Secret `hub-dev-tokens` |
| `config` | see values.yaml | the hub config file (`internal/hub/config.go`): `prometheusURL`, `corsOrigins`, `agentStaleAfter`, `recommender.*`, `history.*` |

Recommender settings under `config.recommender` (all optional; defaults in `internal/recommender/config.go`):
`interval` (production 24h, dev 5m), `backfill.runs`/`every` (dev: 29 × 24h of history before the
first regular run, computed from the stored metrics and filled in between regular runs), `incompleteMetricsGrace` (default 30m: how long a run whose inventory suddenly halves is
refused as incomplete metrics), `step`, `rateWindow`, `riskWindow`, `inventoryLookback`, `throttleThreshold`,
`restartThreshold`, `throttleIncrease`, `oomIncrease`, `minCPU`, `minMemory`, `lowConfidence`,
`defaultProfile`, `defaultCPUCoreHourUSD`, `defaultMemoryGiBHourUSD` (fallback when the cluster has no
`ro_*_hourly_cost_usd` series) and `profiles.<dev|production|high-availability>.{window, cpuPercentile,
memoryPercentile, memoryPeakWindow, cpuHeadroom, memoryBuffer}`.
History: `history.fullResolution` keeps every run, older recommendations are thinned to one per
workload and day, `history.retention` deletes the rest (the latest per workload is always kept).

## Recompute now

`POST /v1/admin/recompute[?cluster=<name>]` (admin token) queues a recommender run and returns `202`
with its `runId`; completion shows as `lastRecommenderRunAt` in `GET /v1/clusters`. The run uses data
up to the last full 5-minute step.

```bash
curl -s -X POST -H "Authorization: Bearer $(kubectl --context k3d-ro-hub -n resource-optimizer get secret hub-dev-tokens -o jsonpath='{.data.admin}' | base64 -d)" \
  'http://localhost:18080/v1/admin/recompute?cluster=ro-spoke-1'
```
