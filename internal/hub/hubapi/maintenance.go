package hubapi

import "time"

// Admin -> Maintenance: component versions and the check for a newer Kerf release.

// Component names (ComponentVersion.component, AgentComponent.component).
const (
	ComponentHub     = "hub"
	ComponentPricing = "pricing"
	ComponentAgent   = "agent"
	ComponentWebhook = "webhook"
)

// ComponentNames lists ComponentName.
var ComponentNames = []string{ComponentHub, ComponentPricing, ComponentAgent, ComponentWebhook}

// AgentComponentNames lists AgentComponent.component.
var AgentComponentNames = []string{ComponentWebhook}

// Version compatibility with the hub (VersionCompat).
const (
	CompatSame    = "same"
	CompatOlder   = "older"
	CompatNewer   = "newer"
	CompatUnknown = "unknown"
)

// VersionCompats lists VersionCompat.
var VersionCompats = []string{CompatSame, CompatOlder, CompatNewer, CompatUnknown}

// Update states (UpdateState).
const (
	UpdateUpToDate  = "up_to_date"
	UpdateAvailable = "update_available"
	UpdateDisabled  = "disabled"
	UpdateFailed    = "failed"
	UpdatePending   = "pending"
	UpdateDevBuild  = "dev_build"
)

// UpdateStates lists UpdateState.
var UpdateStates = []string{UpdateUpToDate, UpdateAvailable, UpdateDisabled, UpdateFailed, UpdatePending, UpdateDevBuild}

// CodeUpdateCheckDisabled answers POST /v1/admin/maintenance/update-check when the check is off.
const CodeUpdateCheckDisabled = "update_check_disabled"

// AgentComponent is a Kerf component an agent found in its cluster (the webhook pods).
type AgentComponent struct {
	Component string `json:"component"`
	Instance  string `json:"instance"`
	Version   string `json:"version"`
}

// ComponentVersion is one component instance on the Maintenance page.
type ComponentVersion struct {
	Component   string     `json:"component"`
	Cluster     string     `json:"cluster,omitempty"`
	Instance    string     `json:"instance,omitempty"`
	Version     string     `json:"version"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	LastSeenAt  *time.Time `json:"lastSeenAt,omitempty"`
	Leader      bool       `json:"leader,omitempty"`
	AgentStatus string     `json:"agentStatus,omitempty"`
	Compat      string     `json:"compat"`
	Message     string     `json:"message,omitempty"`
}

// UpdateStatus is the result of the update check.
type UpdateStatus struct {
	Enabled        bool       `json:"enabled"`
	State          string     `json:"state"`
	CurrentVersion string     `json:"currentVersion"`
	LatestVersion  string     `json:"latestVersion,omitempty"`
	ReleaseName    string     `json:"releaseName,omitempty"`
	ReleaseURL     string     `json:"releaseURL,omitempty"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	CheckedAt      *time.Time `json:"checkedAt,omitempty"`
	LastSuccessAt  *time.Time `json:"lastSuccessAt,omitempty"`
	Error          string     `json:"error,omitempty"`
	Interval       string     `json:"interval"`
	Source         string     `json:"source"`
}

// UpgradeCommands are the helm upgrade commands to the latest version.
type UpgradeCommands struct {
	Version      string `json:"version"`
	Chart        string `json:"chart"`
	HubCommand   string `json:"hubCommand"`
	SpokeCommand string `json:"spokeCommand"`
	DocsURL      string `json:"docsURL"`
}

// Maintenance is the response of GET /v1/admin/maintenance.
type Maintenance struct {
	HubVersion   string             `json:"hubVersion"`
	ChartVersion string             `json:"chartVersion,omitempty"`
	Components   []ComponentVersion `json:"components"`
	Update       UpdateStatus       `json:"update"`
	Upgrade      *UpgradeCommands   `json:"upgrade,omitempty"`
}
