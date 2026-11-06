package hub

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// teamEnv is the test hub with teams:
//
//	pay: namespace payments on c1 (checkout), plus legacy (its namespace's owner label says pay)
//	web: owner label only (frontend; ledger, whose own label wins over pay's selector)
//	unassigned: report (c1/batch), api (c2/payments, outside pay's cluster restriction)
//
// and users payop (operator on pay only), payview (viewer on pay only) and webop (fleet viewer,
// operator on web).
type teamEnv struct {
	*testEnv
	payop, payview, webop, admin session
}

var (
	payWorkloads   = []hubapi.WorkloadKey{kCheckout, kLegacy}
	otherWorkloads = []hubapi.WorkloadKey{kFrontend, kLedger, kReport, kAPI}
)

func newTeamEnv(t *testing.T) *teamEnv {
	t.Helper()
	e := newEnv(t)
	e.seedUsers()
	// Labels as the agent reports them.
	var res hubapi.BatchResult
	st := func(k hubapi.WorkloadKey, labels, ns map[string]string) hubapi.WorkloadStatus {
		return hubapi.WorkloadStatus{Workload: k.Ref(), Phase: "Pending", Labels: labels, NamespaceLabels: ns,
			Modes: hubapi.EffectiveModes{CPU: hubapi.ModeAudit, Memory: hubapi.ModeAudit, CPUSource: "global-default", MemorySource: "global-default"}}
	}
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{
		st(kFrontend, map[string]string{"team": "web", "app": "frontend"}, map[string]string{}),
		st(kLedger, map[string]string{"team": "web"}, map[string]string{}),
		st(kLegacy, map[string]string{"app": "legacy"}, map[string]string{"team": "pay"}),
	}}, &res)
	for _, in := range []hubapi.TeamInput{
		{ID: "pay", DisplayName: "Payments", Namespaces: []string{"payments"}, Clusters: []string{"c1"}, Reason: "teams"},
		{ID: "web", Reason: "teams"},
	} {
		expectCode(t, "create team "+in.ID, e.do(http.MethodPost, "/v1/admin/teams", tokAdmin, in), 201)
	}
	f := false
	for _, u := range []hubapi.UserCreate{
		{Username: "payop", Role: hubapi.UserRoleViewer, Scope: hubapi.ScopeTeams, TeamRoles: []hubapi.TeamRole{{Team: "pay", Role: "operator"}}},
		{Username: "payview", Role: hubapi.UserRoleViewer, Scope: hubapi.ScopeTeams, TeamRoles: []hubapi.TeamRole{{Team: "pay", Role: "viewer"}}},
		{Username: "webop", Role: hubapi.UserRoleViewer, TeamRoles: []hubapi.TeamRole{{Team: "web", Role: "operator"}}},
	} {
		u.Password, u.MustChangePassword, u.Reason = testPassword, &f, "teams"
		expectCode(t, "create user "+u.Username, e.do(http.MethodPost, "/v1/admin/users", tokAdmin, u), 201)
	}
	te := &teamEnv{testEnv: e}
	te.payop, _ = e.login("payop", testPassword)
	te.payview, _ = e.login("payview", testPassword)
	te.webop, _ = e.login("webop", testPassword)
	te.admin, _ = e.login("admin", testPassword)
	return te
}

func TestTeamAssignment(t *testing.T) {
	e := newTeamEnv(t)
	var l hubapi.WorkloadList
	e.getJSON("/v1/workloads?limit=500", tokUI, &l)
	got := map[string]string{}
	for _, r := range l.Items {
		got[r.Name] = r.Team
	}
	want := map[string]string{"checkout": "pay", "legacy": "pay", "frontend": "web", "ledger": "web", "report": "unassigned", "api": "unassigned"}
	for name, team := range want {
		if got[name] != team {
			t.Errorf("%s: team %q, want %q", name, got[name], team)
		}
	}
	// Filter and facets.
	e.getJSON("/v1/workloads?team=unassigned", tokUI, &l)
	if l.Total != 2 {
		t.Errorf("team=unassigned: total %d", l.Total)
	}
	var f hubapi.WorkloadFacets
	e.getJSON("/v1/workloads/facets", tokUI, &f)
	if !slices.Equal(f.Teams, []hubapi.FacetCount{{Value: "pay", Count: 2}, {Value: "unassigned", Count: 2}, {Value: "web", Count: 2}}) {
		t.Errorf("team facet: %+v", f.Teams)
	}
	e.problem(e.do(http.MethodGet, "/v1/workloads?team=Bad_Team", tokUI, nil), 400, "invalid_team")
	// Summary by team; the teams' totals add up to the fleet's workloads.
	var sum hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &sum)
	n := 0
	var ids []string
	for _, ts := range sum.Teams {
		n += ts.Workloads
		ids = append(ids, ts.Team)
	}
	if n != sum.Fleet.Workloads || !slices.Equal(ids, []string{"pay", "web", "unassigned"}) {
		t.Errorf("summary teams %v (%d workloads, fleet %d)", ids, n, sum.Fleet.Workloads)
	}
	// Deleting web moves its workloads: ledger back to pay's selector, frontend to Unassigned.
	var team hubapi.Team
	r := e.getJSON("/v1/admin/teams/web", tokUI, &team)
	expectCode(t, "delete web", e.do(http.MethodDelete, "/v1/admin/teams/web", tokAdmin, hubapi.ChangeReason{Reason: "gone"},
		"If-Match", r.header.Get("ETag")), 204)
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kLedger, ""), tokUI, &d)
	if d.Team != "pay" {
		t.Errorf("ledger after deleting web: %q", d.Team)
	}
	e.getJSON(wlPath(kFrontend, ""), tokUI, &d)
	if d.Team != "unassigned" {
		t.Errorf("frontend after deleting web: %q", d.Team)
	}
	// The binding to web is gone.
	var u hubapi.User
	e.getJSON("/v1/admin/users/webop", tokAdmin, &u)
	if len(u.TeamRoles) != 0 {
		t.Errorf("webop roles after deleting web: %+v", u.TeamRoles)
	}
}

// TestTeamScopedReads is the companion of TestRouteInventory: every route a team-scoped user may
// call is called by a viewer of team pay, and no response may contain another team's workload.
func TestTeamScopedReads(t *testing.T) {
	e := newTeamEnv(t)
	// Some history on other teams' workloads, so lists have something to leak.
	for _, k := range []hubapi.WorkloadKey{kFrontend, kCheckout} {
		var d hubapi.WorkloadDetail
		e.getJSON(wlPath(k, ""), tokUI, &d)
		expectCode(t, "approve "+k.Name, e.sdo(e.admin, http.MethodPost, wlPath(k, "/approve"),
			hubapi.ApprovalRequest{RecommendationID: d.Recommendation.ID, Reason: "history"}), 201)
	}
	foreign := func(body string) []string {
		var out []string
		for _, k := range otherWorkloads {
			if strings.Contains(body, `"`+k.Name+`"`) || strings.Contains(body, k.ID()) {
				out = append(out, k.Name)
			}
		}
		if strings.Contains(body, `"team":"web"`) || strings.Contains(body, `"team":"unassigned"`) {
			out = append(out, "a foreign team")
		}
		return out
	}
	covered := 0
	for _, rt := range e.srv.apiRoutes() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		if rt.access == nil || !rt.access.teams || method != http.MethodGet || rt.access.session == "" {
			continue
		}
		covered++
		t.Run(rt.pattern, func(t *testing.T) {
			own := pathParam.ReplaceAllStringFunc(path, func(p string) string {
				switch p {
				case "{id}":
					return kCheckout.EscapedID()
				case "{teamId}":
					return "pay"
				}
				return "x"
			})
			for _, q := range []string{"", "?limit=500", "?groupBy=team", "?groupBy=cluster", "?team=web", "?team=unassigned"} {
				r := e.sdo(e.payview, method, own+q, nil)
				if r.code >= 500 || r.code == http.StatusForbidden {
					t.Errorf("GET %s%s: %d %s", own, q, r.code, r.body)
				}
				if f := foreign(string(r.body)); len(f) > 0 {
					t.Errorf("GET %s%s leaks %v: %s", own, q, f, r.body)
				}
			}
			if !strings.Contains(path, "{id}") && !strings.Contains(path, "{teamId}") {
				return
			}
			// Another team's workload or team is not found.
			other := pathParam.ReplaceAllStringFunc(path, func(p string) string {
				if p == "{id}" {
					return kFrontend.EscapedID()
				}
				return "web"
			})
			if r := e.sdo(e.payview, method, other, nil); r.code != http.StatusNotFound {
				t.Errorf("GET %s: %d, want 404: %s", other, r.code, r.body)
			}
		})
	}
	if covered < 15 {
		t.Errorf("only %d team-scoped GET routes covered", covered)
	}

	// Counts and totals are computed on the team's workloads only.
	var sum hubapi.FleetSummary
	mustJSON(t, e.sdo(e.payview, http.MethodGet, "/v1/summary", nil).body, &sum)
	if sum.Fleet.Workloads != len(payWorkloads) || len(sum.Clusters) != 1 || sum.Clusters[0].Workloads != len(payWorkloads) {
		t.Errorf("summary: fleet %d, clusters %+v", sum.Fleet.Workloads, sum.Clusters)
	}
	if len(sum.Teams) != 1 || sum.Teams[0].Team != "pay" {
		t.Errorf("summary teams: %+v", sum.Teams)
	}
	var full hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &full)
	if sum.Fleet.MonthlySavingsUSD.Total >= full.Fleet.MonthlySavingsUSD.Total {
		t.Errorf("team total %v not below the fleet's %v", sum.Fleet.MonthlySavingsUSD.Total, full.Fleet.MonthlySavingsUSD.Total)
	}
	var l hubapi.WorkloadList
	mustJSON(t, e.sdo(e.payview, http.MethodGet, "/v1/workloads", nil).body, &l)
	if l.Total != len(payWorkloads) {
		t.Errorf("workloads total %d", l.Total)
	}
	var cl hubapi.ClusterList
	mustJSON(t, e.sdo(e.payview, http.MethodGet, "/v1/clusters", nil).body, &cl)
	if len(cl.Items) != 1 || cl.Items[0].Name != "c1" || cl.Items[0].Counts.Workloads != len(payWorkloads) {
		t.Errorf("clusters: %+v", cl.Items)
	}
	var tl hubapi.TeamList
	mustJSON(t, e.sdo(e.payview, http.MethodGet, "/v1/admin/teams", nil).body, &tl)
	if len(tl.Items) != 1 || tl.Items[0].ID != "pay" || tl.Items[0].Workloads != 2 || tl.Unassigned != nil {
		t.Errorf("teams: %+v", tl)
	}
	// The trend of a team-scoped caller covers its teams (recorded by the runs since teams exist).
	e.run(e.now, false)
	var tr hubapi.SavingsTrend
	mustJSON(t, e.sdo(e.payview, http.MethodGet, "/v1/savings/trend?from="+url.QueryEscape(e.now.Add(-48*3600e9).Format("2006-01-02T15:04:05Z"))+"&groupBy=team", nil).body, &tr)
	for _, s := range tr.Series {
		if s.Team != "pay" {
			t.Errorf("trend series of team %q", s.Team)
		}
	}
	// Fleet-wide endpoints answer 403.
	for _, p := range []string{"/v1/admin/policies", "/v1/clusters/c1/capacity", "/v1/savings/realized/node-cost?cluster=c1", "/v1/admin/users"} {
		r := e.sdo(e.payview, http.MethodGet, p, nil)
		if r.code != http.StatusForbidden {
			t.Errorf("GET %s: %d, want 403", p, r.code)
		}
	}
	e.problem(e.sdo(e.payview, http.MethodGet, "/v1/admin/policies", nil), 403, hubapi.CodeScopeForbidden)
	// A fleet-scoped user sees everything.
	mustJSON(t, e.sdo(e.webop, http.MethodGet, "/v1/workloads", nil).body, &l)
	if l.Total != 6 {
		t.Errorf("fleet viewer: %d workloads", l.Total)
	}
	var me hubapi.Me
	mustJSON(t, e.sdo(e.webop, http.MethodGet, "/v1/me", nil).body, &me)
	if me.Role != hubapi.UserRoleOperator || me.Scope != hubapi.ScopeFleet || len(me.TeamRoles) != 1 {
		t.Errorf("me: %+v", me)
	}
}

func TestTeamScopedWrites(t *testing.T) {
	e := newTeamEnv(t)
	rec := func(k hubapi.WorkloadKey) string {
		var d hubapi.WorkloadDetail
		e.getJSON(wlPath(k, ""), tokUI, &d)
		return d.Recommendation.ID
	}
	approve := func(s session, k hubapi.WorkloadKey) resp {
		return e.sdo(s, http.MethodPost, wlPath(k, "/approve"), hubapi.ApprovalRequest{RecommendationID: rec(k), Reason: "ok"})
	}
	excl := hubapi.WorkloadOverrideInput{Reason: "x"}
	tests := []struct {
		name   string
		s      func() session
		method string
		k      hubapi.WorkloadKey
		status int
		code   string
	}{
		{"team operator approves own team", func() session { return e.payop }, "approve", kCheckout, 201, ""},
		{"team operator approves other team", func() session { return e.payop }, "approve", kFrontend, 403, hubapi.CodeScopeForbidden},
		{"team operator approves unassigned", func() session { return e.payop }, "approve", kAPI, 403, hubapi.CodeScopeForbidden},
		{"team operator overrides other team", func() session { return e.payop }, http.MethodPut, kLedger, 403, hubapi.CodeScopeForbidden},
		{"team operator clears other team's override", func() session { return e.payop }, http.MethodDelete, kLedger, 403, hubapi.CodeScopeForbidden},
		{"team viewer approves own team", func() session { return e.payview }, "approve", kCheckout, 403, "role_forbidden"},
		{"fleet viewer operates its team", func() session { return e.webop }, "approve", kFrontend, 201, ""},
		{"fleet viewer operates other team", func() session { return e.webop }, "approve", kCheckout, 403, hubapi.CodeScopeForbidden},
		{"fleet viewer operates unassigned", func() session { return e.webop }, http.MethodPut, kAPI, 403, hubapi.CodeScopeForbidden},
		{"admin anywhere", func() session { return e.admin }, "approve", kAPI, 201, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r resp
			switch tt.method {
			case "approve":
				r = approve(tt.s(), tt.k)
			case http.MethodPut:
				r = e.sdo(tt.s(), http.MethodPut, wlPath(tt.k, "/override"), excl)
			case http.MethodDelete:
				r = e.sdo(tt.s(), http.MethodDelete, wlPath(tt.k, "/override"), hubapi.ChangeReason{Reason: "x"}, ifMatchHdr(1)...)
			}
			if tt.code != "" {
				e.problem(r, tt.status, tt.code)
			} else {
				expectCode(t, tt.name, r, tt.status)
			}
		})
	}
	// Fleet-wide writes need a fleet-wide role.
	e.problem(e.sdo(e.payop, http.MethodPut, "/v1/clusters/c1/automation", hubapi.AutomationUpdate{Paused: true, Reason: "x"}), 403, hubapi.CodeScopeForbidden)
	e.problem(e.sdo(e.webop, http.MethodPut, "/v1/admin/automation", hubapi.AutomationUpdate{Paused: true, Reason: "x"}), 403, hubapi.CodeScopeForbidden)

	// The audit log records the scope used.
	var cl hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?action=WorkloadApproved&limit=100", tokAdmin, &cl)
	scopes := map[string][]string{}
	for _, c := range cl.Items {
		scopes[c.Actor.Name+" "+c.Target.WorkloadID] = c.Actor.Teams
	}
	for who, want := range map[string][]string{
		"payop " + kCheckout.ID(): {"pay"},
		"webop " + kFrontend.ID(): {"web"},
		"admin " + kAPI.ID():      nil,
	} {
		got, ok := scopes[who]
		if !ok || !slices.Equal(got, want) {
			t.Errorf("%s: actor teams %v (recorded %v), want %v", who, got, ok, want)
		}
	}
	// A team-scoped caller sees only its teams' changes.
	mustJSON(t, e.sdo(e.payop, http.MethodGet, "/v1/audit/config?limit=100", nil).body, &cl)
	for _, c := range cl.Items {
		if c.Target.WorkloadID != kCheckout.ID() && c.Target.WorkloadID != kLegacy.ID() {
			t.Errorf("payop sees change %s on %q", c.Action, c.Target.WorkloadID)
		}
	}
}

func TestTeamBindingsValidation(t *testing.T) {
	e := newTeamEnv(t)
	bad := []struct {
		name string
		u    hubapi.UserCreate
		code string
	}{
		{"admin on a team", hubapi.UserCreate{Role: hubapi.UserRoleAdmin, TeamRoles: []hubapi.TeamRole{{Team: "pay", Role: "viewer"}}}, "invalid_team_roles"},
		{"teams scope without teams", hubapi.UserCreate{Role: hubapi.UserRoleViewer, Scope: hubapi.ScopeTeams}, "invalid_team_roles"},
		{"unknown team", hubapi.UserCreate{Role: hubapi.UserRoleViewer, TeamRoles: []hubapi.TeamRole{{Team: "nope", Role: "viewer"}}}, "unknown_team"},
		{"admin team role", hubapi.UserCreate{Role: hubapi.UserRoleViewer, TeamRoles: []hubapi.TeamRole{{Team: "pay", Role: "admin"}}}, "invalid_team_roles"},
		{"bad scope", hubapi.UserCreate{Role: hubapi.UserRoleViewer, Scope: "world"}, "invalid_scope"},
	}
	for i, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			u := tt.u
			u.Username, u.Password, u.Reason = "bad"+itoa(i), testPassword, "x"
			e.problem(e.do(http.MethodPost, "/v1/admin/users", tokAdmin, u), 400, tt.code)
		})
	}
	e.problem(e.do(http.MethodPost, "/v1/admin/teams", tokAdmin, hubapi.TeamInput{ID: "unassigned", Reason: "x"}), 400, "invalid_team_id")
	e.problem(e.do(http.MethodPost, "/v1/admin/teams", tokAdmin, hubapi.TeamInput{ID: "pay", Reason: "x"}), 409, hubapi.CodeAlreadyExists)
	e.problem(e.do(http.MethodPost, "/v1/admin/teams", tokAdmin, hubapi.TeamInput{ID: "x", Namespaces: []string{"["}, Reason: "x"}), 400, "invalid_selector")
	e.problem(e.sdo(e.payop, http.MethodPost, "/v1/admin/teams", hubapi.TeamInput{ID: "mine", Reason: "x"}), 403, "role_forbidden")
	// Team-scoped user: the role follows its team roles.
	var u hubapi.User
	e.getJSON("/v1/admin/users/payop", tokAdmin, &u)
	if u.Role != hubapi.UserRoleOperator || u.Scope != hubapi.ScopeTeams {
		t.Errorf("payop: %+v", u)
	}
	r := e.do(http.MethodPatch, "/v1/admin/users/payop", tokAdmin, hubapi.UserUpdate{
		TeamRoles: &[]hubapi.TeamRole{{Team: "pay", Role: "viewer"}}, Reason: "less"}, ifMatchHdr(u.Version)...)
	expectCode(t, "patch payop", r, 200)
	mustJSON(t, r.body, &u)
	if u.Role != hubapi.UserRoleViewer {
		t.Errorf("payop after patch: %+v", u)
	}
}

func TestDigestSavingsByTeam(t *testing.T) {
	e := newTeamEnv(t)
	got, err := (&digestSource{s: e.srv}).TeamSavings(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	n := 0
	for i, ts := range got {
		ids = append(ids, ts.Team)
		n += ts.Workloads
		if i > 0 && ts.MonthlySavingsUSD > got[i-1].MonthlySavingsUSD {
			t.Errorf("not sorted by savings: %+v", got)
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"pay", "unassigned", "web"}) || n != 6 {
		t.Errorf("digest teams: %+v", got)
	}
}

// TestUserRoleWithTeams: a fleet user's stored role stays what the admin set; a team-scoped user's
// is derived; moving a team-scoped user to the fleet needs the fleet role.
func TestUserRoleWithTeams(t *testing.T) {
	e := newTeamEnv(t)
	var u hubapi.User
	e.getJSON("/v1/admin/users/webop", tokAdmin, &u)
	if u.Role != hubapi.UserRoleViewer || u.Scope != hubapi.ScopeFleet {
		t.Errorf("fleet viewer with a team operator role: stored %+v", u)
	}
	var me hubapi.Me
	mustJSON(t, e.sdo(e.webop, http.MethodGet, "/v1/me", nil).body, &me)
	if me.Role != hubapi.UserRoleOperator || me.User.Role != hubapi.UserRoleViewer {
		t.Errorf("me: role %q, user.role %q", me.Role, me.User.Role)
	}
	// Changing its team roles leaves the fleet role alone.
	r := e.do(http.MethodPatch, "/v1/admin/users/webop", tokAdmin, hubapi.UserUpdate{
		TeamRoles: &[]hubapi.TeamRole{{Team: "pay", Role: "operator"}}, Reason: "x"}, ifMatchHdr(u.Version)...)
	expectCode(t, "patch webop", r, 200)
	mustJSON(t, r.body, &u)
	if u.Role != hubapi.UserRoleViewer {
		t.Errorf("fleet role changed to %q", u.Role)
	}
	// Team-scoped: derived; to the fleet only with an explicit role.
	e.getJSON("/v1/admin/users/payop", tokAdmin, &u)
	if u.Role != hubapi.UserRoleOperator {
		t.Errorf("team-scoped operator: stored role %q", u.Role)
	}
	fleet := hubapi.ScopeFleet
	e.problem(e.do(http.MethodPatch, "/v1/admin/users/payop", tokAdmin, hubapi.UserUpdate{Scope: &fleet, Reason: "x"},
		ifMatchHdr(u.Version)...), 400, "role_required")
	r = e.do(http.MethodPatch, "/v1/admin/users/payop", tokAdmin, hubapi.UserUpdate{Scope: &fleet, Role: strPtr(hubapi.UserRoleViewer), Reason: "x"},
		ifMatchHdr(u.Version)...)
	expectCode(t, "payop to the fleet", r, 200)
	mustJSON(t, r.body, &u)
	if u.Role != hubapi.UserRoleViewer || u.Scope != hubapi.ScopeFleet || len(u.TeamRoles) != 1 {
		t.Errorf("payop after moving to the fleet: %+v", u)
	}
}
