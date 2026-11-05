# WO-00 hand-over (contracts and skeleton)

Branch: `wo-00-contracts` (6 commits on top of main 95ad8e9; last 9eeadd6 adds the UI endpoints). **Not pushed yet**: origin rejected the
push because the keychain PAT lacks the `workflow` scope needed for `.github/workflows/ci.yaml`.
Push from the worktree with a credential that has `workflow` scope:
`git -C .claude/worktrees/wo-00-contracts-9c44c8 push -u origin wo-00-contracts`

## Toolchain / make
- `go.mod`: `go 1.26.0` (minimum required by k8s.io v0.37), `toolchain go1.27.3`. CI uses the toolchain.
- k8s deps: k8s.io/api + apimachinery v0.37.2. `api/v1alpha1` depends only on those (no controller-runtime), so the hub can import it.
- `make/common.mk`: build, test, vet, fmt, lint, generate, verify-generate, lint-openapi, ci, help.
  Pinned: controller-gen v0.23.0, golangci-lint v2.15.0, redocly 1.34.23. Tools go to `./bin/`.
  Component `.mk` files hook in by prerequisite (`generate: generate-hub`), never by redefining recipes.
- Newest k3s 1.35 tag: `rancher/k3s:v1.35.10-k3s1` (WO-01 should use it).

## CRDs (api/v1alpha1, api/crds, api/examples)
- Short names: `rsp` (RightsizingPolicy, cluster-scoped), `rsr` (RightsizingRecommendation, namespaced). Category `resource-optimizer`.
- **No OpenAPI defaults** in RightsizingPolicy: unset = inherit (cluster default → global default). Global defaults are documented on each field; the agent applies them. Pointers everywhere for that reason.
- Fields beyond the WO text: `selector.excludeNamespaces`; `cpu.limitRatio` (decimal string, required iff `limits: ratio`); `guardrails.minConfidence` (decimal string, default "0.7"); `maxChangePercent` caps **decreases only**; `TimeWindow` is `schedule`+`duration`(+`timeZone`) or `start`+`end`.
- Decimal values in CRDs are strings (`"0.25"`), never floats.
- RSR spec: `targetRef{apiVersion,kind,name}`, `containers[]{name,requests,limits,removeLimits[]}`, `strategy`, `source{recommendationID,generatedAt,confidence,riskFlags}`. After an in-place fallback the agent sets `spec.strategy: rollout` (this is what stops the webhook).
- RSR status: `phase`, `effective{policy,cpuMode,cpuModeSource,memoryMode,memoryModeSource,strategy,profile}`, `previous[]`, `appliedAt`, `appliedRecommendationID`, `rolledBackAt`, `conditions` (types PolicyResolved, Applied, Blocked, HealthWatch).
- RSR naming: name = workload name; if another kind holds it, `<name>-<lowercase kind>`. **Look RSRs up by `spec.targetRef` via cache index `IndexTargetRef` (value `TargetRefIndexValue(kind,name)` = "Deployment/web"), never by name.**
- Keys (prefix `resource-optimizer.oz.io/`): annotations `mode`, `cpu-mode`, `memory-mode`, `strategy`, `profile` (user); `managed`, `previous-resources`, `recommendation-id`, `injected-resources` (written by us). Label `skip-webhook=true` (pod objectSelector and namespace namespaceSelector). `FieldManager = "resource-optimizer"`.
- Reasons (CamelCase) are constants in `wellknown.go`, shared by conditions, events and `ro_actions_total`. A test fails if a reason is not documented in `docs/contracts/events.md`.

## Hub API (api/openapi/hub.yaml)
- `GET /v1/clusters/{cluster}/recommendations` → `{cluster, items[], continue}`; supports `namespace`, `limit`, `continue`, ETag/If-None-Match (304). No `limit` = everything.
- Item `WorkloadRecommendation`: `id` (uuid), `workload{namespace,kind,name}`, `generatedAt`, `strategyName`, `settings`, `averageReplicas`, `containers[]{name, init, current{requests,limits}, recommended{requests,limits}, removeLimits, riskFlags, monthlySavingsUSD}`, `confidence` (number 0–1), `riskFlags`, `monthlySavingsUSD{cpu,memory,total}`, `dataCoverage{ratio, window, covered, firstSample, lastSample, samples}`.
- **`settings` (ValueSettings: profile, cpuLimits, cpuLimitRatio, memoryLimitEqualsRequest) closes the loop between spoke-side policy and hub-side computation**: the agent reports its effective settings in workload-status; the hub uses them in the next run and echoes them in the recommendation; on mismatch the agent waits (`RecommendationStale`).
- `POST .../workload-status` body `{agentVersion, items[]}` (upsert per workload, max 1000) → `{accepted}`.
- `POST .../events` body `{items[]}` → `{accepted, duplicates}`; idempotent by event `id`.
- `GET /v1/audit?cluster&namespace&kind&workload&type(repeatable)&from&to&order(asc|desc, default asc)&limit(default 100, max 1000)&continue` → `{items[], continue}`. Agent tokens see only their cluster; admin tokens see all.
- Quantities are k8s strings, durations Go strings, errors `application/problem+json` (shared `Problem` schema, plus a stable `code` field).
- **Token roles**: `agent` (one cluster), `ui` (read-only, all clusters), `admin` (everything). Declared per operation as `security: - bearerAuth: [role]`. `api_tokens.role` accepts `ui`.

## UI read endpoints (role `ui`, tag `ui`)
- Workload id = `<cluster>/<namespace>/<kind>/<name>`, sent as ONE path segment URL-encoded (`/` → `%2F`). Go 1.22+ ServeMux `{id}` matches the escaped segment and `PathValue` returns it decoded; ingress/proxies must not decode `%2F` (nginx: no URI normalisation on that location).
- `GET /v1/summary` → `{generatedAt, fleet: Summary, clusters: [Summary + cluster]}`; Summary = workloads, workloadsByMode{cpu,memory}{off,audit,auto-patch}, workloadsByRiskFlag{flag:n}, workloadsByPhase, cpu/memory{requested,used,recommended} (cores/bytes; used = 24h average), monthlySavingsUSD{cpu,memory,total}, lastRecommenderRunAt.
- `GET /v1/clusters` → items{name, description, createdAt, lastHeartbeatAt, agentVersion, agentStatus connected|stale|never, counts{workloads, withRecommendation, autoPatch, blocked}}.
- `GET /v1/workloads?cluster&namespace&kind&mode&riskFlag (repeatable)&minSavings&q&sort=savings|confidence|name&order&limit(50, max 500)&continue` → `{items: WorkloadRow[], total, continue}`. Row: id, cluster, namespace, kind, name, modes, phase, recommendationID, generatedAt, containers (current/recommended), confidence, riskFlags, monthlySavingsUSD. Workloads with no recommendation yet are included without recommendation fields.
- `GET /v1/workloads/{id}` → WorkloadDetail{…, firstSeenAt, lastSeenAt, recommendation (WorkloadRecommendation whose containers carry `explanation`), status (WorkloadStatus)}. Explanation: profile, strategyName, window, cpu/memory{statistic, observed{p50,p95,p99,max}, base, margin, roundedTo, limitRule, notes[]}.
- `GET /v1/workloads/{id}/usage?resource=cpu|memory&container&from&to&step` → containers[{name, usage (max across pods), usageAvg, request, limit, recommendation}] as `[unixSeconds, value]` points in cores/bytes.
- `GET /v1/workloads/{id}/history?from&to&limit` → `{recommendations[], events[]}`, newest first.
- `GET /v1/savings/trend?cluster&groupBy=none|cluster&from&to&step` → series[{cluster?, points[{time,cpu,memory,total}]}] (estimated, not realized).
- Defaults: from = to − 7d, to = now, step chosen for ≤ 1000 points. AuditEvent now also carries `workloadId`.

## Metrics (docs/contracts/metrics.md)
- Workload labels: `cluster, namespace, workload_kind, workload` (+ `container`).
- Pricing: `ro_{node,cpu_core,memory_gib}_hourly_cost_usd{cluster,node,instance_type,capacity_type,region,zone,provider}`. Hub-side scrape must use `honor_labels: true` so `cluster` survives.
- Hub: `ro_recommendation_cpu_cores` / `ro_recommendation_memory_bytes` with `type=request|limit`, `source=current|recommended`; `ro_recommendation_monthly_savings_usd{…,container,resource}`; `_confidence`, `_data_coverage_ratio`, `_risk_flag{flag}`, `_generated_timestamp_seconds`, `_average_replicas`; `ro_workload_mode{resource,mode,source}`, `ro_workload_phase{phase}`, `ro_actions_total{type,strategy,resource,reason}` (hub counts stored events).
- Real cAdvisor has **no `workload` label**; WO-02 must emit KSM owner series so joins work (WO-02's verification query uses `workload=` — fine only as a synthetic convenience label).

## DB (db/migrations/0001_init.sql, goose format)
Tables: clusters, api_tokens (sha256, role agent|admin), workloads, recommendations (history, id = API id), view latest_recommendations, workload_status, audit_events. Text + CHECK instead of PG enums. Verified up/down/up on postgres:18.6.
