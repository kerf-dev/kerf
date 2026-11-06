//go:build e2e

package e2e

// Team-scoped access (docs/teams.md): a user bound to one team as operator sees only that team's
// workloads in every list and total, can approve them, gets 403 on another team's workload, and
// the audit log records the team that allowed the approval.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	v1alpha1 "github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestTeamScopedAccess(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	// Team A owns its namespace by selector; team B owns its workload by the owner label (reported
	// by the agent). The namespace names are DNS labels: they serve as the team ids.
	nsA := newNamespace(t, cl, "teama")
	nsB := newNamespace(t, cl, "teamb")
	deploy(t, cl, nsA, small("web"))
	b := small("web")
	b.Labels = map[string]string{"team": nsB}
	deploy(t, cl, nsB, b)
	for _, ns := range []string{nsA, nsB} {
		policy(t, cl, ns, ns, cpuAuditOnly(v1alpha1.StrategyInPlace))
	}
	for _, ns := range []string{nsA, nsB} {
		waitHubRecommendation(t, cl, ns, deployment, "web", nil)
	}

	adm := as(t, admin)
	for _, in := range []hubapi.TeamInput{
		{ID: nsA, DisplayName: "Team A", Namespaces: []string{nsA}, Clusters: []string{cl.Name}, Reason: reason(t, "team A")},
		{ID: nsB, DisplayName: "Team B", Reason: reason(t, "team B")},
	} {
		adm.call(t, http.MethodPost, "/v1/admin/teams", in, nil, http.StatusCreated, nil)
		id := in.ID
		t.Cleanup(func() { deleteTeamQuiet(id, "e2e cleanup") })
	}
	eventually(t, 3*time.Minute, 5*time.Second, "teams resolved", func() (bool, string) {
		var a, bd hubapi.WorkloadDetail
		adm.get(t, workloadPath(cl, nsA, "web"), &a)
		adm.get(t, workloadPath(cl, nsB, "web"), &bd)
		return a.Team == nsA && bd.Team == nsB, fmt.Sprintf("team of A's workload %q, of B's %q", a.Team, bd.Team)
	})

	// A local user bound to team A as operator, and nothing else.
	user := "e2e-teamop-" + randSuffix()
	password := "e2e-" + randSuffix() + "-" + randSuffix()
	f := false
	adm.call(t, http.MethodPost, "/v1/admin/users", hubapi.UserCreate{Username: user, Role: viewer, Password: password,
		MustChangePassword: &f, Scope: hubapi.ScopeTeams, TeamRoles: []hubapi.TeamRole{{Team: nsA, Role: operator}},
		Reason: reason(t, "team-scoped operator")}, nil, http.StatusCreated, nil)
	t.Cleanup(func() { deleteUserQuiet(user, "e2e cleanup") })
	s, err := login(user, password)
	must(t, err)
	// Team-scoped: no fleet role; the effective role is the highest team role.
	if s.Me.Role != operator || s.Me.Scope != hubapi.ScopeTeams || s.Me.User == nil || s.Me.User.Role != operator ||
		!slices.Equal(s.Me.TeamRoles, []hubapi.TeamRole{{Team: nsA, Role: operator}}) {
		t.Errorf("login of the team-scoped operator: %+v", s.Me)
	}
	t.Cleanup(func() { _, _ = s.raw(context.Background(), http.MethodPost, "/v1/auth/logout", nil, nil) })

	// Lists, counts and totals hold team A only.
	var l hubapi.WorkloadList
	s.get(t, "/v1/workloads?limit=500", &l)
	for _, w := range l.Items {
		if w.Team != nsA {
			t.Errorf("team-scoped user sees %s (team %q)", w.ID, w.Team)
		}
	}
	if l.Total != len(l.Items) || !slices.ContainsFunc(l.Items, func(w hubapi.WorkloadRow) bool { return w.ID == workloadID(cl, nsA, "web") }) {
		t.Errorf("workloads: total %d, items %d, team A's workload missing", l.Total, len(l.Items))
	}
	var sum hubapi.FleetSummary
	s.get(t, "/v1/summary", &sum)
	if sum.Fleet.Workloads != l.Total || len(sum.Teams) != 1 || sum.Teams[0].Team != nsA {
		t.Errorf("summary: %d workloads (list %d), teams %+v", sum.Fleet.Workloads, l.Total, sum.Teams)
	}
	// Another team's workload: not found to read, forbidden to approve.
	if r := s.do(t, http.MethodGet, workloadPath(cl, nsB, "web"), nil, nil); r.Status != http.StatusNotFound {
		t.Errorf("GET team B's workload: %s", r)
	}
	var d hubapi.WorkloadDetail
	adm.get(t, workloadPath(cl, nsB, "web"), &d)
	r := s.do(t, http.MethodPost, workloadPath(cl, nsB, "web")+"/approve",
		hubapi.ApprovalRequest{RecommendationID: d.Recommendation.ID, Reason: reason(t, "not my team")}, nil)
	if r.Status != http.StatusForbidden || r.Problem() != hubapi.CodeScopeForbidden {
		t.Errorf("approve team B's workload: %s", r)
	}
	// Fleet-wide endpoints are forbidden.
	if r := s.do(t, http.MethodGet, "/v1/admin/policies", nil, nil); r.Status != http.StatusForbidden {
		t.Errorf("GET /v1/admin/policies: %s", r)
	}

	// Its own team's workload: approved, and the audit log records the scope used.
	sessionsMu.Lock()
	sessions[user] = s
	sessionsMu.Unlock()
	t.Cleanup(func() {
		sessionsMu.Lock()
		delete(sessions, user)
		sessionsMu.Unlock()
	})
	approve(t, user, cl, nsA, "web")
	c := waitConfigChange(t, url.Values{"workloadId": {workloadID(cl, nsA, "web")}}, hubapi.ActionWorkloadApproved, nil)
	if c.Actor.Name != user || !slices.Equal(c.Actor.Teams, []string{nsA}) {
		t.Errorf("approval actor %+v, want %s with teams [%s]", c.Actor, user, nsA)
	}
}

// deleteTeamQuiet deletes a team as the admin, ignoring errors.
func deleteTeamQuiet(id, why string) {
	s := sessionOf(admin)
	if s == nil {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := s.raw(c, http.MethodGet, "/v1/admin/teams/"+url.PathEscape(id), nil, nil)
	if err != nil || r.Status != http.StatusOK {
		return
	}
	_, _ = s.raw(c, http.MethodDelete, "/v1/admin/teams/"+url.PathEscape(id), hubapi.ChangeReason{Reason: why}, hdr{"If-Match": r.Header.Get("ETag")})
}

// deleteUserQuiet deletes a user as the admin, ignoring errors.
func deleteUserQuiet(name, why string) {
	s := sessionOf(admin)
	if s == nil {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := s.raw(c, http.MethodGet, "/v1/admin/users/"+url.PathEscape(name), nil, nil)
	if err != nil || r.Status != http.StatusOK {
		return
	}
	_, _ = s.raw(c, http.MethodDelete, "/v1/admin/users/"+url.PathEscape(name), hubapi.ChangeReason{Reason: why}, hdr{"If-Match": r.Header.Get("ETag")})
}
