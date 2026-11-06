package hubapi

import (
	"encoding/json"
	"time"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

// Admin area (WO-19, D-022/D-023/D-024): types of the admin, auth and config endpoints.
// Flow and examples: docs/contracts/admin.md. Policy specs use the CRD types of api/v1alpha1
// (their JSON is the RightsizingPolicySpec schema); validate them with api/v1alpha1/validate.

// User roles (UserRole). Each includes the ones before it.
const (
	UserRoleViewer   = "viewer"
	UserRoleOperator = "operator"
	UserRoleAdmin    = "admin"
)

// UserRoles lists the UserRole enum, lowest first.
var UserRoles = []string{UserRoleViewer, UserRoleOperator, UserRoleAdmin}

// UserRoleAtLeast reports whether role has at least the rights of min.
func UserRoleAtLeast(role, min string) bool {
	rank := func(r string) int {
		for i, v := range UserRoles {
			if v == r {
				return i
			}
		}
		return -1
	}
	return rank(role) >= 0 && rank(role) >= rank(min)
}

// TokenUserRole maps a bearer token role to the user role it counts as (`ui` → viewer, `admin`
// → admin); "" for `agent`.
func TokenUserRole(tokenRole string) string {
	switch tokenRole {
	case RoleUI:
		return UserRoleViewer
	case RoleAdmin:
		return UserRoleAdmin
	}
	return ""
}

// Session and CSRF (D-023).
const (
	SessionCookie = "kerf_session"
	CSRFHeader    = "X-CSRF-Token"
)

// Auth methods (Me.authMethod).
const (
	AuthMethodSession = "session"
	AuthMethodToken   = "token"
)

// Actor types (Actor.type).
const (
	ActorUser   = "user"
	ActorToken  = "token"
	ActorSystem = "system"
)

// Problem codes of the admin area (Problem.code).
const (
	CodeInvalidCredentials = "invalid_credentials"
	CodeAccountLocked      = "account_locked"
	CodeRateLimited        = "rate_limited"
	CodeIdPManaged         = "idp_managed"
	// CodeScopeForbidden: the caller's team bindings do not cover the endpoint or the workload.
	CodeScopeForbidden       = "scope_forbidden"
	CodeCSRFInvalid          = "csrf_invalid"
	CodeAdminRequired        = "admin_required"
	CodePreconditionRequired = "precondition_required"
	CodeVersionConflict      = "version_conflict"
	CodeAlreadyExists        = "already_exists"
	CodeInvalidPolicy        = "invalid_policy"
	CodeInvalidOverride      = "invalid_override"
	CodeTypeImmutable        = "type_immutable"
	CodeRecommendationStale  = "recommendation_stale"
	CodeNotInAudit           = "not_in_audit"
	CodeLastAdmin            = "last_admin"
	CodeNotImplemented       = "not_implemented"
)

// Settings sections (SettingsSection).
const (
	SettingsRecommender = "recommender"
	SettingsPricing     = "pricing"
)

// Cluster policy sources (ClusterPolicySource) and policy origins (PolicyOrigin).
const (
	PolicySourceHub  = "hub"
	PolicySourceGit  = "git"
	PolicySourceBoth = "both"
)

// Environments (Environment).
const (
	EnvironmentProd    = "prod"
	EnvironmentNonProd = "non-prod"
)

// FleetScope is ConfigTarget.id and the cluster_automation scope of the fleet-wide pause.
const FleetScope = "*"

// Pause scopes (ConfigFlags.pauseScope).
const (
	PauseScopeCluster = "cluster"
	PauseScopeFleet   = "fleet"
)

// Policy version operations (PolicyVersion.operation).
const (
	PolicyOpCreate = "create"
	PolicyOpUpdate = "update"
	PolicyOpDelete = "delete"
	PolicyOpRevert = "revert"
)

// Approval states (Approval.state).
const (
	ApprovalPending    = "pending"
	ApprovalConsumed   = "consumed"
	ApprovalExpired    = "expired"
	ApprovalSuperseded = "superseded"
)

// Approval expiry: default and maximum (ApprovalRequest.expiresAt).
const (
	ApprovalDefaultTTL = 24 * time.Hour
	ApprovalMaxTTL     = 7 * 24 * time.Hour
)

// Layer sources (LayerSource), lowest precedence first.
const (
	LayerBuiltIn             = "built-in"
	LayerGlobalDefault       = "global-default"
	LayerClusterDefaultChart = "cluster-default-chart"
	LayerClusterDefault      = "cluster-default"
	LayerPolicy              = "policy"
	LayerWorkloadOverride    = "workload-override"
	LayerAnnotation          = "annotation"
)

// LayerSources lists the LayerSource enum in precedence order.
var LayerSources = []string{LayerBuiltIn, LayerGlobalDefault, LayerClusterDefaultChart, LayerClusterDefault, LayerPolicy, LayerWorkloadOverride, LayerAnnotation}

// EventConfigChanged is the audit type of configuration changes (ConfigChange.type). Agents never
// send it and GET /v1/audit never returns it.
const EventConfigChanged = "config_changed"

// Config target types (ConfigTargetType).
const (
	TargetSettings         = "settings"
	TargetPolicy           = "policy"
	TargetWorkloadOverride = "workload-override"
	TargetApproval         = "approval"
	TargetCluster          = "cluster"
	TargetAutomation       = "automation"
	TargetToken            = "token"
	TargetUser             = "user"
	TargetTeam             = "team"
)

// ConfigTargetTypes lists the ConfigTargetType enum.
var ConfigTargetTypes = []string{TargetSettings, TargetPolicy, TargetWorkloadOverride, TargetApproval, TargetCluster, TargetAutomation, TargetToken, TargetUser, TargetDatasource, TargetReadiness, TargetNotification, TargetTeam}

// Config actions (ConfigAction, docs/contracts/events.md).
const (
	ActionSettingsUpdated   = "SettingsUpdated"
	ActionPolicyCreated     = "PolicyCreated"
	ActionPolicyUpdated     = "PolicyUpdated"
	ActionPolicyDeleted     = "PolicyDeleted"
	ActionPolicyReverted    = "PolicyReverted"
	ActionOverrideSet       = "OverrideSet"
	ActionOverrideCleared   = "OverrideCleared"
	ActionWorkloadApproved  = "WorkloadApproved"
	ActionClusterCreated    = "ClusterCreated"
	ActionClusterUpdated    = "ClusterUpdated"
	ActionClusterDeleted    = "ClusterDeleted"
	ActionAutomationPaused  = "AutomationPaused"
	ActionAutomationResumed = "AutomationResumed"
	ActionTokenCreated      = "TokenCreated"
	ActionTokenRevoked      = "TokenRevoked"
	ActionUserCreated       = "UserCreated"
	ActionUserUpdated       = "UserUpdated"
	ActionUserDeleted       = "UserDeleted"
	ActionPasswordReset     = "PasswordReset"
	ActionTeamCreated       = "TeamCreated"
	ActionTeamUpdated       = "TeamUpdated"
	ActionTeamDeleted       = "TeamDeleted"
)

// ConfigActions lists the ConfigAction enum in contract order.
var ConfigActions = []string{
	ActionSettingsUpdated, ActionPolicyCreated, ActionPolicyUpdated, ActionPolicyDeleted, ActionPolicyReverted,
	ActionOverrideSet, ActionOverrideCleared, ActionWorkloadApproved, ActionClusterCreated, ActionClusterUpdated,
	ActionClusterDeleted, ActionAutomationPaused, ActionAutomationResumed, ActionTokenCreated, ActionTokenRevoked,
	ActionUserCreated, ActionUserUpdated, ActionUserDeleted, ActionPasswordReset,
	ActionDatasourceCreated, ActionDatasourceUpdated, ActionDatasourceDeleted, ActionReadinessChanged,
	ActionNotificationChannelCreated, ActionNotificationChannelUpdated, ActionNotificationChannelDeleted,
	ActionNotificationRuleCreated, ActionNotificationRuleUpdated, ActionNotificationRuleDeleted, ActionNotificationDigestUpdated,
	ActionTeamCreated, ActionTeamUpdated, ActionTeamDeleted,
}

// Actor is who made a change.
type Actor struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
	// Subject and Email identify a user signed in with the identity provider (source oidc).
	Subject string `json:"subject,omitempty"`
	Email   string `json:"email,omitempty"`
	// Teams is the scope used: the teams whose binding allowed the action. Absent: a fleet-wide role.
	Teams []string `json:"teams,omitempty"`
}

// ChangeReason is the body of the DELETE endpoints.
type ChangeReason struct {
	Reason string `json:"reason"`
}

// LoginRequest is the body of POST /v1/auth/login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Me is the response of login and GET /v1/me.
type Me struct {
	AuthMethod       string     `json:"authMethod"`
	Role             string     `json:"role"`
	User             *User      `json:"user,omitempty"`
	TokenID          string     `json:"tokenId,omitempty"`
	CSRFToken        string     `json:"csrfToken,omitempty"`
	SessionExpiresAt *time.Time `json:"sessionExpiresAt,omitempty"`
	// Scope and TeamRoles: what the caller may see and do (fleet: role everywhere, teams raise it;
	// teams: only the bound teams). Role is the highest role anywhere.
	Scope     string     `json:"scope,omitempty"`
	TeamRoles []TeamRole `json:"teamRoles,omitempty"`
}

// PasswordChange is the body of POST /v1/me/password.
type PasswordChange struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// User is a local user.
type User struct {
	Username           string     `json:"username"`
	DisplayName        string     `json:"displayName"`
	Role               string     `json:"role"`
	Disabled           bool       `json:"disabled"`
	MustChangePassword bool       `json:"mustChangePassword"`
	CreatedAt          time.Time  `json:"createdAt"`
	CreatedBy          string     `json:"createdBy,omitempty"`
	UpdatedAt          *time.Time `json:"updatedAt,omitempty"`
	LastLoginAt        *time.Time `json:"lastLoginAt,omitempty"`
	LockedUntil        *time.Time `json:"lockedUntil,omitempty"`
	DeletedAt          *time.Time `json:"deletedAt,omitempty"`
	Version            int        `json:"version"`
	// Source: UserSourceLocal (password) or UserSourceOIDC (signs in with the identity provider; the
	// role comes from its groups). Email is set for IdP users.
	Source string `json:"source,omitempty"`
	Email  string `json:"email,omitempty"`
	// Scope: fleet (Role everywhere; TeamRoles may raise it on some teams) or teams (only the teams
	// of TeamRoles; Role is the highest of them).
	Scope     string     `json:"scope,omitempty"`
	TeamRoles []TeamRole `json:"teamRoles,omitempty"`
}

// User sources.
const (
	UserSourceLocal = "local"
	UserSourceOIDC  = "oidc"
)

// AuthProviders is the response of GET /v1/auth/providers: how the sign-in page offers to sign in.
type AuthProviders struct {
	// Local: everyone may sign in with a password. false: only the break-glass admin can.
	Local bool `json:"local"`
	// OIDC is set when sign-in with the identity provider is configured.
	OIDC *OIDCProvider `json:"oidc,omitempty"`
}

// OIDCProvider is the identity provider offered on the sign-in page.
type OIDCProvider struct {
	Name     string `json:"name"`
	LoginURL string `json:"loginURL"`
}

// UserList is the response of GET /v1/admin/users.
type UserList struct {
	Items []User `json:"items"`
}

// UserCreate is the body of POST /v1/admin/users.
type UserCreate struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role"`
	Password    string `json:"password"`
	// MustChangePassword defaults to true when absent.
	MustChangePassword *bool `json:"mustChangePassword,omitempty"`
	// Scope (default fleet) and TeamRoles: see User. With scope teams, Role is derived.
	Scope     string     `json:"scope,omitempty"`
	TeamRoles []TeamRole `json:"teamRoles,omitempty"`
	Reason    string     `json:"reason"`
}

// UserUpdate is the body of PATCH /v1/admin/users/{username}; nil fields are unchanged.
type UserUpdate struct {
	DisplayName *string `json:"displayName,omitempty"`
	Role        *string `json:"role,omitempty"`
	Disabled    *bool   `json:"disabled,omitempty"`
	// Scope and TeamRoles replace the user's bindings (nil: unchanged).
	Scope     *string     `json:"scope,omitempty"`
	TeamRoles *[]TeamRole `json:"teamRoles,omitempty"`
	Reason    string      `json:"reason"`
}

// PasswordReset is the body of POST /v1/admin/users/{username}/reset-password.
type PasswordReset struct {
	Password string `json:"password,omitempty"`
	Reason   string `json:"reason"`
}

// PasswordResetResult is its response.
type PasswordResetResult struct {
	TemporaryPassword  string `json:"temporaryPassword,omitempty"`
	MustChangePassword bool   `json:"mustChangePassword"`
}

// TokenInfo describes an API token (never its value).
type TokenInfo struct {
	ID          string     `json:"id"`
	Role        string     `json:"role"`
	Cluster     string     `json:"cluster,omitempty"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"createdAt"`
	CreatedBy   string     `json:"createdBy,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

// TokenList is the response of GET /v1/admin/tokens.
type TokenList struct {
	Items []TokenInfo `json:"items"`
}

// TokenCreate is the body of POST /v1/admin/tokens.
type TokenCreate struct {
	Role        string     `json:"role"`
	Cluster     string     `json:"cluster,omitempty"`
	Description string     `json:"description"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	Reason      string     `json:"reason"`
}

// TokenCreated holds a new token and its value (shown once).
type TokenCreated struct {
	Token TokenInfo `json:"token"`
	Value string    `json:"value"`
}

// RecommenderProfileSettings is one recommender profile. Durations are Go duration strings.
type RecommenderProfileSettings struct {
	Name             string  `json:"name"`
	Window           string  `json:"window"`
	CPUPercentile    float64 `json:"cpuPercentile"`
	MemoryPercentile float64 `json:"memoryPercentile"`
	MemoryPeakWindow string  `json:"memoryPeakWindow,omitempty"`
	CPUHeadroom      float64 `json:"cpuHeadroom"`
	MemoryBuffer     float64 `json:"memoryBuffer"`
}

// RecommenderSettings is the settings section `recommender`.
type RecommenderSettings struct {
	Interval  string                       `json:"interval"`
	Retention string                       `json:"retention"`
	Profiles  []RecommenderProfileSettings `json:"profiles"`
}

// RecommenderSettingsDoc is a stored recommender section.
type RecommenderSettingsDoc struct {
	Version   int                 `json:"version"`
	UpdatedAt *time.Time          `json:"updatedAt,omitempty"`
	UpdatedBy *Actor              `json:"updatedBy,omitempty"`
	Reason    string              `json:"reason,omitempty"`
	Value     RecommenderSettings `json:"value"`
}

// RecommenderSettingsUpdate is the body of PUT /v1/admin/settings/recommender.
type RecommenderSettingsUpdate struct {
	Value  RecommenderSettings `json:"value"`
	Reason string              `json:"reason"`
}

// ClusterPricingAdjustment adjusts one cluster's rates.
type ClusterPricingAdjustment struct {
	Cluster          string   `json:"cluster"`
	DiscountPercent  *float64 `json:"discountPercent,omitempty"`
	CPUCoreHourUSD   *float64 `json:"cpuCoreHourUSD,omitempty"`
	MemoryGiBHourUSD *float64 `json:"memoryGiBHourUSD,omitempty"`
}

// PricingSettings is the settings section `pricing`.
type PricingSettings struct {
	DefaultDiscountPercent *float64                   `json:"defaultDiscountPercent,omitempty"`
	Clusters               []ClusterPricingAdjustment `json:"clusters"`
}

// PricingSettingsDoc is a stored pricing section.
type PricingSettingsDoc struct {
	Version   int             `json:"version"`
	UpdatedAt *time.Time      `json:"updatedAt,omitempty"`
	UpdatedBy *Actor          `json:"updatedBy,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Value     PricingSettings `json:"value"`
	// Rates is where the pricing service's rates come from now (read-only; GET only).
	Rates *PricingRates `json:"rates,omitempty"`
}

// PricingRates is the state of the pricing service's rates, from its series in the hub's store.
type PricingRates struct {
	// CUR is the Cost and Usage Report reader's state; nil when it is off.
	CUR      *PricingCURStatus     `json:"cur,omitempty"`
	Clusters []ClusterPricingRates `json:"clusters"`
}

// PricingCURStatus is the state of the AWS Cost and Usage Report reads.
type PricingCURStatus struct {
	// LastReadOK is false while the last read failed.
	LastReadOK  bool       `json:"lastReadOK"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
	// Rates is the number of instance type, region and capacity type keys with an effective rate.
	Rates  int            `json:"rates"`
	Period *PricingPeriod `json:"period,omitempty"`
}

// ClusterPricingRates counts a cluster's priced nodes by rate source.
type ClusterPricingRates struct {
	Cluster string              `json:"cluster"`
	Sources []PricingRateSource `json:"sources"`
}

// PricingSettingsUpdate is the body of PUT /v1/admin/settings/pricing.
type PricingSettingsUpdate struct {
	Value  PricingSettings `json:"value"`
	Reason string          `json:"reason"`
}

// Settings is the response of GET /v1/admin/settings.
type Settings struct {
	Recommender RecommenderSettingsDoc `json:"recommender"`
	Pricing     PricingSettingsDoc     `json:"pricing"`
}

// SettingsVersion is one saved version of a settings section.
type SettingsVersion struct {
	Section   string          `json:"section"`
	Version   int             `json:"version"`
	Value     json.RawMessage `json:"value"`
	CreatedAt time.Time       `json:"createdAt"`
	Actor     Actor           `json:"actor"`
	Reason    string          `json:"reason"`
}

// SettingsVersionList is the response of GET /v1/admin/settings/{section}/versions.
type SettingsVersionList struct {
	Items    []SettingsVersion `json:"items"`
	Continue string            `json:"continue,omitempty"`
}

// PolicyInput is the body of policy create and update.
type PolicyInput struct {
	Type        string                         `json:"type"`
	Name        string                         `json:"name,omitempty"`
	Description string                         `json:"description,omitempty"`
	Clusters    []string                       `json:"clusters,omitempty"`
	Spec        v1alpha1.RightsizingPolicySpec `json:"spec"`
	Reason      string                         `json:"reason"`
	// OverrideReadiness (admin only) allows auto-patch on clusters that are not AutomationReady (D-025).
	OverrideReadiness bool `json:"overrideReadiness,omitempty"`
}

// PolicyDraft is a policy for the impact preview.
type PolicyDraft struct {
	Type        string                         `json:"type"`
	Name        string                         `json:"name,omitempty"`
	Description string                         `json:"description,omitempty"`
	Clusters    []string                       `json:"clusters,omitempty"`
	WorkloadID  string                         `json:"workloadId,omitempty"`
	Spec        v1alpha1.RightsizingPolicySpec `json:"spec"`
}

// AdminPolicy is a hub-managed policy.
type AdminPolicy struct {
	ID          string                         `json:"id"`
	Type        string                         `json:"type"`
	Name        string                         `json:"name"`
	Description string                         `json:"description,omitempty"`
	Priority    int32                          `json:"priority"`
	Clusters    []string                       `json:"clusters"`
	WorkloadID  string                         `json:"workloadId,omitempty"`
	Spec        v1alpha1.RightsizingPolicySpec `json:"spec"`
	Version     int                            `json:"version"`
	CreatedAt   time.Time                      `json:"createdAt"`
	CreatedBy   Actor                          `json:"createdBy"`
	UpdatedAt   time.Time                      `json:"updatedAt"`
	UpdatedBy   Actor                          `json:"updatedBy"`
	DeletedAt   *time.Time                     `json:"deletedAt,omitempty"`
}

// AdminPolicyList is the response of GET /v1/admin/policies.
type AdminPolicyList struct {
	Items []AdminPolicy `json:"items"`
}

// PolicyVersion is one version of a policy.
type PolicyVersion struct {
	Version      int                            `json:"version"`
	Operation    string                         `json:"operation"`
	RevertedFrom int                            `json:"revertedFrom,omitempty"`
	Name         string                         `json:"name"`
	Description  string                         `json:"description,omitempty"`
	Priority     int32                          `json:"priority"`
	Clusters     []string                       `json:"clusters"`
	Spec         v1alpha1.RightsizingPolicySpec `json:"spec"`
	Deleted      bool                           `json:"deleted"`
	CreatedAt    time.Time                      `json:"createdAt"`
	Actor        Actor                          `json:"actor"`
	Reason       string                         `json:"reason"`
}

// PolicyVersionList is the response of GET /v1/admin/policies/{policyId}/versions.
type PolicyVersionList struct {
	Items    []PolicyVersion `json:"items"`
	Continue string          `json:"continue,omitempty"`
}

// PolicyRevert is the body of POST /v1/admin/policies/{policyId}/revert.
type PolicyRevert struct {
	Version int    `json:"version"`
	Reason  string `json:"reason"`
	// OverrideReadiness (admin only) allows a revert that enables auto-patch on clusters that are
	// not AutomationReady (D-025).
	OverrideReadiness bool `json:"overrideReadiness,omitempty"`
}

// PolicyInventoryItem is one RightsizingPolicy CR on a cluster as the agent reports it.
type PolicyInventoryItem struct {
	Name             string                          `json:"name"`
	Source           string                          `json:"source"`
	Priority         int32                           `json:"priority"`
	Generation       int64                           `json:"generation"`
	Valid            bool                            `json:"valid"`
	Message          string                          `json:"message,omitempty"`
	HubPolicyID      string                          `json:"hubPolicyId,omitempty"`
	HubVersion       int                             `json:"hubVersion,omitempty"`
	HubPolicyType    string                          `json:"hubPolicyType,omitempty"`
	MatchedWorkloads *int32                          `json:"matchedWorkloads,omitempty"`
	Spec             *v1alpha1.RightsizingPolicySpec `json:"spec,omitempty"`
}

// ClusterPolicyInventory is one cluster's reported inventory.
type ClusterPolicyInventory struct {
	Cluster       string                `json:"cluster"`
	ReportedAt    *time.Time            `json:"reportedAt,omitempty"`
	ConfigVersion string                `json:"configVersion,omitempty"`
	Items         []PolicyInventoryItem `json:"items"`
}

// PolicyInventoryList is the response of GET /v1/admin/policy-inventory.
type PolicyInventoryList struct {
	Items []ClusterPolicyInventory `json:"items"`
}

// Preview operations (PolicyChange.op).
const (
	PreviewUpsert = "upsert"
	PreviewDelete = "delete"
)

// PolicyChange is one hypothetical change of the impact preview.
type PolicyChange struct {
	Op       string       `json:"op"`
	PolicyID string       `json:"policyId,omitempty"`
	Policy   *PolicyDraft `json:"policy,omitempty"`
}

// PreviewRequest is the body of POST /v1/admin/preview.
type PreviewRequest struct {
	Changes []PolicyChange `json:"changes"`
	Limit   int            `json:"limit,omitempty"`
}

// ImpactItem is one workload whose effective mode changes.
type ImpactItem struct {
	WorkloadID        string         `json:"workloadId"`
	Cluster           string         `json:"cluster"`
	Namespace         string         `json:"namespace"`
	Kind              string         `json:"kind"`
	Name              string         `json:"name"`
	Before            EffectiveModes `json:"before"`
	After             EffectiveModes `json:"after"`
	PolicyBefore      string         `json:"policyBefore,omitempty"`
	PolicyAfter       string         `json:"policyAfter,omitempty"`
	MonthlySavingsUSD *Savings       `json:"monthlySavingsUSD,omitempty"`
}

// ImpactPreview is the response of POST /v1/admin/preview.
type ImpactPreview struct {
	WorkloadsEvaluated       int          `json:"workloadsEvaluated"`
	WorkloadsChanged         int          `json:"workloadsChanged"`
	SavingsIntoAutoPatchUSD  Savings      `json:"savingsIntoAutoPatchUSD"`
	SavingsOutOfAutoPatchUSD Savings      `json:"savingsOutOfAutoPatchUSD"`
	Items                    []ImpactItem `json:"items"`
	Truncated                bool         `json:"truncated"`
	Warnings                 []string     `json:"warnings,omitempty"`
}

// Automation is the pause state of a cluster or of the fleet.
type Automation struct {
	Paused   bool       `json:"paused"`
	Reason   string     `json:"reason,omitempty"`
	By       *Actor     `json:"by,omitempty"`
	Since    *time.Time `json:"since,omitempty"`
	ResumeAt *time.Time `json:"resumeAt,omitempty"`
	Version  int        `json:"version"`
}

// AutomationUpdate is the body of PUT .../automation.
type AutomationUpdate struct {
	Paused   bool       `json:"paused"`
	Reason   string     `json:"reason"`
	ResumeAt *time.Time `json:"resumeAt,omitempty"`
}

// ClusterAutomationState is one entry of FleetAutomation.clusters.
type ClusterAutomationState struct {
	Cluster    string     `json:"cluster"`
	Automation Automation `json:"automation"`
	Paused     bool       `json:"paused"`
}

// FleetAutomation is the response of GET /v1/admin/automation.
type FleetAutomation struct {
	Fleet    Automation               `json:"fleet"`
	Clusters []ClusterAutomationState `json:"clusters"`
}

// AgentConfigSync tells whether a cluster's agent runs the current config.
type AgentConfigSync struct {
	AgentVersion         string     `json:"agentVersion,omitempty"`
	LastHeartbeatAt      *time.Time `json:"lastHeartbeatAt,omitempty"`
	LastConfigPullAt     *time.Time `json:"lastConfigPullAt,omitempty"`
	ConfigVersion        string     `json:"configVersion"`
	AppliedConfigVersion string     `json:"appliedConfigVersion,omitempty"`
	InSync               bool       `json:"inSync"`
}

// ClusterAdmin is the response of GET /v1/admin/clusters/{cluster}.
type ClusterAdmin struct {
	Name            string                  `json:"name"`
	Description     string                  `json:"description"`
	Environment     string                  `json:"environment,omitempty"`
	Owner           string                  `json:"owner"`
	PolicySource    string                  `json:"policySource"`
	CreatedAt       *time.Time              `json:"createdAt,omitempty"`
	UpdatedAt       *time.Time              `json:"updatedAt,omitempty"`
	UpdatedBy       *Actor                  `json:"updatedBy,omitempty"`
	DeletedAt       *time.Time              `json:"deletedAt,omitempty"`
	Version         int                     `json:"version"`
	Automation      Automation              `json:"automation"`
	Paused          bool                    `json:"paused"`
	Agent           AgentConfigSync         `json:"agent"`
	PolicyInventory *ClusterPolicyInventory `json:"policyInventory,omitempty"`
	Provider        string                  `json:"provider,omitempty"`
	Region          string                  `json:"region,omitempty"`
	Metrics         MetricsBinding          `json:"metrics"`
	Readiness       ReadinessSummary        `json:"readiness"`
}

// ClusterCreate is the body of POST /v1/admin/clusters.
type ClusterCreate struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Environment  string `json:"environment"`
	Owner        string `json:"owner,omitempty"`
	PolicySource string `json:"policySource,omitempty"`
	// CreateAgentToken defaults to true when absent.
	CreateAgentToken *bool           `json:"createAgentToken,omitempty"`
	Provider         string          `json:"provider,omitempty"`
	Region           string          `json:"region,omitempty"`
	Metrics          *MetricsBinding `json:"metrics,omitempty"`
	Reason           string          `json:"reason"`
}

// ClusterCreated is its response.
type ClusterCreated struct {
	Cluster    ClusterAdmin  `json:"cluster"`
	AgentToken *TokenCreated `json:"agentToken,omitempty"`
}

// ClusterUpdate is the body of PATCH /v1/admin/clusters/{cluster}; nil fields are unchanged.
type ClusterUpdate struct {
	Description  *string         `json:"description,omitempty"`
	Environment  *string         `json:"environment,omitempty"`
	Owner        *string         `json:"owner,omitempty"`
	PolicySource *string         `json:"policySource,omitempty"`
	Provider     *string         `json:"provider,omitempty"`
	Region       *string         `json:"region,omitempty"`
	Metrics      *MetricsBinding `json:"metrics,omitempty"`
	Reason       string          `json:"reason"`
}

// ClusterConfig is the response of GET /v1/clusters/{cluster}/config (agent).
type ClusterConfig struct {
	Cluster       string           `json:"cluster"`
	ConfigVersion string           `json:"configVersion"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	PolicySource  string           `json:"policySource"`
	Policies      []ConfigPolicy   `json:"policies"`
	Flags         ConfigFlags      `json:"flags"`
	Approvals     []ConfigApproval `json:"approvals"`
	// HubVersion and MinAgentVersion feed the agent's agent.version check (D-025).
	HubVersion      string `json:"hubVersion,omitempty"`
	MinAgentVersion string `json:"minAgentVersion,omitempty"`
}

// ConfigPolicy is one hub-managed policy to materialise as a CR.
type ConfigPolicy struct {
	ID       string                         `json:"id"`
	Name     string                         `json:"name"`
	Type     string                         `json:"type"`
	Version  int                            `json:"version"`
	Priority int32                          `json:"priority"`
	Spec     v1alpha1.RightsizingPolicySpec `json:"spec"`
}

// ConfigFlags are the cluster flags of the config.
type ConfigFlags struct {
	Paused      bool       `json:"paused"`
	PauseScope  string     `json:"pauseScope,omitempty"`
	PauseReason string     `json:"pauseReason,omitempty"`
	PausedAt    *time.Time `json:"pausedAt,omitempty"`
	ResumeAt    *time.Time `json:"resumeAt,omitempty"`
	// Recheck: re-run the agent self-checks if RecheckRequestedAt is newer than the last run.
	Recheck            bool       `json:"recheck,omitempty"`
	RecheckRequestedAt *time.Time `json:"recheckRequestedAt,omitempty"`
}

// ConfigApproval is an approve-once grant sent to the agent.
type ConfigApproval struct {
	ID               string      `json:"id"`
	Workload         WorkloadRef `json:"workload"`
	RecommendationID string      `json:"recommendationID"`
	ExpiresAt        time.Time   `json:"expiresAt"`
	ApprovedBy       string      `json:"approvedBy"`
	ApprovedAt       time.Time   `json:"approvedAt"`
}

// ApprovalRequest is the body of POST /v1/workloads/{id}/approve.
type ApprovalRequest struct {
	RecommendationID string     `json:"recommendationID"`
	Reason           string     `json:"reason"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	// OverrideReadiness (admin only) allows auto-patch on clusters that are not AutomationReady (D-025).
	OverrideReadiness bool `json:"overrideReadiness,omitempty"`
}

// Approval is an approve-once grant.
type Approval struct {
	ID               string     `json:"id"`
	WorkloadID       string     `json:"workloadId"`
	RecommendationID string     `json:"recommendationID"`
	ApprovedBy       Actor      `json:"approvedBy"`
	ApprovedAt       time.Time  `json:"approvedAt"`
	ExpiresAt        time.Time  `json:"expiresAt"`
	Reason           string     `json:"reason"`
	State            string     `json:"state"`
	ConsumedAt       *time.Time `json:"consumedAt,omitempty"`
	ConsumedEventID  string     `json:"consumedEventId,omitempty"`
}

// WorkloadOverrideInput is the body of PUT /v1/workloads/{id}/override.
type WorkloadOverrideInput struct {
	Description string                         `json:"description,omitempty"`
	Spec        v1alpha1.RightsizingPolicySpec `json:"spec"`
	Reason      string                         `json:"reason"`
	// OverrideReadiness (admin only) allows auto-patch on clusters that are not AutomationReady (D-025).
	OverrideReadiness bool `json:"overrideReadiness,omitempty"`
}

// PolicyLayer is one layer of the precedence chain.
type PolicyLayer struct {
	Source   string                          `json:"source"`
	Name     string                          `json:"name,omitempty"`
	Origin   string                          `json:"origin,omitempty"`
	PolicyID string                          `json:"policyId,omitempty"`
	Priority *int32                          `json:"priority,omitempty"`
	Applied  bool                            `json:"applied"`
	Note     string                          `json:"note,omitempty"`
	Spec     *v1alpha1.RightsizingPolicySpec `json:"spec,omitempty"`
}

// EffectiveField is one resolved field and the layer that set it.
type EffectiveField struct {
	Path   string `json:"path"`
	Value  string `json:"value"`
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
}

// EffectivePolicy is the response of GET /v1/workloads/{id}/effective-policy.
type EffectivePolicy struct {
	WorkloadID  string           `json:"workloadId"`
	Layers      []PolicyLayer    `json:"layers"`
	Fields      []EffectiveField `json:"fields"`
	Modes       EffectiveModes   `json:"modes"`
	AgentModes  *EffectiveModes  `json:"agentModes,omitempty"`
	AgentPolicy string           `json:"agentPolicy,omitempty"`
	Warnings    []string         `json:"warnings,omitempty"`
}

// ConfigTarget is what a configuration change changed.
type ConfigTarget struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Cluster    string `json:"cluster,omitempty"`
	WorkloadID string `json:"workloadId,omitempty"`
}

// ConfigChange is one configuration change (audit type config_changed).
type ConfigChange struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Time    time.Time       `json:"time"`
	Actor   Actor           `json:"actor"`
	Action  string          `json:"action"`
	Target  ConfigTarget    `json:"target"`
	Reason  string          `json:"reason"`
	Version int             `json:"version,omitempty"`
	Before  json.RawMessage `json:"before,omitempty"`
	After   json.RawMessage `json:"after,omitempty"`
}

// ConfigChangeList is the response of GET /v1/audit/config.
type ConfigChangeList struct {
	Items    []ConfigChange `json:"items"`
	Continue string         `json:"continue,omitempty"`
}
