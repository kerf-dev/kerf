//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// TestNodeCapacity: the agent reports nodes and consolidation blockers (read only); the hub's
// capacity endpoint shows totals, a labelled node estimate, and blockers naming their objects:
// a PodDisruptionBudget that allows no disruption and a karpenter.sh/do-not-disrupt pod. The dev
// fleet runs neither Karpenter nor the Cluster Autoscaler; those are covered by the unit tests
// and by the KWOK clusters described in docs/node-savings.md.
func TestNodeCapacity(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "capacity")
	start := time.Now().Add(-5 * time.Second)

	deploy(t, cl, ns, app{Name: "guarded", Replicas: 2, CPULoad: 5, Requests: rl("50m", "32Mi")})
	minAvail := intstr.FromInt32(2)
	must(t, cl.C.Create(ctx(t), &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "guarded", Namespace: ns},
		Spec:       policyv1.PodDisruptionBudgetSpec{MinAvailable: &minAvail, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "guarded"}}},
	}))
	pinned := app{Name: "pinned", CPULoad: 5, Requests: rl("50m", "32Mi")}.deployment(ns)
	pinned.Spec.Template.Annotations = map[string]string{"karpenter.sh/do-not-disrupt": "true"}
	must(t, cl.C.Create(ctx(t), pinned))
	waitReady(t, cl, ns, "pinned", 3*time.Minute)

	var got hubapi.ClusterCapacity
	eventually(t, 4*time.Minute, 5*time.Second, "capacity report with the test's blockers", func() (bool, string) {
		got = hubapi.ClusterCapacity{}
		as(t, hubapi.UserRoleViewer).get(t, "/v1/clusters/"+cl.Name+"/capacity", &got)
		if got.ReportedAt == nil || got.ObservedAt == nil || got.ObservedAt.Before(start) {
			return false, fmt.Sprintf("observedAt %v", got.ObservedAt)
		}
		pdb, pod := false, false
		for _, b := range got.Blockers {
			if b.Object.Namespace != ns {
				continue
			}
			switch {
			case b.Type == hubapi.BlockerPDB && b.Object.Kind == "PodDisruptionBudget" && b.Object.Name == "guarded":
				pdb = true
			case b.Type == hubapi.BlockerDoNotDisruptPod && b.Object.Kind == "Pod":
				pod = true
			}
		}
		return pdb && pod, fmt.Sprintf("pdb blocker %v, do-not-disrupt blocker %v (%d blockers)", pdb, pod, len(got.Blockers))
	})
	for _, b := range got.Blockers {
		if b.Object.Namespace == ns && (b.Message == "" || b.Fix == "") {
			t.Errorf("blocker without message or fix: %+v", b)
		}
	}
	if got.Totals.Nodes < 1 || got.Totals.Allocatable.CPUCores <= 0 || got.Totals.Requested.CPUCores <= 0 {
		t.Errorf("totals %+v", got.Totals)
	}
	e := got.Estimate
	if e == nil || e.Method != hubapi.EstimateFFD || e.MethodURL == "" || e.CurrentNodes != got.Totals.Nodes || e.EstimatedNodes < 1 {
		t.Fatalf("estimate %+v", e)
	}
	if len(got.Pools) == 0 {
		t.Errorf("no pools")
	}
	t.Logf("%s: %d nodes -> %d estimated (%s prices), %d blockers, %d recommendations applied",
		cl.Name, e.CurrentNodes, e.EstimatedNodes, e.PriceSource, len(got.Blockers), e.Recommendations)
}
