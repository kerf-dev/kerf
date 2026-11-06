<!-- Generated from api/openapi/hub.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Schemas

Objects and enumerations used by the [hub API](README.md).

## ClusterName { #clustername }

Type: string. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1`.

## WorkloadKind { #workloadkind }

Type: string. one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob`.

## Mode { #mode }

Type: string. one of `off`, `audit`, `auto-patch`.

## ModeSource { #modesource }

Type: string. one of `global-default`, `cluster-default`, `policy`, `annotation`.

## Strategy { #strategy }

Type: string. one of `in-place`, `rollout`, `deferred`.

## Profile { #profile }

Type: string. one of `dev`, `production`, `high-availability`.

## Phase { #phase }

Type: string. one of `Pending`, `Applying`, `Applied`, `Blocked`, `RolledBack`, `Released`, `Deferred`.

## RiskFlag { #riskflag }

`throttled`, `oom-killed`: the workload is under-provisioned; the recommendation is never
a decrease for that resource. `hpa-coupled`: CPU/memory-utilization HPA, audit-only. `low-confidence`: confidence below the hub threshold. `jvm-fixed-heap`: a JVM
container with a fixed maximum heap (`-Xmx` / `-XX:MaxHeapSize`); its memory is audit-only,
the agent never changes it.

Type: string. one of `throttled`, `oom-killed`, `restarting`, `hpa-coupled`, `sidecar`, `short-lived`, `low-confidence`, `jvm-fixed-heap`.

## EventType { #eventtype }

Type: string. one of `applied`, `skipped`, `blocked`, `fallback`, `rolled_back`, `released`, `failed`.

## ResourceName { #resourcename }

Type: string. one of `cpu`, `memory`.

## Quantity { #quantity }

Kubernetes resource quantity.

Type: string. pattern `^[0-9]+(\.[0-9]+)?(m|k|M|G|T|Ki|Mi|Gi|Ti)?$`; example `250m`, `512Mi`.

## Decimal { #decimal }

Type: number. min 0; max 1.

## ResourceValues { #resourcevalues }

Values per resource. A missing key means "not set".

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cpu` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `memory` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |

## Resources { #resources }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `requests` | [ResourceValues](schemas.md#resourcevalues) |  | Values per resource. A missing key means "not set". |
| `limits` | [ResourceValues](schemas.md#resourcevalues) |  | Values per resource. A missing key means "not set". |

## ContainerResources { #containerresources }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required |  |  |
| `requests` | [ResourceValues](schemas.md#resourcevalues) |  | Values per resource. A missing key means "not set". |
| `limits` | [ResourceValues](schemas.md#resourcevalues) |  | Values per resource. A missing key means "not set". |

## WorkloadRef { #workloadref }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `namespace` | string, required |  |  |
| `kind` | WorkloadKind (enum), required | one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob` |  |
| `name` | string, required |  |  |

## ValueSettings { #valuesettings }

The policy settings that change recommended values. Reported by the agent from its
effective policy; echoed by the hub in each recommendation as the settings it used. If they
differ from the agent's current effective policy, the agent treats the recommendation as
stale (reason `RecommendationStale`) and waits for the next run.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `profile` | Profile (enum) | one of `dev`, `production`, `high-availability` |  |
| `cpuLimits` | string | one of `keep`, `remove`, `ratio` |  |
| `cpuLimitRatio` | string | example `2` | Decimal multiple of the request, only with `cpuLimits = ratio`. |
| `memoryLimitEqualsRequest` | boolean |  |  |

## ContainerRecommendation { #containerrecommendation }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required |  |  |
| `init` | boolean |  | True for init and native sidecar containers. |
| `current` | [Resources](schemas.md#resources), required |  |  |
| `recommended` | [Resources](schemas.md#resources), required |  |  |
| `removeLimits` | array of ResourceName (enum) |  | Resources whose limit should be removed. |
| `riskFlags` | array of RiskFlag (enum) |  |  |
| `monthlySavingsUSD` | [Savings](schemas.md#savings) |  | Estimated monthly savings in USD (estimated, if applied): (current - recommended request) x hourly rate x average replicas x 730 h. Negative for increases. Never added to the realized amounts (`RealizedAmount`). |
| `explanation` | [ContainerExplanation](schemas.md#containerexplanation) |  | Why the values were recommended. Only returned by `GET /v1/workloads/{id}`. |

## Savings { #savings }

Estimated monthly savings in USD (estimated, if applied): (current - recommended request) x
hourly rate x average replicas x 730 h. Negative for increases. Never added to the realized
amounts (`RealizedAmount`).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cpu` | number, required |  |  |
| `memory` | number, required |  |  |
| `total` | number, required |  |  |

## DataCoverage { #datacoverage }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `ratio` | number, required |  | Fraction of the window for which usage samples exist. |
| `window` | string, required | example `336h0m0s` | Recommendation window (Go duration). |
| `covered` | string | example `200h0m0s` | Time span with data (Go duration). The agent compares it with `learningPeriod`. |
| `firstSample` | string (date-time) |  |  |
| `lastSample` | string (date-time) |  |  |
| `samples` | integer | min 0 |  |

## WorkloadRecommendation { #workloadrecommendation }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  | Unique per recommendation; a new run produces a new id. |
| `workload` | [WorkloadRef](schemas.md#workloadref), required |  |  |
| `generatedAt` | string (date-time), required |  |  |
| `strategyName` | string |  | Recommender strategy, e.g. `percentile`. |
| `settings` | [ValueSettings](schemas.md#valuesettings), required |  | The policy settings that change recommended values. Reported by the agent from its effective policy; echoed by the hub in each recommendation as the settings it used. If they differ from the agent's current effective policy, the agent treats the recommendation as stale (reason `RecommendationStale`) and waits for the next run. |
| `averageReplicas` | number | min 0 |  |
| `containers` | array of [ContainerRecommendation](schemas.md#containerrecommendation), required | min items 1 |  |
| `confidence` | number, required | min 0; max 1 |  |
| `riskFlags` | array of RiskFlag (enum), required |  | Union of the containers' flags plus workload-level flags. |
| `monthlySavingsUSD` | [Savings](schemas.md#savings), required |  | Estimated monthly savings in USD (estimated, if applied): (current - recommended request) x hourly rate x average replicas x 730 h. Negative for increases. Never added to the realized amounts (`RealizedAmount`). |
| `dataCoverage` | [DataCoverage](schemas.md#datacoverage), required |  |  |
| `pricing` | [RecommendationPricing](schemas.md#recommendationpricing) |  | Rates used for `monthlySavingsUSD`. `source` is `metrics` when they come from the pricing series (`kerf_*_hourly_cost_usd`, average over the cluster's nodes) and `default` when the cluster had no pricing series and the hub's configured fallback rate was used. Absent on recommendations computed before the field existed. |
| `hpa` | [HPACoordination](schemas.md#hpacoordination) |  | HPA coordination (lifts). Present when the workload has a CPU- or memory-utilization HPA (risk flag `hpa-coupled`). The HPA's utilization is the pod's usage over the sum of its containers' requests of `metric`, so changing those requests changes when it scales. With `recommendedTarget`, the recommended requests of `metric` and that target keep `usage / (sum of requests x target)` unchanged: the agent may apply both in one change (rollout) and the HPA scales as before. The target stays within 40-90 % (or the current target if it is already outside); the requests were adjusted to keep it there. Without `recommendedTarget` (`skipReason` set) the requests of `metric` must not be changed by the agent. |

## HPACoordination { #hpacoordination }

HPA coordination (lifts). Present when the workload has a CPU- or
memory-utilization HPA (risk flag `hpa-coupled`). The HPA's utilization is the pod's usage
over the sum of its containers' requests of `metric`, so changing those requests changes
when it scales. With `recommendedTarget`, the recommended requests of `metric` and that
target keep `usage / (sum of requests x target)` unchanged: the agent may apply both in
one change (rollout) and the HPA scales as before. The target stays within 40-90 %
(or the current target if it is already outside); the requests were adjusted to keep it
there. Without `recommendedTarget` (`skipReason` set) the requests of `metric` must not
be changed by the agent.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required |  | HPA name (same namespace as the workload). |
| `metric` | ResourceName (enum), required | one of `cpu`, `memory` |  |
| `currentTarget` | integer, required | min 1 | Current `averageUtilization` target, percent. |
| `recommendedTarget` | integer | min 1 | `averageUtilization` target to set together with the recommended requests, percent. |
| `minReplicas` | integer | min 0 |  |
| `maxReplicas` | integer | min 1 |  |
| `skipReason` | string | one of `max-replicas-reached`, `unsupported-metrics`, `missing-request`, `target-out-of-bounds` | Why the hub does not coordinate: the HPA was at `maxReplicas` in the window; it scales on more than one utilization metric or on any other metric (pods, object, external); a container has no request of `metric`; or keeping the target within bounds would lower a request that must not decrease (throttled / oom-killed). |

## RecommendationPricing { #recommendationpricing }

Rates used for `monthlySavingsUSD`. `source` is `metrics` when they come from the
pricing series (`kerf_*_hourly_cost_usd`, average over the cluster's nodes) and `default` when
the cluster had no pricing series and the hub's configured fallback rate was used.
Absent on recommendations computed before the field existed.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `source` | string, required | one of `metrics`, `default` |  |
| `cpuCoreHourUSD` | number, required | min 0 |  |
| `memoryGiBHourUSD` | number, required | min 0 |  |
| `rateSources` | array of [PricingRateSource](schemas.md#pricingratesource) |  | With `source: metrics`, the cluster's priced nodes by where the pricing service took their rate from (the `source` label of the pricing series). Absent on older recommendations. |
| `curPeriod` | [PricingPeriod](schemas.md#pricingperiod) |  | The usage the effective (`cur`) rates were computed from. |

## PricingRateSource { #pricingratesource }

Nodes priced from one rate source: `cur` (the effective rate paid, from the AWS Cost and Usage
Report, Savings Plans and Reserved Instances amortised), `list` (AWS on-demand list price),
`spot` (current spot price) or `static` (the static price file).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `source` | string, required | one of `cur`, `list`, `spot`, `static` |  |
| `nodes` | integer, required | min 0 |  |

## PricingPeriod { #pricingperiod }

The usage the effective (`cur`) rates were computed from.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `start` | string (date-time), required |  |  |
| `end` | string (date-time), required |  |  |

## RecommendationList { #recommendationlist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `items` | array of [WorkloadRecommendation](schemas.md#workloadrecommendation), required |  |  |
| `continue` | string |  | Present when more pages exist. |

## EffectiveModes { #effectivemodes }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cpu` | Mode (enum), required | one of `off`, `audit`, `auto-patch` |  |
| `memory` | Mode (enum), required | one of `off`, `audit`, `auto-patch` |  |
| `cpuSource` | ModeSource (enum) | one of `global-default`, `cluster-default`, `policy`, `annotation` |  |
| `memorySource` | ModeSource (enum) | one of `global-default`, `cluster-default`, `policy`, `annotation` |  |

## WorkloadStatus { #workloadstatus }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workload` | [WorkloadRef](schemas.md#workloadref), required |  |  |
| `policy` | string |  | Winning RightsizingPolicy name; empty when none matched. |
| `override` | string |  | Name of the hub workload-override policy layered on top of `policy`, if any. Modes it sets are reported with source `policy`. |
| `modes` | [EffectiveModes](schemas.md#effectivemodes), required |  |  |
| `strategy` | Strategy (enum) | one of `in-place`, `rollout`, `deferred` |  |
| `settings` | [ValueSettings](schemas.md#valuesettings) |  | The policy settings that change recommended values. Reported by the agent from its effective policy; echoed by the hub in each recommendation as the settings it used. If they differ from the agent's current effective policy, the agent treats the recommendation as stale (reason `RecommendationStale`) and waits for the next run. |
| `phase` | Phase (enum), required | one of `Pending`, `Applying`, `Applied`, `Blocked`, `RolledBack`, `Released`, `Deferred` |  |
| `reason` | string |  | Reason for the phase (e.g. the Blocked reason), see `docs/contracts/events.md`. |
| `message` | string |  |  |
| `recommendationID` | string (uuid) |  | Hub recommendation the phase refers to. |
| `live` | array of [ContainerResources](schemas.md#containerresources) |  | Resources currently set on the workload's containers. |
| `appliedAt` | string (date-time) |  |  |
| `nextWindow` | string (date-time) |  | When the next maintenance window opens; set while the phase is `Deferred`. |
| `observedAt` | string (date-time) |  |  |
| `jvm` | array of [JVMContainer](schemas.md#jvmcontainer) |  | JVM containers detected by the agent with their heap settings. Absent when JVM detection is off (`RightsizingPolicy.spec.jvm.detect: false`) or no container is a JVM. |

## JVMContainer { #jvmcontainer }

Heap settings of one JVM container, parsed from `JAVA_TOOL_OPTIONS`, `JDK_JAVA_OPTIONS`,
`JAVA_OPTS`, the container command/args and `_JAVA_OPTIONS` (later sources win, as in the
JVM). A missing value means "not set" (JVM default).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required |  |  |
| `detectedBy` | array of string, required |  | What identified the container as a JVM. |
| `maxHeap` | string |  | `-Xmx` / `-XX:MaxHeapSize`. |
| `initialHeap` | string |  | `-Xms` / `-XX:InitialHeapSize`. |
| `maxRAMPercentage` | number | max 100 | `-XX:MaxRAMPercentage` (or 100 / `-XX:MaxRAMFraction`). |
| `maxRAM` | string |  | `-XX:MaxRAM`. |
| `unresolved` | array of string |  | Option variables set from a ConfigMap/Secret reference, which the agent does not read. |

## WorkloadStatusBatch { #workloadstatusbatch }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `agentVersion` | string |  |  |
| `configVersion` | string |  | `configVersion` of the last cluster config the agent materialised. Absent for agents without config pull. |
| `policyInventory` | array of [PolicyInventoryItem](schemas.md#policyinventoryitem) |  | Every RightsizingPolicy CR on the cluster (hub-managed and Git) and every hub policy the agent could not materialise. Sent with full batches; absent means "unchanged since the last report", an empty array means "none". |
| `capabilities` | array of [AgentCapability](schemas.md#agentcapability) |  | Agent self-check of its RBAC per capability. Absent = unchanged; present = all capabilities. |
| `checks` | array of [ReadinessCheck](schemas.md#readinesscheck) |  | Agent self-check results (source `agent`, docs/contracts/readiness.md). Absent = unchanged; present = every agent check. Sent at start, every 10 minutes and after a recheck. |
| `components` | array of [AgentComponent](schemas.md#agentcomponent) |  | Kerf components the agent found in its cluster (today the webhook pods). Sent with the self-checks; absent = unchanged, an empty array = none running. |
| `items` | array of [WorkloadStatus](schemas.md#workloadstatus), required | max items 1000 |  |

## ContainerChange { #containerchange }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required |  |  |
| `from` | [Resources](schemas.md#resources) |  |  |
| `to` | [Resources](schemas.md#resources) |  |  |

## Event { #event }

See `docs/contracts/events.md` for when each type is emitted.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  | Generated by the agent; idempotency key. |
| `type` | EventType (enum), required | one of `applied`, `skipped`, `blocked`, `fallback`, `rolled_back`, `released`, `failed` |  |
| `time` | string (date-time), required |  |  |
| `workload` | [WorkloadRef](schemas.md#workloadref), required |  |  |
| `recommendationID` | string (uuid) |  |  |
| `strategy` | Strategy (enum) | one of `in-place`, `rollout`, `deferred` |  |
| `resources` | array of ResourceName (enum) |  | Resources the event is about. |
| `containers` | array of [ContainerChange](schemas.md#containerchange) |  |  |
| `reason` | string, required | pattern `^[A-Z][A-Za-z0-9]*$` | CamelCase reason, see `docs/contracts/events.md`. |
| `message` | string | max length 1024 |  |
| `details` | map of string |  | Free-form string attributes (e.g. `oomKills`, `throttlingRatio`). |

## EventBatch { #eventbatch }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [Event](schemas.md#event), required | max items 1000 |  |

## BatchResult { #batchresult }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `accepted` | integer, required | min 0 |  |
| `duplicates` | integer | min 0 | Events already stored (same id); not an error. |

## AuditEvent { #auditevent }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  | Generated by the agent; idempotency key. |
| `type` | EventType (enum), required | one of `applied`, `skipped`, `blocked`, `fallback`, `rolled_back`, `released`, `failed` |  |
| `time` | string (date-time), required |  |  |
| `workload` | [WorkloadRef](schemas.md#workloadref), required |  |  |
| `recommendationID` | string (uuid) |  |  |
| `strategy` | Strategy (enum) | one of `in-place`, `rollout`, `deferred` |  |
| `resources` | array of ResourceName (enum) |  | Resources the event is about. |
| `containers` | array of [ContainerChange](schemas.md#containerchange) |  |  |
| `reason` | string, required | pattern `^[A-Z][A-Za-z0-9]*$` | CamelCase reason, see `docs/contracts/events.md`. |
| `message` | string | max length 1024 |  |
| `details` | map of string |  | Free-form string attributes (e.g. `oomKills`, `throttlingRatio`). |
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `receivedAt` | string (date-time), required |  |  |
| `workloadId` | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |

## AuditEventList { #auditeventlist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [AuditEvent](schemas.md#auditevent), required |  |  |
| `continue` | string |  | Present when more pages exist. |

## WorkloadID { #workloadid }

`<cluster>/<namespace>/<kind>/<name>`

Type: string. pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment|StatefulSet|DaemonSet|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout`.

## UsageStats { #usagestats }

Statistics of per-pod usage over the window.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `p50` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `p95` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `p99` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `max` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |

## ResourceExplanation { #resourceexplanation }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `statistic` | string, required |  | Statistic the request is based on, e.g. `p95` (CPU) or `max(p99,peak)` (memory). |
| `observed` | [UsageStats](schemas.md#usagestats), required |  | Statistics of per-pod usage over the window. |
| `base` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `margin` | number | min 0 | Added fraction; CPU headroom or memory buffer (0.15 = +15 %). |
| `roundedTo` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `limitRule` | string |  | How the limit was derived, e.g. `keep`, `remove`, `ratio 2`, `equals request`. |
| `notes` | array of string |  | Adjustments, e.g. "raised: throttled" or "kept current: oom-killed". |
| `jvm` | [JVMExplanation](schemas.md#jvmexplanation) |  | How the JVM heap shaped the memory recommendation, only on `memory` of JVM containers. `heapSizing`: `fixed` (`-Xmx`; memory audit-only, flag `jvm-fixed-heap`), `percentage` (`-XX:MaxRAMPercentage`, max heap follows the memory limit), `default` (no heap flags: the JVM default of 25 % of the limit). `heapNeed` is the observed peak working set capped at the current maximum heap; `floor` is the smallest memory that keeps the maximum heap at or above `heapNeed` (percentage, default) or fits heap plus `nonHeap` (fixed). |

## JVMExplanation { #jvmexplanation }

How the JVM heap shaped the memory recommendation, only on `memory` of JVM
containers. `heapSizing`: `fixed` (`-Xmx`; memory audit-only, flag `jvm-fixed-heap`),
`percentage` (`-XX:MaxRAMPercentage`, max heap follows the memory limit), `default` (no heap
flags: the JVM default of 25 % of the limit). `heapNeed` is the observed peak working set
capped at the current maximum heap; `floor` is the smallest memory that keeps the maximum
heap at or above `heapNeed` (percentage, default) or fits heap plus `nonHeap` (fixed).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `heapSizing` | string, required | one of `fixed`, `percentage`, `default` |  |
| `detectedBy` | array of string, required |  |  |
| `source` | string | one of `agent`, `image` | `agent`: heap settings reported by the agent; `image`: only the image name was known (no agent report), JVM defaults assumed. |
| `maxRAMPercentage` | number |  |  |
| `maxHeapBefore` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `maxHeapAfter` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `heapNeed` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `nonHeap` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `floor` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |

## ContainerExplanation { #containerexplanation }

Why the values were recommended. Only returned by `GET /v1/workloads/{id}`.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `profile` | Profile (enum), required | one of `dev`, `production`, `high-availability` |  |
| `strategyName` | string, required |  |  |
| `window` | string, required |  | Go duration. |
| `cpu` | [ResourceExplanation](schemas.md#resourceexplanation) |  |  |
| `memory` | [ResourceExplanation](schemas.md#resourceexplanation) |  |  |
| `confidenceFactors` | [ConfidenceFactors](schemas.md#confidencefactors) |  | How this container's confidence was computed: `confidence` = sum of score x weight. The workload's `confidence` is the minimum over its containers. `coverage`: fraction of the window's steps with usage data. `samples`: number of samples relative to the hub's full-confidence sample count. `variance`: stability of CPU usage, 1 / (1 + coefficient of variation); 1 means no variance. Absent on recommendations computed before the field existed. |

## ConfidenceFactor { #confidencefactor }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `score` | number, required |  | Factor score, 0 (worst) to 1 (best). |
| `weight` | number, required |  | Weight of the factor; the weights sum to 1. |

## ConfidenceFactors { #confidencefactors }

How this container's confidence was computed: `confidence` = sum of score x weight.
The workload's `confidence` is the minimum over its containers.
`coverage`: fraction of the window's steps with usage data. `samples`: number of samples
relative to the hub's full-confidence sample count. `variance`: stability of CPU usage,
1 / (1 + coefficient of variation); 1 means no variance.
Absent on recommendations computed before the field existed.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `coverage` | [ConfidenceFactor](schemas.md#confidencefactor), required |  |  |
| `samples` | [ConfidenceFactor](schemas.md#confidencefactor), required |  |  |
| `variance` | [ConfidenceFactor](schemas.md#confidencefactor), required |  |  |
| `confidence` | number, required | min 0; max 1 |  |

## ModeCounts { #modecounts }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `off` | integer, required | min 0 |  |
| `audit` | integer, required | min 0 |  |
| `auto-patch` | integer, required | min 0 |  |

## ResourceTotals { #resourcetotals }

Sums over workloads (per-pod value x average replicas). `used` is the average usage over
the last 24 h. Cores for CPU, bytes for memory.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `requested` | number, required |  |  |
| `used` | number, required |  |  |
| `recommended` | number, required |  |  |

## Summary { #summary }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workloads` | integer, required | min 0 |  |
| `workloadsByMode` | object, required |  |  |
| `workloadsByMode.cpu` | [ModeCounts](schemas.md#modecounts), required |  |  |
| `workloadsByMode.memory` | [ModeCounts](schemas.md#modecounts), required |  |  |
| `workloadsByRiskFlag` | map of integer, required |  | Number of workloads per risk flag; flags with zero workloads may be omitted. |
| `workloadsByPhase` | map of integer |  |  |
| `cpu` | [ResourceTotals](schemas.md#resourcetotals), required |  | Sums over workloads (per-pod value x average replicas). `used` is the average usage over the last 24 h. Cores for CPU, bytes for memory. |
| `memory` | [ResourceTotals](schemas.md#resourcetotals), required |  | Sums over workloads (per-pod value x average replicas). `used` is the average usage over the last 24 h. Cores for CPU, bytes for memory. |
| `monthlySavingsUSD` | [Savings](schemas.md#savings), required |  | Estimated monthly savings in USD (estimated, if applied): (current - recommended request) x hourly rate x average replicas x 730 h. Negative for increases. Never added to the realized amounts (`RealizedAmount`). |
| `lastRecommenderRunAt` | string (date-time) |  | End of the last recommender run that covered these workloads. |

## ClusterSummary { #clustersummary }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workloads` | integer, required | min 0 |  |
| `workloadsByMode` | object, required |  |  |
| `workloadsByMode.cpu` | [ModeCounts](schemas.md#modecounts), required |  |  |
| `workloadsByMode.memory` | [ModeCounts](schemas.md#modecounts), required |  |  |
| `workloadsByRiskFlag` | map of integer, required |  | Number of workloads per risk flag; flags with zero workloads may be omitted. |
| `workloadsByPhase` | map of integer |  |  |
| `cpu` | [ResourceTotals](schemas.md#resourcetotals), required |  | Sums over workloads (per-pod value x average replicas). `used` is the average usage over the last 24 h. Cores for CPU, bytes for memory. |
| `memory` | [ResourceTotals](schemas.md#resourcetotals), required |  | Sums over workloads (per-pod value x average replicas). `used` is the average usage over the last 24 h. Cores for CPU, bytes for memory. |
| `monthlySavingsUSD` | [Savings](schemas.md#savings), required |  | Estimated monthly savings in USD (estimated, if applied): (current - recommended request) x hourly rate x average replicas x 730 h. Negative for increases. Never added to the realized amounts (`RealizedAmount`). |
| `lastRecommenderRunAt` | string (date-time) |  | End of the last recommender run that covered these workloads. |
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |

## FleetSummary { #fleetsummary }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `generatedAt` | string (date-time), required |  |  |
| `fleet` | [Summary](schemas.md#summary), required |  |  |
| `clusters` | array of [ClusterSummary](schemas.md#clustersummary), required |  |  |

## ClusterInfo { #clusterinfo }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `description` | string |  |  |
| `createdAt` | string (date-time) |  |  |
| `lastHeartbeatAt` | string (date-time) |  | Last authenticated agent request. |
| `agentVersion` | string |  |  |
| `agentStatus` | string, required | one of `connected`, `stale`, `never` | `stale` when the last heartbeat is older than the hub's configured threshold. |
| `environment` | Environment (enum) | one of `prod`, `non-prod` | Writes to a `prod` cluster ask for confirmation in the UI and some need an admin. |
| `owner` | string |  |  |
| `policySource` | ClusterPolicySource (enum) | one of `hub`, `git`, `both` | Where a cluster's policies come from. `hub`: hub-managed policies only (Git CRs are still reported). `git`: no hub-managed policies are sent and the admin pages are read-only for that cluster. `both`: both. |
| `paused` | boolean |  | Automation paused (cluster or fleet). |
| `automation` | [Automation](schemas.md#automation) |  | The cluster's own pause state. |
| `configSync` | [AgentConfigSync](schemas.md#agentconfigsync) |  |  |
| `readiness` | [ReadinessSummary](schemas.md#readinesssummary) |  |  |
| `counts` | object, required |  |  |
| `counts.workloads` | integer, required | min 0 |  |
| `counts.withRecommendation` | integer, required | min 0 |  |
| `counts.autoPatch` | integer, required | min 0 | Workloads with CPU or memory in `auto-patch`. |
| `counts.blocked` | integer, required | min 0 |  |

## ClusterList { #clusterlist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [ClusterInfo](schemas.md#clusterinfo), required |  |  |

## WorkloadRow { #workloadrow }

Recommendation fields are absent when the workload has no recommendation yet.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `namespace` | string, required |  |  |
| `kind` | WorkloadKind (enum), required | one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob` |  |
| `name` | string, required |  |  |
| `modes` | [EffectiveModes](schemas.md#effectivemodes), required |  |  |
| `phase` | Phase (enum), required | one of `Pending`, `Applying`, `Applied`, `Blocked`, `RolledBack`, `Released`, `Deferred` |  |
| `recommendationID` | string (uuid) |  |  |
| `generatedAt` | string (date-time) |  |  |
| `containers` | array of [ContainerRecommendation](schemas.md#containerrecommendation) |  |  |
| `confidence` | number | min 0; max 1 |  |
| `riskFlags` | array of RiskFlag (enum) |  |  |
| `monthlySavingsUSD` | [Savings](schemas.md#savings) |  | Estimated monthly savings in USD (estimated, if applied): (current - recommended request) x hourly rate x average replicas x 730 h. Negative for increases. Never added to the realized amounts (`RealizedAmount`). |

## WorkloadList { #workloadlist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [WorkloadRow](schemas.md#workloadrow), required |  |  |
| `total` | integer, required | min 0 | Number of workloads matching the filters (all pages). |
| `continue` | string |  | Present when more pages exist. |

## FacetCount { #facetcount }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `value` | string, required |  |  |
| `count` | integer, required | min 1 |  |

## WorkloadFacets { #workloadfacets }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `total` | integer, required | min 0 | Workloads matching all filters (equals `WorkloadList.total`). |
| `clusters` | array of [FacetCount](schemas.md#facetcount), required |  |  |
| `namespaces` | array of [FacetCount](schemas.md#facetcount), required |  | Namespace names across clusters (the `namespace` filter matches names in any cluster). |
| `kinds` | array of [FacetCount](schemas.md#facetcount), required |  |  |
| `modes` | array of [FacetCount](schemas.md#facetcount), required |  |  |
| `riskFlags` | array of [FacetCount](schemas.md#facetcount), required |  |  |

## WorkloadDetail { #workloaddetail }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `namespace` | string, required |  |  |
| `kind` | WorkloadKind (enum), required | one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob` |  |
| `name` | string, required |  |  |
| `modes` | [EffectiveModes](schemas.md#effectivemodes), required |  |  |
| `phase` | Phase (enum), required | one of `Pending`, `Applying`, `Applied`, `Blocked`, `RolledBack`, `Released`, `Deferred` |  |
| `firstSeenAt` | string (date-time) |  |  |
| `lastSeenAt` | string (date-time) |  |  |
| `recommendation` | [WorkloadRecommendation](schemas.md#workloadrecommendation) |  | Latest recommendation; containers include `explanation`. Absent if none yet. |
| `status` | [WorkloadStatus](schemas.md#workloadstatus) |  | Latest status reported by the agent. Absent if never reported. |
| `approval` | [Approval](schemas.md#approval) |  | The latest approve-once grant, if any. |
| `override` | [AdminPolicy](schemas.md#adminpolicy) |  | The workload's hub override policy, if any. |

## Point { #point }

[unix seconds, value]

Type: array of object. min items 2; max items 2.

## ContainerSeries { #containerseries }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required |  |  |
| `usage` | array of array of object, required |  |  |
| `usageAvg` | array of array of object |  |  |
| `request` | array of array of object, required |  |  |
| `limit` | array of array of object, required |  | Empty when no limit is set. |
| `recommendation` | array of array of object, required |  | Recommended request. |

## UsageSeries { #usageseries }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workloadId` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |
| `resource` | ResourceName (enum), required | one of `cpu`, `memory` |  |
| `unit` | string, required | one of `cores`, `bytes` |  |
| `from` | string (date-time), required |  |  |
| `to` | string (date-time), required |  |  |
| `step` | string, required |  |  |
| `containers` | array of [ContainerSeries](schemas.md#containerseries), required |  |  |

## WorkloadHistory { #workloadhistory }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workloadId` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |
| `recommendations` | array of [WorkloadRecommendation](schemas.md#workloadrecommendation), required |  | Newest first. |
| `events` | array of [AuditEvent](schemas.md#auditevent), required |  | Newest first. |
| `configChanges` | array of [ConfigChange](schemas.md#configchange) |  | Configuration changes targeting this workload (override, approval), newest first. |

## SavingsPoint { #savingspoint }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `time` | string (date-time), required |  |  |
| `cpu` | number, required |  |  |
| `memory` | number, required |  |  |
| `total` | number, required |  |  |

## SavingsTrend { #savingstrend }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `from` | string (date-time), required |  |  |
| `to` | string (date-time), required |  |  |
| `step` | string, required |  |  |
| `series` | array of object, required |  |  |
| `series[].cluster` | string |  | Absent for the fleet series (`groupBy=none`). |
| `series[].points` | array of [SavingsPoint](schemas.md#savingspoint), required |  |  |

## RealizedStep { #realizedstep }

Bucket size of a realized series (UTC).

Type: string. one of `day`, `month`.

## NodeCostStep { #nodecoststep }

Bucket size of a node cost trend (UTC).

Type: string. one of `hour`, `day`.

## RealizedAmount { #realizedamount }

Realized USD: requests released by applied changes x replicas that ran x the cluster's rates
of the time. Not an estimate; never added to `Savings`. Negative when Kerf's changes raised
requests above where they were.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cpu` | number, required |  |  |
| `memory` | number, required |  |  |
| `total` | number, required |  |  |

## ReleasedRequests { #releasedrequests }

Requests released by applied changes, times the replicas that ran.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cpuCores` | number, required |  |  |
| `memoryBytes` | number, required |  |  |

## RealizedPoint { #realizedpoint }

One bucket of a realized series; `time` is the bucket start.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `time` | string (date-time), required |  |  |
| `cpu` | number, required |  |  |
| `memory` | number, required |  |  |
| `total` | number, required |  |  |
| `released` | [ReleasedRequests](schemas.md#releasedrequests), required |  | Average released requests over the computed hours of the bucket. |

## RealizedCoverage { #realizedcoverage }

Workload-hours with released requests in the range, by how they were computed.
`replicasFromRecommendations`: the metrics had no data for the cluster in that hour, so the
replicas of the workload's stored recommendation were used. `withoutReplicas`: neither
existed; those hours count zero. `defaultPricing`: no stored rates, the default rates.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workloadHours` | integer, required | min 0 |  |
| `replicasFromRecommendations` | integer, required | min 0 |  |
| `withoutReplicas` | integer, required | min 0 |  |
| `defaultPricing` | integer, required | min 0 |  |

## RealizedSavings { #realizedsavings }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `since` | string (date-time) |  | First hour with released requests in the scope; absent while nothing was applied. |
| `computedThrough` | string (date-time) |  | Hours before this are computed for every cluster of the scope. |
| `from` | string (date-time), required |  |  |
| `to` | string (date-time), required |  |  |
| `step` | RealizedStep (enum), required | one of `day`, `month` | Bucket size of a realized series (UTC). |
| `total` | [RealizedAmount](schemas.md#realizedamount), required |  | Everything realized since `since` (all time, not only the range). |
| `inRange` | [RealizedAmount](schemas.md#realizedamount), required |  | Realized in [`from`, `to`). |
| `releasedNow` | [ReleasedRequests](schemas.md#releasedrequests), required |  | Requests released in the last computed hour, times replicas. |
| `points` | array of [RealizedPoint](schemas.md#realizedpoint), required |  |  |
| `coverage` | [RealizedCoverage](schemas.md#realizedcoverage), required |  | Workload-hours with released requests in the range, by how they were computed. `replicasFromRecommendations`: the metrics had no data for the cluster in that hour, so the replicas of the workload's stored recommendation were used. `withoutReplicas`: neither existed; those hours count zero. `defaultPricing`: no stored rates, the default rates. |

## NodeCostPoint { #nodecostpoint }

One bucket (hourly averages); a field is absent when the metrics had no data.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `time` | string (date-time), required |  |  |
| `nodeCostUSDPerHour` | number |  | Cost of the cluster's priced nodes, USD per hour. |
| `nodes` | number |  |  |
| `cpuRequestedRatio` | number |  | Scheduled pods' CPU requests / nodes' allocatable CPU. |
| `memoryRequestedRatio` | number |  |  |
| `released` | [ReleasedRequests](schemas.md#releasedrequests), required |  | Requests released by Kerf's applied changes, times replicas. |

## NodeCostTrend { #nodecosttrend }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `from` | string (date-time), required |  |  |
| `to` | string (date-time), required |  |  |
| `step` | NodeCostStep (enum), required | one of `hour`, `day` | Bucket size of a node cost trend (UTC). |
| `relation` | string, required | one of `correlated` | Always `correlated`; the two are shown side by side, not as cause and effect. |
| `computedThrough` | string (date-time) |  |  |
| `points` | array of [NodeCostPoint](schemas.md#nodecostpoint), required |  |  |

## RecomputeAccepted { #recomputeaccepted }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `runId` | string (uuid), required |  | Id of the queued run (UUIDv7). |
| `cluster` | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` | The requested cluster; absent for a run over all clusters. |
| `requestedAt` | string (date-time), required |  | When the run was first requested (for a coalesced request, the earlier one). |
| `coalesced` | boolean, required |  | True when an already queued run for the same scope was returned. |

## UserRole { #userrole }

Role of a local user. `viewer`: everything the read-only UI shows. `operator`:
additionally workload actions (override, exclude, approve-once) and pausing automation.
`admin`: additionally defaults, policies, settings, clusters, tokens and users. Each role
includes the ones before it.

Type: string. one of `viewer`, `operator`, `admin`.

## TokenRole { #tokenrole }

Role of an API (machine) token. `pricing` is the pricing service: only
`/v1/internal/*` (metrics bindings and the datasource query proxy); no cluster.

Type: string. one of `agent`, `ui`, `admin`, `pricing`.

## Username { #username }

Type: string. pattern `^[a-z0-9][a-z0-9._-]{1,62}$`; example `alice`.

## Password { #password }

At least 8 characters; at most 72 bytes (bcrypt).

Type: string. min length 8; max length 72.

## ReasonText { #reasontext }

Why the change is made; stored in the audit log.

Type: string. min length 1; max length 1024.

## Duration { #duration }

Go duration.

Type: string. pattern `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`; example `10m`, `168h`.

## Actor { #actor }

Who made a change. `user`: a local user (`name` = username). `token`: an API token
(`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `type` | string, required | one of `user`, `token`, `system` |  |
| `name` | string, required |  |  |
| `role` | string |  | The user role or token role at the time of the change. |

## ChangeReason { #changereason }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## LoginRequest { #loginrequest }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `username` | string, required | max length 63 |  |
| `password` | string, required | max length 72 |  |

## Me { #me }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `authMethod` | string, required | one of `session`, `token` |  |
| `role` | UserRole (enum), required | one of `viewer`, `operator`, `admin` | Role of a local user. `viewer`: everything the read-only UI shows. `operator`: additionally workload actions (override, exclude, approve-once) and pausing automation. `admin`: additionally defaults, policies, settings, clusters, tokens and users. Each role includes the ones before it. |
| `user` | [User](schemas.md#user) |  | The logged-in user; only with `authMethod = session`. |
| `tokenId` | string |  | Only with `authMethod = token`. |
| `csrfToken` | string |  | Send as `X-CSRF-Token` on writes; only with `authMethod = session`. |
| `sessionExpiresAt` | string (date-time) |  |  |

## PasswordChange { #passwordchange }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `currentPassword` | string, required | max length 72 |  |
| `newPassword` | string, required | min length 8; max length 72 | At least 8 characters; at most 72 bytes (bcrypt). |

## User { #user }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `username` | string, required | pattern `^[a-z0-9][a-z0-9._-]{1,62}$`; example `alice` |  |
| `displayName` | string, required | max length 128 |  |
| `role` | UserRole (enum), required | one of `viewer`, `operator`, `admin` | Role of a local user. `viewer`: everything the read-only UI shows. `operator`: additionally workload actions (override, exclude, approve-once) and pausing automation. `admin`: additionally defaults, policies, settings, clusters, tokens and users. Each role includes the ones before it. |
| `disabled` | boolean, required |  |  |
| `mustChangePassword` | boolean, required |  |  |
| `createdAt` | string (date-time), required |  |  |
| `createdBy` | string |  | Actor name; absent for the bootstrap admin. |
| `updatedAt` | string (date-time) |  |  |
| `lastLoginAt` | string (date-time) |  |  |
| `lockedUntil` | string (date-time) |  | Present while login is locked after failed attempts. |
| `deletedAt` | string (date-time) |  |  |
| `version` | integer, required | min 1 |  |

## UserList { #userlist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [User](schemas.md#user), required |  |  |

## UserCreate { #usercreate }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `username` | string, required | pattern `^[a-z0-9][a-z0-9._-]{1,62}$`; example `alice` |  |
| `displayName` | string | max length 128 |  |
| `role` | UserRole (enum), required | one of `viewer`, `operator`, `admin` | Role of a local user. `viewer`: everything the read-only UI shows. `operator`: additionally workload actions (override, exclude, approve-once) and pausing automation. `admin`: additionally defaults, policies, settings, clusters, tokens and users. Each role includes the ones before it. |
| `password` | string, required | min length 8; max length 72 | At least 8 characters; at most 72 bytes (bcrypt). |
| `mustChangePassword` | boolean | default `True` |  |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## UserUpdate { #userupdate }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `displayName` | string | max length 128 |  |
| `role` | UserRole (enum) | one of `viewer`, `operator`, `admin` | Role of a local user. `viewer`: everything the read-only UI shows. `operator`: additionally workload actions (override, exclude, approve-once) and pausing automation. `admin`: additionally defaults, policies, settings, clusters, tokens and users. Each role includes the ones before it. |
| `disabled` | boolean |  |  |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## PasswordReset { #passwordreset }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `password` | string | min length 8; max length 72 | At least 8 characters; at most 72 bytes (bcrypt). |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## PasswordResetResult { #passwordresetresult }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `temporaryPassword` | string |  | The generated password, shown once; absent when `password` was given. |
| `mustChangePassword` | boolean, required |  |  |

## TokenInfo { #tokeninfo }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string, required | pattern `^[0-9]+$` |  |
| `role` | TokenRole (enum), required | one of `agent`, `ui`, `admin`, `pricing` | Role of an API (machine) token. `pricing` is the pricing service: only `/v1/internal/*` (metrics bindings and the datasource query proxy); no cluster. |
| `cluster` | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `description` | string, required |  |  |
| `createdAt` | string (date-time), required |  |  |
| `createdBy` | string |  | Actor name; absent for tokens seeded from configuration. |
| `expiresAt` | string (date-time) |  |  |
| `lastUsedAt` | string (date-time) |  | Updated at most once a minute. |
| `revokedAt` | string (date-time) |  |  |

## TokenList { #tokenlist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [TokenInfo](schemas.md#tokeninfo), required |  |  |

## TokenCreate { #tokencreate }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `role` | TokenRole (enum), required | one of `agent`, `ui`, `admin`, `pricing` | Role of an API (machine) token. `pricing` is the pricing service: only `/v1/internal/*` (metrics bindings and the datasource query proxy); no cluster. |
| `cluster` | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `description` | string, required | max length 256 |  |
| `expiresAt` | string (date-time) |  | Absent means the token does not expire. |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## TokenCreated { #tokencreated }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `token` | [TokenInfo](schemas.md#tokeninfo), required |  |  |
| `value` | string, required |  | The token value. Shown once; the hub cannot return it again. |

## SettingsSection { #settingssection }

Type: string. one of `recommender`, `pricing`.

## RecommenderProfileSettings { #recommenderprofilesettings }

One recommender profile. Fractions are 0.15 for +15 %.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | Profile (enum), required | one of `dev`, `production`, `high-availability` |  |
| `window` | string, required | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `cpuPercentile` | number, required | max 1 |  |
| `memoryPercentile` | number, required | max 1 |  |
| `memoryPeakWindow` | string |  | Window of the memory peak; absent or 0 means the whole window. |
| `cpuHeadroom` | number, required | min 0; max 10 |  |
| `memoryBuffer` | number, required | min 0; max 10 |  |

## RecommenderSettings { #recommendersettings }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `interval` | string, required |  | Time between scheduled recommender runs. |
| `retention` | string, required |  | Recommendation history retention (the latest per workload is always kept). |
| `profiles` | array of [RecommenderProfileSettings](schemas.md#recommenderprofilesettings), required | min items 3; max items 3 | Exactly one entry per built-in profile (`dev`, `production`, `high-availability`). |

## RecommenderSettingsDoc { #recommendersettingsdoc }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `version` | integer, required | min 0 | 0 = never saved (values from the hub configuration file). |
| `updatedAt` | string (date-time) |  |  |
| `updatedBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `reason` | string |  |  |
| `value` | [RecommenderSettings](schemas.md#recommendersettings), required |  |  |

## RecommenderSettingsUpdate { #recommendersettingsupdate }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `value` | [RecommenderSettings](schemas.md#recommendersettings), required |  |  |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## ClusterPricingAdjustment { #clusterpricingadjustment }

Adjusts the rates of one cluster. A fixed rate replaces the rate from the pricing metrics
(or the default rate); the discount then applies to whichever rate is used.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `discountPercent` | number | min 0; max 100 | Savings Plans / EDP discount, percent off the rate. |
| `cpuCoreHourUSD` | number | min 0 |  |
| `memoryGiBHourUSD` | number | min 0 |  |

## PricingSettings { #pricingsettings }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `defaultDiscountPercent` | number | min 0; max 100 | Discount for clusters without their own entry. |
| `clusters` | array of [ClusterPricingAdjustment](schemas.md#clusterpricingadjustment), required |  | At most one entry per cluster. |

## PricingSettingsDoc { #pricingsettingsdoc }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `version` | integer, required | min 0 |  |
| `updatedAt` | string (date-time) |  |  |
| `updatedBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `reason` | string |  |  |
| `value` | [PricingSettings](schemas.md#pricingsettings), required |  |  |
| `rates` | [PricingRates](schemas.md#pricingrates) |  | Read-only, on GET: where the pricing service's rates come from now, read from its series in the hub's metrics store. Absent when the store cannot be queried. |

## PricingRates { #pricingrates }

Read-only, on GET: where the pricing service's rates come from now, read from its series in the
hub's metrics store. Absent when the store cannot be queried.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cur` | [PricingCURStatus](schemas.md#pricingcurstatus) |  | The AWS Cost and Usage Report reader of the pricing service; absent when it is off. While `lastReadOK` is false the last effective rates are kept for a while (`pricing.cur.maxAge`), then every node falls back to list prices. |
| `clusters` | array of [ClusterPricingRates](schemas.md#clusterpricingrates), required |  |  |

## PricingCURStatus { #pricingcurstatus }

The AWS Cost and Usage Report reader of the pricing service; absent when it is off. While
`lastReadOK` is false the last effective rates are kept for a while (`pricing.cur.maxAge`), then
every node falls back to list prices.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `lastReadOK` | boolean, required |  |  |
| `lastSuccess` | string (date-time) |  |  |
| `rates` | integer, required | min 0 | Instance type, region and capacity type keys with an effective rate in use. |
| `period` | [PricingPeriod](schemas.md#pricingperiod) |  | The usage the effective (`cur`) rates were computed from. |

## ClusterPricingRates { #clusterpricingrates }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `sources` | array of [PricingRateSource](schemas.md#pricingratesource), required |  |  |

## PricingSettingsUpdate { #pricingsettingsupdate }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `value` | [PricingSettings](schemas.md#pricingsettings), required |  |  |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## Settings { #settings }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `recommender` | [RecommenderSettingsDoc](schemas.md#recommendersettingsdoc), required |  |  |
| `pricing` | [PricingSettingsDoc](schemas.md#pricingsettingsdoc), required |  |  |

## SettingsVersion { #settingsversion }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `section` | SettingsSection (enum), required | one of `recommender`, `pricing` |  |
| `version` | integer, required | min 1 |  |
| `value` | object, required |  | `RecommenderSettings` or `PricingSettings`, by `section`. |
| `createdAt` | string (date-time), required |  |  |
| `actor` | [Actor](schemas.md#actor), required |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `reason` | string, required |  |  |

## SettingsVersionList { #settingsversionlist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [SettingsVersion](schemas.md#settingsversion), required |  |  |
| `continue` | string |  |  |

## HubPolicyType { #hubpolicytype }

`global-default` (`ro-global-defaults`, priority 0, every cluster), `cluster-default`
(`ro-cluster-defaults`, priority 10, one cluster), `policy` (named, priority 1-899999),
`workload-override` (`ro-override-…`, priority 900000, one workload). The agent layers the
default types under the winning policy and the override above it.

Type: string. one of `global-default`, `cluster-default`, `policy`, `workload-override`.

## ClusterPolicySource { #clusterpolicysource }

Where a cluster's policies come from. `hub`: hub-managed policies only (Git CRs are
still reported). `git`: no hub-managed policies are sent and the admin pages are read-only
for that cluster. `both`: both.

Type: string. one of `hub`, `git`, `both`.

## PolicyOrigin { #policyorigin }

`hub`: a CR labelled `managed-by=hub`; `git`: any other CR.

Type: string. one of `hub`, `git`.

## Environment { #environment }

Writes to a `prod` cluster ask for confirmation in the UI and some need an admin.

Type: string. one of `prod`, `non-prod`.

## NamespacedWorkloadRef { #namespacedworkloadref }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `kind` | WorkloadKind (enum), required | one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob` |  |
| `namespace` | string, required | min length 1; max length 63 |  |
| `name` | string, required | min length 1; max length 253 |  |

## PolicySelector { #policyselector }

Mirrors `RightsizingPolicy.spec.selector` (api/v1alpha1). All set criteria must match.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `namespaces` | array of string | max items 64 |  |
| `excludeNamespaces` | array of string | max items 64 |  |
| `matchLabels` | map of string |  |  |
| `kinds` | array of WorkloadKind (enum) | max items 4 |  |
| `workloads` | array of [NamespacedWorkloadRef](schemas.md#namespacedworkloadref) | max items 100 |  |

## CPUPolicy { #cpupolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `mode` | Mode (enum) | one of `off`, `audit`, `auto-patch` |  |
| `limits` | string | one of `keep`, `remove`, `ratio` |  |
| `limitRatio` | string | pattern `^([1-9][0-9]*)(\.[0-9]{1,3})?$` | Required iff `limits = ratio`. |

## MemoryPolicy { #memorypolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `mode` | Mode (enum) | one of `off`, `audit`, `auto-patch` |  |
| `limitEqualsRequest` | boolean |  |  |

## TimeWindow { #timewindow }

Either `schedule` + `duration` (recurring) or `start` + `end` (one-off).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `schedule` | string | max length 128 | 5-field cron expression. |
| `duration` | string | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `start` | string (date-time) |  |  |
| `end` | string (date-time) |  |  |
| `timeZone` | string | max length 64 | IANA name; default UTC. |

## ApplyPolicy { #applypolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `strategy` | Strategy (enum) | one of `in-place`, `rollout`, `deferred` |  |
| `fallbackAfter` | string | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `maintenanceWindows` | array of [TimeWindow](schemas.md#timewindow) | max items 16 |  |
| `freezeWindows` | array of [TimeWindow](schemas.md#timewindow) | max items 16 |  |

## RollbackPolicy { #rollbackpolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `watchDuration` | string | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `oomKills` | integer | min 1 |  |
| `restarts` | integer | min 1 |  |
| `throttlingRatio` | string | pattern `^(0(\.[0-9]{1,3})?\|1(\.0{1,3})?)$` |  |

## Guardrails { #guardrails }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `learningPeriod` | string | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `minConfidence` | string | pattern `^(0(\.[0-9]{1,3})?\|1(\.0{1,3})?)$` |  |
| `maxChangePercent` | integer | min 1; max 100 |  |
| `minCPU` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `minMemory` | string | pattern `^[0-9]+(\.[0-9]+)?(m\|k\|M\|G\|T\|Ki\|Mi\|Gi\|Ti)?$`; example `250m`, `512Mi` | Kubernetes resource quantity. |
| `cooldown` | string | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `rollback` | [RollbackPolicy](schemas.md#rollbackpolicy) |  |  |

## HPATargetBounds { #hpatargetbounds }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `minUtilization` | integer | min 1; max 100 |  |
| `maxUtilization` | integer | min 1; max 100 |  |

## HPAPolicy { #hpapolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `coordinate` | boolean |  |  |
| `targetBounds` | [HPATargetBounds](schemas.md#hpatargetbounds) |  |  |

## JVMPolicy { #jvmpolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `detect` | boolean |  |  |

## RightsizingPolicySpec { #rightsizingpolicyspec }

Mirrors `RightsizingPolicy.spec` (api/v1alpha1, `kubectl explain rsp.spec`). Unset fields
inherit from the next lower layer. Validated by the shared validator
(`api/v1alpha1/validate`) exactly like the CRD.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `selector` | [PolicySelector](schemas.md#policyselector) |  | Mirrors `RightsizingPolicy.spec.selector` (api/v1alpha1). All set criteria must match. |
| `priority` | integer | min 0; max 1000000 |  |
| `profile` | Profile (enum) | one of `dev`, `production`, `high-availability` |  |
| `cpu` | [CPUPolicy](schemas.md#cpupolicy) |  |  |
| `memory` | [MemoryPolicy](schemas.md#memorypolicy) |  |  |
| `apply` | [ApplyPolicy](schemas.md#applypolicy) |  |  |
| `guardrails` | [Guardrails](schemas.md#guardrails) |  |  |
| `hpa` | [HPAPolicy](schemas.md#hpapolicy) |  |  |
| `jvm` | [JVMPolicy](schemas.md#jvmpolicy) |  |  |

## PolicyInput { #policyinput }

Body of create and update. For the default types `name` and `spec.priority` are set by the
hub (and ignored if sent); for `policy` both are required. `clusters` empty means every
cluster; `cluster-default` needs exactly one.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `type` | HubPolicyType (enum), required | one of `global-default`, `cluster-default`, `policy`, `workload-override` | `global-default` (`ro-global-defaults`, priority 0, every cluster), `cluster-default` (`ro-cluster-defaults`, priority 10, one cluster), `policy` (named, priority 1-899999), `workload-override` (`ro-override-…`, priority 900000, one workload). The agent layers the default types under the winning policy and the override above it. |
| `name` | string | pattern `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`; max length 253 |  |
| `description` | string | max length 1024 |  |
| `clusters` | array of string |  |  |
| `spec` | [RightsizingPolicySpec](schemas.md#rightsizingpolicyspec), required |  | Mirrors `RightsizingPolicy.spec` (api/v1alpha1, `kubectl explain rsp.spec`). Unset fields inherit from the next lower layer. Validated by the shared validator (`api/v1alpha1/validate`) exactly like the CRD. |
| `overrideReadiness` | boolean |  | Admin only: allow `auto-patch` on clusters that are not `AutomationReady`; audited with the change. |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## PolicyDraft { #policydraft }

A policy as in `PolicyInput`, without reason, for `POST /v1/admin/preview`.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `type` | HubPolicyType (enum), required | one of `global-default`, `cluster-default`, `policy`, `workload-override` | `global-default` (`ro-global-defaults`, priority 0, every cluster), `cluster-default` (`ro-cluster-defaults`, priority 10, one cluster), `policy` (named, priority 1-899999), `workload-override` (`ro-override-…`, priority 900000, one workload). The agent layers the default types under the winning policy and the override above it. |
| `name` | string | max length 253 |  |
| `description` | string | max length 1024 |  |
| `clusters` | array of string |  |  |
| `workloadId` | string |  | Required for `workload-override`. |
| `spec` | [RightsizingPolicySpec](schemas.md#rightsizingpolicyspec), required |  | Mirrors `RightsizingPolicy.spec` (api/v1alpha1, `kubectl explain rsp.spec`). Unset fields inherit from the next lower layer. Validated by the shared validator (`api/v1alpha1/validate`) exactly like the CRD. |

## AdminPolicy { #adminpolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  |  |
| `type` | HubPolicyType (enum), required | one of `global-default`, `cluster-default`, `policy`, `workload-override` | `global-default` (`ro-global-defaults`, priority 0, every cluster), `cluster-default` (`ro-cluster-defaults`, priority 10, one cluster), `policy` (named, priority 1-899999), `workload-override` (`ro-override-…`, priority 900000, one workload). The agent layers the default types under the winning policy and the override above it. |
| `name` | string, required |  | Name of the RightsizingPolicy CR on the clusters. |
| `description` | string |  |  |
| `priority` | integer, required |  | Equal to `spec.priority`. |
| `clusters` | array of string, required |  | Clusters the policy is sent to; empty means every cluster. |
| `workloadId` | string |  | Only for `workload-override`. |
| `spec` | [RightsizingPolicySpec](schemas.md#rightsizingpolicyspec), required |  | Mirrors `RightsizingPolicy.spec` (api/v1alpha1, `kubectl explain rsp.spec`). Unset fields inherit from the next lower layer. Validated by the shared validator (`api/v1alpha1/validate`) exactly like the CRD. |
| `version` | integer, required | min 1 | Increases with every change; the ETag is `"<version>"`. |
| `createdAt` | string (date-time), required |  |  |
| `createdBy` | [Actor](schemas.md#actor), required |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `updatedAt` | string (date-time), required |  |  |
| `updatedBy` | [Actor](schemas.md#actor), required |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `deletedAt` | string (date-time) |  |  |

## AdminPolicyList { #adminpolicylist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [AdminPolicy](schemas.md#adminpolicy), required |  |  |

## PolicyVersion { #policyversion }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `version` | integer, required | min 1 |  |
| `operation` | string, required | one of `create`, `update`, `delete`, `revert` |  |
| `revertedFrom` | integer |  | Source version of a `revert`. |
| `name` | string, required |  |  |
| `description` | string |  |  |
| `priority` | integer, required |  |  |
| `clusters` | array of string, required |  |  |
| `spec` | [RightsizingPolicySpec](schemas.md#rightsizingpolicyspec), required |  | Mirrors `RightsizingPolicy.spec` (api/v1alpha1, `kubectl explain rsp.spec`). Unset fields inherit from the next lower layer. Validated by the shared validator (`api/v1alpha1/validate`) exactly like the CRD. |
| `deleted` | boolean, required |  | True if the policy was deleted in this version. |
| `createdAt` | string (date-time), required |  |  |
| `actor` | [Actor](schemas.md#actor), required |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `reason` | string, required |  |  |

## PolicyVersionList { #policyversionlist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [PolicyVersion](schemas.md#policyversion), required |  |  |
| `continue` | string |  |  |

## PolicyRevert { #policyrevert }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `version` | integer, required | min 1 |  |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |
| `overrideReadiness` | boolean |  | Admin only: allow a revert that enables `auto-patch` on clusters that are not `AutomationReady`. Without it such a revert answers `409 cluster_not_automation_ready`. |

## PolicyInventoryItem { #policyinventoryitem }

One RightsizingPolicy CR on a cluster, as the agent sees it. `valid` is the result of the
shared validator; an invalid hub-managed policy is not materialised (its old CR, if any,
stays) and `message` says why; for a Git CR it is informational only.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required |  |  |
| `source` | PolicyOrigin (enum), required | one of `hub`, `git` | `hub`: a CR labelled `managed-by=hub`; `git`: any other CR. |
| `priority` | integer, required |  |  |
| `generation` | integer, required | min 0 | `metadata.generation`; 0 for a hub policy the agent could not materialise. |
| `valid` | boolean, required |  |  |
| `message` | string | max length 1024 |  |
| `hubPolicyId` | string (uuid) |  |  |
| `hubVersion` | integer |  |  |
| `hubPolicyType` | HubPolicyType (enum) | one of `global-default`, `cluster-default`, `policy`, `workload-override` | `global-default` (`ro-global-defaults`, priority 0, every cluster), `cluster-default` (`ro-cluster-defaults`, priority 10, one cluster), `policy` (named, priority 1-899999), `workload-override` (`ro-override-…`, priority 900000, one workload). The agent layers the default types under the winning policy and the override above it. |
| `matchedWorkloads` | integer | min 0 | Workloads for which this policy is the winning policy (or applied override). |
| `spec` | [RightsizingPolicySpec](schemas.md#rightsizingpolicyspec) |  | Mirrors `RightsizingPolicy.spec` (api/v1alpha1, `kubectl explain rsp.spec`). Unset fields inherit from the next lower layer. Validated by the shared validator (`api/v1alpha1/validate`) exactly like the CRD. |

## ClusterPolicyInventory { #clusterpolicyinventory }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `reportedAt` | string (date-time) |  |  |
| `configVersion` | string |  | Config version the agent had applied when it reported. |
| `items` | array of [PolicyInventoryItem](schemas.md#policyinventoryitem), required |  |  |

## PolicyInventoryList { #policyinventorylist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [ClusterPolicyInventory](schemas.md#clusterpolicyinventory), required |  |  |

## PolicyChange { #policychange }

A hypothetical change for the preview. `upsert` with `policyId` replaces that policy; without
it, adds a new one (a `workload-override` for a workload that already has one replaces it).
`delete` needs `policyId`.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `op` | string, required | one of `upsert`, `delete` |  |
| `policyId` | string (uuid) |  |  |
| `policy` | [PolicyDraft](schemas.md#policydraft) |  | A policy as in `PolicyInput`, without reason, for `POST /v1/admin/preview`. |

## PreviewRequest { #previewrequest }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `changes` | array of [PolicyChange](schemas.md#policychange), required | min items 1; max items 500 |  |
| `limit` | integer | min 1; max 1000; default `200` | Maximum number of `items`; the totals always cover every workload. |

## ImpactItem { #impactitem }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workloadId` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `namespace` | string, required |  |  |
| `kind` | WorkloadKind (enum), required | one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob` |  |
| `name` | string, required |  |  |
| `before` | [EffectiveModes](schemas.md#effectivemodes), required |  |  |
| `after` | [EffectiveModes](schemas.md#effectivemodes), required |  |  |
| `policyBefore` | string |  | Winning policy name before the change. |
| `policyAfter` | string |  |  |
| `monthlySavingsUSD` | [Savings](schemas.md#savings) |  | Estimated monthly savings in USD (estimated, if applied): (current - recommended request) x hourly rate x average replicas x 730 h. Negative for increases. Never added to the realized amounts (`RealizedAmount`). |

## ImpactPreview { #impactpreview }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workloadsEvaluated` | integer, required | min 0 |  |
| `workloadsChanged` | integer, required | min 0 | Workloads whose CPU or memory mode changes. |
| `savingsIntoAutoPatchUSD` | [Savings](schemas.md#savings), required |  | Estimated monthly savings of the resources whose mode becomes `auto-patch` (CPU savings of workloads whose CPU mode becomes auto-patch, likewise memory). |
| `savingsOutOfAutoPatchUSD` | [Savings](schemas.md#savings), required |  | Likewise for resources that leave `auto-patch`. |
| `items` | array of [ImpactItem](schemas.md#impactitem), required |  | Changed workloads, by savings descending, at most `limit`. |
| `truncated` | boolean, required |  |  |
| `warnings` | array of string |  | E.g. validation problems of a draft, or clusters whose labels are unknown. |

## Automation { #automation }

Pause state of one cluster (or of the fleet).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `paused` | boolean, required |  |  |
| `reason` | string |  |  |
| `by` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `since` | string (date-time) |  | When the current state was set. |
| `resumeAt` | string (date-time) |  | Only while paused; the hub resumes at this time. |
| `version` | integer, required | min 0 | 0 = never changed. The ETag is `"<version>"`. |

## AutomationUpdate { #automationupdate }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `paused` | boolean, required |  |  |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |
| `resumeAt` | string (date-time) |  | Only with `paused = true`; must be in the future. |

## FleetAutomation { #fleetautomation }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `fleet` | [Automation](schemas.md#automation), required |  | Pause state of one cluster (or of the fleet). |
| `clusters` | array of object, required |  |  |
| `clusters[].cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `clusters[].automation` | [Automation](schemas.md#automation), required |  | Pause state of one cluster (or of the fleet). |
| `clusters[].paused` | boolean, required |  | Effective state (cluster or fleet paused). |

## AgentConfigSync { #agentconfigsync }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `agentVersion` | string |  |  |
| `lastHeartbeatAt` | string (date-time) |  |  |
| `lastConfigPullAt` | string (date-time) |  |  |
| `configVersion` | string, required |  | Current config version the hub serves this cluster. |
| `appliedConfigVersion` | string |  | Config version the agent last reported as applied. |
| `inSync` | boolean, required |  | `appliedConfigVersion = configVersion`. |

## ClusterAdmin { #clusteradmin }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `description` | string, required |  |  |
| `environment` | Environment (enum) | one of `prod`, `non-prod` | Writes to a `prod` cluster ask for confirmation in the UI and some need an admin. |
| `owner` | string, required |  | Owning team. |
| `policySource` | ClusterPolicySource (enum), required | one of `hub`, `git`, `both` | Where a cluster's policies come from. `hub`: hub-managed policies only (Git CRs are still reported). `git`: no hub-managed policies are sent and the admin pages are read-only for that cluster. `both`: both. |
| `createdAt` | string (date-time) |  |  |
| `updatedAt` | string (date-time) |  |  |
| `updatedBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `deletedAt` | string (date-time) |  |  |
| `version` | integer, required | min 1 | Version of the metadata; the ETag is `"<version>"`. |
| `automation` | [Automation](schemas.md#automation), required |  | Pause state of one cluster (or of the fleet). |
| `paused` | boolean, required |  | Effective pause (cluster or fleet). |
| `agent` | [AgentConfigSync](schemas.md#agentconfigsync), required |  |  |
| `policyInventory` | [ClusterPolicyInventory](schemas.md#clusterpolicyinventory) |  |  |
| `provider` | string | max length 64 | Informational, e.g. `aws`. |
| `region` | string | max length 64 | Informational. |
| `metrics` | [MetricsBinding](schemas.md#metricsbinding), required |  | Where a cluster's metrics are. Every query uses `{<clusterLabel>="<clusterLabelValue>", <extraMatchers>}`; results are re-labelled to the registered cluster name. `clusterLabel` `""` means no cluster matcher (a per-cluster Prometheus). Defaults: `local`, `cluster`, the cluster name. |
| `readiness` | [ReadinessSummary](schemas.md#readinesssummary), required |  |  |

## ClusterCreate { #clustercreate }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `description` | string | max length 1024 |  |
| `environment` | Environment (enum), required | one of `prod`, `non-prod` | Writes to a `prod` cluster ask for confirmation in the UI and some need an admin. |
| `owner` | string | max length 128 |  |
| `policySource` | ClusterPolicySource (enum) | default `both` |  |
| `provider` | string | max length 64 | Informational, e.g. `aws`. |
| `region` | string | max length 64 | Informational. |
| `metrics` | [MetricsBinding](schemas.md#metricsbinding) |  | Default: datasource `local`, label `cluster`, value = the cluster name. |
| `createAgentToken` | boolean | default `True` |  |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## ClusterCreated { #clustercreated }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | [ClusterAdmin](schemas.md#clusteradmin), required |  |  |
| `agentToken` | [TokenCreated](schemas.md#tokencreated) |  |  |

## ClusterUpdate { #clusterupdate }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `description` | string | max length 1024 |  |
| `environment` | Environment (enum) | one of `prod`, `non-prod` | Writes to a `prod` cluster ask for confirmation in the UI and some need an admin. |
| `owner` | string | max length 128 |  |
| `policySource` | ClusterPolicySource (enum) | one of `hub`, `git`, `both` | Where a cluster's policies come from. `hub`: hub-managed policies only (Git CRs are still reported). `git`: no hub-managed policies are sent and the admin pages are read-only for that cluster. `both`: both. |
| `provider` | string | max length 64 | Informational, e.g. `aws`. |
| `region` | string | max length 64 | Informational. |
| `metrics` | [MetricsBinding](schemas.md#metricsbinding) |  | Where a cluster's metrics are. Every query uses `{<clusterLabel>="<clusterLabelValue>", <extraMatchers>}`; results are re-labelled to the registered cluster name. `clusterLabel` `""` means no cluster matcher (a per-cluster Prometheus). Defaults: `local`, `cluster`, the cluster name. |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## ClusterConfig { #clusterconfig }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `configVersion` | string, required |  | Opaque; changes whenever anything in this document changes. |
| `generatedAt` | string (date-time), required |  |  |
| `policySource` | ClusterPolicySource (enum), required | one of `hub`, `git`, `both` | Where a cluster's policies come from. `hub`: hub-managed policies only (Git CRs are still reported). `git`: no hub-managed policies are sent and the admin pages are read-only for that cluster. `both`: both. |
| `policies` | array of [ConfigPolicy](schemas.md#configpolicy), required |  | The complete set of hub-managed policies for the cluster: the agent deletes hub-labelled CRs that are not listed. |
| `flags` | [ConfigFlags](schemas.md#configflags), required |  |  |
| `approvals` | array of [ConfigApproval](schemas.md#configapproval), required |  |  |
| `hubVersion` | string |  | For the agent's `agent.version` check. |
| `minAgentVersion` | string |  | Oldest agent version this hub supports (`agent.version` check). |

## ConfigPolicy { #configpolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  | Annotation `kerf.getkerf.dev/hub-policy-id`. |
| `name` | string, required |  | Name of the CR. |
| `type` | HubPolicyType (enum), required | one of `global-default`, `cluster-default`, `policy`, `workload-override` | `global-default` (`ro-global-defaults`, priority 0, every cluster), `cluster-default` (`ro-cluster-defaults`, priority 10, one cluster), `policy` (named, priority 1-899999), `workload-override` (`ro-override-…`, priority 900000, one workload). The agent layers the default types under the winning policy and the override above it. |
| `version` | integer, required | min 1 | Annotation `kerf.getkerf.dev/hub-version`. |
| `priority` | integer, required |  | Equal to `spec.priority`. |
| `spec` | [RightsizingPolicySpec](schemas.md#rightsizingpolicyspec), required |  | Mirrors `RightsizingPolicy.spec` (api/v1alpha1, `kubectl explain rsp.spec`). Unset fields inherit from the next lower layer. Validated by the shared validator (`api/v1alpha1/validate`) exactly like the CRD. |

## ConfigFlags { #configflags }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `paused` | boolean, required |  | Cluster or fleet paused. Behaves like a cluster-wide freeze (reason `Paused`). |
| `pauseScope` | string | one of `cluster`, `fleet` | Which pause is in effect (`fleet` if both). |
| `pauseReason` | string |  |  |
| `pausedAt` | string (date-time) |  |  |
| `resumeAt` | string (date-time) |  | Informational; the hub ends the pause, the agent does not. |
| `recheck` | boolean |  | A readiness re-check is pending: the agent re-runs its self-checks if `recheckRequestedAt` is newer than its last run, and reports them in the next batch. |
| `recheckRequestedAt` | string (date-time) |  |  |

## ConfigApproval { #configapproval }

An approve-once grant. The agent applies the recommendation `recommendationID` of the
workload once, treating its `audit` resources as `auto-patch`, with every guardrail, if that
is still the latest recommendation and `expiresAt` has not passed. It then emits `applied`
with reason `ApprovedOnce` and `details.approvalId`; the hub marks the approval consumed and
stops sending it.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  |  |
| `workload` | [WorkloadRef](schemas.md#workloadref), required |  |  |
| `recommendationID` | string (uuid), required |  |  |
| `expiresAt` | string (date-time), required |  |  |
| `approvedBy` | string, required |  | Actor name, for the event message. |
| `approvedAt` | string (date-time), required |  |  |

## ApprovalRequest { #approvalrequest }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `recommendationID` | string (uuid), required |  |  |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |
| `expiresAt` | string (date-time) |  | Default 24 h from now; at most 7 days. |
| `overrideReadiness` | boolean |  | Admin only: allow `auto-patch` on clusters that are not `AutomationReady`; audited with the change. |

## Approval { #approval }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  |  |
| `workloadId` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |
| `recommendationID` | string (uuid), required |  |  |
| `approvedBy` | [Actor](schemas.md#actor), required |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `approvedAt` | string (date-time), required |  |  |
| `expiresAt` | string (date-time), required |  |  |
| `reason` | string, required |  |  |
| `state` | string, required | one of `pending`, `consumed`, `expired`, `superseded` | `superseded`: replaced by a newer approval or a newer recommendation. |
| `consumedAt` | string (date-time) |  |  |
| `consumedEventId` | string (uuid) |  | The agent's `applied` / `ApprovedOnce` event. |

## WorkloadOverrideInput { #workloadoverrideinput }

`spec` without `selector` and `priority` (the hub sets both; sending them is a `400`, code
`invalid_override`).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `description` | string | max length 1024 |  |
| `spec` | [RightsizingPolicySpec](schemas.md#rightsizingpolicyspec), required |  | Mirrors `RightsizingPolicy.spec` (api/v1alpha1, `kubectl explain rsp.spec`). Unset fields inherit from the next lower layer. Validated by the shared validator (`api/v1alpha1/validate`) exactly like the CRD. |
| `overrideReadiness` | boolean |  | Admin only: allow `auto-patch` on clusters that are not `AutomationReady`; audited with the change. |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## LayerSource { #layersource }

A precedence layer, lowest first: `built-in` (documented global defaults), `global-default`
(hub `ro-global-defaults`), `cluster-default-chart` (agent chart value `clusterDefaults`),
`cluster-default` (hub `ro-cluster-defaults`), `policy` (the winning RightsizingPolicy, hub
or Git), `workload-override` (hub), `annotation` (workload annotations).

Type: string. one of `built-in`, `global-default`, `cluster-default-chart`, `cluster-default`, `policy`, `workload-override`, `annotation`.

## PolicyLayer { #policylayer }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `source` | LayerSource (enum), required | one of `built-in`, `global-default`, `cluster-default-chart`, `cluster-default`, `policy`, `workload-override`, `annotation` | A precedence layer, lowest first: `built-in` (documented global defaults), `global-default` (hub `ro-global-defaults`), `cluster-default-chart` (agent chart value `clusterDefaults`), `cluster-default` (hub `ro-cluster-defaults`), `policy` (the winning RightsizingPolicy, hub or Git), `workload-override` (hub), `annotation` (workload annotations). |
| `name` | string |  | Policy name (or annotation key for `annotation`). |
| `origin` | PolicyOrigin (enum) | one of `hub`, `git` | `hub`: a CR labelled `managed-by=hub`; `git`: any other CR. |
| `policyId` | string (uuid) |  | Hub policy id, for hub-managed layers. |
| `priority` | integer |  |  |
| `applied` | boolean, required |  | False for a matching policy that lost on priority (listed so the UI can explain it) and for an invalid policy. |
| `note` | string |  | Why a layer was not applied, e.g. "priority 100 < 200 of payments". |
| `spec` | [RightsizingPolicySpec](schemas.md#rightsizingpolicyspec) |  | The fields this layer sets. |

## EffectiveField { #effectivefield }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `path` | string, required | example `cpu.mode` | JSON path in the spec, e.g. `cpu.mode`, `guardrails.rollback.oomKills`. |
| `value` | string, required |  | The effective value as a string; lists and objects as JSON. |
| `source` | LayerSource (enum), required | one of `built-in`, `global-default`, `cluster-default-chart`, `cluster-default`, `policy`, `workload-override`, `annotation` | A precedence layer, lowest first: `built-in` (documented global defaults), `global-default` (hub `ro-global-defaults`), `cluster-default-chart` (agent chart value `clusterDefaults`), `cluster-default` (hub `ro-cluster-defaults`), `policy` (the winning RightsizingPolicy, hub or Git), `workload-override` (hub), `annotation` (workload annotations). |
| `name` | string |  | Name of the policy (or annotation key) that set it. |

## EffectivePolicy { #effectivepolicy }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `workloadId` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |
| `layers` | array of [PolicyLayer](schemas.md#policylayer), required |  | Lowest precedence first. |
| `fields` | array of [EffectiveField](schemas.md#effectivefield), required |  |  |
| `modes` | [EffectiveModes](schemas.md#effectivemodes), required |  |  |
| `agentModes` | [EffectiveModes](schemas.md#effectivemodes) |  | Modes the agent last reported; absent if never reported. |
| `agentPolicy` | string |  | Winning policy the agent last reported. |
| `warnings` | array of string |  | E.g. "workload labels unknown; matchLabels selectors assumed not to match". |

## ConfigTargetType { #configtargettype }

Type: string. one of `settings`, `policy`, `workload-override`, `approval`, `cluster`, `automation`, `token`, `user`, `datasource`, `readiness`, `notification`.

## ConfigAction { #configaction }

CamelCase action of a configuration change (`docs/contracts/events.md`).

Type: string. one of `SettingsUpdated`, `PolicyCreated`, `PolicyUpdated`, `PolicyDeleted`, `PolicyReverted`, `OverrideSet`, `OverrideCleared`, `WorkloadApproved`, `ClusterCreated`, `ClusterUpdated`, `ClusterDeleted`, `AutomationPaused`, `AutomationResumed`, `TokenCreated`, `TokenRevoked`, `UserCreated`, `UserUpdated`, `UserDeleted`, `PasswordReset`, `DatasourceCreated`, `DatasourceUpdated`, `DatasourceDeleted`, `ReadinessChanged`, `NotificationChannelCreated`, `NotificationChannelUpdated`, `NotificationChannelDeleted`, `NotificationRuleCreated`, `NotificationRuleUpdated`, `NotificationRuleDeleted`, `NotificationDigestUpdated`.

## ConfigTarget { #configtarget }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `type` | ConfigTargetType (enum), required | one of `settings`, `policy`, `workload-override`, `approval`, `cluster`, `automation`, `token`, `user`, `datasource`, `readiness`, `notification` |  |
| `id` | string, required |  | Policy or approval id, settings section, cluster name (`*` for the fleet automation), token id or username. |
| `name` | string |  | Display name, e.g. the policy name. |
| `cluster` | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `workloadId` | string | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[^/]+/(Deployment\|StatefulSet\|DaemonSet\|CronJob)/[^/]+$`; example `ro-spoke-1/payments/Deployment/checkout` | `<cluster>/<namespace>/<kind>/<name>` |

## ConfigChange { #configchange }

One configuration change (audit type `config_changed`). `before` and `after` are the
target's API representation before and after the change (`null` on create / delete),
never including password hashes or token values.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  |  |
| `type` | string, required | one of `config_changed` |  |
| `time` | string (date-time), required |  |  |
| `actor` | [Actor](schemas.md#actor), required |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `action` | ConfigAction (enum), required | one of `SettingsUpdated`, `PolicyCreated`, `PolicyUpdated`, `PolicyDeleted`, `PolicyReverted`, `OverrideSet`, `OverrideCleared`, `WorkloadApproved`, `ClusterCreated`, `ClusterUpdated`, `ClusterDeleted`, `AutomationPaused`, `AutomationResumed`, `TokenCreated`, `TokenRevoked`, `UserCreated`, `UserUpdated`, `UserDeleted`, `PasswordReset`, `DatasourceCreated`, `DatasourceUpdated`, `DatasourceDeleted`, `ReadinessChanged`, `NotificationChannelCreated`, `NotificationChannelUpdated`, `NotificationChannelDeleted`, `NotificationRuleCreated`, `NotificationRuleUpdated`, `NotificationRuleDeleted`, `NotificationDigestUpdated` | CamelCase action of a configuration change (`docs/contracts/events.md`). |
| `target` | [ConfigTarget](schemas.md#configtarget), required |  |  |
| `reason` | string, required |  | The reason given with the change. |
| `version` | integer |  | Version of the target after the change, if it is versioned. |
| `before` | object or null |  |  |
| `after` | object or null |  |  |

## ConfigChangeList { #configchangelist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [ConfigChange](schemas.md#configchange), required |  |  |
| `continue` | string |  |  |

## DatasourceType { #datasourcetype }

Type: string. one of `prometheus`, `victoriametrics`, `thanos`, `mimir`, `amp`.

## DatasourceAuthType { #datasourceauthtype }

Type: string. one of `none`, `bearer`, `basic`, `sigv4`, `mtls`.

## DatasourceSigV4 { #datasourcesigv4 }

AWS SigV4 (Amazon Managed Prometheus); credentials come from IRSA / Pod Identity.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `region` | string, required |  |  |
| `service` | string | default `aps` |  |
| `roleARN` | string |  | Role to assume; absent = the hub's own identity. |

## DatasourceTLSInput { #datasourcetlsinput }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `caCert` | string |  | PEM; not secret, returned as `caConfigured`. |
| `clientCert` | string |  | PEM (mTLS). |
| `clientKey` | string | write-only | PEM (mTLS). Write-only. |
| `insecureSkipVerify` | boolean |  |  |

## DatasourceAuthInput { #datasourceauthinput }

Secret fields (`token`, `password`, `tls.clientKey`) are write-only. On update, omitted
secret fields keep the stored values.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `type` | DatasourceAuthType (enum), required | one of `none`, `bearer`, `basic`, `sigv4`, `mtls` |  |
| `token` | string | write-only | For `bearer`. |
| `username` | string |  | For `basic`. |
| `password` | string | write-only | For `basic`. |
| `sigv4` | [DatasourceSigV4](schemas.md#datasourcesigv4) |  | AWS SigV4 (Amazon Managed Prometheus); credentials come from IRSA / Pod Identity. |
| `tls` | [DatasourceTLSInput](schemas.md#datasourcetlsinput) |  |  |

## DatasourceAuthInfo { #datasourceauthinfo }

The stored auth without secrets.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `type` | DatasourceAuthType (enum), required | one of `none`, `bearer`, `basic`, `sigv4`, `mtls` |  |
| `username` | string |  |  |
| `sigv4` | [DatasourceSigV4](schemas.md#datasourcesigv4) |  | AWS SigV4 (Amazon Managed Prometheus); credentials come from IRSA / Pod Identity. |
| `secretConfigured` | boolean, required |  | A token, password or client key is stored. |
| `caConfigured` | boolean |  |  |
| `clientCertConfigured` | boolean |  |  |
| `insecureSkipVerify` | boolean |  |  |

## DatasourceInput { #datasourceinput }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$` |  |
| `type` | DatasourceType (enum), required | one of `prometheus`, `victoriametrics`, `thanos`, `mimir`, `amp` |  |
| `url` | string (uri), required |  | Query base URL (`/api/v1/query` is appended). |
| `auth` | [DatasourceAuthInput](schemas.md#datasourceauthinput) |  | Secret fields (`token`, `password`, `tls.clientKey`) are write-only. On update, omitted secret fields keep the stored values. |
| `clearSecrets` | boolean |  | On update, remove the stored secrets. |
| `headers` | map of string |  | Extra request headers, e.g. `X-Scope-OrgID`. Not secret. |
| `timeout` | string | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `maxPoints` | integer | min 100 | Most points per series per query; the recommender chunks queries to fit. |
| `reason` | string, required | min length 1; max length 1024 | Why the change is made; stored in the audit log. |

## Datasource { #datasource }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string, required |  | `local` for the built-in datasource, otherwise a UUID. |
| `name` | string, required |  |  |
| `type` | DatasourceType (enum), required | one of `prometheus`, `victoriametrics`, `thanos`, `mimir`, `amp` |  |
| `url` | string, required |  |  |
| `builtIn` | boolean, required |  |  |
| `auth` | [DatasourceAuthInfo](schemas.md#datasourceauthinfo), required |  | The stored auth without secrets. |
| `headers` | map of string, required |  |  |
| `timeout` | string | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `maxPoints` | integer |  |  |
| `usedBy` | array of string, required |  | Clusters bound to it. |
| `version` | integer, required | min 1 |  |
| `createdAt` | string (date-time) |  |  |
| `createdBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `updatedAt` | string (date-time) |  |  |
| `updatedBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |

## DatasourceList { #datasourcelist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [Datasource](schemas.md#datasource), required |  |  |

## MetricsJobNames { #metricsjobnames }

Scrape job names, only used to make diagnoses precise.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cadvisor` | array of string |  |  |
| `kubeStateMetrics` | array of string |  |  |

## ClusterMetricsBinding { #clustermetricsbinding }

A cluster's metrics binding for the pricing service.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | string, required |  |  |
| `binding` | [MetricsBinding](schemas.md#metricsbinding), required |  | Where a cluster's metrics are. Every query uses `{<clusterLabel>="<clusterLabelValue>", <extraMatchers>}`; results are re-labelled to the registered cluster name. `clusterLabel` `""` means no cluster matcher (a per-cluster Prometheus). Defaults: `local`, `cluster`, the cluster name. |
| `bound` | boolean, required |  | false = no stored binding (datasource local, label cluster = the name). |

## MetricsBindingList { #metricsbindinglist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [ClusterMetricsBinding](schemas.md#clustermetricsbinding), required |  |  |

## PromQueryForm { #promqueryform }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `query` | string, required |  |  |
| `time` | string |  |  |
| `start` | string |  |  |
| `end` | string |  |  |
| `step` | string |  |  |
| `timeout` | string |  |  |

## PromQueryResponse { #promqueryresponse }

Prometheus HTTP API response (`status`, `data`, `errorType`, `error`), passed through.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `status` | string, required | one of `success`, `error` |  |
| `data` | object |  |  |
| `errorType` | string |  |  |
| `error` | string |  |  |

## MetricsBinding { #metricsbinding }

Where a cluster's metrics are. Every query uses
`{<clusterLabel>="<clusterLabelValue>", <extraMatchers>}`; results are re-labelled to the
registered cluster name. `clusterLabel` `""` means no cluster matcher (a per-cluster
Prometheus). Defaults: `local`, `cluster`, the cluster name.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `datasourceId` | string, required |  |  |
| `clusterLabel` | string, required | pattern `^([a-zA-Z_][a-zA-Z0-9_]*)?$` |  |
| `clusterLabelValue` | string |  | Default the cluster name. |
| `extraMatchers` | array of string | max items 16 |  |
| `jobNames` | [MetricsJobNames](schemas.md#metricsjobnames) |  | Scrape job names, only used to make diagnoses precise. |

## DatasourceTestRequest { #datasourcetestrequest }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `binding` | [MetricsBinding](schemas.md#metricsbinding) |  | Where a cluster's metrics are. Every query uses `{<clusterLabel>="<clusterLabelValue>", <extraMatchers>}`; results are re-labelled to the registered cluster name. `clusterLabel` `""` means no cluster matcher (a per-cluster Prometheus). Defaults: `local`, `cluster`, the cluster name. |

## DatasourceTestResult { #datasourcetestresult }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `ok` | boolean, required |  | No blocking check failed. |
| `latencyMs` | integer | min 0 |  |
| `found` | object |  | What the binding's matchers found. |
| `found.containerSeries` | integer | min 0 |  |
| `found.pods` | integer | min 0 |  |
| `found.nodes` | integer | min 0 |  |
| `labelValues` | array of string |  | Existing values of the cluster label (suggestions when it does not match). |
| `checks` | array of [ReadinessCheck](schemas.md#readinesscheck), required |  |  |

## ReadinessState { #readinessstate }

Each state includes the ones before it (docs/contracts/readiness.md).

Type: string. one of `Registered`, `AgentConnected`, `MetricsOK`, `ReadyAudit`, `AutomationReady`.

## ReadinessSeverity { #readinessseverity }

Type: string. one of `blocking`, `warning`, `info`.

## ReadinessStatus { #readinessstatus }

Type: string. one of `pass`, `warn`, `fail`, `unknown`.

## ReadinessCategory { #readinesscategory }

Type: string. one of `datasource`, `metrics`, `pricing`, `agent`, `rbac`, `apply`, `webhook`, `nodes`, `integration`, `policy`.

## ReadinessCheckID { #readinesscheckid }

Stable check id from the catalog (docs/contracts/readiness.md, api/v1alpha1/readiness).

Type: string. pattern `^[a-z]+\.[a-z0-9-]+$`; example `metrics.ksm-owners`, `rbac.resize`.

## ReadinessCheck { #readinesscheck }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string, required | pattern `^[a-z]+\.[a-z0-9-]+$`; example `metrics.ksm-owners`, `rbac.resize` | Stable check id from the catalog (docs/contracts/readiness.md, api/v1alpha1/readiness). |
| `category` | ReadinessCategory (enum), required | one of `datasource`, `metrics`, `pricing`, `agent`, `rbac`, `apply`, `webhook`, `nodes`, `integration`, `policy` |  |
| `severity` | ReadinessSeverity (enum), required | one of `blocking`, `warning`, `info` |  |
| `source` | string, required | one of `hub`, `agent` |  |
| `gates` | ReadinessState (enum) |  | For blocking checks, the state that needs this check to pass. |
| `status` | ReadinessStatus (enum), required | one of `pass`, `warn`, `fail`, `unknown` |  |
| `message` | string, required | max length 2048 |  |
| `remediation` | string |  | Exact command or values snippet that fixes it. |
| `remediationType` | string | one of `command`, `yaml`, `text` |  |
| `docsURL` | string |  |  |
| `checkedAt` | string (date-time), required |  |  |
| `details` | map of string |  |  |

## AgentCapability { #agentcapability }

Result of the SelfSubjectAccessReviews of one capability (check `rbac.<name>`).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required | one of `read`, `resize`, `rollout`, `hpa`, `evict`, `policies`, `events` |  |
| `granted` | boolean, required |  |  |
| `missing` | array of string |  | ClusterRole rules to add, each as JSON (`{"apiGroups":[…],"resources":[…],"verbs":[…]}`). |
| `message` | string |  |  |

## ReadinessSummary { #readinesssummary }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `state` | ReadinessState (enum), required | one of `Registered`, `AgentConnected`, `MetricsOK`, `ReadyAudit`, `AutomationReady` | Each state includes the ones before it (docs/contracts/readiness.md). |
| `blocking` | integer, required | min 0 | Blocking checks not passing. |
| `warnings` | integer, required | min 0 | Warning checks with status warn or fail. |
| `checkedAt` | string (date-time) |  |  |

## ClusterReadiness { #clusterreadiness }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `cluster` | string, required | pattern `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`; example `ro-spoke-1` |  |
| `state` | ReadinessState (enum), required | one of `Registered`, `AgentConnected`, `MetricsOK`, `ReadyAudit`, `AutomationReady` | Each state includes the ones before it (docs/contracts/readiness.md). |
| `stateSince` | string (date-time) |  |  |
| `checkedAt` | string (date-time) |  | Newest result. |
| `summary` | object, required |  |  |
| `summary.blocking` | integer, required | min 0 |  |
| `summary.warnings` | integer, required | min 0 |  |
| `summary.unknown` | integer, required | min 0 |  |
| `nextState` | ReadinessState (enum) |  | Absent at AutomationReady. |
| `blockedBy` | array of string |  | Ids of the blocking checks that keep the cluster from `nextState`. |
| `capabilities` | array of [AgentCapability](schemas.md#agentcapability) |  |  |
| `recheckRequestedAt` | string (date-time) |  | Present while a re-check is pending. |
| `checks` | array of [ReadinessCheck](schemas.md#readinesscheck), required |  | One result per catalog check, in catalog order. |

## RecheckAccepted { #recheckaccepted }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `requestedAt` | string (date-time), required |  |  |
| `hubChecks` | boolean, required |  | Hub metrics checks queued. |
| `agentRecheck` | boolean, required |  | Recheck flag set in the cluster config. |

## ReadinessHistoryEntry { #readinesshistoryentry }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `time` | string (date-time), required |  |  |
| `kind` | string, required | one of `state`, `check` |  |
| `fromState` | ReadinessState (enum) | one of `Registered`, `AgentConnected`, `MetricsOK`, `ReadyAudit`, `AutomationReady` | Each state includes the ones before it (docs/contracts/readiness.md). |
| `toState` | ReadinessState (enum) | one of `Registered`, `AgentConnected`, `MetricsOK`, `ReadyAudit`, `AutomationReady` | Each state includes the ones before it (docs/contracts/readiness.md). |
| `checkId` | string | pattern `^[a-z]+\.[a-z0-9-]+$`; example `metrics.ksm-owners`, `rbac.resize` | Stable check id from the catalog (docs/contracts/readiness.md, api/v1alpha1/readiness). |
| `fromStatus` | ReadinessStatus (enum) | one of `pass`, `warn`, `fail`, `unknown` |  |
| `toStatus` | ReadinessStatus (enum) | one of `pass`, `warn`, `fail`, `unknown` |  |
| `message` | string |  |  |

## ReadinessHistory { #readinesshistory }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [ReadinessHistoryEntry](schemas.md#readinesshistoryentry), required |  |  |
| `continue` | string |  |  |

## InstallFormat { #installformat }

Type: string. one of `helm`, `values`, `argocd`.

## InstallSnippet { #installsnippet }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `format` | InstallFormat (enum), required | one of `helm`, `values`, `argocd` |  |
| `content` | string, required |  | Shell commands (`helm`), a values file (`values`) or an Application manifest (`argocd`). |
| `filename` | string, required | example `install.sh`, `values.yaml` |  |
| `argocdIgnoreDifferences` | string, required |  | Snippet for `argocd-cm` so Argo CD ignores fields owned by `kerf`. |
| `notes` | array of string |  |  |

## AgentComponent { #agentcomponent }

A Kerf component the agent found running in its cluster. Today: the pods behind the
webhook Service (`component: webhook`), with the tag of their `webhook` container image as
`version` (`sha256:<12 hex>` for an image pinned only by digest).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `component` | string, required | one of `webhook` |  |
| `instance` | string, required |  | Pod name. |
| `version` | string, required |  |  |

## ComponentName { #componentname }

Type: string. one of `hub`, `pricing`, `agent`, `webhook`.

## VersionCompat { #versioncompat }

The component's version against the hub's (both semver): `same`, `older`, `newer`; `unknown`
when either is not a release version (e.g. `dev`) or the component has not reported.

Type: string. one of `same`, `older`, `newer`, `unknown`.

## ComponentVersion { #componentversion }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `component` | ComponentName (enum), required | one of `hub`, `pricing`, `agent`, `webhook` |  |
| `cluster` | string |  | The workload cluster (agent, webhook); absent for hub-side components. |
| `instance` | string |  | Pod name (hub replicas, pricing, webhook pods). |
| `version` | string, required |  | Empty when the component has not reported a version. |
| `startedAt` | string (date-time) |  | Hub replicas only. |
| `lastSeenAt` | string (date-time) |  |  |
| `leader` | boolean |  | Hub replicas only; true for the replica holding the leader lock. |
| `agentStatus` | string |  | Agents only (`ClusterInfo.agentStatus`). |
| `compat` | VersionCompat (enum), required | one of `same`, `older`, `newer`, `unknown` | The component's version against the hub's (both semver): `same`, `older`, `newer`; `unknown` when either is not a release version (e.g. `dev`) or the component has not reported. |
| `message` | string |  | What to do about a mismatch, e.g. "Older than the hub (0.2.0): upgrade the chart in ro-spoke-1." |

## UpdateState { #updatestate }

`up_to_date`, `update_available`, `disabled` (`hub.updateCheck.enabled: false`), `failed` (the
last check failed: `error`; `latestVersion` is the last one found), `pending` (enabled, not
checked yet) or `dev_build` (the hub is not a release version, so nothing is compared).

Type: string. one of `up_to_date`, `update_available`, `disabled`, `failed`, `pending`, `dev_build`.

## UpdateStatus { #updatestatus }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `enabled` | boolean, required |  |  |
| `state` | UpdateState (enum), required | one of `up_to_date`, `update_available`, `disabled`, `failed`, `pending`, `dev_build` | `up_to_date`, `update_available`, `disabled` (`hub.updateCheck.enabled: false`), `failed` (the last check failed: `error`; `latestVersion` is the last one found), `pending` (enabled, not checked yet) or `dev_build` (the hub is not a release version, so nothing is compared). |
| `currentVersion` | string, required |  | The hub version the result was computed for. |
| `latestVersion` | string |  | The latest stable release (or a pre-release of the running pre-release's minor). |
| `releaseName` | string |  |  |
| `releaseURL` | string (uri) |  | Release notes. |
| `publishedAt` | string (date-time) |  |  |
| `checkedAt` | string (date-time) |  |  |
| `lastSuccessAt` | string (date-time) |  |  |
| `error` | string |  |  |
| `interval` | string, required | pattern `^([0-9]+(\.[0-9]+)?(ns\|us\|µs\|ms\|s\|m\|h))+$`; example `10m`, `168h` | Go duration. |
| `source` | string, required | example `https://api.github.com/repos/kerf-dev/kerf/releases` | The releases URL that is read. |

## UpgradeCommands { #upgradecommands }

`helm upgrade` commands to the latest version (docs/install/eks.md §8): the hub cluster first,
then each workload cluster. `<...>` marks what to fill in.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `version` | string, required |  |  |
| `chart` | string, required |  |  |
| `hubCommand` | string, required |  |  |
| `spokeCommand` | string, required |  |  |
| `docsURL` | string (uri), required |  |  |

## Maintenance { #maintenance }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `hubVersion` | string, required |  | Version of the hub replica that answered. |
| `chartVersion` | string |  | Version of the hub chart this replica was installed with. |
| `components` | array of [ComponentVersion](schemas.md#componentversion), required |  | Hub replicas, pricing instances, then per cluster (by name) its agent (one entry, also when it never reported) and webhook pods. |
| `update` | [UpdateStatus](schemas.md#updatestatus), required |  |  |
| `upgrade` | [UpgradeCommands](schemas.md#upgradecommands) |  | `helm upgrade` commands to the latest version (docs/install/eks.md §8): the hub cluster first, then each workload cluster. `<...>` marks what to fill in. |

## Problem { #problem }

RFC 9457 problem details; the single error schema for every endpoint. `code` is a stable
machine-readable identifier (e.g. `cluster_forbidden`, `invalid_workload_id`).

| Field | Type | Constraints | Description |
|---|---|---|---|
| `type` | string (uri-reference) |  |  |
| `title` | string |  |  |
| `status` | integer |  |  |
| `detail` | string |  |  |
| `instance` | string |  |  |
| `code` | string |  |  |

## NotificationChannelType { #notificationchanneltype }

Type: string. one of `slack`, `teams`, `webhook`.

## NotificationEventType { #notificationeventtype }

`applied`, `rolled_back`, `failed` and `blocked` are the workload events of `docs/contracts/events.md`;
`readiness_degraded` (a cluster's readiness state went down), `automation_paused` and
`automation_resumed` come from the configuration audit.

Type: string. one of `applied`, `rolled_back`, `failed`, `blocked`, `readiness_degraded`, `automation_paused`, `automation_resumed`.

## NotificationDeliveryKind { #notificationdeliverykind }

Type: string. one of `event`, `digest`, `test`.

## NotificationDeliveryStatus { #notificationdeliverystatus }

Type: string. one of `pending`, `sent`, `failed`.

## NotificationChannelInput { #notificationchannelinput }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required |  | A DNS-style label (lower case letters, digits, `-`; at most 63). |
| `type` | NotificationChannelType (enum), required | one of `slack`, `teams`, `webhook` |  |
| `url` | string (uri) | write-only | The incoming webhook URL. Required on create. `https` only, unless the hub allows `http` (`notifications.allowInsecureHTTP`, for tests). |
| `secret` | string | write-only | Webhook channels only, the HMAC-SHA256 signing secret (at least 16 characters). |
| `clearSecret` | boolean |  | Remove the stored signing secret (webhook channels). |
| `enabled` | boolean |  | Default true. |
| `reason` | string, required | min length 1; max length 1024 |  |

## NotificationLastDelivery { #notificationlastdelivery }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `status` | string, required | one of `sent`, `failed`, `retrying` |  |
| `at` | string (date-time), required |  |  |
| `error` | string |  | Why the last attempt failed (never contains the URL). |

## NotificationChannel { #notificationchannel }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  |  |
| `name` | string, required |  |  |
| `type` | NotificationChannelType (enum), required | one of `slack`, `teams`, `webhook` |  |
| `source` | string, required | one of `api`, `config` | `config`: defined in the chart values, read-only here. |
| `enabled` | boolean, required |  |  |
| `url` | string, required |  | The URL masked to its scheme and host. |
| `secretSet` | boolean, required |  | A signing secret is stored (webhook channels). |
| `pending` | integer, required |  | Deliveries queued for this channel. |
| `lastDelivery` | [NotificationLastDelivery](schemas.md#notificationlastdelivery) |  |  |
| `lastSuccessAt` | string (date-time) |  |  |
| `version` | integer, required |  |  |
| `createdAt` | string (date-time), required |  |  |
| `createdBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `updatedAt` | string (date-time), required |  |  |
| `updatedBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |

## NotificationChannelList { #notificationchannellist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [NotificationChannel](schemas.md#notificationchannel), required |  |  |

## NotificationTestResult { #notificationtestresult }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `ok` | boolean, required |  |  |
| `httpStatus` | integer |  | The destination's HTTP status, when it answered. |
| `latencyMs` | integer, required |  |  |
| `error` | string |  |  |

## NotificationRuleInput { #notificationruleinput }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `name` | string, required | min length 1; max length 100 |  |
| `enabled` | boolean |  | Default true. |
| `eventTypes` | array of NotificationEventType (enum), required | min items 1 |  |
| `clusters` | array of string |  | Glob patterns of cluster names; empty = every cluster. |
| `namespaces` | array of string |  | Glob patterns of namespaces; empty = every namespace. |
| `channelIds` | array of string (uuid), required | min items 1 |  |
| `reason` | string, required | min length 1; max length 1024 |  |

## NotificationRule { #notificationrule }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  |  |
| `name` | string, required |  |  |
| `enabled` | boolean, required |  |  |
| `eventTypes` | array of NotificationEventType (enum), required |  |  |
| `clusters` | array of string, required |  |  |
| `namespaces` | array of string, required |  |  |
| `channelIds` | array of string (uuid), required |  |  |
| `version` | integer, required |  |  |
| `createdAt` | string (date-time), required |  |  |
| `createdBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |
| `updatedAt` | string (date-time), required |  |  |
| `updatedBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |

## NotificationRuleList { #notificationrulelist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [NotificationRule](schemas.md#notificationrule), required |  |  |

## NotificationDigestInput { #notificationdigestinput }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `enabled` | boolean, required |  |  |
| `weekday` | integer, required | min 0; max 6 | 0 = Sunday ... 6 = Saturday. |
| `time` | string, required | pattern `^([01][0-9]\|2[0-3]):[0-5][0-9]$` | Local time of day in `timezone`. |
| `timezone` | string, required |  | IANA time zone, e.g. `Europe/Berlin`. |
| `channelIds` | array of string (uuid), required |  |  |
| `reason` | string, required | min length 1; max length 1024 |  |

## NotificationDigest { #notificationdigest }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `enabled` | boolean, required |  |  |
| `weekday` | integer, required |  |  |
| `time` | string, required |  |  |
| `timezone` | string, required |  |  |
| `channelIds` | array of string (uuid), required |  |  |
| `nextSendAt` | string (date-time) |  | Set while the digest is enabled. |
| `lastSentAt` | string (date-time) |  | The scheduled time of the last digest that was queued. |
| `version` | integer, required |  |  |
| `updatedAt` | string (date-time), required |  |  |
| `updatedBy` | [Actor](schemas.md#actor) |  | Who made a change. `user`: a local user (`name` = username). `token`: an API token (`name` = `token:<id>`). `system`: the hub itself (e.g. an auto-resume at `resumeAt`). |

## NotificationDigestQueued { #notificationdigestqueued }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `queued` | integer, required |  | Deliveries queued (one per channel). |

## NotificationDelivery { #notificationdelivery }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `id` | string (uuid), required |  |  |
| `channelId` | string (uuid), required |  |  |
| `channelName` | string, required |  |  |
| `kind` | NotificationDeliveryKind (enum), required | one of `event`, `digest`, `test` |  |
| `eventType` | NotificationEventType (enum) | one of `applied`, `rolled_back`, `failed`, `blocked`, `readiness_degraded`, `automation_paused`, `automation_resumed` | `applied`, `rolled_back`, `failed` and `blocked` are the workload events of `docs/contracts/events.md`; `readiness_degraded` (a cluster's readiness state went down), `automation_paused` and `automation_resumed` come from the configuration audit. |
| `summary` | string, required |  |  |
| `status` | NotificationDeliveryStatus (enum), required | one of `pending`, `sent`, `failed` |  |
| `attempts` | integer, required |  |  |
| `nextAttemptAt` | string (date-time) |  | Set while `pending`. |
| `lastError` | string |  |  |
| `httpStatus` | integer |  |  |
| `createdAt` | string (date-time), required |  |  |
| `lastAttemptAt` | string (date-time) |  |  |
| `sentAt` | string (date-time) |  |  |

## NotificationDeliveryList { #notificationdeliverylist }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `items` | array of [NotificationDelivery](schemas.md#notificationdelivery), required |  |  |
| `continue` | string |  |  |

## NotificationStatus { #notificationstatus }

| Field | Type | Constraints | Description |
|---|---|---|---|
| `secretsKey` | boolean, required |  | Webhook URLs can be stored (the secrets encryption key is configured). |
| `links` | boolean, required |  | Messages link to the web UI (`notifications.baseURL` is set). |
| `rateLimitPerMinute` | integer, required |  |  |
| `retryWindowSeconds` | integer, required |  |  |
