package hubapi

import "time"

// Cluster onboarding, metrics sources and readiness.
// docs/contracts/readiness.md; check catalog and state computation: api/v1alpha1/readiness.

// LocalDatasourceID is the id of the built-in hub VictoriaMetrics datasource.
const LocalDatasourceID = "local"

// Default metrics binding values.
const (
	DefaultClusterLabel = "cluster"
)

// Datasource types (DatasourceType) and auth types (DatasourceAuthType).
const (
	DatasourcePrometheus      = "prometheus"
	DatasourceVictoriaMetrics = "victoriametrics"
	DatasourceThanos          = "thanos"
	DatasourceMimir           = "mimir"
	DatasourceAMP             = "amp"
	// DatasourceDynatrace reads Kubernetes metrics from a Dynatrace environment with DQL, not PromQL.
	DatasourceDynatrace = "dynatrace"

	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthBasic  = "basic"
	AuthSigV4  = "sigv4"
	AuthMTLS   = "mtls"
)

// Install formats (InstallFormat) and the Secret the snippets reference.
const (
	InstallHelm   = "helm"
	InstallValues = "values"
	InstallArgoCD = "argocd"

	AgentTokenSecretName = "kerf-agent-token"
	AgentTokenSecretKey  = "token"
)

// Problem codes of onboarding and readiness.
const (
	CodeClusterNotAutomationReady = "cluster_not_automation_ready"
	CodeDatasourceInUse           = "datasource_in_use"
	CodeBuiltin                   = "builtin"
	CodeNotSupported              = "not_supported"
)

// Config actions and target types of onboarding (ConfigAction, ConfigTargetType).
const (
	ActionDatasourceCreated = "DatasourceCreated"
	ActionDatasourceUpdated = "DatasourceUpdated"
	ActionDatasourceDeleted = "DatasourceDeleted"
	ActionReadinessChanged  = "ReadinessChanged"

	TargetDatasource = "datasource"
	TargetReadiness  = "readiness"
)

// DatasourceSigV4 is AWS SigV4 auth (AMP).
type DatasourceSigV4 struct {
	Region  string `json:"region"`
	Service string `json:"service,omitempty"`
	RoleARN string `json:"roleARN,omitempty"`
}

// DatasourceTLSInput is TLS / mTLS input; ClientKey is write-only.
type DatasourceTLSInput struct {
	CACert             string `json:"caCert,omitempty"`
	ClientCert         string `json:"clientCert,omitempty"`
	ClientKey          string `json:"clientKey,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
}

// DatasourceAuthInput is auth as written; Token, Password and TLS.ClientKey are write-only.
type DatasourceAuthInput struct {
	Type  string `json:"type"`
	Token string `json:"token,omitempty"`
	// TokenSecret names a datasource Secret mounted into the hub that holds the bearer token.
	TokenSecret string              `json:"tokenSecret,omitempty"`
	Username    string              `json:"username,omitempty"`
	Password    string              `json:"password,omitempty"`
	SigV4       *DatasourceSigV4    `json:"sigv4,omitempty"`
	TLS         *DatasourceTLSInput `json:"tls,omitempty"`
}

// DatasourceAuthInfo is the stored auth without secrets.
type DatasourceAuthInfo struct {
	Type                 string           `json:"type"`
	Username             string           `json:"username,omitempty"`
	SigV4                *DatasourceSigV4 `json:"sigv4,omitempty"`
	TokenSecret          string           `json:"tokenSecret,omitempty"`
	SecretConfigured     bool             `json:"secretConfigured"`
	CAConfigured         bool             `json:"caConfigured,omitempty"`
	ClientCertConfigured bool             `json:"clientCertConfigured,omitempty"`
	InsecureSkipVerify   bool             `json:"insecureSkipVerify,omitempty"`
}

// DatasourceInput is the body of datasource create and update.
type DatasourceInput struct {
	Name         string               `json:"name"`
	Type         string               `json:"type"`
	URL          string               `json:"url"`
	Auth         *DatasourceAuthInput `json:"auth,omitempty"`
	Dynatrace    *DynatraceSettings   `json:"dynatrace,omitempty"`
	ClearSecrets bool                 `json:"clearSecrets,omitempty"`
	Headers      map[string]string    `json:"headers,omitempty"`
	Timeout      string               `json:"timeout,omitempty"`
	MaxPoints    int                  `json:"maxPoints,omitempty"`
	Reason       string               `json:"reason"`
}

// Datasource is a metrics datasource as returned by the API (never with secrets).
type Datasource struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Type      string             `json:"type"`
	URL       string             `json:"url"`
	BuiltIn   bool               `json:"builtIn"`
	Auth      DatasourceAuthInfo `json:"auth"`
	Dynatrace *DynatraceSettings `json:"dynatrace,omitempty"`
	Headers   map[string]string  `json:"headers"`
	Timeout   string             `json:"timeout,omitempty"`
	MaxPoints int                `json:"maxPoints,omitempty"`
	UsedBy    []string           `json:"usedBy"`
	Version   int                `json:"version"`
	CreatedAt *time.Time         `json:"createdAt,omitempty"`
	CreatedBy *Actor             `json:"createdBy,omitempty"`
	UpdatedAt *time.Time         `json:"updatedAt,omitempty"`
	UpdatedBy *Actor             `json:"updatedBy,omitempty"`
}

// DynatraceSettings are the settings of a dynatrace datasource (schema DatasourceDynatrace).
type DynatraceSettings struct {
	// ActiveGateURL, when set, receives the queries instead of the environment URL.
	ActiveGateURL string `json:"activeGateURL,omitempty"`
}

// DatasourceList is the response of GET /v1/admin/datasources.
type DatasourceList struct {
	Items []Datasource `json:"items"`
}

// MetricsJobNames are scrape job names used for diagnoses.
type MetricsJobNames struct {
	CAdvisor         []string `json:"cadvisor,omitempty"`
	KubeStateMetrics []string `json:"kubeStateMetrics,omitempty"`
}

// MetricsBinding says where a cluster's metrics are. ClusterLabel "" = no cluster matcher.
type MetricsBinding struct {
	DatasourceID      string           `json:"datasourceId"`
	ClusterLabel      string           `json:"clusterLabel"`
	ClusterLabelValue string           `json:"clusterLabelValue,omitempty"`
	ExtraMatchers     []string         `json:"extraMatchers,omitempty"`
	JobNames          *MetricsJobNames `json:"jobNames,omitempty"`
}

// DefaultMetricsBinding is the binding of a cluster without one (behaviour without a binding).
func DefaultMetricsBinding(cluster string) MetricsBinding {
	return MetricsBinding{DatasourceID: LocalDatasourceID, ClusterLabel: DefaultClusterLabel, ClusterLabelValue: cluster}
}

// DatasourceTestRequest is the body of POST /v1/admin/datasources/{id}/test.
type DatasourceTestRequest struct {
	Binding *MetricsBinding `json:"binding,omitempty"`
}

// DatasourceTestFound is what a binding's matchers found.
type DatasourceTestFound struct {
	ContainerSeries int `json:"containerSeries,omitempty"`
	Pods            int `json:"pods,omitempty"`
	Nodes           int `json:"nodes,omitempty"`
}

// DatasourceTestResult is its response.
type DatasourceTestResult struct {
	OK          bool                 `json:"ok"`
	LatencyMs   int                  `json:"latencyMs,omitempty"`
	Found       *DatasourceTestFound `json:"found,omitempty"`
	LabelValues []string             `json:"labelValues,omitempty"`
	// Metrics maps Kerf's inputs to the datasource's own metrics (dynatrace).
	Metrics []DatasourceMetricStatus `json:"metrics,omitempty"`
	Checks  []ReadinessCheck         `json:"checks"`
}

// Metric statuses (DatasourceMetricStatus.status).
const (
	MetricFound        = "found"
	MetricMissing      = "missing"
	MetricNotAvailable = "notAvailable"
)

// DatasourceMetricStatus says whether one Kerf input was found in the datasource.
type DatasourceMetricStatus struct {
	Input        string `json:"input"`
	SourceMetric string `json:"sourceMetric,omitempty"`
	UsedFor      string `json:"usedFor,omitempty"`
	Status       string `json:"status"`
	Series       int    `json:"series,omitempty"`
	Note         string `json:"note,omitempty"`
}

// Remediation types (ReadinessCheck.remediationType).
const (
	RemediationCommand = "command"
	RemediationYAML    = "yaml"
	RemediationText    = "text"
)

// ReadinessCheck is one check result. Id, category, severity, source and gates come from the
// catalog in api/v1alpha1/readiness.
type ReadinessCheck struct {
	ID              string            `json:"id"`
	Category        string            `json:"category"`
	Severity        string            `json:"severity"`
	Source          string            `json:"source"`
	Gates           string            `json:"gates,omitempty"`
	Status          string            `json:"status"`
	Message         string            `json:"message"`
	Remediation     string            `json:"remediation,omitempty"`
	RemediationType string            `json:"remediationType,omitempty"`
	DocsURL         string            `json:"docsURL,omitempty"`
	CheckedAt       time.Time         `json:"checkedAt"`
	Details         map[string]string `json:"details,omitempty"`
}

// AgentCapability is the RBAC self-check of one capability.
type AgentCapability struct {
	Name    string   `json:"name"`
	Granted bool     `json:"granted"`
	Missing []string `json:"missing,omitempty"`
	Message string   `json:"message,omitempty"`
}

// ReadinessSummary is the readiness of a cluster in lists.
type ReadinessSummary struct {
	State     string     `json:"state"`
	Blocking  int        `json:"blocking"`
	Warnings  int        `json:"warnings"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
}

// ReadinessCounts are ClusterReadiness.summary.
type ReadinessCounts struct {
	Blocking int `json:"blocking"`
	Warnings int `json:"warnings"`
	Unknown  int `json:"unknown"`
}

// ClusterReadiness is the response of GET /v1/clusters/{cluster}/readiness.
type ClusterReadiness struct {
	Cluster            string            `json:"cluster"`
	State              string            `json:"state"`
	StateSince         *time.Time        `json:"stateSince,omitempty"`
	CheckedAt          *time.Time        `json:"checkedAt,omitempty"`
	Summary            ReadinessCounts   `json:"summary"`
	NextState          string            `json:"nextState,omitempty"`
	BlockedBy          []string          `json:"blockedBy,omitempty"`
	Capabilities       []AgentCapability `json:"capabilities,omitempty"`
	RecheckRequestedAt *time.Time        `json:"recheckRequestedAt,omitempty"`
	Checks             []ReadinessCheck  `json:"checks"`
}

// RecheckAccepted is the response of POST /v1/clusters/{cluster}/readiness/recheck.
type RecheckAccepted struct {
	RequestedAt  time.Time `json:"requestedAt"`
	HubChecks    bool      `json:"hubChecks"`
	AgentRecheck bool      `json:"agentRecheck"`
}

// ReadinessHistoryEntry is a state transition (kind state) or a check status change (kind check).
type ReadinessHistoryEntry struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	FromState  string    `json:"fromState,omitempty"`
	ToState    string    `json:"toState,omitempty"`
	CheckID    string    `json:"checkId,omitempty"`
	FromStatus string    `json:"fromStatus,omitempty"`
	ToStatus   string    `json:"toStatus,omitempty"`
	Message    string    `json:"message,omitempty"`
}

// ReadinessHistory is the response of GET /v1/clusters/{cluster}/readiness/history.
type ReadinessHistory struct {
	Items    []ReadinessHistoryEntry `json:"items"`
	Continue string                  `json:"continue,omitempty"`
}

// InstallSnippet is the response of GET /v1/admin/clusters/{cluster}/install.
type InstallSnippet struct {
	Format                  string   `json:"format"`
	Content                 string   `json:"content"`
	Filename                string   `json:"filename"`
	ArgoCDIgnoreDifferences string   `json:"argocdIgnoreDifferences"`
	Notes                   []string `json:"notes,omitempty"`
}

// ClusterMetricsBinding is a cluster's metrics binding for the pricing service. Bound is
// false for clusters without a stored binding (datasource local, label cluster = the name).
type ClusterMetricsBinding struct {
	Cluster string         `json:"cluster"`
	Binding MetricsBinding `json:"binding"`
	Bound   bool           `json:"bound"`
	// DatasourceType is the bound datasource's type (dynatrace clusters are read through the
	// node inventory endpoint, not the query proxy).
	DatasourceType string `json:"datasourceType,omitempty"`
}

// NodeInventoryItem is one node of GET /v1/internal/clusters/{cluster}/nodes.
type NodeInventoryItem struct {
	Node                   string  `json:"node"`
	InstanceType           string  `json:"instanceType,omitempty"`
	CapacityType           string  `json:"capacityType,omitempty"`
	Zone                   string  `json:"zone,omitempty"`
	Region                 string  `json:"region,omitempty"`
	AllocatableCores       float64 `json:"allocatableCores,omitempty"`
	AllocatableMemoryBytes float64 `json:"allocatableMemoryBytes,omitempty"`
}

// NodeInventory is the response of GET /v1/internal/clusters/{cluster}/nodes.
type NodeInventory struct {
	Items []NodeInventoryItem `json:"items"`
}

// MetricsBindingList is the response of GET /v1/internal/metrics-bindings.
type MetricsBindingList struct {
	Items []ClusterMetricsBinding `json:"items"`
}

// PromQueryForm are the parameters the datasource query proxy forwards.
type PromQueryForm struct {
	Query   string `json:"query"`
	Time    string `json:"time,omitempty"`
	Start   string `json:"start,omitempty"`
	End     string `json:"end,omitempty"`
	Step    string `json:"step,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

// PromQueryResponse is a Prometheus HTTP API response, passed through by the proxy.
type PromQueryResponse struct {
	Status    string         `json:"status"`
	Data      map[string]any `json:"data,omitempty"`
	ErrorType string         `json:"errorType,omitempty"`
	Error     string         `json:"error,omitempty"`
}
