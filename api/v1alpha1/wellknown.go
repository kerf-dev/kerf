package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Identifiers (D-006, D-012).
const (
	// Prefix of every annotation and label key owned by Kerf.
	Prefix = "kerf.getkerf.dev/"

	// FieldManager is the server-side-apply field manager for every write to workloads (D-006).
	// Argo CD ignores fields owned by this manager.
	FieldManager = "kerf"
)

// Annotations set by users on workload objects (Deployment, StatefulSet, DaemonSet, CronJob).
// They are the highest precedence level (D-005).
const (
	// AnnotationMode sets the mode for both CPU and memory: off | audit | auto-patch.
	AnnotationMode = Prefix + "mode"
	// AnnotationCPUMode sets the CPU mode; overrides AnnotationMode for CPU.
	AnnotationCPUMode = Prefix + "cpu-mode"
	// AnnotationMemoryMode sets the memory mode; overrides AnnotationMode for memory.
	AnnotationMemoryMode = Prefix + "memory-mode"
	// AnnotationStrategy overrides the apply strategy: in-place | rollout | deferred.
	AnnotationStrategy = Prefix + "strategy"
	// AnnotationProfile overrides the recommender profile: dev | production | high-availability.
	AnnotationProfile = Prefix + "profile"
	// AnnotationAllowDisruption set to "true" allows changes that leave a single-replica workload
	// (one replica, or a PodDisruptionBudget that allows no disruption) without a Ready pod for a
	// moment: an in-place resize that restarts the container, or a rollout without surge.
	AnnotationAllowDisruption = Prefix + "allow-disruption"
)

// Annotations written by Kerf.
const (
	// AnnotationManaged is set to "true" on a workload while the agent owns resource fields on it
	// (rollout strategy). Removed on release.
	AnnotationManaged = Prefix + "managed"
	// AnnotationPreviousResources is set on a workload (rollout) or pod (in-place) and holds the
	// values before the most recent change, as JSON: []ContainerResources.
	AnnotationPreviousResources = Prefix + "previous-resources"
	// AnnotationRecommendationID is the hub recommendation id that produced the live values.
	AnnotationRecommendationID = Prefix + "recommendation-id"
	// AnnotationInjectedResources is set by the webhook on pods it mutated, as JSON:
	// []ContainerResources with the values it injected.
	AnnotationInjectedResources = Prefix + "injected-resources"
)

// Labels.
const (
	// LabelWebhookSkip with value "true" opts a pod (objectSelector) or a whole namespace
	// (namespaceSelector) out of the mutating webhook.
	LabelWebhookSkip = Prefix + "skip-webhook"

	// LabelManagedBy marks RightsizingPolicy CRs the agent materialised from the hub config
	// (value ManagedByHub, D-022). The agent creates, updates and deletes only CRs with this label;
	// CRs without it (e.g. from Git) are never touched.
	LabelManagedBy = Prefix + "managed-by"
	// ManagedByHub is the LabelManagedBy value of hub-managed policies.
	ManagedByHub = "hub"
)

// Annotations on hub-managed RightsizingPolicy CRs (D-022, docs/contracts/admin.md).
const (
	// AnnotationHubPolicyID is the hub policy id (UUID) a CR was materialised from.
	AnnotationHubPolicyID = Prefix + "hub-policy-id"
	// AnnotationHubVersion is the hub policy version (decimal integer) the CR holds.
	AnnotationHubVersion = Prefix + "hub-version"
	// AnnotationHubPolicyType is the hub policy type (HubPolicyType*). The agent layers
	// global-default and cluster-default under the winning policy and workload-override above
	// it, instead of letting them compete by priority.
	AnnotationHubPolicyType = Prefix + "hub-policy-type"
)

// Hub policy types (AnnotationHubPolicyType values, hub API AdminPolicy.type).
const (
	HubPolicyTypeGlobalDefault    = "global-default"
	HubPolicyTypeClusterDefault   = "cluster-default"
	HubPolicyTypePolicy           = "policy"
	HubPolicyTypeWorkloadOverride = "workload-override"
)

// Names of hub-managed policies the hub generates. User-named hub policies must not use the
// ReservedNamePrefix.
const (
	ReservedNamePrefix        = "ro-"
	GlobalDefaultsPolicyName  = "ro-global-defaults"
	ClusterDefaultsPolicyName = "ro-cluster-defaults"
	// OverridePolicyNamePrefix + 16 hex characters, see OverridePolicyName.
	OverridePolicyNamePrefix = "ro-override-"
)

// Reserved priority bands of hub-managed policies. They also make an agent that does not know
// AnnotationHubPolicyType degrade safely: defaults lose against every other policy, overrides win.
const (
	PriorityGlobalDefault    int32 = 0
	PriorityClusterDefault   int32 = 10
	PriorityPolicyMin        int32 = 1
	PriorityPolicyMax        int32 = 899999
	PriorityWorkloadOverride int32 = 900000
	PriorityMax              int32 = 1000000
)

// OverridePolicyName returns the name of the hub workload-override policy of a workload:
// OverridePolicyNamePrefix + the first 16 hex characters of sha256("<namespace>/<kind>/<name>").
func OverridePolicyName(namespace string, kind WorkloadKind, name string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + string(kind) + "/" + name))
	return OverridePolicyNamePrefix + hex.EncodeToString(sum[:])[:16]
}

// IndexTargetRef is the cache field index for RightsizingRecommendation by target workload.
// Index value: TargetRefIndexValue(kind, name). Namespace is implied by the namespaced cache.
const IndexTargetRef = "spec.targetRef"

// TargetRefIndexValue returns the IndexTargetRef value for a workload, e.g. "Deployment/web".
func TargetRefIndexValue(kind WorkloadKind, name string) string {
	return string(kind) + "/" + name
}

// RecommendationName returns the conventional RightsizingRecommendation name for a workload.
// If taken bool is true (the plain name is used by a different kind), the kind is appended.
func RecommendationName(kind WorkloadKind, name string, taken bool) string {
	if !taken {
		return name
	}
	return name + "-" + strings.ToLower(string(kind))
}

// Reasons. They are used verbatim as metav1.Condition reasons, as the "reason" of events
// (docs/contracts/events.md, hub API Event.reason) and as the "reason" label of kerf_actions_total.
const (
	// skipped
	ReasonModeOff             = "ModeOff"
	ReasonModeAudit           = "ModeAudit"
	ReasonNoSignificantChange = "NoSignificantChange"
	ReasonRecommendationStale = "RecommendationStale"

	// blocked
	ReasonLearningPeriod           = "LearningPeriod"
	ReasonLowConfidence            = "LowConfidence"
	ReasonCooldown                 = "Cooldown"
	ReasonHPACoupled               = "HPACoupled"
	ReasonThrottled                = "Throttled"
	ReasonOOMKilled                = "OOMKilled"
	ReasonFreezeWindow             = "FreezeWindow"
	ReasonOutsideMaintenanceWindow = "OutsideMaintenanceWindow"
	ReasonUnsupportedNode          = "UnsupportedNode"
	ReasonRestartPolicyRequired    = "RestartPolicyRequired"
	ReasonHubUnreachable           = "HubUnreachable"
	// ReasonPaused: automation of the cluster (or the fleet) is paused in the hub config; like a
	// cluster-wide freeze, rollbacks still run (D-022).
	ReasonPaused = "Paused"
	// ReasonJVMFixedHeap: memory change refused on a JVM with a fixed -Xmx.
	ReasonJVMFixedHeap = "JVMFixedHeap"
	// ReasonSingleReplicaUnsafe: single-replica workload and the change would leave it without a
	// Ready pod; opt in with AnnotationAllowDisruption.
	ReasonSingleReplicaUnsafe = "SingleReplicaUnsafe"
	// ReasonPodDisruptionBudget: a PodDisruptionBudget refused the eviction of an OnDelete
	// StatefulSet pod; the agent retries.
	ReasonPodDisruptionBudget = "PodDisruptionBudget"

	// fallback
	ReasonResizeInfeasible      = "ResizeInfeasible"
	ReasonResizeDeferredTimeout = "ResizeDeferredTimeout"

	// rolled_back
	ReasonOOMKills   = "OOMKills"
	ReasonRestarts   = "Restarts"
	ReasonThrottling = "Throttling"

	// released
	ReasonModeChanged = "ModeChanged"

	// failed
	ReasonApplyError    = "ApplyError"
	ReasonResizeError   = "ResizeError"
	ReasonRollbackError = "RollbackError"
	ReasonReleaseError  = "ReleaseError"

	// applied
	ReasonRecommendationApplied = "RecommendationApplied"
	// ReasonApprovedOnce: a recommendation of a workload in audit mode was applied once because an
	// operator approved it in the hub (D-024).
	ReasonApprovedOnce = "ApprovedOnce"
)
