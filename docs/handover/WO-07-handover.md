# WO-07 hand-over (mutating admission webhook)

Branch `wo-07-webhook` (pushed, no PR). Deployed: Helm release `webhook`, namespace `resource-optimizer`,
on k3d-ro-spoke-1 and k3d-ro-spoke-2 (`make deploy-webhook`), image
`k3d-ro-registry:5005/resource-optimizer/webhook:dev-20261104-042900`.

## What it does
On pod `CREATE` the webhook resolves the owning workload from ownerReferences
(Pod → ReplicaSet → Deployment, Pod → StatefulSet | DaemonSet, Pod → Job → CronJob), lists the
workload's RightsizingRecommendation from an informer cache by `spec.targetRef` (index
`IndexTargetRef`, never by name), and injects the values only if **all** of these hold:
- `status.phase == Applied`
- `spec.strategy == in-place` (after a fallback the agent sets `rollout`, which stops injection)
- `status.observedGeneration == metadata.generation`. The agent writes spec and status in two
  requests, so a new spec is not treated as live before its status is written.

It never calls the hub and never denies a pod. Every error path allows the pod unchanged.

Injection, per container named in `spec.containers` (regular, init and native sidecar):
- Only the cpu/memory keys the RSR sets are written (auto-patch resources only). Other keys and
  other containers keep the template values. Limits in `removeLimits` are removed, unless the RSR
  also sets that limit, in which case the explicit limit wins.
- If a result would make the pod invalid (a request above its limit), that resource of that
  container keeps the template's request and limit. This is logged, and the other values are still
  injected.
- Pods with pod-level resources (`spec.resources`) are skipped (`pod-level-resources`).
- Annotations on a mutated pod:
  - `resource-optimizer.oz.io/injected-resources`: JSON `[]ContainerResources` with the RSR values the
    pod now carries.
  - `resource-optimizer.oz.io/recommendation-id`: `status.appliedRecommendationID`, falling back to
    `spec.source.recommendationID`.
  - A removed limit cannot be expressed in `[]ContainerResources`, so it does not appear in the
    annotation.
- Nothing is patched if the pod already carries the values (`no-change`).

## Deployment / chart (`deploy/charts/webhook`)
- **No CRDs** in the chart; it needs the CRDs from the agent chart.
- **cert-manager:** a namespaced self-signed `Issuer` and a `Certificate` `webhook-serving` (ECDSA,
  1y, renew 30d before expiry) create Secret `webhook-tls`. The `cert-manager.io/inject-ca-from`
  annotation puts the CA in the MutatingWebhookConfiguration. The server reloads rotated certs
  without a restart. `certManager.issuerRef` uses an existing (Cluster)Issuer instead.
- **MutatingWebhookConfiguration `resource-optimizer-webhook`** (named `<namespace>-<release>`):
  - `failurePolicy: Ignore`, `timeoutSeconds: 2`, `sideEffects: None`, `reinvocationPolicy: Never`.
  - Rule: pods, CREATE only.
  - `namespaceSelector`: excludes `kube-system`, the release namespace, `webhook.excludeNamespaces`,
    and namespaces labelled `resource-optimizer.oz.io/skip-webhook=true`.
  - `objectSelector`: excludes pods labelled `skip-webhook=true` (the webhook's own pods carry it).
  - `matchConditions` (CEL): the API server only calls the webhook for pods that have a controller
    ownerReference, so bare pods never reach it.
- **Deployment `webhook`:** 2 replicas, `maxUnavailable: 0`, PDB `minAvailable: 1`, topology spread on
  hostname and zone (`ScheduleAnyway`; the k3d spokes have one node each). Non-root, read-only
  rootfs. Readiness waits for the informer caches to sync and the TLS server to start.
- **Services:** `webhook:443` (HTTPS for the API server) and `webhook-metrics:8080` (annotated for
  scraping).
- **RBAC (ClusterRole):** get/list/watch on `rightsizingrecommendations`, `replicasets` and `jobs`.
  ReplicaSets and Jobs are read as metadata only.

## Metrics (`ro_webhook_*`, scraped by the spoke vmagents, in the hub VM with `cluster`)
- `ro_webhook_admissions_total{result,reason}` — `result` is `mutated`, `skipped` or `error`:
  - `mutated`: reason is empty.
  - `skipped` reasons: `not-applied`, `not-in-place`, `stale-status`, `no-recommendation`,
    `no-owner`, `unmanaged-owner`, `opt-out`, `cache-not-synced`, `pod-level-resources`,
    `no-change`, `not-pod-create`.
  - `error` reasons: `decode`, `owner-lookup`, `recommendation-lookup`, `patch`.
- `ro_webhook_admission_duration_seconds{result}` histogram. Live: about 2 ms for a mutation.
- `ro_webhook_cache_synced` (0/1).

## Code layout
- `internal/webhook`: `owner.go` (Resolver; on a cache miss for a ReplicaSet created milliseconds
  before its pod, it falls back to a live GET), `mutate.go` (Eligible, Mutate), `handler.go`,
  `metrics.go`, `setup.go` (index, informers, readiness).
- `cmd/webhook`: flags `--port 9443`, `--cert-dir`, `--metrics-bind-address :8080`,
  `--health-probe-bind-address :8081`, `--log-level`.
- Make targets: `build-webhook`, `test-webhook` (downloads envtest 1.35 into `./bin/envtest`),
  `webhook-envtest`, `lint-webhook-chart`, `docker-webhook`, `deploy-webhook`.
- Tests: table-driven unit tests for owner resolution (every kind plus the unmanaged chains),
  phase/strategy/generation filtering, mutation and the handler. There is also an envtest suite
  where a real kube-apiserver calls the webhook through a configuration equivalent to the chart's
  (Pending, Applied, live-lookup fallback, matchConditions, opt-out label, fallback to rollout).
  The envtest suite is skipped when no envtest binaries are found.

## Verification (2026-11-04)
- `go test ./internal/webhook/... -cover`: ok, 92.4% (with envtest; `-race` clean).
  golangci-lint: 0 issues. `helm lint` passes.
- Live, in my own namespace `wo07-test` (now deleted on both spokes). The RSRs were hand-written
  stand-ins for what the agent writes, with status patched to Applied; the sample workloads in
  `default` were not touched.
  - **Owner kinds:** Deployment, StatefulSet, DaemonSet and CronJob (via
    `kubectl create job --from=cronjob/...`) pods all got the recommended values and both annotations.
    The DaemonSet's cpu limit was removed.
  - **ro-spoke-1:** `scale deploy/overprovisioned --replicas=3` made all pods 250m while the
    Deployment template stayed 500m. `-n resource-optimizer scale deploy/webhook --replicas=0` then
    `--replicas=4` and `rollout status` succeeded within a second; the new pod got the template value
    500m (failure is ignored).
  - **Filtering:** phase Pending gave 500m, Applied with spec.strategy rollout gave 500m, and back to
    in-place gave 250m.
  - **ro-spoke-2:** same result: 3× 250m, then with the webhook down 500m on the new pod.
- Hub VM: `max by (cluster)(ro_webhook_cache_synced)` returns 1 for ro-spoke-1 and ro-spoke-2.

## For other tracks
- **Agent (WO-06):** with the webhook, new pods of Applied in-place workloads already match, so the
  agent's drift repair has nothing to do. While an RSR is `Applying`, or its status lags its spec,
  new pods get template values and drift repair covers them.
- **The agent does not overwrite a hand-made Applied RSR** for an audit workload: it keeps the phase
  and only fills `status.effective`. That is how the live test was done. **e2e (WO-09)** can use the
  same trick, or drive the real flow via `overprovisioned` set to auto-patch.
- **Health watch (WO-08):** pods mutated by the webhook carry `injected-resources` and
  `recommendation-id`. After a rollback the agent sets phase `RolledBack`, which stops injection.

## Open points / deviations
- **vmagent duplicate-target errors:** vmagent on both spokes logs "skipping duplicate scrape target
  with identical labels" for every annotated Service (the webhook, the agent and kube-dns), even
  single-port ones. Scraping works; this is WO-01's scrape config, not this chart.
- **Removed limits:** these are not recorded in `injected-resources`, because the contract type has
  no field for them. They are visible by comparing the pod with its template.
- **Conflicting values:** on a request/limit conflict the webhook keeps that resource's template
  values instead of raising the limit. The agent computes consistent targets, so this should not
  happen in practice.
