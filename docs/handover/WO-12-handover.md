# WO-12 hand-over (JVM-aware memory)

Branch `wo-12-jvm` (pushed, no PR), rebased on `origin/main` 828458d (WO-04c and WO-08b merged).

## What it does
- **Agent detection** (`internal/agent/workload/jvm.go`). A container is a JVM if any of these hold:
  - its image matches `java|openjdk|jdk|jre|temurin|corretto|amazoncorretto|zulu|graalvm|semeru|liberica|adoptopenjdk` (as a token);
  - its command or args run `java` or pass heap options (`sh -c "java ..."` is split into words);
  - it sets `JAVA_TOOL_OPTIONS` / `JDK_JAVA_OPTIONS` / `JAVA_OPTS` / `_JAVA_OPTIONS`.

  Parsed options: `-Xmx`, `-XX:MaxHeapSize`, `-Xms`, `-XX:InitialHeapSize`, `-XX:MaxRAMPercentage`,
  `-XX:MaxRAMFraction` (converted to a percentage) and `-XX:MaxRAM`.
  - Precedence follows the JVM: `JAVA_TOOL_OPTIONS` < `JDK_JAVA_OPTIONS` < `JAVA_OPTS` < command/args < `_JAVA_OPTIONS`.
  - Env values set via `valueFrom` are not read; they are listed as `unresolved`.
  - The result is reported in `WorkloadStatus.jvm`. Nothing is reported when `RightsizingPolicy.spec.jvm.detect` is false.
- **Agent guardrail.** A memory change on a JVM with a fixed `-Xmx` is refused with reason
  `JVMFixedHeap`. The trigger is either live detection or the hub's per-container flag
  `jvm-fixed-heap`. This applies to every strategy (rollout too), and the check runs before the D-007
  `RestartPolicyRequired` check. CPU is still applied. The existing D-007 rule is kept and tested:
  in-place memory changes on JVM/Python containers need `RestartContainer`. `HeapBound` also
  recognises the new image tokens (it missed `amazoncorretto`).
- **Hub.** The reported `jvm` list is stored in `workload_status.settings` under the key `jvm`, so no
  table change was needed. `Store.ReportedJVM` reads it, `GET /v1/workloads/{id}` returns
  `status.jvm`, and the job passes it to the engine through `Engine.JVM`.
- **Recommender** (`internal/recommender/jvm.go`). The memory floor depends on how the heap is sized:
  - **Fixed `-Xmx`:** memory ≥ max(Xmx, Xms) + non-heap, where non-heap = max(64Mi, 10 % of the heap).
    The container and the workload get flag `jvm-fixed-heap` (memory audit-only) and a note.
  - **`MaxRAMPercentage`, or no flags (JVM default 25 %):** the usage rule applies, plus a floor so
    that pct × limit ≥ heap need. Heap need = observed peak working set, capped at the current max
    heap (pct × min(current limit, MaxRAM)); there are no heap metrics.
    - The floor applies when the limit follows the request (the default). A kept limit already
      satisfies it.
    - Without a limit, the heap follows the node's memory and only the `-Xms` floor applies.
  - **Without an agent report** (synth-a/b): JVMs are found from `kube_pod_container_info{image}`
    (new query `Queries.ContainerImages`), JVM defaults are assumed, and a note says so.
  - **Explanation:** `explanation.memory.jvm` carries heapSizing, detectedBy, source,
    maxRAMPercentage, maxHeapBefore/After, heapNeed, nonHeap and floor, plus notes.
  - **Config:** `recommender.Config.JVM` (NonHeapMinBytes, NonHeapRatio, DefaultMaxRAMPercentage,
    DetectByImage). It is not yet exposed in the hub YAML, which is WO-04's file.

## Contract changes (additive, optional)
- `api/openapi/hub.yaml`:
  - RiskFlag `jvm-fixed-heap`;
  - `WorkloadStatus.jvm` (`JVMContainer`);
  - `ResourceExplanation.jvm` (`JVMExplanation`).
- `docs/contracts/events.md`: reason `JVMFixedHeap`.
- `api/v1alpha1`: constants `ReasonJVMFixedHeap` and `RiskJVMFixedHeap`. Constants only; the CRDs
  are unchanged and `verify-generate` passes.
- **`db/migrations/0003_jvm_fixed_heap_flag.sql`** (not listed in the WO, but required). The
  `recommendations.risk_flags` CHECK constraint lists the flags, so the first hub run with the new
  flag failed. The new store test `TestAllRiskFlagsStorable` covers every API flag.
- `web/src/api/schema.d.ts` regenerated. `RISK_FLAGS` and `RISK_INFO` gained `jvm-fixed-heap`; the
  `Record<RiskFlag, …>` type required the entry. tsc, eslint, prettier and vitest (39 tests) pass.

## Verification (2026-11-04)
- `make ci` on the rebased branch: build, vet, golangci-lint 0 issues, all tests ok (including agent
  envtest and DB tests with `RO_TEST_DATABASE_URL`), verify-generate ok, redocly lint valid, exit 0.
- `go test ./internal/recommender/... ./internal/agent/... -cover`: all ok. Recommender coverage is
  93.3 %.
- Tests:
  - recommender: `TestJVMMemory` (11 cases), `TestAttachJVM`, `TestIsJVMImage`, `TestEngineJVM`,
    and `TestSynthJVMFixture`, which runs WO-02's `synth-jvm-0.json` (image only; memory stays
    within keep and is never below the current value);
  - agent: `TestDetectJVM` (includes the dev `jvm-maxram` / `jvm-xmx` specs), `TestParseJVMSize`,
    `TestJVMReport`, `TestJVMFixedHeapRefusal`, guardrails `TestJVMFixedHeap` (8 cases), and the
    controller envtest `TestJVMFixedHeap` (rollout: CPU applied, memory kept, status.jvm reported);
  - store: `TestReportedJVM`, `TestAllRiskFlagsStorable`.
- Live (hub `wo12-dd3a826`, agents `wo12-dd3a826`, run 04:27 UTC succeeded on all 4 clusters):
  - `status.jvm` arrived from both spokes: `jvm-xmx` `{detectedBy:[image,command], maxHeap:128Mi}`;
    `jvm-maxram` `{detectedBy:[image,command,env], maxRAMPercentage:75}`.
  - `curl .../v1/workloads?search=jvm | jq '.items[] | {id, riskFlags}'` gave:
    - `ro-spoke-1/default/Deployment/jvm-xmx` `["low-confidence","jvm-fixed-heap"]`;
    - `ro-spoke-1/default/Deployment/jvm-maxram` `["low-confidence"]`;
    - also the e2e `jvm` workload with a fixed heap.
  - ro-spoke-1 `jvm-xmx`: hub memory 192Mi (heap 128Mi + non-heap 64Mi), explanation
    `{heapSizing:fixed, source:agent, floor:192Mi}`. The RSR (audit) keeps memory at 512Mi/768Mi
    because the agent refuses the change.
  - ro-spoke-1 `jvm-maxram`: RSR proposal 304Mi. Max heap after = 75 % × 304Mi = 228Mi, above the
    observed peak working set (~202Mi) and the peak heap in use (~150Mi, from the JVM's log).
  - ro-spoke-2 `jvm-xmx` was not flagged in that run. Its status at 04:26 predates the new agent,
    and WO-09 scenario 11 then swapped the spoke-2 agent token (401) during the check.

## Incidents / open points
- **Migration numbers collide.** WO-10 (`0003_deferred_windows`), WO-11 (`0003_recommendation_hpa`)
  and WO-12 each add a `0003`. Mine was applied to the shared DB first, at 04:27. `hub:wo11-1`
  (04:30) then skipped its `0003` and fails with "column r.hpa does not exist". I reported this to
  the hub session and the orchestrator; writing to the shared DB was not permitted for me.
  Order agreed with the orchestrator: 0003 jvm (kept, already in the DB), 0004 hpa (WO-11), 0005
  deferred windows (WO-10). The hub session fixes the live hub by renumbering and redeploying.
  WO-12 does not redeploy the hub until WO-11's fix is live, nor the spokes during e2e runs.
- **The live hub is no longer my build.** WO-11 redeployed it (`hub:wo11-1`). The JVM recommender
  code is live again only after main contains WO-12, or with a combined build.
- **First hub deploy (04:11 UTC) was rolled back** at 04:21 to WO-04c's revision. Without the
  migration, the new flag violated the constraint.
- **Agent redeploys overlapped e2e.** My agent redeploys (04:11, 04:27 UTC) restarted the spoke-2
  agent during an e2e scenario-11 run. That may have disturbed its timing.
- **Estimates and limits:**
  - The heap need is estimated from the working set; with heap metrics (JMX) it could be exact.
  - `MinRAMPercentage` (used by the JVM for very small limits) is ignored.
  - JVM options set via ConfigMap/Secret env refs are not resolved.
