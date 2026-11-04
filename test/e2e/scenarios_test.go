//go:build e2e

package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

const deployment = "Deployment"

// overCPU is the CPU request of the over-provisioned test workloads, which burn ~50m. Kept small:
// all scenarios run at once on one 8-core spoke node next to ~2.6 cores of shared workloads.
const overCPU = "250m"

// overprovisioned: requests overCPU, burns ~5% of a core.
func overprovisioned(name string) app {
	return app{Name: name, CPULoad: 5, Requests: rl(overCPU, "64Mi"), Limits: rl("", "128Mi")}
}

// cpuBelow accepts a recommendation whose first container's CPU request is below limit.
func cpuBelow(limit string) func(*workloadRec) (bool, string) {
	return func(r *workloadRec) (bool, string) {
		got := r.Containers[0].Recommended.Requests.CPU
		return got != "" && q(got).Cmp(resource.MustParse(limit)) < 0, "cpu " + got
	}
}

// armed creates the policy with CPU in audit, waits for a recommendation that decreases CPU
// below `below` (a mature one: the first hub run may see a few samples only), then switches CPU to
// auto-patch, so the agent applies a representative recommendation.
func armed(t *testing.T, cl cluster, name, ns, workload, below string, spec v1alpha1.RightsizingPolicySpec) {
	t.Helper()
	armedFor(t, cl, corev1.ResourceCPU, name, ns, workload, below, spec)
}

// armedFor is armed for either resource: res starts in audit and switches to auto-patch once the
// hub recommends a request below `below`.
func armedFor(t *testing.T, cl cluster, res corev1.ResourceName, name, ns, workload, below string, spec v1alpha1.RightsizingPolicySpec) {
	t.Helper()
	setMode := func(s *v1alpha1.RightsizingPolicySpec, m v1alpha1.Mode) {
		if res == corev1.ResourceMemory {
			s.Memory.Mode = m
		} else {
			s.CPU.Mode = m
		}
	}
	setMode(&spec, v1alpha1.ModeAudit)
	p := policy(t, cl, name, ns, spec)
	cond := cpuBelow(below)
	if res == corev1.ResourceMemory {
		cond = memoryBelow(below)
	}
	rec := waitHubRecommendation(t, cl, ns, deployment, workload, cond)
	t.Logf("hub recommends %v for %s; switching %s to auto-patch", rec.Containers[0].Recommended.Requests, workload, res)
	eventually(t, time.Minute, 2*time.Second, "policy switched to auto-patch", func() (bool, string) {
		var cur v1alpha1.RightsizingPolicy
		if err := cl.C.Get(ctx(t), client.ObjectKeyFromObject(p), &cur); err != nil {
			return false, err.Error()
		}
		setMode(&cur.Spec, v1alpha1.ModeAutoPatch)
		if err := cl.C.Update(ctx(t), &cur); err != nil {
			return false, err.Error()
		}
		return true, ""
	})
}

// memoryBelow accepts a recommendation whose first container's memory request is below limit.
func memoryBelow(limit string) func(*workloadRec) (bool, string) {
	return func(r *workloadRec) (bool, string) {
		got := r.Containers[0].Recommended.Requests.Memory
		return got != "" && q(got).Cmp(resource.MustParse(limit)) < 0, "memory " + got
	}
}

// Scenario 1: audit mode. The hub recommends, nothing changes in the cluster.
func TestAudit(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "audit")
	policy(t, cl, ns, ns, v1alpha1.RightsizingPolicySpec{
		CPU:        &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit},
		Memory:     &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAudit},
		Guardrails: relaxed(),
	})
	deploy(t, cl, ns, overprovisioned("web"))
	before := snapshot(t, cl, ns, "web")
	gen := getDeployment(t, cl, ns, "web").Generation

	rec := waitHubRecommendation(t, cl, ns, deployment, "web", cpuBelow(overCPU))
	t.Logf("hub recommends cpu %s (current %s)", rec.Containers[0].Recommended.Requests.CPU, overCPU)
	// The agent has seen it and records why it does nothing.
	waitReason(t, cl, ns, deployment, "web", v1alpha1.ReasonModeAudit, 3*time.Minute)
	consistently(t, 45*time.Second, 5*time.Second, "audit leaves the workload alone", func() (bool, string) {
		after := snapshot(t, cl, ns, "web")
		if fmt.Sprint(after) != fmt.Sprint(before) {
			return false, fmt.Sprintf("pods changed: before %v after %v", before, after)
		}
		if g := getDeployment(t, cl, ns, "web").Generation; g != gen {
			return false, fmt.Sprintf("deployment generation %d -> %d", gen, g)
		}
		return true, ""
	})
	if r := rsr(t, cl, ns, deployment, "web"); r.Status.Phase == v1alpha1.PhaseApplied {
		t.Fatalf("audit workload reached phase Applied: %s", rsrStatus(r))
	}
}

// Scenario 2: in-place CPU decrease. Pods are resized, no restarts, the Deployment is untouched.
func TestInPlaceCPU(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "inplace")
	a := overprovisioned("web")
	a.Replicas = 2
	deploy(t, cl, ns, a)
	before := snapshot(t, cl, ns, "web")
	gen := getDeployment(t, cl, ns, "web").Generation
	armed(t, cl, ns, ns, "web", overCPU, autoCPU(v1alpha1.StrategyInPlace))

	r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	want := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
	if want.Cmp(resource.MustParse(overCPU)) >= 0 {
		t.Fatalf("applied cpu request %s is not a decrease", want.String())
	}
	after := snapshot(t, cl, ns, "web")
	if len(after) != len(before) {
		t.Fatalf("pods replaced: before %v after %v", before, after)
	}
	for name, b := range before {
		a, ok := after[name]
		switch {
		case !ok || a.UID != b.UID:
			t.Errorf("pod %s was recreated", name)
		case a.Restarts != b.Restarts:
			t.Errorf("pod %s restarted (%d -> %d)", name, b.Restarts, a.Restarts)
		case q(a.CPUReq).Cmp(want) != 0:
			t.Errorf("pod %s cpu request %s, want %s", name, a.CPUReq, want.String())
		}
	}
	// The kubelet actually applied it.
	for _, p := range pods(t, cl, ns, "web") {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Resources != nil && cs.Resources.Requests.Cpu().Cmp(want) != 0 {
				t.Errorf("pod %s: kubelet reports cpu request %s, want %s", p.Name, cs.Resources.Requests.Cpu(), want.String())
			}
		}
	}
	d := getDeployment(t, cl, ns, "web")
	if d.Generation != gen || templateCPU(d).Cmp(resource.MustParse(overCPU)) != 0 {
		t.Errorf("deployment changed: generation %d -> %d, template cpu %s", gen, d.Generation, templateCPU(d).String())
	}
	waitEvent(t, cl, ns, "web", "applied", func(e auditEvent) bool { return e.Strategy == string(v1alpha1.StrategyInPlace) })
}

// Scenario 3: in-place memory decrease on a JVM workload restarts the container, not the pod.
// On nodes with swap the agent must refuse in-place memory changes instead (D-007, WO-06).
func TestInPlaceJVMMemory(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "jvm")
	a := app{Name: "jvm", JVM: true, Requests: rl("200m", "512Mi"), Limits: rl("", "512Mi"), Resize: corev1.RestartContainer}
	deploy(t, cl, ns, a)
	before := snapshot(t, cl, ns, "jvm")
	policy(t, cl, ns, ns, v1alpha1.RightsizingPolicySpec{
		CPU:        &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit},
		Memory:     &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAutoPatch},
		Apply:      &v1alpha1.ApplyPolicy{Strategy: v1alpha1.StrategyInPlace},
		Guardrails: relaxed(),
	})

	if swap := nodeSwap(t, cl, ns, "jvm"); swap != "" {
		// Exercised path: the swap guard. Pods must stay untouched.
		waitReason(t, cl, ns, deployment, "jvm", v1alpha1.ReasonUnsupportedNode, env.ApplyTimeout)
		if after := snapshot(t, cl, ns, "jvm"); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("pods changed although in-place memory is blocked: %v -> %v", before, after)
		}
		t.Skipf("node %s; the agent correctly blocks in-place memory (UnsupportedNode). "+
			"The RestartContainer path needs a node without swap", swap)
	}

	r := waitPhase(t, cl, ns, deployment, "jvm", v1alpha1.PhaseApplied)
	want := r.Spec.Containers[0].Requests[corev1.ResourceMemory]
	if want.Cmp(resource.MustParse("512Mi")) >= 0 {
		t.Fatalf("applied memory request %s is not a decrease", want.String())
	}
	after := snapshot(t, cl, ns, "jvm")
	for name, b := range before {
		a, ok := after[name]
		switch {
		case !ok || a.UID != b.UID:
			t.Errorf("pod %s was recreated; RestartContainer should restart only the container", name)
		case a.Restarts != b.Restarts+1:
			t.Errorf("pod %s: container restarts %d -> %d, want exactly one restart", name, b.Restarts, a.Restarts)
		case q(a.MemReq).Cmp(want) != 0:
			t.Errorf("pod %s memory request %s, want %s", name, a.MemReq, want.String())
		}
	}
}

// WO-12: a JVM with a fixed maximum heap (-Xmx) is memory audit-only, whatever the policy says.
func TestJVMFixedHeapIsAuditOnly(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "jvmfixed")
	deploy(t, cl, ns, app{Name: "jvm", JVM: true, FixedHeap: true, Requests: rl("200m", "512Mi"), Limits: rl("", "512Mi"), Resize: corev1.RestartContainer})
	before := snapshot(t, cl, ns, "jvm")
	gen := getDeployment(t, cl, ns, "jvm").Generation
	policy(t, cl, ns, ns, v1alpha1.RightsizingPolicySpec{
		CPU:        &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit},
		Memory:     &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAutoPatch},
		Apply:      &v1alpha1.ApplyPolicy{Strategy: v1alpha1.StrategyRollout},
		Guardrails: relaxed(),
	})
	waitReason(t, cl, ns, deployment, "jvm", v1alpha1.ReasonJVMFixedHeap, env.ApplyTimeout)
	if after := snapshot(t, cl, ns, "jvm"); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("pods changed although memory is audit-only: %v -> %v", before, after)
	}
	if d := getDeployment(t, cl, ns, "jvm"); d.Generation != gen {
		t.Errorf("deployment changed (generation %d -> %d) although memory is audit-only", gen, d.Generation)
	}
}

// nodeSwap returns "<node> has <n> swap" if a pod of the app runs on a node reporting swap.
func nodeSwap(t *testing.T, cl cluster, ns, app string) string {
	t.Helper()
	for _, p := range pods(t, cl, ns, app) {
		var n corev1.Node
		must(t, cl.C.Get(ctx(t), client.ObjectKey{Name: p.Spec.NodeName}, &n))
		if sw := n.Status.NodeInfo.Swap; sw != nil && sw.Capacity != nil && *sw.Capacity > 0 {
			return fmt.Sprintf("%s reports %s swap", n.Name, resource.NewQuantity(*sw.Capacity, resource.BinarySI))
		}
	}
	return ""
}

// Scenario 4: a resize the node can never fit (kubelet: PodResizePending/Infeasible) falls back to
// rollout and records a `fallback` event. The policy's CPU floor (guardrails.minCPU) is set to
// twice the node's capacity, so whatever the hub recommends the agent resizes to 16 cores.
func TestInPlaceFallback(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "fallback")
	deploy(t, cl, ns, overprovisioned("web"))
	floor := resource.MustParse(getenv("E2E_INFEASIBLE_CPU", "16"))
	spec := autoCPU(v1alpha1.StrategyInPlace)
	spec.Guardrails.MinCPU = &floor
	policy(t, cl, ns, ns, spec)

	ev := waitEventFor(t, cl, ns, "web", "fallback", env.ApplyTimeout, func(e auditEvent) bool {
		return e.Reason == v1alpha1.ReasonResizeInfeasible
	})
	t.Logf("fallback event: %s", ev.Message)
	var r *v1alpha1.RightsizingRecommendation
	eventually(t, 2*time.Minute, 5*time.Second, "RSR switched to rollout", func() (bool, string) {
		r = rsr(t, cl, ns, deployment, "web")
		return r != nil && r.Spec.Strategy == v1alpha1.StrategyRollout, rsrStatus(r)
	})
	eventually(t, 2*time.Minute, 5*time.Second, "template carries the infeasible value", func() (bool, string) {
		got := templateCPU(getDeployment(t, cl, ns, "web"))
		return got.Cmp(floor) == 0, "template cpu " + got.String()
	})
	// The new ReplicaSet's pod can't be scheduled (16 cores); the old pod keeps serving.
	if n := len(pods(t, cl, ns, "web")); n == 0 {
		t.Error("no pods left: the rollout should keep the old pod while the new one is pending")
	}
}

// Scenario 5: rollout. New ReplicaSet with the new values; field manager resource-optimizer
// owns the resource fields of the template.
func TestRollout(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "rollout")
	deploy(t, cl, ns, overprovisioned("web"))
	armed(t, cl, ns, ns, "web", overCPU, autoCPU(v1alpha1.StrategyRollout))

	r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	want := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
	waitReady(t, cl, ns, "web", 3*time.Minute)
	d := getDeployment(t, cl, ns, "web")
	if templateCPU(d).Cmp(want) != 0 || want.Cmp(resource.MustParse(overCPU)) >= 0 {
		t.Fatalf("template cpu %s, recommendation %s: want a decrease rolled out", templateCPU(d).String(), want.String())
	}
	if !ownsCPURequest(d.ManagedFields) {
		t.Fatalf("field manager %q does not own the cpu request: %s", fieldManager, managers(d.ManagedFields))
	}
	for _, p := range pods(t, cl, ns, "web") {
		if c := p.Spec.Containers[0].Resources.Requests.Cpu(); c.Cmp(want) != 0 {
			t.Errorf("pod %s cpu %s, want %s (new ReplicaSet)", p.Name, c.String(), want.String())
		}
	}
	if n := replicaSets(t, cl, ns, "web"); n < 2 {
		t.Errorf("%d ReplicaSets, want a new one for the rollout", n)
	}
	waitEvent(t, cl, ns, "web", "applied", func(e auditEvent) bool { return e.Strategy == string(v1alpha1.StrategyRollout) })
}

// Scenarios 6 and 7: Argo CD. After a rollout patch a hard sync keeps our values and the
// Application stays Synced; after switching to audit the next sync restores the Git values.
func TestArgoCD(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "argocd")
	d := overprovisioned("web").deployment(ns)
	d.Namespace = ""
	d.APIVersion, d.Kind = "apps/v1", "Deployment"
	manifest, err := toYAML(d)
	must(t, err)
	gitWrite(t, ns, map[string][]byte{"deployment.yaml": manifest}, "e2e: add "+ns)
	app := argoApp(t, cl, ns)
	waitAppSynced(t, cl, app, 4*time.Minute)
	waitReady(t, cl, ns, "web", 3*time.Minute)
	armed(t, cl, ns, ns, "web", overCPU, autoCPU(v1alpha1.StrategyRollout))

	r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	applied := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
	if templateCPU(getDeployment(t, cl, ns, "web")).Cmp(applied) != 0 {
		t.Fatalf("template does not carry the applied value %s", applied.String())
	}

	t.Run("hard sync keeps values", func(t *testing.T) {
		hardSync(t, cl, app)
		waitAppSynced(t, cl, app, 2*time.Minute)
		if got := templateCPU(getDeployment(t, cl, ns, "web")); got.Cmp(applied) != 0 {
			t.Fatalf("after a hard sync the template cpu is %s, want %s (reverted to Git?)", got.String(), applied.String())
		}
		// And self-heal (60 s reconciliation) does not revert it either.
		consistently(t, 75*time.Second, 5*time.Second, "values survive self-heal", func() (bool, string) {
			got := templateCPU(getDeployment(t, cl, ns, "web"))
			s, _ := getApp(t, cl, app)
			return got.Cmp(applied) == 0 && s.Sync == "Synced", fmt.Sprintf("cpu %s, app %+v", got.String(), s)
		})
	})

	t.Run("release restores Git values", func(t *testing.T) {
		var p v1alpha1.RightsizingPolicy
		must(t, cl.C.Get(ctx(t), client.ObjectKey{Name: ns}, &p))
		p.Spec.CPU.Mode = v1alpha1.ModeAudit
		must(t, cl.C.Update(ctx(t), &p))
		eventually(t, 3*time.Minute, 5*time.Second, "agent releases field ownership", func() (bool, string) {
			d := getDeployment(t, cl, ns, "web")
			return !ownsCPURequest(d.ManagedFields), managers(d.ManagedFields)
		})
		hardSync(t, cl, app)
		eventually(t, 3*time.Minute, 5*time.Second, "Git value "+overCPU+" restored", func() (bool, string) {
			got := templateCPU(getDeployment(t, cl, ns, "web"))
			return got.Cmp(resource.MustParse(overCPU)) == 0, "template cpu " + got.String()
		})
		waitAppSynced(t, cl, app, 2*time.Minute)
	})
}

// Scenario 9: guardrails.
func TestGuardrails(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1

	t.Run("throttled is never decreased", func(t *testing.T) {
		t.Parallel()
		ns := newNamespace(t, cl, "throttled")
		// Wants a full core, limit 200m: heavily CFS-throttled.
		deploy(t, cl, ns, app{Name: "web", CPULoad: 100, Requests: rl("100m", "64Mi"), Limits: rl("200m", "128Mi")})
		policy(t, cl, ns, ns, autoCPU(v1alpha1.StrategyInPlace))
		rec := waitHubRecommendation(t, cl, ns, deployment, "web", func(r *workloadRec) (bool, string) {
			return slices.Contains(r.RiskFlags, "throttled"), fmt.Sprintf("flags %v", r.RiskFlags)
		})
		if got := q(rec.Containers[0].Recommended.Requests.CPU); got.Cmp(resource.MustParse("100m")) <= 0 {
			t.Fatalf("hub recommends cpu %s for a throttled workload at 100m", got.String())
		}
		r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
		applied := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
		if applied.Cmp(resource.MustParse("100m")) <= 0 {
			t.Fatalf("throttled workload got cpu %s (<= 100m)", applied.String())
		}
		for _, p := range pods(t, cl, ns, "web") {
			if c := p.Spec.Containers[0].Resources.Requests.Cpu(); c.Cmp(resource.MustParse("100m")) < 0 {
				t.Errorf("pod %s cpu decreased to %s", p.Name, c.String())
			}
		}
	})

	t.Run("hpa-coupled is untouched", func(t *testing.T) {
		t.Parallel()
		ns := newNamespace(t, cl, "hpa")
		deploy(t, cl, ns, app{Name: "web", Replicas: 2, CPULoad: 5, Requests: rl("200m", "64Mi"), Limits: rl("", "128Mi")})
		hpa(t, cl, ns, "web", 60)
		before := snapshot(t, cl, ns, "web")
		gen := getDeployment(t, cl, ns, "web").Generation
		policy(t, cl, ns, ns, autoCPU(v1alpha1.StrategyInPlace))
		waitReason(t, cl, ns, deployment, "web", v1alpha1.ReasonHPACoupled, env.ApplyTimeout)
		for name, b := range before {
			if a, ok := snapshot(t, cl, ns, "web")[name]; ok && a.CPUReq != b.CPUReq {
				t.Errorf("pod %s resized %s -> %s despite the HPA", name, b.CPUReq, a.CPUReq)
			}
		}
		d := getDeployment(t, cl, ns, "web")
		if templateCPU(d).Cmp(resource.MustParse("200m")) != 0 || ownsCPURequest(d.ManagedFields) {
			t.Errorf("deployment changed despite the HPA (generation %d -> %d, cpu %s)", gen, d.Generation, templateCPU(d).String())
		}
		if r := rsr(t, cl, ns, deployment, "web"); r.Status.Phase == v1alpha1.PhaseApplied {
			t.Errorf("hpa-coupled workload reached Applied")
		}
	})

	t.Run("max change percent is respected", func(t *testing.T) {
		t.Parallel()
		ns := newNamespace(t, cl, "maxchange")
		deploy(t, cl, ns, overprovisioned("web"))
		spec := autoCPU(v1alpha1.StrategyInPlace)
		spec.Guardrails.MaxChangePercent = ptr(int32(20))
		// Wait for a recommendation well below the 20% cap so the cap is what limits the change.
		armed(t, cl, ns, ns, "web", "150m", spec)
		rec := waitHubRecommendation(t, cl, ns, deployment, "web", nil)
		r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
		applied := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
		floor := resource.MustParse(overCPU)
		floor.SetMilli(floor.MilliValue() * 80 / 100)
		t.Logf("hub recommends %s, applied %s (max change 20%% of %s)", rec.Containers[0].Recommended.Requests.CPU, applied.String(), overCPU)
		if applied.Cmp(floor) < 0 || applied.Cmp(resource.MustParse(overCPU)) >= 0 {
			t.Fatalf("applied cpu %s, want a decrease capped at 20%% (%s..%s)", applied.String(), floor.String(), overCPU)
		}
	})
}

// waitEvent waits for a hub audit event (the agent reports within seconds; the default timeout
// covers a hub that is slow to store). Use waitEventFor when the event depends on a first
// recommendation.
func waitEvent(t *testing.T, cl cluster, ns, name, typ string, match func(auditEvent) bool) auditEvent {
	t.Helper()
	return waitEventFor(t, cl, ns, name, typ, 3*time.Minute, match)
}

func waitEventFor(t *testing.T, cl cluster, ns, name, typ string, timeout time.Duration, match func(auditEvent) bool) auditEvent {
	t.Helper()
	var found auditEvent
	eventually(t, timeout, 5*time.Second, fmt.Sprintf("hub audit event %q for %s/%s", typ, ns, name), func() (bool, string) {
		evs, err := env.Hub.audit(ctx(t), cl.Name, ns, deployment, name, typ)
		if err != nil {
			return false, err.Error()
		}
		var seen []string
		for _, e := range evs {
			if match == nil || match(e) {
				found = e
				return true, ""
			}
			seen = append(seen, e.Reason+"/"+e.Strategy)
		}
		return false, fmt.Sprintf("%d events: %s", len(evs), strings.Join(seen, ", "))
	})
	return found
}
