package controller

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// Approve once when a recommender run supersedes the approval around the agent's apply.
// The stub hub removes an approval when it stores its applied/ApprovedOnce event, as the hub
// marks it consumed; a superseded approval is one the stub stops sending.

// inPlaceAudit deploys an audit in-place workload (memory off) with one running pod and serves
// rec, then waits for the audit skip. It returns the pod name.
func inPlaceAudit(t *testing.T, name string, rec hubclient.WorkloadRecommendation) string {
	t.Helper()
	u := deploymentApply(name, map[string]string{v1alpha1.AnnotationStrategy: "in-place", v1alpha1.AnnotationMemoryMode: "off"}, "500m", "256Mi")
	if err := argoApply(t, u, false); err != nil {
		t.Fatal(err)
	}
	pod := makePod(t, getDeploy(t, name))
	setRec(rec)
	eventually(t, 10*time.Second, "skipped in audit", wantEvent(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit))
	return pod.Name
}

func approvalOf(name, id string, rec hubclient.WorkloadRecommendation) hubclient.ConfigApproval {
	return hubclient.ConfigApproval{ID: id, RecommendationID: recID(rec), ApprovedBy: "bob", ApprovedAt: time.Now(),
		Workload: hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: name}, ExpiresAt: time.Now().Add(time.Hour)}
}

// podResized waits until the agent submitted the resize of pod to cpu.
func podResized(t *testing.T, pod, cpu string) {
	t.Helper()
	eventually(t, 20*time.Second, "pod resize submitted", func() error {
		if got := getPod(t, pod).Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != cpu {
			return fmt.Errorf("pod cpu request %s", got.String())
		}
		return nil
	})
}

// approvedEvent waits for the applied/ApprovedOnce event of approval id and returns it.
func approvedEvent(t *testing.T, name, id string, timeout time.Duration) hubclient.Event {
	t.Helper()
	var ev hubclient.Event
	eventually(t, timeout, "applied/ApprovedOnce for approval "+id, func() error {
		for _, e := range eventsFor(name) {
			if e.Type == hubclient.EventApplied && e.Details[hubclient.DetailApprovalID] == id {
				ev = e
				return nil
			}
		}
		return fmt.Errorf("none; events %v", eventsFor(name))
	})
	return ev
}

// noApprovalLeft waits until the stub hub holds no approval (all consumed).
func noApprovalLeft(t *testing.T) {
	t.Helper()
	eventually(t, 5*time.Second, "approvals consumed", func() error {
		if a := stub.Config(testCluster).Approvals; len(a) != 0 {
			return fmt.Errorf("approvals left: %+v", a)
		}
		return nil
	})
}

// checkApprovedEvents: every applied event that consumes an approval names the approved
// recommendation, and only one applied event changed values.
func checkApprovedEvents(t *testing.T, name string, approved map[string]string) {
	t.Helper()
	changes := 0
	for _, e := range eventsFor(name) {
		if e.Type != hubclient.EventApplied {
			continue
		}
		if len(e.Containers) > 0 {
			changes++
		}
		id := e.Details[hubclient.DetailApprovalID]
		if want, ok := approved[id]; !ok || e.RecommendationID != want || e.Reason != v1alpha1.ReasonApprovedOnce {
			t.Errorf("applied event %s: recommendation %s, approval %q (approved %q)", e.Reason, e.RecommendationID, id, want)
		}
	}
	if changes != 1 {
		t.Errorf("%d applied events with changes, want 1", changes)
	}
}

// TestApproveOnceSupersededAfterApply reproduces the TestApproveOnce e2e flake: approval A is
// applied, a recommender run issues B with the same values and supersedes A before the hub stores
// A's event, and the operator approves B. B's values are live already: the agent changes nothing
// and B is consumed by an applied/ApprovedOnce event that names B.
func TestApproveOnceSupersededAfterApply(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "approve-race-after"
	recA := recFor(name, "250m", "256Mi")
	pod := inPlaceAudit(t, name, recA)
	a := approvalOf(name, "6c1d0000-0000-4000-8000-000000000291", recA)
	setConfig(t, hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{a}})
	podResized(t, pod, "250m")
	actuate(t, name)
	approvedEvent(t, name, a.ID, 20*time.Second)
	eventually(t, 10*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))

	recB := recA
	recB.ID = "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b0291"
	setRec(recB)
	b := approvalOf(name, "6c1d0000-0000-4000-8000-000000000292", recB)
	setConfig(t, hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{b}})

	ev := approvedEvent(t, name, b.ID, 20*time.Second)
	if ev.RecommendationID != b.RecommendationID {
		t.Fatalf("approval B consumed by an event for recommendation %s, want %s", ev.RecommendationID, b.RecommendationID)
	}
	noApprovalLeft(t)
	time.Sleep(time.Second) // several reconciles
	checkApprovedEvents(t, name, map[string]string{a.ID: a.RecommendationID, b.ID: b.RecommendationID})
	if got := getPod(t, pod).Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "250m" {
		t.Fatalf("pod cpu %s", got.String())
	}
	rsr, _ := getRSR(name)
	if rsr.Status.AppliedRecommendationID != b.RecommendationID || !hasCondReason(rsr, v1alpha1.ConditionApplied, v1alpha1.ReasonApprovedOnce) {
		t.Fatalf("RSR applied %s, conditions %+v", rsr.Status.AppliedRecommendationID, rsr.Status.Conditions)
	}
}

// TestApproveOnceSupersededInFlight: the recommender run supersedes approval A while A's in-place
// resize is in flight, and the operator approves B (same values). A's change finishes under A (its
// event names A), and B, whose values are then live, is consumed without a second change.
func TestApproveOnceSupersededInFlight(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "approve-race-flight"
	recA := recFor(name, "250m", "256Mi")
	pod := inPlaceAudit(t, name, recA)
	a := approvalOf(name, "6c1d0000-0000-4000-8000-000000000293", recA)
	setConfig(t, hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{a}})
	podResized(t, pod, "250m")
	if err := wantPhase(name, v1alpha1.PhaseApplying)(); err != nil {
		t.Fatal(err)
	}

	recB := recA
	recB.ID = "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b0293"
	setRec(recB)
	b := approvalOf(name, "6c1d0000-0000-4000-8000-000000000294", recB)
	setConfig(t, hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{b}})
	time.Sleep(time.Second) // reconciles with A in flight, B approved
	actuate(t, name)

	evA := approvedEvent(t, name, a.ID, 20*time.Second)
	evB := approvedEvent(t, name, b.ID, 20*time.Second)
	if evA.RecommendationID != a.RecommendationID || evB.RecommendationID != b.RecommendationID {
		t.Fatalf("events name %s (approval A) and %s (approval B)", evA.RecommendationID, evB.RecommendationID)
	}
	noApprovalLeft(t)
	time.Sleep(time.Second)
	checkApprovedEvents(t, name, map[string]string{a.ID: a.RecommendationID, b.ID: b.RecommendationID})
}

// TestApproveOnceSupersededInFlightNotReapproved: approval A is superseded while its resize is in
// flight and nobody approves B. The agent acted on A: the change finishes and A is consumed by an
// event that names A; B (audit) is not applied.
func TestApproveOnceSupersededInFlightNotReapproved(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "approve-race-dropped"
	recA := recFor(name, "250m", "256Mi")
	pod := inPlaceAudit(t, name, recA)
	a := approvalOf(name, "6c1d0000-0000-4000-8000-000000000295", recA)
	setConfig(t, hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{a}})
	podResized(t, pod, "250m")

	recB := recFor(name, "200m", "256Mi")
	recB.ID = "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b0295"
	setRec(recB)
	setConfig(t, hubclient.ClusterConfig{}) // A superseded
	time.Sleep(time.Second)
	actuate(t, name)

	ev := approvedEvent(t, name, a.ID, 20*time.Second)
	if ev.RecommendationID != a.RecommendationID {
		t.Fatalf("approval A consumed by an event for %s", ev.RecommendationID)
	}
	eventually(t, 10*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))
	time.Sleep(time.Second)
	checkApprovedEvents(t, name, map[string]string{a.ID: a.RecommendationID})
	if got := getPod(t, pod).Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "250m" {
		t.Fatalf("B applied without approval: pod cpu %s", got.String())
	}
}

func hasCondReason(r *v1alpha1.RightsizingRecommendation, typ, reason string) bool {
	for _, c := range r.Status.Conditions {
		if c.Type == typ {
			return c.Reason == reason
		}
	}
	return false
}
