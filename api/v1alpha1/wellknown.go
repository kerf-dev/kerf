package v1alpha1

import "strings"

// Identifiers (D-006, D-012).
const (
	// Prefix of every annotation and label key owned by resource-optimizer.
	Prefix = "resource-optimizer.oz.io/"

	// FieldManager is the server-side-apply field manager for every write to workloads (D-006).
	// Argo CD ignores fields owned by this manager.
	FieldManager = "resource-optimizer"
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
)

// Annotations written by resource-optimizer.
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
)

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
// (docs/contracts/events.md, hub API Event.reason) and as the "reason" label of ro_actions_total.
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
	// ReasonJVMFixedHeap: memory change refused on a JVM with a fixed -Xmx (WO-12).
	ReasonJVMFixedHeap = "JVMFixedHeap"

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
)
