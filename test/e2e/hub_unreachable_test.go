//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// The hub outage is simulated on spoke 2 only, with a NetworkPolicy that blocks the agent's
// egress to the hub endpoint (k3s enforces NetworkPolicies). It is removed when the test ends,
// and `make e2e` removes a leftover one before and after the run.
const blockPolicyName = "e2e-hub-unreachable"

// agentNamespace and agentSelector locate the deployed agent (deploy/charts/agent).
var (
	agentNamespace = getenv("E2E_AGENT_NAMESPACE", "resource-optimizer")
	agentSelector  = map[string]string{"app.kubernetes.io/name": "resource-optimizer-agent"}
)

// hubEndpointIPs returns the addresses behind the spoke's selector-less hub-api Service (WO-01).
func hubEndpointIPs(t *testing.T, cl cluster) []string {
	t.Helper()
	var slices discoveryv1.EndpointSliceList
	must(t, cl.C.List(ctx(t), &slices, client.InNamespace(getenv("E2E_HUB_SERVICE_NAMESPACE", "hub")),
		client.MatchingLabels{discoveryv1.LabelServiceName: getenv("E2E_HUB_SERVICE", "hub-api")}))
	var ips []string
	for _, s := range slices.Items {
		for _, ep := range s.Endpoints {
			ips = append(ips, ep.Addresses...)
		}
	}
	if len(ips) == 0 {
		t.Fatal("no endpoints for the hub-api Service")
	}
	return ips
}

// blockHub cuts the agent off from the hub and returns the function that restores it.
func blockHub(t *testing.T, cl cluster) func() {
	t.Helper()
	var except []string
	for _, ip := range hubEndpointIPs(t, cl) {
		except = append(except, ip+"/32")
	}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: blockPolicyName, Namespace: agentNamespace, Labels: map[string]string{suiteLabel: "true"}},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: agentSelector},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: except}}},
			}},
		},
	}
	_ = cl.C.Delete(ctx(t), np.DeepCopy())
	must(t, cl.C.Create(ctx(t), np))
	t.Logf("agent on %s cut off from the hub (%v)", cl.Name, except)
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := ignoreNotFound(cl.C.Delete(c, np)); err != nil {
			t.Errorf("REMOVE NetworkPolicy %s/%s on %s BY HAND: %v", agentNamespace, blockPolicyName, cl.Name, err)
		}
		t.Logf("agent on %s reconnected to the hub", cl.Name)
	}
	t.Cleanup(restore)
	return restore
}

// Scenario 11: hub unreachable. The agent applies nothing new and reverts nothing; once the
// hub is back, the pending change goes through.
func TestHubUnreachable(t *testing.T) {
	t.Parallel()
	cl := env.Spoke2
	ns := newNamespace(t, cl, "hubdown")
	staleAfter, err := time.ParseDuration(getenv("E2E_HUB_STALE_AFTER", "5m"))
	must(t, err)

	// "applied": auto-patch from the start; "held": audit until the hub is gone.
	applied, held := overprovisioned("applied"), overprovisioned("held")
	applied.Labels = map[string]string{"e2e-role": "applied"}
	deploy(t, cl, ns, applied)
	deploy(t, cl, ns, held)
	policy(t, cl, ns, ns, v1alpha1.RightsizingPolicySpec{
		CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit}, Memory: &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAudit},
		Apply: &v1alpha1.ApplyPolicy{Strategy: v1alpha1.StrategyInPlace}, Guardrails: relaxed(),
	})
	pa := autoCPU(v1alpha1.StrategyInPlace)
	pa.Selector.MatchLabels = map[string]string{"e2e-role": "applied"}
	pa.Priority = 1001
	policy(t, cl, ns+"-applied", ns, pa)

	waitPhase(t, cl, ns, deployment, "applied", v1alpha1.PhaseApplied)
	waitReason(t, cl, ns, deployment, "held", v1alpha1.ReasonModeAudit, 3*time.Minute)
	appliedPods := snapshot(t, cl, ns, "applied")
	heldPods := snapshot(t, cl, ns, "held")

	restore := blockHub(t, cl)
	wait := staleAfter + 45*time.Second
	t.Logf("waiting %s for the agent to consider the hub unreachable (hubStaleAfter %s)", wait, staleAfter)
	time.Sleep(wait)

	// A change that would apply now: switch "held" to auto-patch.
	var p v1alpha1.RightsizingPolicy
	must(t, cl.C.Get(ctx(t), client.ObjectKey{Name: ns}, &p))
	p.Spec.CPU.Mode = v1alpha1.ModeAutoPatch
	must(t, cl.C.Update(ctx(t), &p))
	waitReason(t, cl, ns, deployment, "held", v1alpha1.ReasonHubUnreachable, 3*time.Minute)
	consistently(t, time.Minute, 5*time.Second, "nothing applied, nothing reverted", func() (bool, string) {
		if got := snapshot(t, cl, ns, "held"); fmt.Sprint(got) != fmt.Sprint(heldPods) {
			return false, fmt.Sprintf("held workload changed while the hub is unreachable: %v -> %v", heldPods, got)
		}
		if got := snapshot(t, cl, ns, "applied"); fmt.Sprint(got) != fmt.Sprint(appliedPods) {
			return false, fmt.Sprintf("applied workload reverted while the hub is unreachable: %v -> %v", appliedPods, got)
		}
		return true, ""
	})

	restore()
	waitPhase(t, cl, ns, deployment, "held", v1alpha1.PhaseApplied)
	for name, s := range snapshot(t, cl, ns, "held") {
		if q(s.CPUReq).Cmp(*q("500m")) >= 0 {
			t.Errorf("pod %s still at %s after the hub came back", name, s.CPUReq)
		}
	}
}
