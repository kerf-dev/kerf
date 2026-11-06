package teams

import (
	"context"
	"fmt"
	"maps"
	"slices"
)

// Roles, lowest first (hubapi.UserRoles; repeated to keep this package free of the API types).
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

var rank = map[string]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// AtLeast reports whether role has at least the rights of min.
func AtLeast(role, min string) bool { return rank[role] > 0 && rank[role] >= rank[min] }

// Max returns the higher of two roles ("" is lowest).
func Max(a, b string) string {
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Scope is what a caller may see and do: a role over the whole fleet and/or roles on teams.
type Scope struct {
	// FleetRole applies to every workload; "" = none (the caller sees only its teams).
	FleetRole string
	// TeamRoles are roles on teams (team id -> viewer or operator).
	TeamRoles map[string]string
}

// Fleet is the scope of a fleet-wide role (bearer tokens, users without team bindings).
func Fleet(role string) Scope { return Scope{FleetRole: role} }

// All reports whether the scope covers every workload, Unassigned included.
func (s Scope) All() bool { return s.FleetRole != "" }

// Teams returns the team ids the scope names, sorted (the visible teams when !All).
func (s Scope) Teams() []string { return slices.Sorted(maps.Keys(s.TeamRoles)) }

// CanSee reports whether a workload of team ("" = Unassigned) is visible.
func (s Scope) CanSee(team string) bool {
	if s.All() {
		return true
	}
	_, ok := s.TeamRoles[team]
	return ok && team != ""
}

// RoleFor returns the caller's role on workloads of team ("" = Unassigned).
func (s Scope) RoleFor(team string) string {
	if team == "" {
		return s.FleetRole
	}
	return Max(s.FleetRole, s.TeamRoles[team])
}

// MaxRole returns the caller's highest role anywhere.
func (s Scope) MaxRole() string {
	r := s.FleetRole
	for _, tr := range s.TeamRoles {
		r = Max(r, tr)
	}
	return r
}

// Grants reports whether the caller has at least min on team, and whether a team binding (not the
// fleet role) is what grants it: the scope used, which the audit log records.
func (s Scope) Grants(team, min string) (ok, byTeam bool) {
	if AtLeast(s.FleetRole, min) {
		return true, false
	}
	if team != "" && AtLeast(s.TeamRoles[team], min) {
		return true, true
	}
	return false, false
}

// Binding is one team role of a user.
type Binding struct {
	Team string `json:"team"`
	Role string `json:"role"`
}

// UserScope builds the scope of a user: scope "teams" has no fleet role.
func UserScope(role string, teamsOnly bool, bindings []Binding) Scope {
	s := Scope{TeamRoles: map[string]string{}}
	if !teamsOnly {
		s.FleetRole = role
	}
	for _, b := range bindings {
		s.TeamRoles[b.Team] = Max(s.TeamRoles[b.Team], b.Role)
	}
	return s
}

// ValidateBindings checks team roles: a valid team id, viewer or operator, each team once.
func ValidateBindings(bs []Binding) error {
	seen := map[string]bool{}
	for _, b := range bs {
		switch {
		case !ValidID(b.Team):
			return fmt.Errorf("invalid team id %q", b.Team)
		case b.Role != RoleViewer && b.Role != RoleOperator:
			return fmt.Errorf("team %s: role must be viewer or operator (admin is always fleet-wide)", b.Team)
		case seen[b.Team]:
			return fmt.Errorf("team %s is bound twice", b.Team)
		}
		seen[b.Team] = true
	}
	return nil
}

type ctxKey struct{}

// WithScope returns ctx carrying the caller's scope. The store filters every read by it.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the caller's scope. ok is false for the hub's own work (recommender runs,
// notifications), which is not restricted.
func FromContext(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(ctxKey{}).(Scope)
	return s, ok
}

// Restriction returns the teams a read is restricted to: restricted is false when every workload
// is visible. A restricted read with no teams sees nothing.
func Restriction(ctx context.Context) (teamIDs []string, restricted bool) {
	s, ok := FromContext(ctx)
	if !ok || s.All() {
		return nil, false
	}
	ids := s.Teams()
	if ids == nil {
		ids = []string{}
	}
	return ids, true
}

// SQL returns a condition restricting column col (a team id, NULL = Unassigned) to the caller's
// teams, appending the argument to args; "true" when not restricted.
func SQL(ctx context.Context, col string, args *[]any) string {
	ids, restricted := Restriction(ctx)
	if !restricted {
		return "true"
	}
	*args = append(*args, ids)
	return fmt.Sprintf("%s = ANY($%d::text[])", col, len(*args))
}
