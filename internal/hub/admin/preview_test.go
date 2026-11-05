package admin

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func pw(k hubapi.WorkloadKey, cpu, mem float64) PreviewWorkload {
	return PreviewWorkload{Workload: Workload{Key: k}, Savings: &hubapi.Savings{CPU: cpu, Memory: mem, Total: cpu + mem}}
}

func TestPreviewIntoAutoPatch(t *testing.T) {
	ws := []PreviewWorkload{
		pw(api, 10, 5),
		pw(checkout, 61.2, 8.1),
		{Workload: Workload{Key: worker}},
		pw(web, 100, 100),
	}
	before := []Candidate{global(modes("audit", "audit"))}
	after, err := ApplyChanges(before, []hubapi.PolicyChange{upsertOf("", hubapi.PolicyDraft{
		Type: v1alpha1.HubPolicyTypePolicy, Name: "payments-cpu-auto",
		Spec: v1alpha1.RightsizingPolicySpec{Priority: 100, Selector: v1alpha1.PolicySelector{Namespaces: []string{"payments"}},
			CPU: &v1alpha1.CPUPolicy{Mode: "auto-patch"}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	p := Preview(ws, before, after, 0)
	if p.WorkloadsEvaluated != 4 || p.WorkloadsChanged != 3 || p.Truncated {
		t.Errorf("counts %+v", p)
	}
	if want := (hubapi.Savings{CPU: 71.2, Total: 71.2}); p.SavingsIntoAutoPatchUSD != want {
		t.Errorf("into %+v, want %+v", p.SavingsIntoAutoPatchUSD, want)
	}
	if p.SavingsOutOfAutoPatchUSD != (hubapi.Savings{}) {
		t.Errorf("out of %+v", p.SavingsOutOfAutoPatchUSD)
	}
	var ids []string
	for _, it := range p.Items {
		ids = append(ids, it.WorkloadID)
	}
	if want := []string{checkout.ID(), api.ID(), worker.ID()}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("items %v, want %v", ids, want)
	}
	it := p.Items[0]
	if it.Cluster != "c1" || it.Namespace != "payments" || it.Kind != "Deployment" || it.Name != "checkout" ||
		it.Before != em("audit", "global-default", "audit", "global-default") ||
		it.After != em("auto-patch", "policy", "audit", "global-default") ||
		it.PolicyBefore != "" || it.PolicyAfter != "payments-cpu-auto" || it.MonthlySavingsUSD.Total != 69.3 {
		t.Errorf("item %+v", it)
	}
	if p.Items[2].MonthlySavingsUSD != nil {
		t.Errorf("worker savings %+v", p.Items[2].MonthlySavingsUSD)
	}
	if p.Warnings != nil {
		t.Errorf("warnings %v", p.Warnings)
	}

	// The reverse: out of auto-patch.
	r := Preview(ws, after, before, 2)
	if want := (hubapi.Savings{CPU: 71.2, Total: 71.2}); r.SavingsOutOfAutoPatchUSD != want || r.SavingsIntoAutoPatchUSD != (hubapi.Savings{}) {
		t.Errorf("reverse %+v / %+v", r.SavingsOutOfAutoPatchUSD, r.SavingsIntoAutoPatchUSD)
	}
	if !r.Truncated || len(r.Items) != 2 || r.WorkloadsChanged != 3 || r.Items[0].PolicyBefore != "payments-cpu-auto" {
		t.Errorf("reverse items %+v", r)
	}
}

func TestPreviewSourceOnlyAndOverride(t *testing.T) {
	ws := []PreviewWorkload{pw(checkout, 1.006, 2), pw(api, 3, 4)}
	pol := hubPol("pin-audit", 5, nil, modes("audit", ""))
	ov := override(checkout, modes("", "auto-patch"))
	// before: a policy pins cpu audit (source policy); after: policy deleted, override sets memory auto-patch.
	p := Preview(ws, []Candidate{pol}, []Candidate{ov}, 10)
	if p.WorkloadsChanged != 1 || len(p.Items) != 2 {
		t.Fatalf("preview %+v", p)
	}
	byID := map[string]hubapi.ImpactItem{}
	for _, it := range p.Items {
		byID[it.WorkloadID] = it
	}
	a := byID[api.ID()]
	if a.Before.CPU != a.After.CPU || a.Before.CPUSource != "policy" || a.After.CPUSource != "global-default" ||
		a.PolicyBefore != "pin-audit" || a.PolicyAfter != "" {
		t.Errorf("api item %+v", a)
	}
	c := byID[checkout.ID()]
	if c.After.Memory != "auto-patch" || c.After.MemorySource != "policy" || c.PolicyAfter != "" {
		t.Errorf("checkout item %+v", c)
	}
	if want := (hubapi.Savings{Memory: 2, Total: 2}); p.SavingsIntoAutoPatchUSD != want {
		t.Errorf("into %+v", p.SavingsIntoAutoPatchUSD)
	}

	// An override that sets the cpu mode names the override.
	ov2 := override(checkout, modes("auto-patch", ""))
	q := Preview(ws[:1], nil, []Candidate{ov2}, 10)
	if len(q.Items) != 1 || q.Items[0].PolicyAfter != ov2.Name || q.SavingsIntoAutoPatchUSD.CPU != 1.01 {
		t.Errorf("override preview %+v", q)
	}
}

func TestPreviewLimitsAndWarnings(t *testing.T) {
	var ws []PreviewWorkload
	for i := 0; i < 600; i++ {
		ws = append(ws, pw(hubapi.WorkloadKey{Cluster: "c1", Namespace: "ns", Kind: "Deployment", Name: fmt.Sprintf("w%03d", i)}, 0, 0))
	}
	after := []Candidate{global(modes("off", ""))}
	for _, tc := range []struct {
		limit, want int
		truncated   bool
	}{{0, 50, true}, {-1, 50, true}, {10, 10, true}, {1000, 500, true}} {
		p := Preview(ws, nil, after, tc.limit)
		if len(p.Items) != tc.want || p.Truncated != tc.truncated || p.WorkloadsChanged != 600 {
			t.Errorf("limit %d: %d items, truncated %v", tc.limit, len(p.Items), p.Truncated)
		}
		if p.Items[0].WorkloadID != "c1/ns/Deployment/w000" {
			t.Errorf("first item %s", p.Items[0].WorkloadID)
		}
	}

	unchanged := Preview(ws[:3], after, after, 0)
	if unchanged.WorkloadsEvaluated != 3 || unchanged.WorkloadsChanged != 0 || unchanged.Items == nil || len(unchanged.Items) != 0 {
		t.Errorf("unchanged %+v", unchanged)
	}

	// Label warnings: distinct, capped.
	var labelled []Candidate
	for i := 0; i < 30; i++ {
		c := hubPol(fmt.Sprintf("lab%02d", i), int32(10+i), nil, modes("off", ""))
		c.Spec.Selector.MatchLabels = map[string]string{"a": "b"}
		labelled = append(labelled, c)
	}
	p := Preview(ws[:5], nil, labelled, 0)
	if len(p.Warnings) != maxPreviewWarnings || p.Warnings[0] != labelWarning("lab00") {
		t.Errorf("warnings %d: %v", len(p.Warnings), p.Warnings)
	}
	if p.WorkloadsChanged != 0 {
		t.Errorf("label policies applied without confirmation: %+v", p)
	}
}
