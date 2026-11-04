# WO-18b handover: production-grade component charts

Branch `wo-18b-chart-hardening`. Charts: `deploy/charts/{hub,web,pricing,agent,webhook}`. Make
targets: `make/charts.mk`. Every new feature is off by default where it needs an add-on (ESO,
Prometheus or VictoriaMetrics operator, an ingress controller, a NetworkPolicy-enforcing CNI);
`ci/values-prod.yaml` in each chart switches everything on with EKS-like values.

## Make targets

| Target | What |
|---|---|
| `make charts-sync` | copy `api/crds/*.yaml` → `agent/files/crds`, `webhook/files/crds`; `dashboards/*.json` → `hub/files/dashboards`; `hub/templates/_common.tpl` → the other charts (prefix replaced) |
| `make charts-check-sync` | fail when a copy drifts (also `go test ./deploy/charts`) |
| `make charts-check-crds` | rendered CRD templates == `api/crds` apart from labels and `helm.sh/resource-policy: keep` |
| `make charts-lint` | the two checks above, then per chart × {defaults, `values-dev.yaml`, `ci/values-prod.yaml`}: `helm lint --strict`, `helm template --include-crds` \| kubeconform (`-strict`, Kubernetes 1.35.0, CRD schemas from the datree CRDs-catalog, `CustomResourceDefinition` skipped: no upstream schema) |
| `make charts-diff-dev` | dev render (release names and values files as `make deploy-*`) of `CHARTS_DIFF_BASE` (default `main`) vs this tree, normalised with yq (key and document order, comments; the hub's random datasource key and `checksum/secrets` masked) |

Tools are installed into `./bin` (`kubeconform-v0.7.0`, `yq-v4.47.1`).

## Value names (the same in every chart; WO-18d maps these from the umbrella)

| Value | hub | web | pricing | agent | webhook | Notes |
|---|---|---|---|---|---|---|
| `global.imageRegistry` | ✓ | ✓ | ✓ | ✓ | ✓ | prefixed to `image.repository`; `image.registry` wins over it; both empty = repository is the full reference (dev) |
| `global.imagePullSecrets` | ✓ | ✓ | ✓ | ✓ | ✓ | names or `{name}`; merged with `imagePullSecrets` |
| `global.externalSecrets.secretStoreRef` | ✓ | ✓ | ✓ | ✓ | (–) | fallback `{kind, name}` for every ExternalSecret (kind defaults to `ClusterSecretStore`) |
| `image.{registry,repository,tag,digest,pullPolicy}` | ✓ | ✓ | ✓ | ✓ | ✓ | `repo:tag@digest` when `digest` is set; empty tag = `appVersion` |
| `imagePullSecrets` | ✓ | ✓ | ✓ | ✓ | ✓ | |
| `replicaCount` | 1 | 2 (dev 1) | 1, >1 fails | 1 | 2 | aliases kept: pricing/agent/webhook `replicas` (default `null`; wins when set) |
| `updateStrategy` | Recreate | – | – | – | – | hub only; RollingUpdate once WO-18a's leader election is in |
| `podDisruptionBudget.{enabled,minAvailable,maxUnavailable}` | on | on | – | on | on | rendered only when >1 pod can run (replicaCount > 1 or HPA minReplicas > 1); `maxUnavailable` wins when set |
| `autoscaling.{enabled,minReplicas,maxReplicas,targetCPUUtilizationPercentage,targetMemoryUtilizationPercentage,behavior}` | off | off | – | – | off | Deployment `replicas` omitted when enabled |
| `topologySpread.{enabled,whenUnsatisfiable,maxSkew}` | on | on | – | on | on | default hostname + zone spread, `ScheduleAnyway`, only when >1 pod can run |
| `topologySpreadConstraints` | ✓ | ✓ | – | ✓ | ✓ | explicit list, replaces the default spread |
| `priorityClassName`, `nodeSelector`, `tolerations`, `affinity` | ✓ | ✓ | ✓ | ✓ | ✓ | |
| `serviceAccount.{create,name,annotations}` | true | **false** | true | true | true | `create: false` on agent/webhook needs `name` (still bound to the ClusterRole) |
| `serviceAccount.automountServiceAccountToken` | false | false | (pod: true only with `provider: aws`, as before) | – | – | |
| `podLabels`, `podAnnotations` | ✓ | ✓ | ✓ | ✓ | ✓ | |
| `extraEnv`, `extraEnvFrom`, `extraVolumes`, `extraVolumeMounts` | ✓ | ✓ | ✓ | ✓ | ✓ | |
| `podSecurityContext`, `securityContext` | ✓ | ✓ | ✓ | ✓ | ✓ | defaults = previous hard-coded values |
| `resources` | ✓ | ✓ | ✓ | ✓ | ✓ | unchanged |
| `ingress.{enabled,className,annotations,hosts[{host,paths[{path,pathType}]}],tls}` | ✓ | ✓ | – | – | – | ALB examples in values.yaml comments; default path `/` Prefix |
| `networkPolicy.enabled` | off | off | off | off | off | |
| `networkPolicy.ingress.from` | ✓ (+ web, pricing pods built in) | ✓ ([] = any) | – | – | – | NetworkPolicyPeers |
| `networkPolicy.ingress.metricsFrom` | ✓ (same port as the API) | – | ✓ | ✓ | ✓ | [] = any |
| `networkPolicy.ingress.apiServerFrom` | – | – | – | – | ✓ | [] = any (API server is not a pod) |
| `networkPolicy.egress.{enabled,ports,to}` | `[443,8428,9090]` + `database.port` | `[8080,443]` | `[443,8080,8428]` | `[443,6443,8080,8428]` | `[443,6443]` | DNS (53 UDP/TCP) always; `ports` = TCP to any destination; `to` = raw egress rules |
| `serviceMonitor.{enabled,kind,namespace,interval,scrapeTimeout,honorLabels,labels,relabelings,metricRelabelings}` | ✓ honorLabels true | – (nginx has no metrics) | ✓ honorLabels true | ✓ | ✓ | `kind: ServiceMonitor` or `VMServiceScrape` |
| `externalSecrets.{apiVersion,refreshInterval,secretStoreRef}` | ✓ | ✓ | ✓ | ✓ | – | chart-wide ESO settings (`external-secrets.io/v1`, `1h`) |

Per-secret ExternalSecret switches (each `{enabled, remoteRef{key,property,version}, refreshInterval, secretStoreRef}`):

| Chart | Value | Secret it builds |
|---|---|---|
| hub | `database.externalSecret` (+ `source: rds\|url`) | `<release>-db`, key `database.existingSecretKey` (`DATABASE_URL`). `rds`: remote JSON `username`/`password` → `postgres://u:p@database.host:database.port/database.name?sslmode=<database.sslmode>&sslrootcert=<path>&<database.params>` |
| hub | `auth.bootstrapAdmin.externalSecret` | `<release>-bootstrap-admin`, key `auth.bootstrapAdmin.existingSecretKey` |
| hub | `onboarding.secretsKey.externalSecret` | `<release>-datasource-key` (ExternalSecret kept on uninstall) |
| web | `token.externalSecret` | `<release>-hub-token`, key `token` (nginx reads it at start: restart after a rotation) |
| pricing | `hub.tokenSecret.externalSecret` | `hub.tokenSecret.name`, key `hub.tokenSecret.key` |
| agent | `hub.tokenSecret.externalSecret` | `hub.tokenSecret.name`, key `hub.tokenSecret.key` |

Every one is exclusive with the matching `existingSecret` / `create` (the chart fails).

Component-specific:

| Value | Chart | Meaning |
|---|---|---|
| `database.{host,port,name,sslmode,sslrootcert,params}` | hub | URL parts for the RDS ExternalSecret; `sslmode` default `verify-full` |
| `database.caBundle.{existingConfigMap,existingSecret,key,pem}` | hub | CA for verify-full, mounted at `/etc/hub/db-ca/ca.pem`; `sslrootcert` defaults to it |
| `startupProbe.{enabled,periodSeconds,failureThreshold}` | hub | on, 5 s × 120 (migrations before listen) |
| `service.annotations` | hub, web, pricing | extra Service annotations; `prometheus.io/*` kept (hub `service.scrape`, pricing `service.annotations`, agent `metrics.scrape`, webhook metrics Service) |
| `prometheusRule.{enabled,kind,namespace,labels,additionalLabels,alerts.<name>.{enabled,for,severity,...},additionalRules}` | hub | `PrometheusRule` or `VMRule`; alerts `agentOffline`, `readinessBlocking`, `recommenderFailing`, `recommenderStale` (`staleAfter`, default 2 × `config.recommender.interval`), `noLeader` (`max(ro_hub_leader) == 0`, WO-18a), `webhookErrors` (`ratio`), `pricingStale` (`staleAfter` 30m) |
| `dashboards.{enabled,namespace,labels,annotations}` | hub | one ConfigMap per `dashboards/*.json`, labels `grafana_dashboard: "1"`, annotation `grafana_folder: resource-optimizer` |
| `hub.caBundle.{existingConfigMap,existingSecret,key,pem}` | agent, pricing | CA of the hub's HTTPS certificate, mounted at `/etc/resource-optimizer/hub-ca`, added to the system roots with `SSL_CERT_DIR=/etc/ssl/certs:/etc/resource-optimizer/hub-ca` (Go reads both; no app change) |
| `containerPort` | web (80), webhook (9443) | web: 8080 with the non-root WO-18c image; webhook: `--port` and the container port (the Service keeps 443 → named port) |
| `readOnlyPaths` | web | emptyDirs mounted when `securityContext.readOnlyRootFilesystem: true`: `/var/cache/nginx`, `/var/run`, `/tmp`, `/etc/nginx/conf.d` |
| `crds.install` (alias `installCRDs`) | agent (true), webhook (false) | CRDs as templates with `helm.sh/resource-policy: keep`; exactly one release per cluster |

## Dev render diff (`make charts-diff-dev`, base `main`)

- web, pricing, webhook: no difference.
- agent: the two CRDs move from `crds/` to templates: only `helm.sh/resource-policy: keep` and the chart labels added.
- hub: new ServiceAccount `hub` (`automountServiceAccountToken: false`), the pod's `serviceAccountName: hub` and
  `automountServiceAccountToken: false`, and a `startupProbe` on `/healthz` (5 s × 120). The hub never calls the
  Kubernetes API, and the probe only delays liveness until the hub listens (after migrations): no runtime
  change in steady state. These are required by the WO (own ServiceAccount, startupProbe) and are not labels.

## Things for other tracks / the orchestrator

1. **CRD adoption on existing clusters.** CRDs installed by the old `crds/` chart have no Helm ownership
   metadata, so the first upgrade to this chart fails with "invalid ownership metadata" unless it runs
   with `--take-ownership` (Helm ≥ 3.17). Done on ro-spoke-1 and ro-spoke-2 (`make deploy-agent
   AGENT_HELM_ARGS=--take-ownership`); later deploys need nothing. WO-18d's upgrade section must say it.
   A deploy of the old chart from `main` in between leaves the CRDs in place (the keep annotation).
2. **`make/agent.mk` (not owned by WO-18b):** `agent-sync-crds` still copies into `deploy/charts/agent/crds/`;
   it must become `make charts-sync` (a `crds/` directory again would make Helm install those copies and
   clash with the templates; `TestNoCRDsDirectory` fails if it happens).
3. **App text (not changed):** `internal/agent/selfcheck/selfcheck.go:207` remediation says Helm installs
   `crds/` only on first install; with WO-18b a `helm upgrade` of the agent chart upgrades the CRDs.
4. **Moved test:** `internal/agent/chart_test.go` (agent `crds/` sync) is replaced by
   `deploy/charts/sync_test.go` (CRD copies in agent and webhook, dashboards in hub, `_common.tpl`, no `crds/`).
5. **WO-18c (web image):** checked with the current image (`docker run --read-only --user 101:101
   --cap-drop ALL` + tmpfs on the four `readOnlyPaths`): nginx starts fine except
   `bind() to 0.0.0.0:80 failed (13: Permission denied)` once unprivileged ports start at 1024 (the
   Kubernetes default; Docker hides it). The image needs `listen 8080` in `nginx/default.conf.template`
   and `EXPOSE 8080`; the pid file and temp paths already work through the emptyDirs; the `user nginx;`
   directive only warns. The orchestrator then sets `containerPort: 8080` plus the non-root
   `podSecurityContext`/`securityContext` from `web/ci/values-prod.yaml` (or makes them the default).
6. **WO-18a:** `noLeader` alert uses `ro_hub_leader`; set the hub `updateStrategy` to RollingUpdate and
   `replicaCount: 2` once leader election is merged.
7. **Alerts need the fleet's series:** agent and webhook series are remote-written to the hub's
   VictoriaMetrics, so the rules must be evaluated there (vmalert / `VMRule`, or a Prometheus reading it).
8. **ESO URL template:** the RDS URL escapes username and password with Go's `urlquery` (space → `+`);
   RDS-generated passwords contain no spaces.
9. Chart versions are unchanged (0.1.0); WO-18c/18d own versioning.
