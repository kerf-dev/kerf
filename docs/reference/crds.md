<!-- Generated from api/crds (built from api/v1alpha1) by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Custom resources

Kerf installs two cluster-scoped custom resources in the API group `kerf.getkerf.dev`. A `RightsizingPolicy` says which workloads to rightsize and how; the agent writes a `RightsizingRecommendation` for each workload it manages. The concepts behind the fields are explained under [Concepts](../concepts/README.md).

## RightsizingPolicy { #rightsizingpolicy }

RightsizingPolicy configures rightsizing for the workloads it selects. Cluster-scoped.

| | |
|---|---|
| API version | `kerf.getkerf.dev/v1alpha1` |
| Kind | `RightsizingPolicy` (list: `RightsizingPolicyList`) |
| Resource | `rightsizingpolicies`, short name `rsp` |
| Scope | Cluster |
| Status subresource | yes |

`kubectl get` columns: Priority (`.spec.priority`), CPU (`.spec.cpu.mode`), Memory (`.spec.memory.mode`), Strategy (`.spec.apply.strategy`), Matched (`.status.matchedWorkloads`), Age (`.metadata.creationTimestamp`).

### RightsizingPolicy spec { #rightsizingpolicy-spec }

RightsizingPolicySpec selects workloads and configures how they are rightsized.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `spec.apply` | object |  | Apply configures how changes are applied. |
| `spec.apply.fallbackAfter` | string |  | FallbackAfter is how long an in-place resize may stay Deferred before the agent falls back to rollout. Infeasible resizes fall back immediately. Global default: 10m. |
| `spec.apply.freezeWindows` | []object | max items 16 | FreezeWindows are times in which no strategy may apply changes, and no field ownership is released. Rollbacks (health watch) are still allowed. A cluster-wide freeze configured on the agent applies in addition to these and cannot be overridden by a policy. |
| `spec.apply.freezeWindows[].duration` | string |  | Duration of a recurring window. |
| `spec.apply.freezeWindows[].end` | string (date-time) |  | End of a one-off window. |
| `spec.apply.freezeWindows[].schedule` | string | max length 128 | Schedule is a standard 5-field cron expression marking when a recurring window opens. |
| `spec.apply.freezeWindows[].start` | string (date-time) |  | Start of a one-off window. |
| `spec.apply.freezeWindows[].timeZone` | string | max length 64 | TimeZone (IANA name) the schedule is evaluated in. Default: UTC. |
| `spec.apply.maintenanceWindows` | []object | max items 16 | MaintenanceWindows are the only times the deferred strategy may apply changes. |
| `spec.apply.maintenanceWindows[].duration` | string |  | Duration of a recurring window. |
| `spec.apply.maintenanceWindows[].end` | string (date-time) |  | End of a one-off window. |
| `spec.apply.maintenanceWindows[].schedule` | string | max length 128 | Schedule is a standard 5-field cron expression marking when a recurring window opens. |
| `spec.apply.maintenanceWindows[].start` | string (date-time) |  | Start of a one-off window. |
| `spec.apply.maintenanceWindows[].timeZone` | string | max length 64 | TimeZone (IANA name) the schedule is evaluated in. Default: UTC. |
| `spec.apply.strategy` | string | one of `in-place`, `rollout`, `deferred` | Strategy used to apply changes. Global default: in-place. |
| `spec.cpu` | object | rule: limitRatio is required when limits is ratio; rule: limitRatio is only allowed when limits is ratio | CPU configures CPU handling. |
| `spec.cpu.limitRatio` | string | pattern `^([1-9][0-9]*)(\.[0-9]{1,3})?$` | LimitRatio is the CPU limit as a multiple of the request, as a decimal string >= 1 (e.g. "2" or "1.5"). Required when limits is ratio. |
| `spec.cpu.limits` | string | one of `keep`, `remove`, `ratio` | Limits says how CPU limits are recommended. Global default: keep. |
| `spec.cpu.mode` | string | one of `off`, `audit`, `auto-patch` | Mode for CPU. Global default: audit. |
| `spec.guardrails` | object |  | Guardrails limit what the agent may change. |
| `spec.guardrails.cooldown` | string |  | Cooldown is the minimum time between two changes, and after a rollback the time during which further decreases of the rolled-back resource are blocked. Global default: 24h. |
| `spec.guardrails.learningPeriod` | string |  | LearningPeriod is the minimum data coverage before a workload may be changed. Global default: 168h. |
| `spec.guardrails.maxChangePercent` | integer | min 1; max 100 | MaxChangePercent caps a single decrease step, in percent of the current value. Increases are never capped. Global default: 50. |
| `spec.guardrails.minCPU` | int or string | pattern `^(\+\|-)?(([0-9]+(\.[0-9]*)?)\|(\.[0-9]+))(([KMGTPE]i)\|[numkMGTPE]\|([eE](\+\|-)?(([0-9]+(\.[0-9]*)?)\|(\.[0-9]+))))?$` | MinCPU is the lowest CPU request the agent will set. Global default: 10m. |
| `spec.guardrails.minConfidence` | string | pattern `^(0(\.[0-9]{1,3})?\|1(\.0{1,3})?)$` | MinConfidence is the minimum recommendation confidence (0-1, decimal string) for a change. Global default: "0.7". |
| `spec.guardrails.minMemory` | int or string | pattern `^(\+\|-)?(([0-9]+(\.[0-9]*)?)\|(\.[0-9]+))(([KMGTPE]i)\|[numkMGTPE]\|([eE](\+\|-)?(([0-9]+(\.[0-9]*)?)\|(\.[0-9]+))))?$` | MinMemory is the lowest memory request the agent will set. Global default: 32Mi. |
| `spec.guardrails.rollback` | object |  | Rollback configures the health watch after each change. |
| `spec.guardrails.rollback.oomKills` | integer | min 1 | OOMKills is the number of OOM kills that triggers a rollback. Global default: 1. |
| `spec.guardrails.rollback.restarts` | integer | min 1 | Restarts is the number of container restarts (any reason) that triggers a rollback. Global default: 3. |
| `spec.guardrails.rollback.throttlingRatio` | string | pattern `^(0(\.[0-9]{1,3})?\|1(\.0{1,3})?)$` | ThrottlingRatio (0-1, decimal string) is the CFS throttled-periods ratio that triggers a CPU rollback. Global default: "0.25". |
| `spec.guardrails.rollback.watchDuration` | string |  | WatchDuration is how long a workload is watched after a change. Global default: 30m. |
| `spec.hpa` | object |  | HPA configures coordination with HorizontalPodAutoscalers. Without coordination, workloads with a CPU/memory-utilization HPA stay audit-only. |
| `spec.hpa.coordinate` | boolean |  | Coordinate changes requests and the HPA utilization target in one step. Global default: false. |
| `spec.hpa.targetBounds` | object | rule: minUtilization must be <= maxUtilization | TargetBounds limits the HPA utilization target the agent may set. |
| `spec.hpa.targetBounds.maxUtilization` | integer | min 1; max 100 |  |
| `spec.hpa.targetBounds.minUtilization` | integer | min 1; max 100 |  |
| `spec.jvm` | object |  | JVM configures JVM detection. |
| `spec.jvm.detect` | boolean |  | Detect JVM containers and treat their memory as heap-bound (memory changes need RestartContainer or rollout). Global default: true. |
| `spec.memory` | object |  | Memory configures memory handling. |
| `spec.memory.limitEqualsRequest` | boolean |  | LimitEqualsRequest sets the memory limit equal to the memory request. Global default: true. |
| `spec.memory.mode` | string | one of `off`, `audit`, `auto-patch` | Mode for memory. Global default: audit. |
| `spec.priority` | integer | min 0; max 1000000 | Priority decides between several matching policies: the highest priority wins; ties are broken by policy name in ascending lexical order. Only the winning policy applies (fields are not merged across policies). |
| `spec.profile` | string | one of `dev`, `production`, `high-availability` | Profile selects the recommender profile. Global default: production. |
| `spec.selector` | object |  | Selector chooses the workloads this policy applies to. An empty selector matches every workload. |
| `spec.selector.excludeNamespaces` | []string | max items 64 | ExcludeNamespaces is a list of namespace globs that never match, even if listed in namespaces. |
| `spec.selector.kinds` | []string | max items 4 | Kinds restricts the workload kinds. Empty means all supported kinds. |
| `spec.selector.matchLabels` | map[string]string |  | MatchLabels must all be present with equal values on the workload object (not the pod template). |
| `spec.selector.namespaces` | []string | max items 64 | Namespaces is a list of namespace globs (path.Match syntax, e.g. "team-*"). A workload matches if its namespace matches any entry. Empty means all namespaces. |
| `spec.selector.workloads` | []object | max items 100 | Workloads restricts the policy to the listed workloads: a workload matches if it equals any entry. Empty means no restriction. Like every criterion it is combined with the others (AND). The hub uses it for UI workload overrides. Agents older than this field ignore it, so the hub also sets namespaces and kinds to narrow such a policy. |
| `spec.selector.workloads[].kind` | string, required | one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob` | Kind of the workload. |
| `spec.selector.workloads[].name` | string, required | min length 1; max length 253 | Name of the workload. |
| `spec.selector.workloads[].namespace` | string, required | min length 1; max length 63 | Namespace of the workload. |

### RightsizingPolicy status { #rightsizingpolicy-status }

RightsizingPolicyStatus is the observed state of a RightsizingPolicy.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `status.conditions` | []object |  | Conditions: Ready. |
| `status.conditions[].lastTransitionTime` | string (date-time), required |  | lastTransitionTime is the last time the condition transitioned from one status to another. This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable. |
| `status.conditions[].message` | string, required | max length 32768 | message is a human readable message indicating details about the transition. This may be an empty string. |
| `status.conditions[].observedGeneration` | integer | min 0 | observedGeneration represents the .metadata.generation that the condition was set based upon. For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date with respect to the current state of the instance. |
| `status.conditions[].reason` | string, required | min length 1; max length 1024; pattern `^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$` | reason contains a programmatic identifier indicating the reason for the condition's last transition. Producers of specific condition types may define expected values and meanings for this field, and whether the values are considered a guaranteed API. The value should be a CamelCase string. This field may not be empty. |
| `status.conditions[].status` | string, required | one of `True`, `False`, `Unknown` | status of the condition, one of True, False, Unknown. |
| `status.conditions[].type` | string, required | max length 316; pattern `^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$` | type of condition in CamelCase or in foo.example.com/CamelCase. |
| `status.matchedWorkloads` | integer |  | MatchedWorkloads is the number of workloads for which this policy is the winning policy. |
| `status.observedGeneration` | integer |  | ObservedGeneration is the generation last processed by the agent. |

## RightsizingRecommendation { #rightsizingrecommendation }

RightsizingRecommendation is the agent's record of the change for one workload. One per
workload, in the workload's namespace, written only by the agent.

Naming: by convention metadata.name equals the workload name; if two workloads of different
kinds share a name, the later one is named "<name>-<lowercase kind>". Consumers (webhook,
health watch) must therefore look recommendations up by spec.targetRef (cache index
IndexTargetRef), never by name.

| | |
|---|---|
| API version | `kerf.getkerf.dev/v1alpha1` |
| Kind | `RightsizingRecommendation` (list: `RightsizingRecommendationList`) |
| Resource | `rightsizingrecommendations`, short name `rsr` |
| Scope | Namespaced |
| Status subresource | yes |

`kubectl get` columns: Kind (`.spec.targetRef.kind`), Target (`.spec.targetRef.name`), Phase (`.status.phase`), Strategy (`.spec.strategy`), CPU (`.status.effective.cpuMode`), Memory (`.status.effective.memoryMode`), Next Window (`.status.nextWindow`), Age (`.metadata.creationTimestamp`).

### RightsizingRecommendation spec { #rightsizingrecommendation-spec }

RightsizingRecommendationSpec is the change the agent wants for one workload.
Written only by the agent.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `spec.containers` | []object, required | min items 1; max items 64 | Containers are the recommended values, after guardrails (clamping, floors) were applied. |
| `spec.containers[].limits` | map[string]int or string | rule: only cpu and memory are supported | Limits for cpu and/or memory. A missing key means "unchanged / not set". |
| `spec.containers[].name` | string, required | min length 1; max length 63 | Name of the container (regular or init/sidecar container). |
| `spec.containers[].removeLimits` | []string | max items 2 | RemoveLimits lists resources whose limit is to be removed (e.g. cpu with limits: remove). |
| `spec.containers[].requests` | map[string]int or string | rule: only cpu and memory are supported | Requests for cpu and/or memory. A missing key means "unchanged / not set". |
| `spec.hpa` | object |  | HPA is the HPA utilization target applied together with the containers: set when the workload's HPA scales on the utilization of a resource whose requests change. |
| `spec.hpa.averageUtilization` | integer, required | min 1 | AverageUtilization target in percent. |
| `spec.hpa.name` | string, required | min length 1; max length 253 | Name of the HPA, in the same namespace. |
| `spec.hpa.resource` | string, required | one of `cpu`, `memory` | Resource the HPA scales on (Resource metric, Utilization target). |
| `spec.source` | object, required |  | Source is the hub recommendation this was built from. |
| `spec.source.confidence` | string | max length 8 | Confidence (0-1, decimal string) as reported by the hub. Informational. |
| `spec.source.generatedAt` | string (date-time), required |  | GeneratedAt is when the hub generated the recommendation. |
| `spec.source.recommendationID` | string, required | min length 1; max length 64 | RecommendationID is the hub's recommendation id (hub API: WorkloadRecommendation.id). |
| `spec.source.riskFlags` | []string | max items 16 | RiskFlags as reported by the hub. Informational. |
| `spec.strategy` | string, required | one of `in-place`, `rollout`, `deferred` | Strategy currently in effect. After an in-place fallback the agent sets this to rollout, which also stops the webhook from injecting values. |
| `spec.targetRef` | object, required |  | TargetRef is the workload, in the same namespace. |
| `spec.targetRef.apiVersion` | string |  | APIVersion of the workload, e.g. apps/v1 or batch/v1. |
| `spec.targetRef.kind` | string, required | one of `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob` | Kind of the workload. |
| `spec.targetRef.name` | string, required | min length 1; max length 253 | Name of the workload. |

### RightsizingRecommendation status { #rightsizingrecommendation-status }

RightsizingRecommendationStatus is the observed state of a RightsizingRecommendation.

| Field | Type | Constraints | Description |
|---|---|---|---|
| `status.appliedAt` | string (date-time) |  | AppliedAt is when the most recent change became live. |
| `status.appliedRecommendationID` | string |  | AppliedRecommendationID is the hub recommendation id that is live. |
| `status.conditions` | []object |  | Conditions: PolicyResolved, Applied, Blocked, HealthWatch. |
| `status.conditions[].lastTransitionTime` | string (date-time), required |  | lastTransitionTime is the last time the condition transitioned from one status to another. This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable. |
| `status.conditions[].message` | string, required | max length 32768 | message is a human readable message indicating details about the transition. This may be an empty string. |
| `status.conditions[].observedGeneration` | integer | min 0 | observedGeneration represents the .metadata.generation that the condition was set based upon. For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date with respect to the current state of the instance. |
| `status.conditions[].reason` | string, required | min length 1; max length 1024; pattern `^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$` | reason contains a programmatic identifier indicating the reason for the condition's last transition. Producers of specific condition types may define expected values and meanings for this field, and whether the values are considered a guaranteed API. The value should be a CamelCase string. This field may not be empty. |
| `status.conditions[].status` | string, required | one of `True`, `False`, `Unknown` | status of the condition, one of True, False, Unknown. |
| `status.conditions[].type` | string, required | max length 316; pattern `^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$` | type of condition in CamelCase or in foo.example.com/CamelCase. |
| `status.effective` | object |  | Effective is the resolved policy for the workload. |
| `status.effective.cpuMode` | string, required | one of `off`, `audit`, `auto-patch` | CPUMode is the effective CPU mode. |
| `status.effective.cpuModeSource` | string, required | one of `global-default`, `cluster-default`, `policy`, `annotation` | CPUModeSource is the precedence level CPUMode came from. |
| `status.effective.memoryMode` | string, required | one of `off`, `audit`, `auto-patch` | MemoryMode is the effective memory mode. |
| `status.effective.memoryModeSource` | string, required | one of `global-default`, `cluster-default`, `policy`, `annotation` | MemoryModeSource is the precedence level MemoryMode came from. |
| `status.effective.policy` | string |  | Policy is the name of the winning RightsizingPolicy, empty if none matched. |
| `status.effective.profile` | string, required | one of `dev`, `production`, `high-availability` | Profile is the effective recommender profile. |
| `status.effective.strategy` | string, required | one of `in-place`, `rollout`, `deferred` | Strategy is the effective configured strategy (before any fallback). |
| `status.nextWindow` | string (date-time) |  | NextWindow is when the next maintenance window opens, set while the phase is Deferred. |
| `status.observedGeneration` | integer |  | ObservedGeneration is the spec generation the status refers to. |
| `status.phase` | string | one of `Pending`, `Applying`, `Applied`, `Blocked`, `RolledBack`, `Released`, `Deferred` | Phase of the recommendation. |
| `status.previous` | []object | max items 64 | Previous holds the values that were live before the most recent apply. Used by rollback. |
| `status.previous[].limits` | map[string]int or string | rule: only cpu and memory are supported | Limits for cpu and/or memory. A missing key means "unchanged / not set". |
| `status.previous[].name` | string, required | min length 1; max length 63 | Name of the container (regular or init/sidecar container). |
| `status.previous[].requests` | map[string]int or string | rule: only cpu and memory are supported | Requests for cpu and/or memory. A missing key means "unchanged / not set". |
| `status.previousHPATarget` | integer | min 1 | PreviousHPATarget is the HPA target that was live before the most recent apply, restored by a rollback together with the containers. |
| `status.rolledBackAt` | string (date-time) |  | RolledBackAt is when the most recent rollback happened (cooldown for decreases starts here). |
