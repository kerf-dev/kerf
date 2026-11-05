//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kerf-dev/kerf/api/v1alpha1"
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
	agentNamespace   = getenv("E2E_AGENT_NAMESPACE", "kerf")
	agentTokenSecret = getenv("E2E_AGENT_TOKEN_SECRET", "kerf-agent-token")
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
		if err := releaseFields(c, cl, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}); err != nil {
			t.Errorf("%v (make e2e-unblock drops it)", err)
		}
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

// agentDeployment is the agent's Deployment on every spoke (agent chart, release "agent").
var agentDeployment = getenv("E2E_AGENT_DEPLOYMENT", "kerf-agent")

// agentFlag returns the duration flag --name of the agent running on cl (its Deployment args), so
// the suite waits for the agent's actual timers (dev values) instead of assuming them.
func agentFlag(t *testing.T, cl cluster, name string, def time.Duration) time.Duration {
	t.Helper()
	var d appsv1.Deployment
	must(t, cl.C.Get(ctx(t), client.ObjectKey{Namespace: agentNamespace, Name: agentDeployment}, &d))
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, a := range c.Args {
			if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
				got, err := time.ParseDuration(v)
				must(t, err)
				return got
			}
		}
	}
	return def
}

// tokenNudgeAnnotation is set on the agent pods while the hub is blocked and removed afterwards
// (nudgePods): the kubelet refreshes the mounted token Secret at once instead of at its next
// periodic sync. The agent re-reads the token file on every request. `make e2e-unblock` removes a
// leftover annotation.
const tokenNudgeAnnotation = "e2e.kerf.getkerf.dev/token-nudge"

// nudgeAgent sets (value != "") or removes the token nudge annotation on the agent's pods.
func nudgeAgent(t *testing.T, cl cluster, value string) {
	t.Helper()
	var d appsv1.Deployment
	if err := cl.C.Get(ctx(t), client.ObjectKey{Namespace: agentNamespace, Name: agentDeployment}, &d); err != nil {
		t.Logf("nudge agent on %s: %v (the kubelet refreshes the token at its next sync)", cl.Name, err)
		return
	}
	nudgePods(t, cl, agentNamespace, d.Spec.Selector.MatchLabels, tokenNudgeAnnotation, value)
}

// heartbeatEvery is how often the hub records an agent's requests at most (internal/hub/server.go).
const heartbeatEvery = 15 * time.Second

// waitCutOff waits until the hub stops seeing the agent's requests and returns the last
// heartbeat it recorded. The agent's last successful pull is before that heartbeat plus
// heartbeatEvery.
func waitCutOff(t *testing.T, cl cluster) time.Time {
	t.Helper()
	var last time.Time
	eventually(t, 4*time.Minute, 3*time.Second, "hub stops seeing the "+cl.Name+" agent", func() (bool, string) {
		a := clusterAgent(t, cl.Name)
		if a.LastHeartbeatAt == nil {
			return false, "no heartbeat"
		}
		last = *a.LastHeartbeatAt
		// The agent reports every few seconds; the hub records it at most every heartbeatEvery.
		quiet := time.Since(last)
		return quiet > heartbeatEvery+10*time.Second, fmt.Sprintf("last heartbeat %s ago", quiet.Round(time.Second))
	})
	return last
}

// cutOff blocks the agent's token (again, after someone restored it), nudges the kubelet and
// returns when the agent will count the hub as unreachable: the last heartbeat plus
// heartbeatEvery plus hubStaleAfter (and a small margin).
func cutOff(t *testing.T, cl cluster, staleAfter time.Duration) time.Time {
	t.Helper()
	nudgeAgent(t, cl, time.Now().UTC().Format(time.RFC3339Nano))
	last := waitCutOff(t, cl)
	return last.Add(heartbeatEvery + staleAfter + 5*time.Second)
}

// holdOutage keeps the agent cut off until it counts the hub as unreachable (staleAfter after its
// last successful pull), without interruption. Other tracks redeploy the shared agent (helm
// upgrade rewrites the token Secret): when that happens the token is swapped again and the wait
// starts over, up to three times.
func holdOutage(t *testing.T, cl cluster, staleAfter time.Duration) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		start := time.Now()
		until := cutOff(t, cl, staleAfter)
		t.Logf("agent on %s cut off; it counts the hub as unreachable from %s (hubStaleAfter %s)",
			cl.Name, until.Format(time.TimeOnly), staleAfter)
		intact := true
		for time.Now().Before(until) {
			if agentToken(t, cl) != invalidToken {
				intact = false
				break
			}
			time.Sleep(min(5*time.Second, max(time.Until(until), 0)))
		}
		if intact {
			t.Logf("hub outage held for %s", time.Since(start).Round(time.Second))
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
	// The agent's timers (dev values: deploy/charts/agent/values-dev.yaml); E2E_HUB_STALE_AFTER
	// overrides the stale threshold.
	staleAfter := agentFlag(t, cl, "hub-stale-after", 5*time.Minute)
	if v := os.Getenv("E2E_HUB_STALE_AFTER"); v != "" {
		var err error
		staleAfter, err = time.ParseDuration(v)
		must(t, err)
	}
	pull := agentFlag(t, cl, "pull-interval", time.Minute)
	t.Logf("agent on %s: hubStaleAfter %s, pullInterval %s", cl.Name, staleAfter, pull)

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
	// The apply and, below, the outage must not overlap the other spoke-2 scenarios (lockSpoke2).
	// Both recommendations before the first lock: while other spoke-2 scenarios hold the lock they
	// may also hold spoke-2 recomputes (TestAdminPause).
	waitHubRecommendation(t, cl, ns, deployment, "applied", cpuBelow(overCPU))
	// "held" must have a representative recommendation cached by the agent before the outage.
	waitHubRecommendation(t, cl, ns, deployment, "held", cpuBelow(overCPU))
	unlock := lockSpoke2(t)
	armed(t, cl, ns+"-applied", ns, "applied", overCPU, pa)
	waitPhase(t, cl, ns, deployment, "applied", v1alpha1.PhaseApplied)
	unlock()
	waitReason(t, cl, ns, deployment, "held", v1alpha1.ReasonModeAudit, 3*time.Minute)
	sleep(t, 2*pull, "at least one agent pull after that recommendation")
	appliedPods := snapshot(t, cl, ns, "applied")
	heldPods := snapshot(t, cl, ns, "held")

	lockSpoke2(t)
	restore := blockHub(t, cl)
	t.Cleanup(func() { nudgeAgent(t, cl, "") })
	holdOutage(t, cl, staleAfter)

	// A change that would apply now: switch "held" to auto-patch.
	var p v1alpha1.RightsizingPolicy
	must(t, cl.C.Get(ctx(t), client.ObjectKey{Name: ns}, &p))
	p.Spec.CPU.Mode = v1alpha1.ModeAutoPatch
	must(t, cl.C.Update(ctx(t), &p))
	waitReason(t, cl, ns, deployment, "held", v1alpha1.ReasonHubUnreachable, 3*time.Minute)
	// Nothing changes for at least three agent pulls (two before WO-30's shorter dev pull).
	consistently(t, max(3*pull, 30*time.Second), 5*time.Second, "nothing applied, nothing reverted", func() (bool, string) {
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
	nudgeAgent(t, cl, "") // the kubelet refreshes the restored token now
	waitPhase(t, cl, ns, deployment, "held", v1alpha1.PhaseApplied)
	for name, s := range snapshot(t, cl, ns, "held") {
		if q(s.CPUReq).Cmp(*q(overCPU)) >= 0 {
			t.Errorf("pod %s still at %s after the hub came back", name, s.CPUReq)
		}
	}
}
