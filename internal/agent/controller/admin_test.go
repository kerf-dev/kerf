package controller

import (
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/apply"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
	"github.com/kerf-dev/kerf/internal/agent/hubstub"
)

// setConfig serves cfg to the agent and waits until it pulled that version.
func setConfig(t *testing.T, cfg hubclient.ClusterConfig) string {
	t.Helper()
	stub.SetConfig(testCluster, &cfg)
	want := stub.Config(testCluster).ConfigVersion
	eventually(t, 5*time.Second, "config "+want+" pulled", func() error {
		if got := adminState.ConfigVersion(); got != want {
			return fmt.Errorf("agent has config %q", got)
		}
		return nil
	})
	return want
}

// resetConfig serves an empty config at the end of a test (no hub policies, not paused).
func resetConfig(t *testing.T) {
	t.Cleanup(func() {
		setConfig(t, hubclient.ClusterConfig{})
		eventually(t, 5*time.Second, "hub CRs deleted", func() error {
			var list v1alpha1.RightsizingPolicyList
			if err := k8s.List(testCtx, &list, client.MatchingLabels{v1alpha1.LabelManagedBy: v1alpha1.ManagedByHub}); err != nil {
				return err
			}
			if len(list.Items) > 0 {
				return fmt.Errorf("%d hub CRs left", len(list.Items))
			}
			return nil
		})
	})
}

func recID(r hubclient.WorkloadRecommendation) string {
	return hubstub.Normalize([]hubclient.WorkloadRecommendation{r}, time.Now())[0].ID
}

// TestHubPolicyMaterialised: a policy from the hub appears as a CR within one pull,
// governs its workloads, and is reported in the inventory; a workload override layers on top.
func TestHubPolicyMaterialised(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "hub-governed"
	u := deploymentApply(name, nil, "500m", "256Mi")
	u.SetLabels(map[string]string{"hubpol": name})
	if err := argoApply(t, u, false); err != nil {
		t.Fatal(err)
	}
	git := &v1alpha1.RightsizingPolicy{}
	git.Name = "git-hubtest"
	git.Spec.Priority = 5
	git.Spec.Selector.Namespaces = []string{"nowhere"}
	if err := k8s.Create(testCtx, git); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k8s.Delete(testCtx, git) }()

	pol := hubclient.ConfigPolicy{ID: "0192f7c4-1111-7000-8000-000000000001", Name: "hub-rollout", Type: v1alpha1.HubPolicyTypePolicy,
		Version: 1, Priority: 500, Spec: v1alpha1.RightsizingPolicySpec{Priority: 500,
			Selector: v1alpha1.PolicySelector{MatchLabels: map[string]string{"hubpol": name}},
			CPU:      &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAutoPatch},
			Apply:    &v1alpha1.ApplyPolicy{Strategy: v1alpha1.StrategyRollout}}}
	start := time.Now()
	version := setConfig(t, hubclient.ClusterConfig{Policies: []hubclient.ConfigPolicy{pol}})
	eventually(t, 2*time.Second, "CR materialised", func() error {
		var p v1alpha1.RightsizingPolicy
		return k8s.Get(testCtx, client.ObjectKey{Name: "hub-rollout"}, &p)
	})
	t.Logf("CR materialised %s after the config changed", time.Since(start).Round(time.Millisecond))

	setRec(recFor(name, "250m", "200Mi"))
	eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))
	if d := getDeploy(t, name); cpuReq(d) != "250m" || memReq(d) != "256Mi" {
		t.Fatalf("template %s %s", cpuReq(d), memReq(d))
	}
	rsr, _ := getRSR(name)
	if rsr.Status.Effective.Policy != "hub-rollout" || rsr.Status.Effective.CPUModeSource != v1alpha1.ModeSourcePolicy {
		t.Fatalf("effective %+v", rsr.Status.Effective)
	}
	eventually(t, 10*time.Second, "inventory reported", func() error {
		inv, applied := stub.Inventory(testCluster)
		if applied != version {
			return fmt.Errorf("configVersion %q, want %q", applied, version)
		}
		var hub, gitItem *hubclient.PolicyInventoryItem
		for i := range inv {
			switch inv[i].Name {
			case "hub-rollout":
				hub = &inv[i]
			case "git-hubtest":
				gitItem = &inv[i]
			}
		}
		if hub == nil || hub.Source != "hub" || !hub.Valid || hub.HubVersion != 1 || hub.MatchedWorkloads == nil || *hub.MatchedWorkloads != 1 {
			return fmt.Errorf("hub item %+v", hub)
		}
		if gitItem == nil || gitItem.Source != "git" || gitItem.MatchedWorkloads == nil || *gitItem.MatchedWorkloads != 0 {
			return fmt.Errorf("git item %+v", gitItem)
		}
		return nil
	})

	// A workload override (layer above the winning policy) takes CPU back to audit.
	ovName := v1alpha1.OverridePolicyName("default", v1alpha1.KindDeployment, name)
	ov := hubclient.ConfigPolicy{ID: "0192f7c4-1111-7000-8000-000000000002", Name: ovName, Type: v1alpha1.HubPolicyTypeWorkloadOverride,
		Version: 1, Priority: v1alpha1.PriorityWorkloadOverride, Spec: v1alpha1.RightsizingPolicySpec{Priority: v1alpha1.PriorityWorkloadOverride,
			Selector: v1alpha1.PolicySelector{Namespaces: []string{"default"}, Kinds: []v1alpha1.WorkloadKind{v1alpha1.KindDeployment},
				Workloads: []v1alpha1.NamespacedWorkloadRef{{Kind: v1alpha1.KindDeployment, Namespace: "default", Name: name}}},
			CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit}}}
	setConfig(t, hubclient.ClusterConfig{Policies: []hubclient.ConfigPolicy{pol, ov}})
	eventually(t, 10*time.Second, "override reported", func() error {
		st, ok := stub.Statuses(testCluster)["default/Deployment/"+name]
		if !ok || st.Override != ovName || st.Policy != "hub-rollout" || st.Modes.CPU != "audit" || st.Modes.CPUSource != "policy" {
			return fmt.Errorf("status %+v", st)
		}
		return nil
	})
	eventually(t, 10*time.Second, "released by the override", wantEvent(name, hubclient.EventReleased, v1alpha1.ReasonModeChanged))
}

// TestPause (D-022): a paused cluster applies nothing (blocked/Paused once, with the scope) and
// applies the change after the resume.
func TestPause(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "paused-rollout"
	u := deploymentApply(name, map[string]string{v1alpha1.AnnotationCPUMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	if err := argoApply(t, u, false); err != nil {
		t.Fatal(err)
	}
	setConfig(t, hubclient.ClusterConfig{Flags: hubclient.ConfigFlags{Paused: true, PauseScope: hubclient.PauseScopeFleet, PauseReason: "INC-1"}})
	setRec(recFor(name, "250m", "200Mi"))
	eventually(t, 10*time.Second, "blocked", wantPhase(name, v1alpha1.PhaseBlocked))
	eventually(t, 5*time.Second, "paused event", wantEvent(name, hubclient.EventBlocked, v1alpha1.ReasonPaused))
	time.Sleep(1500 * time.Millisecond) // several pulls and resyncs
	if n := countEvents(name, hubclient.EventBlocked, v1alpha1.ReasonPaused); n != 1 {
		t.Fatalf("%d Paused events, want 1 (D-017)", n)
	}
	for _, e := range eventsFor(name) {
		if e.Reason == v1alpha1.ReasonPaused && (e.Details[hubclient.DetailPauseScope] != "fleet" || e.Message == "") {
			t.Fatalf("paused event %+v", e)
		}
	}
	if d := getDeploy(t, name); cpuReq(d) != "500m" {
		t.Fatalf("changed while paused: %s", cpuReq(d))
	}
	st := stub.Statuses(testCluster)["default/Deployment/"+name]
	if st.Reason != v1alpha1.ReasonPaused {
		t.Fatalf("hub status reason %q", st.Reason)
	}
	setConfig(t, hubclient.ClusterConfig{})
	eventually(t, 10*time.Second, "applied after resume", wantPhase(name, v1alpha1.PhaseApplied))
	if d := getDeploy(t, name); cpuReq(d) != "250m" {
		t.Fatalf("not applied after resume: %s", cpuReq(d))
	}
}

// TestApproveOnce (D-024): an audit workload's approved recommendation is applied once with the
// configured strategy (applied/ApprovedOnce), the workload stays in audit, the approval is
// consumed, and the next recommendation is not applied.
func TestApproveOnce(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "approved-once"
	u := deploymentApply(name, map[string]string{v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	if err := argoApply(t, u, false); err != nil {
		t.Fatal(err)
	}
	r1 := recFor(name, "250m", "200Mi")
	setRec(r1)
	eventually(t, 10*time.Second, "skipped in audit", wantEvent(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit))
	ref := hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: name}
	approval := hubclient.ConfigApproval{ID: "6c1d0000-0000-4000-8000-000000000001", Workload: ref, RecommendationID: recID(r1),
		ExpiresAt: time.Now().Add(time.Hour), ApprovedBy: "bob", ApprovedAt: time.Now()}
	stub.SetConfig(testCluster, &hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{approval}})

	eventually(t, 20*time.Second, "approved once", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonApprovedOnce))
	for _, e := range eventsFor(name) {
		if e.Type == hubclient.EventApplied {
			if e.Reason != v1alpha1.ReasonApprovedOnce || e.Details[hubclient.DetailApprovalID] != approval.ID ||
				e.Details[hubclient.DetailApprovedBy] != "bob" || e.Strategy != "rollout" || e.RecommendationID != approval.RecommendationID {
				t.Fatalf("applied event %+v", e)
			}
		}
	}
	if d := getDeploy(t, name); cpuReq(d) != "250m" || memReq(d) != "200Mi" {
		t.Fatalf("template %s %s", cpuReq(d), memReq(d))
	}
	// The hub consumed it (stub: on storing the event); the workload stays in audit.
	eventually(t, 5*time.Second, "approval consumed", func() error {
		if n := len(stub.Config(testCluster).Approvals); n != 0 {
			return fmt.Errorf("%d approvals left", n)
		}
		return nil
	})
	rsr, _ := getRSR(name)
	if e := rsr.Status.Effective; e.CPUMode != v1alpha1.ModeAudit || e.MemoryMode != v1alpha1.ModeAudit {
		t.Fatalf("effective modes %+v", e)
	}
	st := stub.Statuses(testCluster)["default/Deployment/"+name]
	if st.Modes.CPU != "audit" || st.Modes.Memory != "audit" {
		t.Fatalf("hub modes %+v", st.Modes)
	}

	// The next recommendation is not applied.
	setRec(recFor(name, "120m", "150Mi"))
	time.Sleep(2 * time.Second)
	if d := getDeploy(t, name); cpuReq(d) != "250m" || memReq(d) != "200Mi" {
		t.Fatalf("next recommendation applied: %s %s", cpuReq(d), memReq(d))
	}
	if n := countEvents(name, hubclient.EventApplied, v1alpha1.ReasonApprovedOnce) + countEvents(name, hubclient.EventApplied, v1alpha1.ReasonRecommendationApplied); n != 1 {
		t.Fatalf("%d applied events", n)
	}
	if n := countEvents(name, hubclient.EventReleased, v1alpha1.ReasonModeChanged); n != 0 {
		t.Fatalf("released emitted for an approve-once workload: %d", n)
	}
	var d appsv1.Deployment
	if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name}, &d); apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

// TestApproveOncePaused: an approval waits while the cluster is paused.
func TestApproveOncePaused(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "approved-paused"
	u := deploymentApply(name, map[string]string{v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	if err := argoApply(t, u, false); err != nil {
		t.Fatal(err)
	}
	r1 := recFor(name, "250m", "200Mi")
	setRec(r1)
	approval := hubclient.ConfigApproval{ID: "6c1d0000-0000-4000-8000-000000000002", RecommendationID: recID(r1),
		Workload: hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: name}, ExpiresAt: time.Now().Add(time.Hour), ApprovedBy: "bob"}
	setConfig(t, hubclient.ClusterConfig{Flags: hubclient.ConfigFlags{Paused: true}, Approvals: []hubclient.ConfigApproval{approval}})
	eventually(t, 10*time.Second, "paused", wantEvent(name, hubclient.EventBlocked, v1alpha1.ReasonPaused))
	if d := getDeploy(t, name); cpuReq(d) != "500m" {
		t.Fatalf("applied while paused: %s", cpuReq(d))
	}
	setConfig(t, hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{approval}})
	eventually(t, 20*time.Second, "approved after resume", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonApprovedOnce))
}

// approveAndApply approves the current recommendation of an audit rollout workload and waits for
// the approved apply.
func approveAndApply(t *testing.T, name, approvalID string) hubclient.WorkloadRecommendation {
	t.Helper()
	u := deploymentApply(name, map[string]string{v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	if err := argoApply(t, u, false); err != nil {
		t.Fatal(err)
	}
	r1 := recFor(name, "250m", "200Mi")
	setRec(r1)
	eventually(t, 10*time.Second, "skipped in audit", wantEvent(name, hubclient.EventSkipped, v1alpha1.ReasonModeAudit))
	stub.SetConfig(testCluster, &hubclient.ClusterConfig{Approvals: []hubclient.ConfigApproval{{ID: approvalID, RecommendationID: recID(r1),
		Workload: hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: name}, ExpiresAt: time.Now().Add(time.Hour), ApprovedBy: "bob"}}})
	eventually(t, 20*time.Second, "approved once", wantEvent(name, hubclient.EventApplied, v1alpha1.ReasonApprovedOnce))
	return hubstub.Normalize([]hubclient.WorkloadRecommendation{r1}, time.Now())[0]
}

func owns(t *testing.T, name string) map[corev1.ResourceName]bool {
	t.Helper()
	return apply.OwnedResources(getDeploy(t, name))
}

// TestApprovedOwnership (D-027): after an approve-once rollout the agent keeps the field manager on
// the approved fields, so an Argo CD sync (here: a non-forced apply of the Git values) cannot revert
// them; a new recommendation id with the same values keeps it; different values release it.
func TestApprovedOwnership(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "approved-owned"
	r1 := approveAndApply(t, name, "6c1d0000-0000-4000-8000-000000000011")
	eventually(t, 10*time.Second, "Applied condition ApprovedOnce", func() error {
		rsr, err := getRSR(name)
		if err != nil {
			return err
		}
		if c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionApplied); c == nil || c.Reason != v1alpha1.ReasonApprovedOnce {
			return fmt.Errorf("applied condition %+v", c)
		}
		return nil
	})
	time.Sleep(1500 * time.Millisecond) // several reconciles in audit
	if o := owns(t, name); !o[corev1.ResourceCPU] || !o[corev1.ResourceMemory] {
		t.Fatalf("ownership dropped after the approved apply: %v", o)
	}
	git := deploymentApply(name, map[string]string{v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	if err := argoApply(t, git, false); !apierrors.IsConflict(err) {
		t.Fatalf("a sync of the Git values must conflict with the approved fields, got %v", err)
	}

	// Same values, new id (the hub's next run): still held.
	same := r1
	same.ID = "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b7c11"
	setRec(same)
	time.Sleep(1500 * time.Millisecond)
	if o := owns(t, name); !o[corev1.ResourceCPU] || !o[corev1.ResourceMemory] {
		t.Fatalf("ownership dropped for a recommendation with the same values: %v", o)
	}

	// Different values: released, values stay, nothing applied, no released event.
	setRec(recFor(name, "120m", "150Mi"))
	eventually(t, 10*time.Second, "ownership released", func() error {
		if o := owns(t, name); len(o) > 0 {
			return fmt.Errorf("still owned: %v", o)
		}
		return nil
	})
	if d := getDeploy(t, name); cpuReq(d) != "250m" || memReq(d) != "200Mi" {
		t.Fatalf("values changed on release: %s %s", cpuReq(d), memReq(d))
	}
	if n := countEvents(name, hubclient.EventReleased, v1alpha1.ReasonModeChanged); n != 0 {
		t.Fatalf("released events: %d", n)
	}
	if err := argoApply(t, git, false); err != nil {
		t.Fatalf("Git sync after the release: %v", err)
	}
	if d := getDeploy(t, name); cpuReq(d) != "500m" {
		t.Fatalf("Git values not restored: %s", cpuReq(d))
	}
}

// TestApprovedOwnershipModeOff (D-027): setting a resource to off releases that resource only.
func TestApprovedOwnershipModeOff(t *testing.T) {
	needEnv(t)
	resetConfig(t)
	const name = "approved-off"
	approveAndApply(t, name, "6c1d0000-0000-4000-8000-000000000012")
	time.Sleep(time.Second)
	if o := owns(t, name); !o[corev1.ResourceCPU] || !o[corev1.ResourceMemory] {
		t.Fatalf("not held: %v", o)
	}
	annotate(t, &appsv1.Deployment{}, name, map[string]any{v1alpha1.AnnotationCPUMode: "off"})
	eventually(t, 10*time.Second, "cpu released", func() error {
		if o := owns(t, name); o[corev1.ResourceCPU] || !o[corev1.ResourceMemory] {
			return fmt.Errorf("owned %v, want memory only", o)
		}
		return nil
	})
	if d := getDeploy(t, name); cpuReq(d) != "250m" || memReq(d) != "200Mi" {
		t.Fatalf("values changed: %s %s", cpuReq(d), memReq(d))
	}
	annotate(t, &appsv1.Deployment{}, name, map[string]any{v1alpha1.AnnotationMode: "off"})
	eventually(t, 10*time.Second, "all released", func() error {
		if o := owns(t, name); len(o) > 0 {
			return fmt.Errorf("owned %v", o)
		}
		return nil
	})
}

func TestSameAdvice(t *testing.T) {
	q := resource.MustParse
	tgt := []v1alpha1.ContainerRecommendation{{ContainerResources: v1alpha1.ContainerResources{Name: "app",
		Requests: corev1.ResourceList{corev1.ResourceCPU: q("250m"), corev1.ResourceMemory: q("200Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: q("200Mi")}}}}
	rec := func(cpu, mem string, memLimit string) []v1alpha1.ContainerRecommendation {
		c := v1alpha1.ContainerRecommendation{ContainerResources: v1alpha1.ContainerResources{Name: "app",
			Requests: corev1.ResourceList{corev1.ResourceCPU: q(cpu), corev1.ResourceMemory: q(mem)}}}
		if memLimit != "" {
			c.Limits = corev1.ResourceList{corev1.ResourceMemory: q(memLimit)}
		}
		return []v1alpha1.ContainerRecommendation{c}
	}
	both := map[corev1.ResourceName]bool{corev1.ResourceCPU: true, corev1.ResourceMemory: true}
	cpuOnly := map[corev1.ResourceName]bool{corev1.ResourceCPU: true}
	tests := []struct {
		name string
		rec  []v1alpha1.ContainerRecommendation
		res  map[corev1.ResourceName]bool
		want bool
	}{
		{"same", rec("250m", "200Mi", ""), both, true},
		{"within tolerance", rec("260m", "205Mi", "205Mi"), both, true},
		{"cpu differs", rec("120m", "200Mi", ""), both, false},
		{"memory differs, cpu held only", rec("250m", "100Mi", ""), cpuOnly, true},
		{"limit differs", rec("250m", "200Mi", "400Mi"), both, false},
		{"container missing", []v1alpha1.ContainerRecommendation{{ContainerResources: v1alpha1.ContainerResources{Name: "other"}}}, both, false},
	}
	for _, tt := range tests {
		if got := sameAdvice(tt.rec, tgt, tt.res, 0.05); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}
