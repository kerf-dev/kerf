# WO-12 JVM detection and heap-aware memory recommendations

- **Phase / wave:** 3 / 1
- **Depends on:** WO-04, WO-06 merged
- **Owns:** `internal/recommender/jvm.go` (new), `internal/agent/workload/jvm.go` (new), additive risk flag / explanation fields in `api/openapi/hub.yaml` and `docs/contracts/` (approved at kickoff)
- **Decisions:** D-007

## Goal
Memory recommendations for JVM containers are safe: they respect the heap configuration, and the agent never shrinks memory below what the JVM will try to use.

## Deliverables
- Detection (agent, reported in workload-status): image name/tag heuristics (`temurin`, `jre`, `jdk`, `corretto`), `JAVA_TOOL_OPTIONS` / `JDK_JAVA_OPTIONS` / `JAVA_OPTS` env and container args; parse `-Xmx`, `-Xms`, `-XX:MaxRAMPercentage`, `-XX:MaxRAM`.
- Recommender: for JVM containers memory = max(current rule, heap max + non-heap estimate); with fixed `-Xmx` → memory is audit-only with flag `jvm-fixed-heap` and an explanation; with `MaxRAMPercentage` → normal rule plus a floor so heap = pct × limit ≥ observed heap need.
- Memory changes on JVM containers require `resizePolicy: RestartContainer` or `rollout` (agent already blocks otherwise; keep and test).
- Synthetic data: WO-02's `jvm` profile and `dev/workloads/jvm-maxram`, `jvm-xmx` are the fixtures.

## Definition of done
`jvm-xmx` is never auto-patched for memory (flagged), `jvm-maxram` gets a recommendation whose heap after the change covers its observed peak; explanations say why.

## Verification
```bash
go test ./internal/recommender/... ./internal/agent/... -cover
make lint test
curl -s -H "Authorization: Bearer $UI_TOKEN" 'localhost:18080/v1/workloads?search=jvm' | jq '.items[] | {id, riskFlags}'
```
