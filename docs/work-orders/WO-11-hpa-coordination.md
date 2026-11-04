# WO-11 HPA coordination

- **Phase / wave:** 3 / 2
- **Depends on:** WO-04, WO-06 merged
- **Owns:** `internal/recommender/hpa.go` (new), `internal/agent/apply/hpa.go` (new), additive fields in `api/openapi/hub.yaml` and the RSR spec (approved at kickoff)
- **Decisions:** D-011 (this WO lifts it)

## Goal
Workloads with a utilization-based HPA can be rightsized without changing their scaling behaviour.

## Deliverables
- Recommender: for `hpa-coupled` workloads compute a request change together with a new HPA `averageUtilization` target so that `usage / (request × target)` at P95 is unchanged; never let the target leave 40–90 %.
- Recommendation carries `hpa: {name, metric, currentTarget, recommendedTarget}`.
- Agent: in `auto-patch` apply both in one change (`rollout` strategy, HPA patched with field manager `resource-optimizer`, Argo CD ignore covers it); in-place is not used for HPA workloads.
- Guardrail: skip if HPA is at `maxReplicas` in the window, or uses external/custom metrics.

## Definition of done
On k3d, `dev/workloads/hpa-cpu` in auto-patch gets lower requests and a matching HPA target in one change; replica count stays within ±1 under the same load.

## Verification
```bash
go test ./internal/recommender/... ./internal/agent/... -cover
make lint test e2e
```
