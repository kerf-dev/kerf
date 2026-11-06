package hub

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/hub/teams"
)

// Teams (docs/teams.md): admins define them; everyone sees the teams in their scope.

// teamOf is the API team id of a workload view (hubapi.TeamUnassigned for none).
func teamOf(v *store.WorkloadView) string { return cmp.Or(v.Team, hubapi.TeamUnassigned) }

// teamCounts groups the caller's workloads by team (hubapi.TeamUnassigned for none).
func (s *Server) teamCounts(r *http.Request) (map[string]*hubapi.TeamCounts, error) {
	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{})
	if err != nil {
		return nil, err
	}
	byTeam := map[string][]*store.WorkloadView{}
	for _, v := range views {
		byTeam[teamOf(v)] = append(byTeam[teamOf(v)], v)
	}
	out := map[string]*hubapi.TeamCounts{}
	for t, vs := range byTeam {
		out[t] = &hubapi.TeamCounts{Workloads: len(vs), MonthlySavingsUSD: clusterSavings(vs)}
	}
	return out, nil
}

func withCounts(t *hubapi.Team, counts map[string]*hubapi.TeamCounts) {
	t.MonthlySavingsUSD = hubapi.Savings{}
	if c := counts[t.ID]; c != nil {
		t.Workloads, t.MonthlySavingsUSD = c.Workloads, c.MonthlySavingsUSD
	}
}

func (s *Server) ownerLabel() string { return cmp.Or(s.Store.OwnerLabel, teams.DefaultOwnerLabel) }

// listTeams serves GET /v1/admin/teams.
func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListTeams(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	counts, err := s.teamCounts(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range items {
		withCounts(&items[i], counts)
	}
	out := hubapi.TeamList{Items: items, OwnerLabel: s.ownerLabel()}
	if PrincipalFrom(r.Context()).Scope.All() {
		out.Unassigned = &hubapi.TeamCounts{}
		if c := counts[hubapi.TeamUnassigned]; c != nil {
			out.Unassigned = c
		}
	}
	writeJSON(w, out)
}

// getTeam serves GET /v1/admin/teams/{teamId}.
func (s *Server) getTeam(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTeam(r.Context(), r.PathValue("teamId"))
	if err != nil {
		s.storeError(w, r, err, "team")
		return
	}
	counts, err := s.teamCounts(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	withCounts(&t, counts)
	setETag(w, t.Version)
	writeJSON(w, t)
}

// validTeamInput answers 400 for an invalid definition.
func validTeamInput(w http.ResponseWriter, r *http.Request, in *hubapi.TeamInput) bool {
	bad := func(code, msg string) bool {
		badRequest(w, r, code, msg)
		return false
	}
	if utf8.RuneCountInString(in.DisplayName) > 128 {
		return bad("invalid_display_name", "displayName has more than 128 characters.")
	}
	if utf8.RuneCountInString(in.Description) > 1024 {
		return bad("invalid_description", "description has more than 1024 characters.")
	}
	for _, list := range [][]string{in.Namespaces, in.Clusters} {
		if len(list) > 100 {
			return bad("invalid_selector", "At most 100 namespace and cluster patterns.")
		}
		for _, p := range list {
			if !teams.ValidGlob(p) || len(p) > 253 {
				return bad("invalid_selector", "Invalid pattern "+p+" (a name or a glob such as payments-*).")
			}
		}
	}
	if len(in.NamespaceLabels) > 20 {
		return bad("invalid_selector", "At most 20 namespace labels.")
	}
	for k, v := range in.NamespaceLabels {
		if k == "" || len(k) > 317 || len(v) > 63 {
			return bad("invalid_selector", "Invalid namespace label "+k+".")
		}
	}
	if len(in.OwnerValues) > 50 {
		return bad("invalid_owner_values", "At most 50 owner label values.")
	}
	for _, v := range in.OwnerValues {
		if v == "" || len(v) > 63 {
			return bad("invalid_owner_values", "Owner label values have 1-63 characters.")
		}
	}
	in.DisplayName, in.Description = strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.Description)
	slices.Sort(in.Namespaces)
	in.Namespaces = slices.Compact(in.Namespaces)
	slices.Sort(in.Clusters)
	in.Clusters = slices.Compact(in.Clusters)
	slices.Sort(in.OwnerValues)
	in.OwnerValues = slices.Compact(in.OwnerValues)
	return true
}

// createTeam serves POST /v1/admin/teams.
func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	var in hubapi.TeamInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if !teams.ValidID(in.ID) {
		badRequest(w, r, "invalid_team_id", "id must be a DNS label (lowercase letters, digits, -; at most 63) other than unassigned.")
		return
	}
	if !validTeamInput(w, r, &in) {
		return
	}
	t, err := s.Store.CreateTeam(r.Context(), in, s.change(r, hubapi.ActionTeamCreated, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "team")
		return
	}
	counts, err := s.teamCounts(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	withCounts(&t, counts)
	setETag(w, t.Version)
	writeJSONStatus(w, http.StatusCreated, t)
}

// updateTeam serves PUT /v1/admin/teams/{teamId}.
func (s *Server) updateTeam(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.TeamInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if in.ID != "" && in.ID != r.PathValue("teamId") {
		badRequest(w, r, "invalid_team_id", "A team's id cannot change.")
		return
	}
	if !validTeamInput(w, r, &in) {
		return
	}
	t, err := s.Store.UpdateTeam(r.Context(), r.PathValue("teamId"), ver, in, s.change(r, hubapi.ActionTeamUpdated, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "team")
		return
	}
	counts, err := s.teamCounts(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	withCounts(&t, counts)
	setETag(w, t.Version)
	writeJSON(w, t)
}

// deleteTeam serves DELETE /v1/admin/teams/{teamId}.
func (s *Server) deleteTeam(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if err := s.Store.DeleteTeam(r.Context(), r.PathValue("teamId"), ver, s.change(r, hubapi.ActionTeamDeleted, in.Reason)); err != nil {
		s.storeError(w, r, err, "team")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validBindings checks a user's scope and team roles (and that the teams exist); it normalises
// scope (default fleet). Admins are fleet-wide; a team-scoped user needs at least one team.
func (s *Server) validBindings(w http.ResponseWriter, r *http.Request, role string, scope *string, roles []hubapi.TeamRole) bool {
	if *scope == "" {
		*scope = hubapi.ScopeFleet
	}
	if !validEnum(w, r, "scope", *scope, hubapi.UserScopes...) {
		return false
	}
	bs := make([]teams.Binding, 0, len(roles))
	ids := make([]string, 0, len(roles))
	for _, b := range roles {
		bs = append(bs, teams.Binding{Team: b.Team, Role: b.Role})
		ids = append(ids, b.Team)
	}
	if err := teams.ValidateBindings(bs); err != nil {
		badRequest(w, r, "invalid_team_roles", err.Error()+".")
		return false
	}
	if *scope == hubapi.ScopeTeams && (role == hubapi.UserRoleAdmin || len(roles) == 0) {
		badRequest(w, r, "invalid_team_roles", "A team-scoped user needs at least one team role and cannot be an admin.")
		return false
	}
	if role == hubapi.UserRoleAdmin && len(roles) > 0 {
		badRequest(w, r, "invalid_team_roles", "An admin is fleet-wide: it has no team roles.")
		return false
	}
	unknown, err := s.Store.UnknownTeams(r.Context(), ids)
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if len(unknown) > 0 {
		badRequest(w, r, "unknown_team", "Unknown team "+strings.Join(unknown, ", ")+".")
		return false
	}
	return true
}

// validTeamFilter answers 400 for a team filter value that is not a team id or unassigned.
func validTeamFilter(w http.ResponseWriter, r *http.Request, ids []string) bool {
	for _, t := range ids {
		if !hubapi.TeamIDRE.MatchString(t) {
			badRequest(w, r, "invalid_team", "Invalid team "+t+".")
			return false
		}
	}
	return true
}
