package admin

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/api/v1alpha1/validate"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

func upsertOf(id string, d hubapi.PolicyDraft) hubapi.PolicyChange {
	return hubapi.PolicyChange{Op: hubapi.PreviewUpsert, PolicyID: id, Policy: &d}
}

func TestApplyChanges(t *testing.T) {
	base := []Candidate{
		global(modes("audit", "")),
		clusterDef("c1", modes("off", "")),
		hubPol("payments", 100, []string{"payments"}, modes("auto-patch", "")),
		gitPol("c1", "payments", 50, nil, modes("off", "")),
		override(checkout, modes("off", "off")),
	}
	baseCopy := append([]Candidate(nil), base...)
	byKey := func(cs []Candidate) map[string]Candidate {
		m := map[string]Candidate{}
		for _, c := range cs {
			m[c.Origin+"/"+c.Type+"/"+c.Name+"/"+c.WorkloadID+"/"+strings.Join(c.Clusters, ",")] = c
		}
		return m
	}
	tests := []struct {
		name    string
		changes []hubapi.PolicyChange
		check   func(t *testing.T, got []Candidate)
	}{
		{"no changes", nil, func(t *testing.T, got []Candidate) {
			if !reflect.DeepEqual(got, base) {
				t.Errorf("got %+v", got)
			}
		}},
		{"delete", []hubapi.PolicyChange{{Op: hubapi.PreviewDelete, PolicyID: "p-payments"}}, func(t *testing.T, got []Candidate) {
			if len(got) != 4 || got[2].Origin != "git" {
				t.Errorf("got %+v", got)
			}
		}},
		{"global default replaces by identity", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeGlobalDefault, Name: "whatever", Clusters: []string{"c9"}, Spec: modes("off", "off"),
		})}, func(t *testing.T, got []Candidate) {
			if len(got) != 5 {
				t.Fatalf("len %d", len(got))
			}
			g := got[0]
			if g.Name != v1alpha1.GlobalDefaultsPolicyName || g.PolicyID != "g" || g.Clusters != nil || g.Spec.CPU.Mode != "off" || g.Origin != "hub" {
				t.Errorf("global %+v", g)
			}
		}},
		{"cluster default same cluster replaces", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeClusterDefault, Clusters: []string{"c1"}, Spec: modes("auto-patch", ""),
		})}, func(t *testing.T, got []Candidate) {
			if len(got) != 5 || got[1].PolicyID != "cd-c1" || got[1].Spec.CPU.Mode != "auto-patch" || got[1].Spec.Priority != 10 {
				t.Errorf("got %+v", got[1])
			}
		}},
		{"cluster default other cluster appends", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeClusterDefault, Clusters: []string{"c2"}, Spec: modes("auto-patch", ""),
		})}, func(t *testing.T, got []Candidate) {
			if len(got) != 6 || got[5].Name != v1alpha1.ClusterDefaultsPolicyName || got[5].Clusters[0] != "c2" || got[5].PolicyID != "" {
				t.Errorf("got %+v", got)
			}
		}},
		{"policy by name replaces hub one, not git", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypePolicy, Name: "payments", Clusters: []string{"c1"},
			Spec: v1alpha1.RightsizingPolicySpec{Priority: 7, Memory: &v1alpha1.MemoryPolicy{Mode: "off"}},
		})}, func(t *testing.T, got []Candidate) {
			if len(got) != 5 {
				t.Fatalf("len %d", len(got))
			}
			p := got[2]
			if p.PolicyID != "p-payments" || p.Spec.Priority != 7 || p.Spec.CPU != nil || !reflect.DeepEqual(p.Clusters, []string{"c1"}) {
				t.Errorf("policy %+v", p)
			}
			if got[3].Origin != "git" || got[3].Spec.Priority != 50 {
				t.Errorf("git policy changed: %+v", got[3])
			}
		}},
		{"new policy appends", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypePolicy, Name: "new", Spec: v1alpha1.RightsizingPolicySpec{Priority: 7},
		})}, func(t *testing.T, got []Candidate) {
			if len(got) != 6 || got[5].Name != "new" || got[5].Origin != "hub" {
				t.Errorf("got %+v", got)
			}
		}},
		{"policy by id renames", []hubapi.PolicyChange{upsertOf("p-payments", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypePolicy, Name: "renamed", Spec: v1alpha1.RightsizingPolicySpec{Priority: 7},
		})}, func(t *testing.T, got []Candidate) {
			if len(got) != 5 || got[2].Name != "renamed" || got[2].PolicyID != "p-payments" {
				t.Errorf("got %+v", got[2])
			}
		}},
		{"new override", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeWorkloadOverride, WorkloadID: api.ID(), Spec: modes("auto-patch", ""),
		})}, func(t *testing.T, got []Candidate) {
			if len(got) != 6 {
				t.Fatalf("len %d", len(got))
			}
			o := got[5]
			want := validate.OverrideSelector("payments", "Deployment", "api")
			if o.Name != v1alpha1.OverridePolicyName("payments", "Deployment", "api") || o.WorkloadID != api.ID() ||
				o.Spec.Priority != v1alpha1.PriorityWorkloadOverride || !reflect.DeepEqual(o.Spec.Selector, want) ||
				!reflect.DeepEqual(o.Clusters, []string{"c1"}) {
				t.Errorf("override %+v", o)
			}
		}},
		{"override replaces same workload", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeWorkloadOverride, WorkloadID: checkout.ID(), Spec: modes("auto-patch", ""),
		})}, func(t *testing.T, got []Candidate) {
			if len(got) != 5 || got[4].PolicyID != "o-checkout" || got[4].Spec.CPU.Mode != "auto-patch" || got[4].Spec.Memory != nil {
				t.Errorf("got %+v", got[4])
			}
		}},
		{"sequence", []hubapi.PolicyChange{
			{Op: hubapi.PreviewDelete, PolicyID: "g"},
			upsertOf("", hubapi.PolicyDraft{Type: v1alpha1.HubPolicyTypeGlobalDefault, Spec: modes("off", "")}),
		}, func(t *testing.T, got []Candidate) {
			m := byKey(got)
			g, ok := m["hub/global-default/ro-global-defaults//"]
			if len(got) != 5 || !ok || g.PolicyID != "" || g.Spec.CPU.Mode != "off" {
				t.Errorf("got %+v", got)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyChanges(base, tc.changes)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, got)
			if !reflect.DeepEqual(base, baseCopy) {
				t.Fatal("input modified")
			}
		})
	}
}

func TestApplyChangesErrors(t *testing.T) {
	base := []Candidate{global(modes("audit", "")), hubPol("payments", 100, nil, modes("auto-patch", ""))}
	tests := []struct {
		name    string
		changes []hubapi.PolicyChange
		code    string // "" = ErrUnknownPolicy
		msg     string
	}{
		{"delete unknown", []hubapi.PolicyChange{{Op: hubapi.PreviewDelete, PolicyID: "nope"}}, "", `"nope"`},
		{"delete without id", []hubapi.PolicyChange{{Op: hubapi.PreviewDelete}}, "", "unknown policy"},
		{"upsert unknown id", []hubapi.PolicyChange{upsertOf("nope", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypePolicy, Name: "x", Spec: v1alpha1.RightsizingPolicySpec{Priority: 1}})}, "", `"nope"`},
		{"bad op", []hubapi.PolicyChange{{Op: "patch"}}, hubapi.CodeInvalidPolicy, "change 0: op"},
		{"no policy", []hubapi.PolicyChange{{Op: hubapi.PreviewUpsert}}, hubapi.CodeInvalidPolicy, "policy"},
		{"type change", []hubapi.PolicyChange{upsertOf("g", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypePolicy, Name: "x", Spec: v1alpha1.RightsizingPolicySpec{Priority: 1}})}, hubapi.CodeTypeImmutable, "immutable"},
		{"reserved name", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypePolicy, Name: "ro-mine", Spec: v1alpha1.RightsizingPolicySpec{Priority: 1}})}, hubapi.CodeInvalidPolicy, "reserved"},
		{"policy priority 0", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypePolicy, Name: "x"})}, hubapi.CodeInvalidPolicy, "spec.priority"},
		{"bad mode", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeGlobalDefault, Spec: modes("sometimes", "")})}, hubapi.CodeInvalidPolicy, "spec.cpu.mode"},
		{"global default with selector", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeGlobalDefault, Spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Namespaces: []string{"a"}}}})},
			hubapi.CodeInvalidPolicy, "spec.selector"},
		{"unknown type", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{Type: "magic", Name: "x"})}, hubapi.CodeInvalidPolicy, "type"},
		{"cluster default without cluster", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeClusterDefault})}, hubapi.CodeInvalidPolicy, "exactly one cluster"},
		{"cluster default with two clusters", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeClusterDefault, Clusters: []string{"a", "b"}})}, hubapi.CodeInvalidPolicy, "exactly one cluster"},
		{"override without workload", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeWorkloadOverride})}, hubapi.CodeInvalidOverride, "workloadId"},
		{"override with selector", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeWorkloadOverride, WorkloadID: checkout.ID(),
			Spec: v1alpha1.RightsizingPolicySpec{Selector: v1alpha1.PolicySelector{Kinds: []v1alpha1.WorkloadKind{"Deployment"}}}})},
			hubapi.CodeInvalidOverride, "selector"},
		{"override with priority", []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
			Type: v1alpha1.HubPolicyTypeWorkloadOverride, WorkloadID: checkout.ID(), Spec: v1alpha1.RightsizingPolicySpec{Priority: 5}})},
			hubapi.CodeInvalidOverride, "priority"},
		{"second change invalid", []hubapi.PolicyChange{
			{Op: hubapi.PreviewDelete, PolicyID: "g"},
			upsertOf("", hubapi.PolicyDraft{Type: v1alpha1.HubPolicyTypePolicy, Name: "x"}),
		}, hubapi.CodeInvalidPolicy, "change 1:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyChanges(base, tc.changes)
			if err == nil {
				t.Fatalf("no error, got %+v", got)
			}
			if got != nil {
				t.Errorf("candidates returned with error: %+v", got)
			}
			var ie *InvalidError
			switch {
			case tc.code == "":
				if !errors.Is(err, ErrUnknownPolicy) {
					t.Errorf("err %v, want ErrUnknownPolicy", err)
				}
			case !errors.As(err, &ie):
				t.Errorf("err %T %v, want *InvalidError", err, err)
			case ie.Code != tc.code || len(ie.Errs) == 0:
				t.Errorf("code %q errs %v, want %q", ie.Code, ie.Errs, tc.code)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q does not contain %q", err, tc.msg)
			}
		})
	}
}
