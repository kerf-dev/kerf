// Package readiness is the catalog of cluster readiness checks (D-025, docs/contracts/readiness.md).
//
// Check ids are stable: the hub (metrics checks), the agent (self-checks), the UI, the metric
// kerf_cluster_readiness{check} and the stored history all use them. Adding a check is a contract
// change: add it to Catalog and to the table in docs/contracts/readiness.md (a test keeps both in
// sync). Never rename or reuse an id.
package readiness

// State is a cluster readiness state; each one includes the ones before it.
type State string

const (
	StateRegistered      State = "Registered"
	StateAgentConnected  State = "AgentConnected"
	StateMetricsOK       State = "MetricsOK"
	StateReadyAudit      State = "ReadyAudit"
	StateAutomationReady State = "AutomationReady"
)

// States lists the states in order.
var States = []State{StateRegistered, StateAgentConnected, StateMetricsOK, StateReadyAudit, StateAutomationReady}

// AtLeast reports whether s is st or a later state.
func (s State) AtLeast(st State) bool { return rank(s) >= 0 && rank(s) >= rank(st) }

func rank(s State) int {
	for i, v := range States {
		if v == s {
			return i
		}
	}
	return -1
}

// Severity of a failing check.
type Severity string

const (
	// SeverityBlocking: while the check is not `pass`, the cluster cannot reach Check.Gates.
	SeverityBlocking Severity = "blocking"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Status of a check result.
type Status string

const (
	StatusPass    Status = "pass"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusUnknown Status = "unknown"
)

// Statuses lists the Status enum.
var Statuses = []Status{StatusPass, StatusWarn, StatusFail, StatusUnknown}

// Source says who runs a check.
type Source string

const (
	// SourceHub: run by the hub against the cluster's datasource (registration, re-check, hourly).
	SourceHub Source = "hub"
	// SourceAgent: run by the agent (start, every 10 minutes, recheck flag), reported in the
	// status batch.
	SourceAgent Source = "agent"
)

// Category groups checks in the UI.
type Category string

const (
	CategoryDatasource  Category = "datasource"
	CategoryMetrics     Category = "metrics"
	CategoryPricing     Category = "pricing"
	CategoryAgent       Category = "agent"
	CategoryRBAC        Category = "rbac"
	CategoryApply       Category = "apply"
	CategoryWebhook     Category = "webhook"
	CategoryNodes       Category = "nodes"
	CategoryIntegration Category = "integration"
	CategoryPolicy      Category = "policy"
)

// Categories lists the Category enum.
var Categories = []Category{CategoryDatasource, CategoryMetrics, CategoryPricing, CategoryAgent, CategoryRBAC,
	CategoryApply, CategoryWebhook, CategoryNodes, CategoryIntegration, CategoryPolicy}

// Capability is a group of Kubernetes permissions the agent needs (rbac.* checks).
type Capability string

const (
	CapabilityRead     Capability = "read"
	CapabilityResize   Capability = "resize"
	CapabilityRollout  Capability = "rollout"
	CapabilityHPA      Capability = "hpa"
	CapabilityEvict    Capability = "evict"
	CapabilityPolicies Capability = "policies"
	CapabilityEvents   Capability = "events"
)

// Capabilities lists the Capability enum.
var Capabilities = []Capability{CapabilityRead, CapabilityResize, CapabilityRollout, CapabilityHPA, CapabilityEvict, CapabilityPolicies, CapabilityEvents}

// Check ids.
const (
	// metrics (hub, via the cluster's datasource)
	CheckDatasourceReachable   = "datasource.reachable"
	CheckClusterLabel          = "metrics.cluster-label"
	CheckCAdvisor              = "metrics.cadvisor"
	CheckKSMOwners             = "metrics.ksm-owners"
	CheckKSMResources          = "metrics.ksm-resources"
	CheckOwnerJoin             = "metrics.owner-join"
	CheckScrapeInterval        = "metrics.scrape-interval"
	CheckHistory               = "metrics.history"
	CheckThrottling            = "metrics.throttling"
	CheckRestartsOOM           = "metrics.restarts-oom"
	CheckHPASeries             = "metrics.hpa"
	CheckNodeLabels            = "metrics.node-labels"
	CheckNodeAllocatable       = "metrics.node-allocatable"
	CheckFreshness             = "metrics.freshness"
	CheckPricingCoverage       = "pricing.coverage"
	CheckRecommendations       = "metrics.recommendations"
	CheckAgentConnected        = "agent.connected"
	CheckAgentVersion          = "agent.version"
	CheckCRDs                  = "agent.crds"
	CheckClockSkew             = "agent.clock-skew"
	CheckRBACRead              = "rbac.read"
	CheckRBACResize            = "rbac.resize"
	CheckRBACRollout           = "rbac.rollout"
	CheckRBACHPA               = "rbac.hpa"
	CheckRBACEvict             = "rbac.evict"
	CheckRBACPolicies          = "rbac.policies"
	CheckRBACEvents            = "rbac.events"
	CheckInPlaceResize         = "apply.in-place-resize"
	CheckWebhook               = "webhook.healthy"
	CheckCertManager           = "webhook.cert-manager"
	CheckNodesInPlaceMemory    = "nodes.in-place-memory"
	CheckArgoCD                = "integration.argocd"
	CheckPolicyNamespaceCovers = "policy.namespace-coverage"
)

// Check describes one readiness check.
type Check struct {
	ID       string
	Category Category
	Severity Severity
	Source   Source
	// Gates is the state a blocking check must pass for; empty for warning and info checks.
	Gates State
	// Capability is set on rbac.* checks.
	Capability Capability
	Title      string
}

// Catalog lists every check, in display order.
var Catalog = []Check{
	{ID: CheckAgentConnected, Category: CategoryAgent, Severity: SeverityBlocking, Source: SourceHub, Gates: StateAgentConnected, Title: "Agent has pulled from the hub"},
	{ID: CheckAgentVersion, Category: CategoryAgent, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAgentConnected, Title: "Agent and hub versions are compatible"},
	{ID: CheckCRDs, Category: CategoryAgent, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAgentConnected, Title: "CRDs installed and current"},
	{ID: CheckClockSkew, Category: CategoryAgent, Severity: SeverityWarning, Source: SourceAgent, Title: "Clock skew to the hub under 30 s"},

	{ID: CheckDatasourceReachable, Category: CategoryDatasource, Severity: SeverityBlocking, Source: SourceHub, Gates: StateMetricsOK, Title: "Datasource reachable and authenticated"},
	{ID: CheckClusterLabel, Category: CategoryMetrics, Severity: SeverityBlocking, Source: SourceHub, Gates: StateMetricsOK, Title: "Cluster label matches series"},
	{ID: CheckCAdvisor, Category: CategoryMetrics, Severity: SeverityBlocking, Source: SourceHub, Gates: StateMetricsOK, Title: "cAdvisor CPU and memory working set"},
	{ID: CheckKSMOwners, Category: CategoryMetrics, Severity: SeverityBlocking, Source: SourceHub, Gates: StateMetricsOK, Title: "kube-state-metrics owner series"},
	{ID: CheckKSMResources, Category: CategoryMetrics, Severity: SeverityBlocking, Source: SourceHub, Gates: StateMetricsOK, Title: "kube-state-metrics requests and limits"},
	{ID: CheckFreshness, Category: CategoryMetrics, Severity: SeverityBlocking, Source: SourceHub, Gates: StateMetricsOK, Title: "Data fresh (last sample under 5 min)"},
	{ID: CheckOwnerJoin, Category: CategoryMetrics, Severity: SeverityWarning, Source: SourceHub, Title: "Pod to owner join coverage at least 95 %"},
	{ID: CheckScrapeInterval, Category: CategoryMetrics, Severity: SeverityWarning, Source: SourceHub, Title: "Scrape resolution 60 s or finer"},
	{ID: CheckHistory, Category: CategoryMetrics, Severity: SeverityWarning, Source: SourceHub, Title: "History covers the profile window"},
	{ID: CheckThrottling, Category: CategoryMetrics, Severity: SeverityWarning, Source: SourceHub, Title: "CPU throttling series"},
	{ID: CheckRestartsOOM, Category: CategoryMetrics, Severity: SeverityWarning, Source: SourceHub, Title: "Restart and OOM series"},
	{ID: CheckHPASeries, Category: CategoryMetrics, Severity: SeverityWarning, Source: SourceHub, Title: "HPA series"},
	{ID: CheckNodeLabels, Category: CategoryMetrics, Severity: SeverityWarning, Source: SourceHub, Title: "Node labels allowlisted in kube-state-metrics"},
	{ID: CheckNodeAllocatable, Category: CategoryMetrics, Severity: SeverityWarning, Source: SourceHub, Title: "Node allocatable series"},
	{ID: CheckPricingCoverage, Category: CategoryPricing, Severity: SeverityWarning, Source: SourceHub, Title: "Nodes priced"},
	{ID: CheckRecommendations, Category: CategoryMetrics, Severity: SeverityBlocking, Source: SourceHub, Gates: StateReadyAudit, Title: "Recommendations produced"},

	{ID: CheckRBACRead, Category: CategoryRBAC, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAgentConnected, Capability: CapabilityRead, Title: "RBAC: read workloads, pods, HPAs, nodes"},
	{ID: CheckRBACResize, Category: CategoryRBAC, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAutomationReady, Capability: CapabilityResize, Title: "RBAC: patch pods/resize"},
	{ID: CheckRBACRollout, Category: CategoryRBAC, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAutomationReady, Capability: CapabilityRollout, Title: "RBAC: patch deployments, statefulsets, daemonsets"},
	{ID: CheckRBACHPA, Category: CategoryRBAC, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAutomationReady, Capability: CapabilityHPA, Title: "RBAC: patch HPAs"},
	{ID: CheckRBACEvict, Category: CategoryRBAC, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAutomationReady, Capability: CapabilityEvict, Title: "RBAC: create pods/eviction"},
	{ID: CheckRBACPolicies, Category: CategoryRBAC, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAutomationReady, Capability: CapabilityPolicies, Title: "RBAC: write rightsizingpolicies"},
	{ID: CheckRBACEvents, Category: CategoryRBAC, Severity: SeverityBlocking, Source: SourceAgent, Gates: StateAutomationReady, Capability: CapabilityEvents, Title: "RBAC: create events"},
	{ID: CheckInPlaceResize, Category: CategoryApply, Severity: SeverityWarning, Source: SourceAgent, Title: "In-place resize available"},
	{ID: CheckWebhook, Category: CategoryWebhook, Severity: SeverityWarning, Source: SourceAgent, Title: "Webhook installed and healthy"},
	{ID: CheckCertManager, Category: CategoryWebhook, Severity: SeverityInfo, Source: SourceAgent, Title: "cert-manager present"},
	{ID: CheckNodesInPlaceMemory, Category: CategoryNodes, Severity: SeverityInfo, Source: SourceAgent, Title: "Nodes without swap or static managers"},
	{ID: CheckArgoCD, Category: CategoryIntegration, Severity: SeverityWarning, Source: SourceAgent, Title: "Argo CD ignores Kerf fields"},
	{ID: CheckPolicyNamespaceCovers, Category: CategoryPolicy, Severity: SeverityInfo, Source: SourceAgent, Title: "Namespace coverage"},
}

// Lookup returns the catalog entry of id.
func Lookup(id string) (Check, bool) {
	for _, c := range Catalog {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}

// Result is the minimal view of a check result the state computation needs.
type Result struct {
	ID     string
	Status Status
}

// ComputeState returns the highest state whose blocking checks, and those of every earlier state,
// all have status pass. A blocking check without a result counts as not passing. Registered is
// always reached.
func ComputeState(results []Result) State {
	status := map[string]Status{}
	for _, r := range results {
		status[r.ID] = r.Status
	}
	reached := StateRegistered
	for _, st := range States[1:] {
		for _, c := range Catalog {
			if c.Severity == SeverityBlocking && c.Gates == st && status[c.ID] != StatusPass {
				return reached
			}
		}
		reached = st
	}
	return reached
}
