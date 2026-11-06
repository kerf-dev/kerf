package capacity

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestBlockers(t *testing.T) {
	web := &hubapi.WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "web"}
	known := func(w hubapi.WorkloadRef) string {
		if w == *web {
			return "c1/shop/Deployment/web"
		}
		return ""
	}
	ca := []hubapi.CapacityAutoscaler{{Kind: "cluster-autoscaler"}}
	tests := []struct {
		name   string
		rep    hubapi.CapacityReport
		needed map[string]int
		want   []string // type object workloadId
	}{
		{"nothing", hubapi.CapacityReport{}, nil, nil},
		{
			"pdb at zero, linked to its workload",
			hubapi.CapacityReport{PDBs: []hubapi.CapacityPDB{{Namespace: "shop", Name: "web", ExpectedPods: 3, CurrentHealthy: 3, DesiredHealthy: 3, Workload: web}}},
			nil, []string{"pdb-zero-disruptions PodDisruptionBudget shop/web c1/shop/Deployment/web"},
		},
		{
			"each annotation counts only for the autoscaler of the pod's pool",
			hubapi.CapacityReport{
				Autoscalers: append(ca, hubapi.CapacityAutoscaler{Kind: "karpenter"}),
				NodePools:   []hubapi.CapacityNodePool{{Name: "k", Autoscaler: "karpenter"}, {Name: "ng", Autoscaler: "cluster-autoscaler"}},
				Pods: []hubapi.CapacityPod{
					{Namespace: "shop", Name: "cache-0", Node: "n1", NodePool: "k", Reasons: []string{"do-not-disrupt", "local-storage", "safe-to-evict-false"}},
					{Namespace: "shop", Name: "scratch-0", Node: "n2", NodePool: "ng", Reasons: []string{"do-not-disrupt", "local-storage", "safe-to-evict-false"}, Workload: web},
				},
			},
			nil, []string{
				"do-not-disrupt-pod Pod shop/cache-0 ",
				"safe-to-evict-false Pod shop/scratch-0 c1/shop/Deployment/web",
				"local-storage Pod shop/scratch-0 c1/shop/Deployment/web",
			},
		},
		{
			"pods outside a known pool count when the autoscaler runs in the cluster",
			hubapi.CapacityReport{
				Autoscalers: ca,
				Pods:        []hubapi.CapacityPod{{Namespace: "shop", Name: "p", Reasons: []string{"do-not-disrupt", "local-storage"}}},
			},
			nil, []string{"local-storage Pod shop/p "},
		},
		{
			"node annotations",
			hubapi.CapacityReport{
				NodePools: []hubapi.CapacityNodePool{{Name: "k", Autoscaler: "karpenter"}, {Name: "ng", Autoscaler: "cluster-autoscaler"}},
				Nodes: []hubapi.CapacityNode{
					{Name: "n1", NodePool: "k", DoNotDisrupt: true, ScaleDownDisabled: true},
					{Name: "n2", NodePool: "ng", DoNotDisrupt: true, ScaleDownDisabled: true},
					{Name: "n3"},
				},
			},
			nil, []string{"do-not-disrupt-node Node /n1 ", "scale-down-disabled Node /n2 "},
		},
		{
			"karpenter policy and budgets",
			hubapi.CapacityReport{
				NodePools: []hubapi.CapacityNodePool{
					{Name: "batch", Autoscaler: "karpenter", ConsolidationPolicy: "WhenEmpty", Budgets: []hubapi.CapacityDisruptionBudget{{Nodes: "0"}}},
					{Name: "general", Autoscaler: "karpenter", ConsolidationPolicy: "WhenEmptyOrUnderutilized", Budgets: []hubapi.CapacityDisruptionBudget{
						{Nodes: "10%"},
						{Nodes: "0%", Schedule: "0 9 * * *", Duration: "8h"},
						{Nodes: "0", Reasons: []string{"Drifted"}}, // not consolidation
					}},
					{Name: "idle", Autoscaler: "karpenter", ConsolidationPolicy: "WhenEmpty"}, // no nodes
				},
				Nodes: []hubapi.CapacityNode{{Name: "b1", NodePool: "batch"}, {Name: "g1", NodePool: "general"}},
			},
			nil, []string{
				"consolidation-policy NodePool /batch ",
				"disruption-budget-zero NodePool /batch ",
				"disruption-budget-zero NodePool /general ",
			},
		},
		{
			"minimum above the estimate",
			hubapi.CapacityReport{
				NodePools: []hubapi.CapacityNodePool{
					{Name: "ng-large", Autoscaler: "cluster-autoscaler", MinNodes: ptrI(2)},
					{Name: "ng-small", Autoscaler: "cluster-autoscaler", MinNodes: ptrI(1)},
				},
				Nodes: []hubapi.CapacityNode{{Name: "l1", NodePool: "ng-large"}, {Name: "l2", NodePool: "ng-large"}, {Name: "s1", NodePool: "ng-small"}},
			},
			map[string]int{"ng-large": 1, "ng-small": 1},
			[]string{"node-pool-minimum NodeGroup /ng-large "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, omitted := Blockers(tt.rep, tt.needed, known)
			var lines []string
			for _, b := range got {
				if b.Message == "" || b.Fix == "" {
					t.Errorf("%s without message or fix", b.Type)
				}
				lines = append(lines, fmt.Sprintf("%s %s %s/%s %s", b.Type, b.Object.Kind, b.Object.Namespace, b.Object.Name, b.WorkloadID))
			}
			if strings.Join(lines, "\n") != strings.Join(tt.want, "\n") || omitted != 0 {
				t.Errorf("got (omitted %d)\n%s\nwant\n%s", omitted, strings.Join(lines, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestBlockersCapped(t *testing.T) {
	rep := hubapi.CapacityReport{Autoscalers: []hubapi.CapacityAutoscaler{{Kind: "karpenter"}}}
	for i := range MaxBlockers + 5 {
		rep.Nodes = append(rep.Nodes, hubapi.CapacityNode{Name: fmt.Sprintf("n%03d", i), DoNotDisrupt: true})
	}
	got, omitted := Blockers(rep, nil, nil)
	if len(got) != MaxBlockers || omitted != 5 {
		t.Errorf("len %d omitted %d", len(got), omitted)
	}
}
