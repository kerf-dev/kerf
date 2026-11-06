package hubapi

import (
	"regexp"
	"time"
)

// TeamUnassigned is the team id of workloads in no team (filters, facets, groupings).
const TeamUnassigned = "unassigned"

// TeamIDRE is the form of a team id (TeamID): a DNS label.
var TeamIDRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// User scopes (UserScope).
const (
	ScopeFleet = "fleet"
	ScopeTeams = "teams"
)

// UserScopes lists the UserScope enum.
var UserScopes = []string{ScopeFleet, ScopeTeams}

// TeamRole binds a user to a team with a role (viewer or operator).
type TeamRole struct {
	Team string `json:"team"`
	Role string `json:"role"`
}

// TeamInput is the body of POST /v1/admin/teams (with id) and PUT /v1/admin/teams/{teamId}.
type TeamInput struct {
	ID              string            `json:"id,omitempty"`
	DisplayName     string            `json:"displayName,omitempty"`
	Description     string            `json:"description,omitempty"`
	Namespaces      []string          `json:"namespaces,omitempty"`
	NamespaceLabels map[string]string `json:"namespaceLabels,omitempty"`
	Clusters        []string          `json:"clusters,omitempty"`
	OwnerValues     []string          `json:"ownerValues,omitempty"`
	Reason          string            `json:"reason"`
}

// Team is a team with what it owns now.
type Team struct {
	ID              string            `json:"id"`
	DisplayName     string            `json:"displayName"`
	Description     string            `json:"description,omitempty"`
	Namespaces      []string          `json:"namespaces"`
	NamespaceLabels map[string]string `json:"namespaceLabels"`
	Clusters        []string          `json:"clusters"`
	OwnerValues     []string          `json:"ownerValues"`
	Version         int               `json:"version"`
	CreatedAt       time.Time         `json:"createdAt"`
	CreatedBy       string            `json:"createdBy,omitempty"`
	UpdatedAt       *time.Time        `json:"updatedAt,omitempty"`
	// Workloads and MonthlySavingsUSD are the team's live workloads and their estimated savings now.
	Workloads         int     `json:"workloads"`
	MonthlySavingsUSD Savings `json:"monthlySavingsUSD"`
}

// TeamList is the response of GET /v1/admin/teams.
type TeamList struct {
	Items []Team `json:"items"`
	// OwnerLabel is the label key on workloads and namespaces that names their team.
	OwnerLabel string `json:"ownerLabel"`
	// Unassigned counts the workloads in no team; absent for team-scoped callers.
	Unassigned *TeamCounts `json:"unassigned,omitempty"`
}

// TeamCounts are a group's live workloads and estimated savings.
type TeamCounts struct {
	Workloads         int     `json:"workloads"`
	MonthlySavingsUSD Savings `json:"monthlySavingsUSD"`
}

// TeamSummary is GET /v1/summary per team (team "unassigned" = no team).
type TeamSummary struct {
	Summary
	Team        string `json:"team"`
	DisplayName string `json:"displayName"`
}
