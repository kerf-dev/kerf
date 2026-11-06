package hub

import (
	"net/http"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestClusterCapacity(t *testing.T) {
	e := newEnv(t)
	const path = "/v1/clusters/c1/capacity"

	// No report yet: empty, no reportedAt.
	var empty hubapi.ClusterCapacity
	e.getJSON(path, tokUI, &empty)
	if empty.ReportedAt != nil || empty.Estimate != nil || empty.Pools == nil || empty.Blockers == nil || empty.Autoscalers == nil {
		t.Fatalf("before a report: %+v", empty)
	}

	checkout := &hubapi.WorkloadRef{Namespace: "payments", Kind: "Deployment", Name: "checkout"}
	unknown := &hubapi.WorkloadRef{Namespace: "other", Kind: "Deployment", Name: "unknown"}
	var nodes []hubapi.CapacityNode
	for _, n := range []string{"n1", "n2", "n3", "n4"} {
		nodes = append(nodes, hubapi.CapacityNode{Name: n, NodePool: "general", InstanceType: "m5.xlarge", CapacityType: "on-demand",
			Allocatable: hubapi.ResourceValues{CPU: "4000m", Memory: "17179869184"}})
	}
	rep := &hubapi.CapacityReport{
		ObservedAt:  e.now.Add(-time.Minute),
		Autoscalers: []hubapi.CapacityAutoscaler{{Kind: "karpenter"}},
		NodePools:   []hubapi.CapacityNodePool{{Name: "general", Autoscaler: "karpenter", ConsolidationPolicy: "WhenEmpty"}},
		Nodes:       nodes,
		PodGroups: []hubapi.CapacityPodGroup{{
			Owner:    &hubapi.CapacityObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "checkout"},
			Workload: checkout, NodePool: "general", Count: 8,
			Containers: []hubapi.ContainerResources{{Name: "app", Requests: &hubapi.ResourceValues{CPU: "1500m", Memory: "4294967296"}}},
		}},
		PDBs: []hubapi.CapacityPDB{{Namespace: "payments", Name: "checkout", ExpectedPods: 8, CurrentHealthy: 8, DesiredHealthy: 8, Workload: checkout}},
		Pods: []hubapi.CapacityPod{{Namespace: "other", Name: "cache-0", Node: "n1", NodePool: "general", Workload: unknown, Reasons: []string{"do-not-disrupt"}}},
	}

	// Validation.
	bad := *rep
	bad.Pods = []hubapi.CapacityPod{{Namespace: "x", Name: "y", Reasons: []string{"because"}}}
	if r := e.do("POST", "/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{}, Capacity: &bad}); r.code != http.StatusBadRequest {
		t.Errorf("bad reason: status %d", r.code)
	}

	if r := e.do("POST", "/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{}, Capacity: rep}); r.code != http.StatusOK {
		t.Fatalf("report: status %d %s", r.code, r.body)
	}
	// A batch without capacity keeps the report.
	if r := e.do("POST", "/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{}}); r.code != http.StatusOK {
		t.Fatalf("empty batch: status %d", r.code)
	}

	var got hubapi.ClusterCapacity
	e.getJSON(path, tokUI, &got)
	if got.ReportedAt == nil || !got.ObservedAt.Equal(rep.ObservedAt) {
		t.Fatalf("reportedAt %v observedAt %v", got.ReportedAt, got.ObservedAt)
	}
	if got.Totals.Nodes != 4 || got.Totals.Requested.CPUCores != 12 || got.Totals.RequestedAfter.CPUCores >= 12 {
		t.Errorf("totals %+v (the checkout recommendation must lower the requests)", got.Totals)
	}
	est := got.Estimate
	if est == nil || est.CurrentNodes != 4 || est.EstimatedNodes >= 4 || est.Recommendations != 1 ||
		est.PriceSource != "rates" || est.SavingsMonthlyUSD <= 0 || est.Method != "first-fit-decreasing" || est.MethodURL == "" {
		t.Errorf("estimate %+v", est)
	}
	if len(got.Pools) != 1 || got.Pools[0].Name != "general" || got.Pools[0].EstimatedNodes != est.EstimatedNodes {
		t.Errorf("pools %+v", got.Pools)
	}
	want := map[string]string{
		"pdb-zero-disruptions": "c1/payments/Deployment/checkout",
		"do-not-disrupt-pod":   "",
		"consolidation-policy": "",
	}
	if len(got.Blockers) != len(want) {
		t.Errorf("blockers %+v", got.Blockers)
	}
	for _, b := range got.Blockers {
		id, ok := want[b.Type]
		if !ok || b.WorkloadID != id {
			t.Errorf("blocker %s: workloadId %q, want %q (listed %v)", b.Type, b.WorkloadID, id, ok)
		}
	}

	// Another cluster's report is separate; agents cannot read the UI endpoint of other clusters.
	var c2 hubapi.ClusterCapacity
	e.getJSON("/v1/clusters/c2/capacity", tokUI, &c2)
	if c2.ReportedAt != nil {
		t.Errorf("c2 has a report: %+v", c2)
	}
	if r := e.do("GET", "/v1/clusters/zz/capacity", tokAdmin, nil); r.code != http.StatusNotFound {
		t.Errorf("unknown cluster: status %d", r.code)
	}
}
