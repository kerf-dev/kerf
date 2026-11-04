//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// The hub outage is simulated on spoke 2 only, by replacing the agent's hub token with an invalid
// one: every pull fails (401), exactly as if the hub were gone, and the agent re-reads the token
// file on each request. A NetworkPolicy is not used: the k3s policy controller keeps ESTABLISHED
// connections open, and the agent's keep-alive connection to the hub never goes idle, so blocking
// the hub endpoint only works when no connection happens to be open.
// The token is put back at the end; `make e2e-unblock` restores it from the hub's dev token Secret
// if a run was killed.
const invalidToken = "e2e-hub-unreachable-invalid-token"

var (
	agentNamespace   = getenv("E2E_AGENT_NAMESPACE", "resource-optimizer")
	agentTokenSecret = getenv("E2E_AGENT_TOKEN_SECRET", "resource-optimizer-agent-token")
)

// blockHub cuts the agent off from the hub and returns the function that restores it.
func blockHub(t *testing.T, cl cluster) func() {
	t.Helper()
	key := client.ObjectKey{Namespace: agentNamespace, Name: agentTokenSecret}
	var sec corev1.Secret
	must(t, cl.C.Get(ctx(t), key, &sec))
	original := append([]byte(nil), sec.Data["token"]...)
	if string(original) == invalidToken {
		t.Fatalf("the agent token on %s is still the e2e invalid token; run `make e2e-unblock`", cl.Name)
	}
	sec.Data["token"] = []byte(invalidToken)
	must(t, cl.C.Update(ctx(t), &sec))
	t.Logf("agent on %s cut off from the hub (token Secret %s replaced)", cl.Name, key)
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var cur corev1.Secret
		err := cl.C.Get(c, key, &cur)
		if err == nil {
			cur.Data["token"] = original
			err = cl.C.Update(c, &cur)
		}
		if err != nil {
			t.Errorf("RESTORE the agent token on %s BY HAND (make e2e-unblock): %v", cl.Name, err)
			return
		}
		t.Logf("agent on %s reconnected to the hub (token restored)", cl.Name)
	}
	t.Cleanup(restore)
	return restore
}

// agentToken returns the token the agent's Secret holds now.
func agentToken(t *testing.T, cl cluster) string {
	t.Helper()
	var sec corev1.Secret
	if err := cl.C.Get(ctx(t), client.ObjectKey{Namespace: agentNamespace, Name: agentTokenSecret}, &sec); err != nil {
		return "error: " + err.Error()
	}
	return string(sec.Data["token"])
}

// holdOutage keeps the agent cut off for d without interruption. Other tracks redeploy the
// shared agent (helm upgrade rewrites the token Secret): when that happens the token is swapped
// again and the wait starts over, up to three times.
func holdOutage(t *testing.T, cl cluster, d time.Duration) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		start := time.Now()
		intact := true
		for time.Since(start) < d {
			if agentToken(t, cl) != invalidToken {
				intact = false
				break
			}
			time.Sleep(10 * time.Second)
		}
		if intact {
			return
		}
		if attempt == 2 {
			t.Fatalf("the agent token on %s keeps being restored (agent redeployed?); cannot hold a hub outage", cl.Name)
		}
		t.Logf("the agent token on %s was restored by someone else after %s (agent redeployed?); swapping it again and restarting the wait",
			cl.Name, time.Since(start).Round(time.Second))
		var sec corev1.Secret
		must(t, cl.C.Get(ctx(t), client.ObjectKey{Namespace: agentNamespace, Name: agentTokenSecret}, &sec))
		sec.Data["token"] = []byte(invalidToken)
		must(t, cl.C.Update(ctx(t), &sec))
	}
}

// Scenario 11: hub unreachable. The agent applies nothing new and reverts nothing; once the
// hub is back, the pending change goes through.
func TestHubUnreachable(t *testing.T) {
	t.Parallel()
	cl := env.Spoke2
	ns := newNamespace(t, cl, "hubdown")
	staleAfter, err := time.ParseDuration(getenv("E2E_HUB_STALE_AFTER", "5m"))
	must(t, err)

	// "applied": auto-patch before the outage; "held": audit until the hub is gone.
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
	armed(t, cl, ns+"-applied", ns, "applied", "500m", pa)

	waitPhase(t, cl, ns, deployment, "applied", v1alpha1.PhaseApplied)
	// "held" must have a representative recommendation cached by the agent before the outage.
	waitHubRecommendation(t, cl, ns, deployment, "held", cpuBelow("500m"))
	waitReason(t, cl, ns, deployment, "held", v1alpha1.ReasonModeAudit, 3*time.Minute)
	time.Sleep(time.Minute) // at least one agent pull after that recommendation
	appliedPods := snapshot(t, cl, ns, "applied")
	heldPods := snapshot(t, cl, ns, "held")

	restore := blockHub(t, cl)
	// hubStaleAfter, plus up to ~1 min for the kubelet to refresh the mounted Secret.
	wait := staleAfter + 90*time.Second
	t.Logf("waiting %s for the agent to consider the hub unreachable (hubStaleAfter %s)", wait, staleAfter)
	holdOutage(t, cl, wait)

	// A change that would apply now: switch "held" to auto-patch.
	var p v1alpha1.RightsizingPolicy
	must(t, cl.C.Get(ctx(t), client.ObjectKey{Name: ns}, &p))
	p.Spec.CPU.Mode = v1alpha1.ModeAutoPatch
	must(t, cl.C.Update(ctx(t), &p))
	waitReason(t, cl, ns, deployment, "held", v1alpha1.ReasonHubUnreachable, 3*time.Minute)
	consistently(t, time.Minute, 5*time.Second, "nothing applied, nothing reverted", func() (bool, string) {
		if agentToken(t, cl) != invalidToken {
			return false, "the agent token was restored by someone else during the outage (agent redeployed?); result not meaningful"
		}
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
