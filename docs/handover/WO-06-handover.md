# WO-06 hand-over (agent)

Branch `wo-06-agent` (pushed; commits a46e40e, 07bcb11, 3081e1c on top of main f84e6ea).
Deployed: Helm release `agent`, namespace `resource-optimizer`, on k3d-ro-spoke-1 and k3d-ro-spoke-2
(`make deploy-agent`), pointing at the real hub `http://hub-api.hub.svc:8080`, stub hub disabled.
Chart docs + RBAC table: `deploy/charts/agent/README.md`.

## OPEN: agent token Secret not created (my attempt was denied by the permission classifier)
The agents currently get `401 Missing bearer token` from the hub and run offline (nothing applied).
To connect them, per spoke (token = key `<cluster>` of Secret `hub-dev-tokens` on ro-hub):

    for c in ro-spoke-1 ro-spoke-2; do
      kubectl --context k3d-ro-hub -n resource-optimizer get secret hub-dev-tokens -o jsonpath="{.data.$c}" | base64 -d \
        | kubectl --context k3d-$c -n resource-optimizer create secret generic resource-optimizer-agent-token --from-file=token=/dev/stdin
    done

The token file is re-read on every request: no restart needed.

## Layout
- `cmd/agent` (also `agent stub-hub` subcommand), `internal/agent/{hubclient,hubstub,policy,guardrails,workload,apply,controller}`.
- `hubclient` types are hand-written; `spec_test.go` checks every struct against `api/openapi/hub.yaml`
  (property names both ways, required ⇒ not omitempty) and the three agent paths.
- Make: `test-agent` (downloads envtest 1.35 into ./bin/envtest), `deploy-agent`, `docker-agent`,
  `agent-sync-crds`, `agent-crds`, `lint-agent-chart`. envtest tests skip when no binaries.
- go.mod: + controller-runtime v0.25.2, client-go v0.37.2, robfig/cron/v3 v3.0.1, google/uuid v1.6.0.

## For the hub (WO-04)
- Status: full batch after every pull (+ changed items every reportInterval). Every workload is reported,
  with `settings` (always all four fields filled) even before it has a recommendation.
- Stale check: recommendation `settings` vs effective policy, missing fields = global defaults, ratio compared numerically.
- Learning period uses `dataCoverage.covered`; if absent, `ratio × window`.
- Events: skipped/blocked are emitted once per (workload, recommendation id, type, reason). With a new id
  every hub run (dev 5 min), every audit workload produces one `skipped ModeAudit` event per run, and an
  applied workload in cooldown one `blocked Cooldown` per run. Contract-conformant but noisy; the
  orchestrator may want "once per (workload, reason) until it changes" instead (contract change).
- Partial guardrails (Throttled/OOMKilled/RestartPolicyRequired on one resource) emit `blocked` with
  `resources: [cpu|memory]` while the other resource is still applied.

## For the webhook (WO-07)
- CRDs ship in the agent chart's `crds/` (Helm installs only if missing, never upgrades/deletes) — a webhook
  chart can ship the same files safely. Installed on both spokes.
- Inject only when `status.phase == Applied` and `spec.strategy == in-place`. `spec.containers` carries values
  **only for auto-patch resources** (e.g. cpu only); a workload with no auto-patch resource gets the full
  proposed values for display but never reaches Applied.
- `spec.containers[].removeLimits`: in-place cannot remove a limit on running pods, so removals only take
  effect through the webhook on new pods (or rollout).
- The agent also resizes new pods that started with template values while Applied (drift repair), so it
  works without the webhook; with the webhook these pods already match and nothing happens.
- Agent annotates resized pods with `previous-resources` and `recommendation-id`; its own pods carry
  `skip-webhook=true`.

## Decisions / deviations to review
- Swap (D-007): `nodeInfo.swap.capacity > 0` blocks in-place **memory** changes only. k3d nodes report
  1 GiB swap (Docker Desktop VM) while kubelet's NoSwap keeps pods off it; CPU resize is unaffected.
  Static CPU/Memory Manager is not visible in the API: configurable `unsupportedNodeSelectors` (labels).
- Cluster default level = chart value `clusterDefaults` (a RightsizingPolicy spec). Empty = global audit.
- `readOnly: true` caps every mode at audit and drops all workload/pod write verbs from the ClusterRole.
- Fallback to rollout is sticky until the configured strategy changes. Release of an in-place workload
  leaves running pods at the resized values until they are recreated (template never touched).
- Condition reasons used only on RSR conditions (never as events): `PolicyResolved`, `NotBlocked`,
  `Applying`, `Pending`, `Released`.
- Strategy `deferred` (WO-10) is applied like rollout inside a maintenance window, blocked
  (`OutsideMaintenanceWindow`) otherwise.
