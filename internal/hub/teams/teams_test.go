package teams

import (
	"context"
	"slices"
	"testing"
)

func TestResolve(t *testing.T) {
	defs := []Def{
		{ID: "payments", Namespaces: []string{"payments", "payments-*"}, OwnerValues: []string{"pay"}},
		{ID: "web", NamespaceLabels: map[string]string{"tier": "frontend"}},
		{ID: "prod-web", Namespaces: []string{"web"}, Clusters: []string{"prod-*"}},
		{ID: "search"}, // owner label only
		{ID: "a-first", Namespaces: []string{"shared"}},
		{ID: "z-last", Namespaces: []string{"shared"}},
	}
	r := NewResolver(defs, "")
	tests := []struct {
		name string
		w    Workload
		want string
	}{
		{"namespace glob", Workload{Cluster: "c1", Namespace: "payments-eu"}, "payments"},
		{"namespace exact", Workload{Cluster: "c1", Namespace: "payments"}, "payments"},
		{"no match", Workload{Cluster: "c1", Namespace: "kube-system"}, ""},
		{"namespace labels", Workload{Cluster: "c1", Namespace: "x", NamespaceLabels: map[string]string{"tier": "frontend", "o": "1"}}, "web"},
		{"namespace labels mismatch", Workload{Cluster: "c1", Namespace: "x", NamespaceLabels: map[string]string{"tier": "backend"}}, ""},
		{"cluster restriction match", Workload{Cluster: "prod-eu", Namespace: "web"}, "prod-web"},
		{"cluster restriction miss", Workload{Cluster: "dev", Namespace: "web"}, ""},
		{"workload label wins over selector", Workload{Cluster: "c1", Namespace: "payments", Labels: map[string]string{"team": "search"}}, "search"},
		{"workload label wins over namespace label", Workload{Cluster: "c1", Namespace: "x",
			Labels: map[string]string{"team": "search"}, NamespaceLabels: map[string]string{"team": "web"}}, "search"},
		{"namespace owner label", Workload{Cluster: "c1", Namespace: "payments", NamespaceLabels: map[string]string{"team": "web"}}, "web"},
		{"owner value alias", Workload{Cluster: "c1", Namespace: "x", Labels: map[string]string{"team": "pay"}}, "payments"},
		{"unknown owner falls through", Workload{Cluster: "c1", Namespace: "payments", Labels: map[string]string{"team": "nobody"}}, "payments"},
		{"several selectors: first by id", Workload{Cluster: "c1", Namespace: "shared"}, "a-first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Resolve(tt.w); got != tt.want {
				t.Errorf("Resolve = %q, want %q", got, tt.want)
			}
		})
	}
	if got := NewResolver(defs, "app.kubernetes.io/part-of").Resolve(Workload{Namespace: "x",
		Labels: map[string]string{"app.kubernetes.io/part-of": "search", "team": "web"}}); got != "search" {
		t.Errorf("custom owner label: %q", got)
	}
	// A team's own id wins over another team's owner value.
	r2 := NewResolver([]Def{{ID: "web"}, {ID: "alpha", OwnerValues: []string{"web"}}}, "")
	if got := r2.Resolve(Workload{Labels: map[string]string{"team": "web"}}); got != "web" {
		t.Errorf("id vs owner value: %q", got)
	}
}

func TestScope(t *testing.T) {
	teamOp := UserScope(RoleOperator, true, []Binding{{Team: "a", Role: RoleOperator}, {Team: "b", Role: RoleViewer}})
	fleetViewerTeamOp := UserScope(RoleViewer, false, []Binding{{Team: "a", Role: RoleOperator}})
	admin := Fleet(RoleAdmin)
	tests := []struct {
		name        string
		s           Scope
		team, need  string
		see, ok, by bool
	}{
		{"team op on own team", teamOp, "a", RoleOperator, true, true, true},
		{"team op on viewer team", teamOp, "b", RoleOperator, true, false, false},
		{"team op views viewer team", teamOp, "b", RoleViewer, true, true, true},
		{"team op on other team", teamOp, "c", RoleViewer, false, false, false},
		{"team op on unassigned", teamOp, "", RoleViewer, false, false, false},
		{"fleet viewer + op on a", fleetViewerTeamOp, "a", RoleOperator, true, true, true},
		{"fleet viewer + op on c", fleetViewerTeamOp, "c", RoleOperator, true, false, false},
		{"fleet viewer reads c", fleetViewerTeamOp, "c", RoleViewer, true, true, false},
		{"fleet viewer reads unassigned", fleetViewerTeamOp, "", RoleViewer, true, true, false},
		{"admin anywhere", admin, "c", RoleOperator, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.CanSee(tt.team); got != tt.see {
				t.Errorf("CanSee = %v, want %v", got, tt.see)
			}
			ok, by := tt.s.Grants(tt.team, tt.need)
			if ok != tt.ok || by != tt.by {
				t.Errorf("Grants = %v, %v, want %v, %v", ok, by, tt.ok, tt.by)
			}
		})
	}
	if teamOp.MaxRole() != RoleOperator || fleetViewerTeamOp.MaxRole() != RoleOperator || teamOp.All() || !fleetViewerTeamOp.All() {
		t.Errorf("MaxRole/All: %+v %+v", teamOp, fleetViewerTeamOp)
	}
}

func TestRestriction(t *testing.T) {
	ctx := context.Background()
	if _, r := Restriction(ctx); r {
		t.Error("no scope: restricted")
	}
	if _, r := Restriction(WithScope(ctx, Fleet(RoleViewer))); r {
		t.Error("fleet scope: restricted")
	}
	ids, r := Restriction(WithScope(ctx, UserScope(RoleViewer, true, []Binding{{Team: "b", Role: RoleViewer}, {Team: "a", Role: RoleOperator}})))
	if !r || !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("team scope: %v %v", ids, r)
	}
	ids, r = Restriction(WithScope(ctx, Scope{}))
	if !r || ids == nil || len(ids) != 0 {
		t.Errorf("empty scope must restrict to nothing: %v %v", ids, r)
	}
	var args []any
	if got := SQL(WithScope(ctx, Scope{TeamRoles: map[string]string{"a": RoleViewer}}), "w.team", &args); got != "w.team = ANY($1::text[])" || len(args) != 1 {
		t.Errorf("SQL = %q %v", got, args)
	}
}

func TestValidateBindings(t *testing.T) {
	tests := []struct {
		bs []Binding
		ok bool
	}{
		{[]Binding{{"a", RoleViewer}, {"b", RoleOperator}}, true},
		{[]Binding{{"a", RoleAdmin}}, false},
		{[]Binding{{"a", RoleViewer}, {"a", RoleOperator}}, false},
		{[]Binding{{"Bad", RoleViewer}}, false},
		{[]Binding{{Unassigned, RoleViewer}}, false},
	}
	for _, tt := range tests {
		if err := ValidateBindings(tt.bs); (err == nil) != tt.ok {
			t.Errorf("%v: err %v", tt.bs, err)
		}
	}
}
