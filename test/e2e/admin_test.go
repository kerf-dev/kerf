//go:build e2e

package e2e

// WO-24: admin-area scenarios (D-022..D-027). They drive the hub API as logged-in dev users
// (admin_client_test.go) and check the effect on the spokes. Hub objects they create (policies,
// clusters, pause state) are removed at the end of each test and swept at the start of a run
// (sweepHub) in case a run was killed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"

	v1alpha1 "github.com/kerf-dev/kerf/api/v1alpha1"
	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

const (
	admin    = hubapi.UserRoleAdmin
	operator = hubapi.UserRoleOperator
	viewer   = hubapi.UserRoleViewer
)

// smallCPU is the CPU request of the admin scenarios' workloads on ro-spoke-1, which burn ~20m:
// smaller than overCPU because the WO-09 scenarios already fill most of that 8-core node.
const smallCPU = "100m"

// small: requests smallCPU, burns ~2% of a core.
func small(name string) app {
	return app{Name: name, CPULoad: 2, Requests: rl(smallCPU, "64Mi"), Limits: rl("", "128Mi")}
}

// adminActor is the config-audit actor of the dev admin's session.
var adminActor = hubapi.Actor{Type: hubapi.ActorUser, Name: admin, Role: admin}

func auditSpec(strategy v1alpha1.Strategy) v1alpha1.RightsizingPolicySpec {
	return v1alpha1.RightsizingPolicySpec{
		CPU:        &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit},
		Memory:     &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAudit},
		Apply:      &v1alpha1.ApplyPolicy{Strategy: strategy},
		Guardrails: relaxed(),
	}
}

// cpuAuditOnly: CPU in audit, memory off (approve-once then changes CPU only; in-place memory
// changes are refused on the k3d nodes, which have swap).
func cpuAuditOnly(strategy v1alpha1.Strategy) v1alpha1.RightsizingPolicySpec {
	s := auditSpec(strategy)
	s.Memory.Mode = v1alpha1.ModeOff
	return s
}

func getPolicyCR(t *testing.T, cl cluster, name string) (*v1alpha1.RightsizingPolicy, error) {
	var p v1alpha1.RightsizingPolicy
	err := cl.C.Get(ctx(t), client.ObjectKey{Name: name}, &p)
	return &p, err
}

// waitHubCR waits until the spoke has the hub-labelled CR of the policy at the version.
func waitHubCR(t *testing.T, cl cluster, p hubapi.AdminPolicy, version int) *v1alpha1.RightsizingPolicy {
	t.Helper()
	var cr *v1alpha1.RightsizingPolicy
	eventually(t, 3*time.Minute, 3*time.Second, fmt.Sprintf("CR %s at hub version %d on %s", p.Name, version, cl.Name), func() (bool, string) {
		var err error
		if cr, err = getPolicyCR(t, cl, p.Name); err != nil {
			return false, err.Error()
		}
		a := cr.Annotations
		if cr.Labels[v1alpha1.LabelManagedBy] != v1alpha1.ManagedByHub {
			return false, fmt.Sprintf("labels %v", cr.Labels)
		}
		return a[v1alpha1.AnnotationHubPolicyID] == p.ID && a[v1alpha1.AnnotationHubVersion] == strconv.Itoa(version) &&
			a[v1alpha1.AnnotationHubPolicyType] == v1alpha1.HubPolicyTypePolicy, fmt.Sprintf("annotations %v", a)
	})
	return cr
}

func requireActor(t *testing.T, c hubapi.ConfigChange, want hubapi.Actor) {
	t.Helper()
	if c.Actor != want {
		t.Errorf("config change %s: actor %+v, want %+v", c.Action, c.Actor, want)
	}
}

func afterField(t *testing.T, c hubapi.ConfigChange, key string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.After, &m); err != nil {
		t.Fatalf("config change %s: after %s: %v", c.Action, c.After, err)
	}
	return m[key]
}

// Scenarios 1 and 4: a namespace policy created through the admin API is materialised on the
// spoke as a hub-labelled CR and applied; the config audit names the actor; a revert restores the
// previous version and the CR follows; a delete removes the CR.
func TestAdminPolicy(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "hubpol")
	deploy(t, cl, ns, small("web"))
	adm := as(t, admin)

	in := hubPolicy(t, ns, ns, []string{cl.Name}, auditSpec(v1alpha1.StrategyInPlace))
	p := createHubPolicy(t, in)
	if cr := waitHubCR(t, cl, p, 1); cr.Spec.CPU.Mode != v1alpha1.ModeAudit || cr.Spec.Priority != 1000 {
		t.Fatalf("CR spec: cpu %s priority %d, want audit 1000", cr.Spec.CPU.Mode, cr.Spec.Priority)
	}
	waitHubRecommendation(t, cl, ns, deployment, "web", cpuBelow(smallCPU))
	waitReason(t, cl, ns, deployment, "web", v1alpha1.ReasonModeAudit, 3*time.Minute)

	// Version 2: CPU auto-patch.
	in.Spec.CPU.Mode = v1alpha1.ModeAutoPatch
	in.Reason = reason(t, "cpu auto-patch")
	var p2 hubapi.AdminPolicy
	retryNotReady(t, "update policy to auto-patch", func() reply {
		return adm.do(t, http.MethodPut, "/v1/admin/policies/"+p.ID, in, ifMatch(p.Version))
	}, http.StatusOK, &p2)
	if p2.Version != 2 {
		t.Fatalf("updated policy version %d, want 2", p2.Version)
	}
	if cr := waitHubCR(t, cl, p2, 2); cr.Spec.CPU.Mode != v1alpha1.ModeAutoPatch {
		t.Fatalf("CR cpu mode %s after the update, want auto-patch", cr.Spec.CPU.Mode)
	}
	r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	applied := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
	if applied.Cmp(resource.MustParse(smallCPU)) >= 0 {
		t.Fatalf("applied cpu %s is not a decrease", applied.String())
	}
	waitEvent(t, cl, ns, "web", "applied", func(e auditEvent) bool { return e.Reason == v1alpha1.ReasonRecommendationApplied })

	q := url.Values{"targetType": {hubapi.TargetPolicy}, "targetId": {p.ID}}
	requireActor(t, waitConfigChange(t, q, hubapi.ActionPolicyCreated, nil), adminActor)
	requireActor(t, waitConfigChange(t, q, hubapi.ActionPolicyUpdated, func(c hubapi.ConfigChange) bool { return c.Version == 2 }), adminActor)

	// Revert to version 1 (audit): version 3, and the CR follows.
	var p3 hubapi.AdminPolicy
	adm.call(t, http.MethodPost, "/v1/admin/policies/"+p.ID+"/revert", hubapi.PolicyRevert{Version: 1, Reason: reason(t, "revert to v1")},
		ifMatch(p2.Version), http.StatusOK, &p3)
	if p3.Version != 3 || p3.Spec.CPU.Mode != v1alpha1.ModeAudit {
		t.Fatalf("reverted policy: version %d cpu %s, want 3 audit", p3.Version, p3.Spec.CPU.Mode)
	}
	if cr := waitHubCR(t, cl, p3, 3); cr.Spec.CPU.Mode != v1alpha1.ModeAudit {
		t.Fatalf("CR cpu mode %s after the revert, want audit", cr.Spec.CPU.Mode)
	}
	eventually(t, 3*time.Minute, 5*time.Second, "agent resolves cpu audit after the revert", func() (bool, string) {
		r := rsr(t, cl, ns, deployment, "web")
		return r != nil && r.Status.Effective != nil && r.Status.Effective.CPUMode == v1alpha1.ModeAudit, rsrStatus(r)
	})
	requireActor(t, waitConfigChange(t, q, hubapi.ActionPolicyReverted, func(c hubapi.ConfigChange) bool { return c.Version == 3 }), adminActor)

	// Delete: the agent removes the CR.
	adm.call(t, http.MethodDelete, "/v1/admin/policies/"+p.ID, hubapi.ChangeReason{Reason: reason(t, "delete")}, ifMatch(p3.Version), http.StatusOK, nil)
	eventually(t, 3*time.Minute, 3*time.Second, "CR "+p.Name+" removed", func() (bool, string) {
		_, err := getPolicyCR(t, cl, p.Name)
		return apierrors.IsNotFound(err), fmt.Sprint(err)
	})
}

// clusterAgent returns the hub's view of a cluster's agent config sync.
func clusterAgent(t *testing.T, name string) hubapi.AgentConfigSync {
	t.Helper()
	var ca hubapi.ClusterAdmin
	as(t, viewer).get(t, "/v1/admin/clusters/"+url.PathEscape(name), &ca)
	return ca.Agent
}

// waitAgentHasConfig waits until the cluster's agent applied a config newer than `before`.
func waitAgentHasConfig(t *testing.T, name, before string) {
	t.Helper()
	eventually(t, 3*time.Minute, 3*time.Second, name+" agent pulled the new config", func() (bool, string) {
		a := clusterAgent(t, name)
		return a.ConfigVersion != before && a.AppliedConfigVersion == a.ConfigVersion,
			fmt.Sprintf("config %s applied %s (before %s)", a.ConfigVersion, a.AppliedConfigVersion, before)
	})
}

// Scenario 2: a pause blocks new applies (blocked/Paused), a health-watch rollback still happens,
// and a resume lets the pending change through. ro-spoke-2: a pause is cluster-wide, and spoke-1
// runs the other scenarios.
func TestAdminPause(t *testing.T) {
	t.Parallel()
	cl := env.Spoke2
	ns := newNamespace(t, cl, "pause")
	// "rb" is applied before the pause and made to crash while paused; "web" waits in audit.
	setControl(t, cl, ns, "rb", map[string]string{"mib": "10", "crash": "0"})
	rb := controllableApp("rb", rl(overCPU, "64Mi"), rl("", "128Mi"))
	rb.Labels = map[string]string{"e2e-role": "rb"}
	deploy(t, cl, ns, rb)
	deploy(t, cl, ns, overprovisioned("web"))
	base := auditSpec(v1alpha1.StrategyInPlace)
	base.Guardrails = rollbackGuardrails()
	policy(t, cl, ns, ns, base)
	waitHubRecommendation(t, cl, ns, deployment, "rb", cpuBelow(overCPU))
	waitHubRecommendation(t, cl, ns, deployment, "web", cpuBelow(overCPU))
	waitReason(t, cl, ns, deployment, "web", v1alpha1.ReasonModeAudit, 3*time.Minute)

	unlock := lockSpoke2(t)
	pa := autoCPU(v1alpha1.StrategyInPlace)
	pa.Guardrails = rollbackGuardrails()
	pa.Selector.MatchLabels = map[string]string{"e2e-role": "rb"}
	pa.Priority = 1001
	armed(t, cl, ns+"-rb", ns, "rb", overCPU, pa)
	waitPhase(t, cl, ns, deployment, "rb", v1alpha1.PhaseApplied)

	before := clusterAgent(t, cl.Name).ConfigVersion
	if r := setPause(t, operator, cl.Name, true, nil); r.Status != http.StatusOK {
		t.Fatalf("operator pause of %s: %s", cl.Name, r)
	}
	pausedAt := time.Now()
	t.Cleanup(func() {
		if r, err := sessionOf(admin).raw(context.Background(), http.MethodPut, "/v1/clusters/"+cl.Name+"/automation",
			hubapi.AutomationUpdate{Paused: false, Reason: "e2e cleanup: resume"}, nil); err != nil || r.Status != http.StatusOK {
			t.Errorf("cleanup: resume %s: %v %s", cl.Name, err, r)
		}
	})
	waitAgentHasConfig(t, cl.Name, before)

	// A change that would apply now: web to auto-patch.
	var p v1alpha1.RightsizingPolicy
	must(t, cl.C.Get(ctx(t), client.ObjectKey{Name: ns}, &p))
	p.Spec.CPU.Mode = v1alpha1.ModeAutoPatch
	must(t, cl.C.Update(ctx(t), &p))
	webPods := snapshot(t, cl, ns, "web")
	ev := waitEvent(t, cl, ns, "web", "blocked", func(e auditEvent) bool { return e.Reason == v1alpha1.ReasonPaused })
	t.Logf("blocked event: %s", ev.Message)
	consistently(t, 45*time.Second, 5*time.Second, "nothing applied while paused", func() (bool, string) {
		if got := snapshot(t, cl, ns, "web"); fmt.Sprint(got) != fmt.Sprint(webPods) {
			return false, fmt.Sprintf("web changed while paused: %v -> %v", webPods, got)
		}
		r := rsr(t, cl, ns, deployment, "web")
		return r == nil || r.Status.Phase != v1alpha1.PhaseApplied, rsrStatus(r)
	})

	// Rollbacks still happen while paused.
	setControl(t, cl, ns, "rb", map[string]string{"mib": "10", "crash": "1"})
	waitPhase(t, cl, ns, deployment, "rb", v1alpha1.PhaseRolledBack)
	setControl(t, cl, ns, "rb", map[string]string{"mib": "10", "crash": "0"})
	rbEv := waitEvent(t, cl, ns, "rb", "rolled_back", func(e auditEvent) bool { return e.Reason == v1alpha1.ReasonRestarts })
	if rbEv.Time.Before(pausedAt.Add(-5 * time.Second)) {
		t.Errorf("rolled_back event at %s is from before the pause (%s)", rbEv.Time, pausedAt)
	}

	// Resume: the pending change goes through.
	if r := setPause(t, operator, cl.Name, false, nil); r.Status != http.StatusOK {
		t.Fatalf("operator resume of %s: %s", cl.Name, r)
	}
	waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	evs, err := env.Hub.audit(ctx(t), cl.Name, ns, deployment, "web", "blocked")
	must(t, err)
	if n := len(slices.DeleteFunc(evs, func(e auditEvent) bool { return e.Reason != v1alpha1.ReasonPaused })); n != 1 {
		t.Errorf("%d blocked/Paused events for web, want exactly 1 (D-017)", n)
	}
	q := url.Values{"targetType": {hubapi.TargetAutomation}, "cluster": {cl.Name}}
	opActor := hubapi.Actor{Type: hubapi.ActorUser, Name: operator, Role: operator}
	since := func(c hubapi.ConfigChange) bool { return !c.Time.Before(pausedAt.Add(-time.Minute)) }
	requireActor(t, waitConfigChange(t, q, hubapi.ActionAutomationPaused, since), opActor)
	requireActor(t, waitConfigChange(t, q, hubapi.ActionAutomationResumed, since), opActor)
	unlock()
}

// Scenario 3: approve once applies exactly one recommendation of an audit workload; the next one
// is not applied.
func TestApproveOnce(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "approve")
	deploy(t, cl, ns, small("web"))
	policy(t, cl, ns, ns, cpuAuditOnly(v1alpha1.StrategyInPlace))
	waitHubRecommendation(t, cl, ns, deployment, "web", cpuBelow(smallCPU))
	waitReason(t, cl, ns, deployment, "web", v1alpha1.ReasonModeAudit, 3*time.Minute)

	a, ev := approveApplied(t, operator, cl, ns, "web")
	t.Logf("applied event: %s", ev.Message)
	if ev.RecommendationID != "" && ev.RecommendationID != a.RecommendationID {
		t.Errorf("applied recommendation %s, approved %s", ev.RecommendationID, a.RecommendationID)
	}
	r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	want := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
	if want.Cmp(resource.MustParse(smallCPU)) >= 0 {
		t.Fatalf("approved cpu %s is not a decrease", want.String())
	}
	if !hasReason(r, v1alpha1.ReasonApprovedOnce) {
		t.Errorf("RSR does not record the approval: %s", rsrStatus(r))
	}
	eventually(t, 2*time.Minute, 5*time.Second, "pods resized to the approved value", func() (bool, string) {
		for name, s := range snapshot(t, cl, ns, "web") {
			if q(s.CPUReq).Cmp(want) != 0 {
				return false, fmt.Sprintf("pod %s cpu %s, want %s", name, s.CPUReq, want.String())
			}
		}
		return true, ""
	})
	eventually(t, 2*time.Minute, 5*time.Second, "approval consumed", func() (bool, string) {
		var d hubapi.WorkloadDetail
		as(t, viewer).get(t, workloadPath(cl, ns, "web"), &d)
		return d.Approval != nil && d.Approval.ID == a.ID && d.Approval.State == hubapi.ApprovalConsumed, fmt.Sprintf("%+v", d.Approval)
	})
	requireActor(t, waitConfigChange(t, url.Values{"workloadId": {workloadID(cl, ns, "web")}}, hubapi.ActionWorkloadApproved, nil),
		hubapi.Actor{Type: hubapi.ActorUser, Name: operator, Role: operator})

	// The next recommendation stays in audit.
	pods := snapshot(t, cl, ns, "web")
	next := waitHubRecommendation(t, cl, ns, deployment, "web", func(rec *workloadRec) (bool, string) {
		return rec.ID != a.RecommendationID, "still the approved recommendation"
	})
	t.Logf("next recommendation %s: cpu %s (approved %s)", next.ID, next.Containers[0].Recommended.Requests.CPU, want.String())
	time.Sleep(time.Minute) // two agent pulls with the new recommendation
	consistently(t, 45*time.Second, 5*time.Second, "next recommendation not applied", func() (bool, string) {
		if got := snapshot(t, cl, ns, "web"); fmt.Sprint(got) != fmt.Sprint(pods) {
			return false, fmt.Sprintf("pods changed: %v -> %v", pods, got)
		}
		evs, err := env.Hub.audit(ctx(t), cl.Name, ns, deployment, "web", "applied")
		if err != nil {
			return true, ""
		}
		return len(evs) == 1, fmt.Sprintf("%d applied events, want 1", len(evs))
	})
}

// Scenario 5: a Git-managed CR (no hub label) is never modified or deleted by the agent, even when
// a hub policy has its name, and the inventory lists it as a Git policy.
func TestGitPolicyUntouched(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "gitpol")
	policy(t, cl, ns, ns, auditSpec(v1alpha1.StrategyInPlace))
	git, err := getPolicyCR(t, cl, ns)
	must(t, err)

	spec := auditSpec(v1alpha1.StrategyRollout)
	spec.Priority = 2000
	p := createHubPolicy(t, hubPolicy(t, ns, ns, []string{cl.Name}, spec))

	inventory := func() (gitItem, hubItem *hubapi.PolicyInventoryItem, why string) {
		var out hubapi.PolicyInventoryList
		as(t, viewer).get(t, "/v1/admin/policy-inventory?cluster="+cl.Name, &out)
		if len(out.Items) != 1 {
			return nil, nil, fmt.Sprintf("%d clusters in the inventory", len(out.Items))
		}
		for i := range out.Items[0].Items {
			it := &out.Items[0].Items[i]
			switch {
			case it.Name == ns && it.Source == "git":
				gitItem = it
			case it.Name == ns && it.HubPolicyID == p.ID:
				hubItem = it
			}
		}
		return gitItem, hubItem, fmt.Sprintf("git %+v hub %+v", gitItem, hubItem)
	}
	eventually(t, 3*time.Minute, 5*time.Second, "inventory lists the Git CR and the refused hub policy", func() (bool, string) {
		g, h, why := inventory()
		return g != nil && g.Valid && g.Generation == git.Generation && g.HubPolicyID == "" &&
			h != nil && !h.Valid && h.Generation == 0, why
	})
	_, h, _ := inventory()
	t.Logf("hub policy %s reported: %s", p.Name, h.Message)

	unchanged := func() (bool, string) {
		cur, err := getPolicyCR(t, cl, ns)
		if err != nil {
			return false, "Git CR: " + err.Error()
		}
		if cur.Generation != git.Generation || cur.UID != git.UID || len(cur.Annotations) != len(git.Annotations) ||
			cur.Labels[v1alpha1.LabelManagedBy] != "" || cur.Spec.Apply.Strategy != v1alpha1.StrategyInPlace {
			return false, fmt.Sprintf("Git CR changed: generation %d->%d labels %v annotations %v", git.Generation, cur.Generation, cur.Labels, cur.Annotations)
		}
		return true, ""
	}
	consistently(t, time.Minute, 5*time.Second, "Git CR untouched while a hub policy has its name", unchanged)

	must(t, deleteHubPolicyQuiet(p.ID, reason(t, "delete")))
	consistently(t, 45*time.Second, 5*time.Second, "Git CR not deleted with the hub policy", unchanged)
	eventually(t, 2*time.Minute, 5*time.Second, "inventory still lists the Git CR", func() (bool, string) {
		g, h, why := inventory()
		return g != nil && h == nil, why
	})
}

// Scenario 6: roles. A viewer gets 403 on writes; an operator can pause (and resume a non-prod
// cluster) but cannot edit defaults or settings; a session write without the CSRF token is refused.
// The cluster paused is a registered one without an agent, so nothing else is affected.
func TestAdminRoles(t *testing.T) {
	t.Parallel()
	name := "e2e-roles-" + randSuffix()
	registerCluster(t, hubapi.ClusterCreate{Name: name})
	forbidden := func(r reply, code, what string) {
		t.Helper()
		if r.Status != http.StatusForbidden || r.Problem() != code {
			t.Errorf("%s: want 403 %s, got %s", what, code, r)
		}
	}
	pol := hubPolicy(t, name, "e2e-roles-none", []string{name}, auditSpec(v1alpha1.StrategyInPlace))

	// Viewer: reads, never writes.
	v := as(t, viewer)
	v.get(t, "/v1/admin/policies", &hubapi.AdminPolicyList{})
	forbidden(v.do(t, http.MethodPost, "/v1/admin/policies", pol, nil), "role_forbidden", "viewer creates a policy")
	forbidden(setPause(t, viewer, name, true, nil), "role_forbidden", "viewer pauses")
	forbidden(v.do(t, http.MethodPost, "/v1/clusters/"+name+"/readiness/recheck", nil, nil), "role_forbidden", "viewer rechecks")

	// Operator: no defaults, no settings, no policies.
	o := as(t, operator)
	def := hubapi.PolicyInput{Type: v1alpha1.HubPolicyTypeGlobalDefault, Spec: auditSpec(v1alpha1.StrategyInPlace), Reason: reason(t, "edit defaults")}
	forbidden(o.do(t, http.MethodPost, "/v1/admin/policies", def, nil), "role_forbidden", "operator edits the global defaults")
	forbidden(o.do(t, http.MethodPost, "/v1/admin/policies", pol, nil), "role_forbidden", "operator creates a policy")
	var settings hubapi.RecommenderSettingsDoc
	o.get(t, "/v1/admin/settings/recommender", &settings)
	forbidden(o.do(t, http.MethodPut, "/v1/admin/settings/recommender",
		hubapi.RecommenderSettingsUpdate{Value: settings.Value, Reason: reason(t, "edit settings")}, ifMatch(settings.Version)),
		"role_forbidden", "operator edits the recommender settings")

	// Operator: pause and resume, with the CSRF token only.
	forbidden(setPause(t, operator, name, true, hdr{hubapi.CSRFHeader: ""}), hubapi.CodeCSRFInvalid, "pause without the CSRF token")
	start := time.Now()
	r := setPause(t, operator, name, true, nil)
	if r.Status != http.StatusOK {
		t.Fatalf("operator pause: %s", r)
	}
	t.Cleanup(func() {
		_, _ = sessionOf(admin).raw(context.Background(), http.MethodPut, "/v1/clusters/"+name+"/automation", hubapi.AutomationUpdate{Reason: "e2e cleanup"}, nil)
	})
	var ca hubapi.ClusterAdmin
	v.get(t, "/v1/admin/clusters/"+name, &ca)
	if !ca.Paused || ca.Automation.By == nil || ca.Automation.By.Name != operator {
		t.Errorf("cluster after the pause: paused %v by %+v", ca.Paused, ca.Automation.By)
	}
	if r := setPause(t, operator, name, false, nil); r.Status != http.StatusOK {
		t.Fatalf("operator resume of a non-prod cluster: %s", r)
	}
	q := url.Values{"cluster": {name}, "targetType": {hubapi.TargetAutomation}}
	recent := func(c hubapi.ConfigChange) bool { return !c.Time.Before(start.Add(-time.Minute)) }
	requireActor(t, waitConfigChange(t, q, hubapi.ActionAutomationPaused, recent), hubapi.Actor{Type: hubapi.ActorUser, Name: operator, Role: operator})
}

// Scenario 7 (D-027): approve once on an Argo CD application with self-heal: the approved values
// stay after a hard sync and through self-heal.
func TestApproveOnceArgoCD(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "argoappr")
	d := small("web").deployment(ns)
	d.Namespace = ""
	d.APIVersion, d.Kind = "apps/v1", "Deployment"
	manifest, err := toYAML(d)
	must(t, err)
	gitWrite(t, ns, map[string][]byte{"deployment.yaml": manifest}, "e2e: add "+ns)
	app := argoApp(t, cl, ns)
	waitAppSynced(t, cl, app, 4*time.Minute)
	waitReady(t, cl, ns, "web", 3*time.Minute)
	policy(t, cl, ns, ns, cpuAuditOnly(v1alpha1.StrategyRollout))
	waitHubRecommendation(t, cl, ns, deployment, "web", cpuBelow(smallCPU))
	waitReason(t, cl, ns, deployment, "web", v1alpha1.ReasonModeAudit, 3*time.Minute)

	// D-027: the agent holds the approved fields until a newer recommendation with other values
	// arrives; the suite's recompute requests stay off for the rest of the test.
	env.Hub.holdRecompute(t)
	a, ev := approveApplied(t, operator, cl, ns, "web")
	if ev.Strategy != string(v1alpha1.StrategyRollout) {
		t.Errorf("approved change applied with strategy %q, want rollout", ev.Strategy)
	}
	r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	approved := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
	eventually(t, 2*time.Minute, 3*time.Second, "template carries the approved value", func() (bool, string) {
		got := templateCPU(getDeployment(t, cl, ns, "web"))
		return got.Cmp(approved) == 0, "template cpu " + got.String()
	})
	if approved.Cmp(resource.MustParse(smallCPU)) >= 0 {
		t.Fatalf("approved cpu %s is not a decrease", approved.String())
	}
	waitReady(t, cl, ns, "web", 3*time.Minute)
	// The agent keeps ownership of the approved fields (D-027), so Argo CD ignores them.
	eventually(t, 2*time.Minute, 5*time.Second, "agent keeps the field ownership", func() (bool, string) {
		d := getDeployment(t, cl, ns, "web")
		return ownsCPURequest(d.ManagedFields), managers(d.ManagedFields)
	})

	hardSync(t, cl, app)
	waitAppSynced(t, cl, app, 2*time.Minute)
	if got := templateCPU(getDeployment(t, cl, ns, "web")); got.Cmp(approved) != 0 {
		t.Fatalf("after a hard sync the template cpu is %s, want the approved %s", got.String(), approved.String())
	}
	// 75 s of self-heal. The hub's periodic run may deliver a newer recommendation with other
	// values meanwhile; then the hold ends by design (D-027) and the check stops there.
	for end := time.Now().Add(75 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Second) {
		if rec, err := env.Hub.workload(ctx(t), cl.Name, ns, deployment, "web"); err == nil && rec != nil && rec.Recommendation != nil &&
			rec.Recommendation.ID != a.RecommendationID && q(rec.Recommendation.Containers[0].Recommended.Requests.CPU).Cmp(approved) != 0 {
			t.Logf("a newer recommendation %s (cpu %s) ends the approved hold (D-027); self-heal checked until now",
				rec.Recommendation.ID, rec.Recommendation.Containers[0].Recommended.Requests.CPU)
			return
		}
		d := getDeployment(t, cl, ns, "web")
		got := templateCPU(d)
		s, _ := getApp(t, cl, app)
		if got.Cmp(approved) != 0 || s.Sync != "Synced" || !ownsCPURequest(d.ManagedFields) {
			t.Fatalf("approved values did not survive self-heal: cpu %s, app %+v, %s", got.String(), s, managers(d.ManagedFields))
		}
	}
}

// Scenario 8 (D-025): onboarding. A new cluster bound to datasource local with an existing spoke's
// label: the datasource test finds its series, readiness stays Registered (no agent), auto-patch is
// refused (409 cluster_not_automation_ready) unless an admin overrides it, which is audited.
func TestOnboarding(t *testing.T) {
	t.Parallel()
	adm := as(t, admin)
	name := "e2e-onb-" + randSuffix()
	binding := hubapi.MetricsBinding{DatasourceID: hubapi.LocalDatasourceID, ClusterLabel: hubapi.DefaultClusterLabel, ClusterLabelValue: env.Spoke1.Name}

	var res hubapi.DatasourceTestResult
	adm.call(t, http.MethodPost, "/v1/admin/datasources/"+hubapi.LocalDatasourceID+"/test", hubapi.DatasourceTestRequest{Binding: &binding}, nil, http.StatusOK, &res)
	if !res.OK || res.Found == nil || res.Found.ContainerSeries == 0 || res.Found.Pods == 0 {
		t.Fatalf("datasource test of the %s binding: ok %v found %+v checks %+v", env.Spoke1.Name, res.OK, res.Found, res.Checks)
	}
	t.Logf("datasource test: %d container series, %d pods, %d nodes in %d ms", res.Found.ContainerSeries, res.Found.Pods, res.Found.Nodes, res.LatencyMs)

	created := registerCluster(t, hubapi.ClusterCreate{Name: name, Provider: "k3d", Metrics: &binding})
	if m := created.Cluster.Metrics; m.DatasourceID != hubapi.LocalDatasourceID || m.ClusterLabelValue != env.Spoke1.Name {
		t.Errorf("registered binding %+v", m)
	}
	acc := recheck(t, name)
	if !acc.HubChecks {
		t.Errorf("recheck of %s: hub checks not run: %+v", name, acc)
	}
	var rr hubapi.ClusterReadiness
	eventually(t, 2*time.Minute, 3*time.Second, name+" metrics checks ran", func() (bool, string) {
		rr = readiness(t, name)
		c := check(rr, rd.CheckCAdvisor)
		return c != nil && !c.CheckedAt.Before(acc.RequestedAt.Add(-2*time.Second)), fmt.Sprintf("%+v", c)
	})
	if rr.State != string(rd.StateRegistered) {
		t.Errorf("readiness of %s (no agent) is %s, want Registered", name, rr.State)
	}
	if c := check(rr, rd.CheckCAdvisor); c.Status != "pass" {
		t.Errorf("%s: %s %s", c.ID, c.Status, c.Message)
	}
	if c := check(rr, rd.CheckAgentConnected); c != nil && c.Status == "pass" {
		t.Errorf("agent.connected passes for a cluster without an agent")
	}

	// Auto-patch gating.
	in := hubPolicy(t, name, "e2e-onb-none", []string{name}, autoCPU(v1alpha1.StrategyInPlace))
	if r := adm.do(t, http.MethodPost, "/v1/admin/policies", in, nil); r.Status != http.StatusConflict || r.Problem() != hubapi.CodeClusterNotAutomationReady {
		if r.Status == http.StatusCreated {
			var p hubapi.AdminPolicy
			_ = json.Unmarshal(r.Body, &p)
			_ = deleteHubPolicyQuiet(p.ID, "e2e: created by mistake")
		}
		t.Fatalf("auto-patch policy on a Registered cluster: want 409 %s, got %s", hubapi.CodeClusterNotAutomationReady, r)
	}
	in.OverrideReadiness = true
	if r := as(t, operator).do(t, http.MethodPost, "/v1/admin/policies", in, nil); r.Status != http.StatusForbidden {
		t.Errorf("operator with overrideReadiness: want 403, got %s", r)
	}
	p := createHubPolicy(t, in)
	c := waitConfigChange(t, url.Values{"targetType": {hubapi.TargetPolicy}, "targetId": {p.ID}}, hubapi.ActionPolicyCreated, nil)
	requireActor(t, c, adminActor)
	if v := afterField(t, c, "overrideReadiness"); v != true {
		t.Errorf("PolicyCreated after.overrideReadiness = %v, want true (after: %s)", v, c.After)
	}

	// Clean up: policy, then the cluster.
	must(t, deleteHubPolicyQuiet(p.ID, reason(t, "delete")))
	must(t, deleteClusterQuiet(name, reason(t, "delete")))
	if r := adm.do(t, http.MethodGet, "/v1/admin/clusters/"+name, nil, nil); r.Status == http.StatusOK {
		var ca hubapi.ClusterAdmin
		must(t, json.Unmarshal(r.Body, &ca))
		if ca.DeletedAt == nil {
			t.Errorf("cluster %s not deleted", name)
		}
	}
}

// Scenario 9 (D-025): both spokes are AutomationReady after a recheck.
func TestReadiness(t *testing.T) {
	t.Parallel()
	for _, cl := range []cluster{env.Spoke1, env.Spoke2} {
		t.Run(cl.Name, func(t *testing.T) {
			t.Parallel()
			if cl.Name == env.Spoke2.Name {
				lockSpoke2(t)
			}
			r := waitReadiness(t, cl.Name, 5*time.Minute, "AutomationReady", stateIs(string(rd.StateAutomationReady)))
			if r.Summary.Blocking != 0 {
				t.Errorf("%d blocking checks failing", r.Summary.Blocking)
			}
			for _, c := range r.Capabilities {
				if !c.Granted {
					t.Errorf("capability %s not granted: %v", c.Name, c.Missing)
				}
			}
			for _, c := range r.Checks {
				if c.Status != "pass" {
					t.Logf("non-passing check %s (%s): %s %s", c.ID, c.Severity, c.Status, c.Message)
				}
			}
		})
	}
}

// agentClusterRole is the spoke agent's ClusterRole (Helm release kerf-agent).
func agentClusterRole() string { return getenv("E2E_AGENT_CLUSTERROLE", "kerf-agent") }

func hasResizeRule(cr *rbacv1.ClusterRole) int {
	for i, r := range cr.Rules {
		if slices.Contains(r.Resources, "pods/resize") {
			return i
		}
	}
	return -1
}

// restoreResizeRule puts the agent's pods/resize rule back (no-op when present) and drops the
// suite's field ownership of the rules, so the next helm upgrade of the agent doesn't conflict.
func restoreResizeRule(c context.Context, cl cluster, rule rbacv1.PolicyRule) error {
	var lastErr error
	for i := 0; i < 5; i++ {
		var cr rbacv1.ClusterRole
		if lastErr = cl.C.Get(c, client.ObjectKey{Name: agentClusterRole()}, &cr); lastErr != nil {
			continue
		}
		if hasResizeRule(&cr) < 0 {
			cr.Rules = append(cr.Rules, rule)
			if lastErr = cl.C.Update(c, &cr); lastErr != nil {
				time.Sleep(time.Second)
				continue
			}
		}
		return releaseFields(c, cl, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: agentClusterRole()}})
	}
	return lastErr
}

// Scenario 10 (D-025): removing the agent's pods/resize permission on ro-spoke-2 drops readiness
// to ReadyAudit after a recheck, writes a ReadinessChanged audit row and refuses auto-patch. The
// rule is restored at the end, also when the test fails.
func TestReadinessRBAC(t *testing.T) {
	t.Parallel()
	cl := env.Spoke2
	lockSpoke2(t)
	waitReadiness(t, cl.Name, 5*time.Minute, "AutomationReady before the RBAC change", stateIs(string(rd.StateAutomationReady)))

	var cr rbacv1.ClusterRole
	must(t, cl.C.Get(ctx(t), client.ObjectKey{Name: agentClusterRole()}, &cr))
	i := hasResizeRule(&cr)
	if i < 0 {
		t.Fatalf("ClusterRole %s has no pods/resize rule", cr.Name)
	}
	removed := *cr.Rules[i].DeepCopy()
	if len(removed.Resources) != 1 {
		t.Fatalf("pods/resize shares its rule with %v; refusing to remove more than pods/resize", removed.Resources)
	}
	start := time.Now()
	restored := false
	t.Cleanup(func() { // registered before the change: runs even if the update below half-fails
		if restored {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := restoreResizeRule(c, cl, removed); err != nil {
			t.Errorf("cleanup: restore the pods/resize rule of %s on %s: %v (make e2e-unblock restores it)", agentClusterRole(), cl.Name, err)
			return
		}
		t.Logf("cleanup: pods/resize rule restored on %s", cl.Name)
		if r, err := sessionOf(operator).raw(c, http.MethodPost, "/v1/clusters/"+cl.Name+"/readiness/recheck", nil, nil); err != nil || r.Status != http.StatusAccepted {
			t.Logf("cleanup: recheck %s: %v %s", cl.Name, err, r)
		}
	})
	cr.Rules = slices.Delete(cr.Rules, i, i+1)
	must(t, cl.C.Update(ctx(t), &cr))
	t.Logf("removed the pods/resize rule from ClusterRole %s on %s", cr.Name, cl.Name)

	r := waitReadiness(t, cl.Name, 4*time.Minute, "ReadyAudit without pods/resize", func(r hubapi.ClusterReadiness) (bool, string) {
		c := check(r, rd.CheckRBACResize)
		return r.State == string(rd.StateReadyAudit) && c != nil && c.Status == "fail", fmt.Sprintf("rbac.resize %+v", c)
	})
	if !slices.Contains(r.BlockedBy, rd.CheckRBACResize) {
		t.Errorf("blockedBy %v does not name %s", r.BlockedBy, rd.CheckRBACResize)
	}
	for _, c := range r.Capabilities {
		if c.Name == string(rd.CapabilityResize) && c.Granted {
			t.Errorf("capability resize still granted")
		}
	}
	q := url.Values{"cluster": {cl.Name}, "targetType": {hubapi.TargetReadiness}}
	recent := func(c hubapi.ConfigChange) bool { return !c.Time.Before(start.Add(-5 * time.Second)) }
	ch := waitConfigChange(t, q, hubapi.ActionReadinessChanged, func(c hubapi.ConfigChange) bool {
		return recent(c) && afterField(t, c, "state") == string(rd.StateReadyAudit)
	})
	requireActor(t, ch, hubapi.Actor{Type: hubapi.ActorSystem, Name: "readiness"})

	in := hubPolicy(t, "e2e-rbac-"+randSuffix(), "e2e-rbac-none", []string{cl.Name}, autoCPU(v1alpha1.StrategyInPlace))
	if res := as(t, admin).do(t, http.MethodPost, "/v1/admin/policies", in, nil); res.Status != http.StatusConflict || res.Problem() != hubapi.CodeClusterNotAutomationReady {
		if res.Status == http.StatusCreated {
			var p hubapi.AdminPolicy
			_ = json.Unmarshal(res.Body, &p)
			_ = deleteHubPolicyQuiet(p.ID, "e2e: created by mistake")
		}
		t.Errorf("auto-patch policy on %s without pods/resize: want 409 %s, got %s", cl.Name, hubapi.CodeClusterNotAutomationReady, res)
	}

	// Restore and see the cluster come back, so later scenarios find it AutomationReady.
	must(t, restoreResizeRule(ctx(t), cl, removed))
	restored = true
	waitReadiness(t, cl.Name, 5*time.Minute, "AutomationReady after the restore", stateIs(string(rd.StateAutomationReady)))
	waitConfigChange(t, q, hubapi.ActionReadinessChanged, func(c hubapi.ConfigChange) bool {
		return recent(c) && afterField(t, c, "state") == string(rd.StateAutomationReady)
	})
}

// Scenario 11 (D-025): kube-state-metrics scaled to 0 on ro-spoke-2 → metrics.freshness fails
// (blocking) and the banner condition (blocking > 0) holds. Disruptive for every track using
// spoke-2's metrics: only with KERF_E2E_DISRUPTIVE=1 (and the user's OK).
func TestReadinessKSMDown(t *testing.T) {
	if os.Getenv("KERF_E2E_DISRUPTIVE") != "1" {
		t.Skip("scales kube-state-metrics on ro-spoke-2 to 0: set KERF_E2E_DISRUPTIVE=1 (needs the user's OK)")
	}
	t.Parallel()
	cl := env.Spoke2
	lockSpoke2(t)
	key := client.ObjectKey{Namespace: getenv("E2E_KSM_NAMESPACE", "monitoring"), Name: getenv("E2E_KSM_DEPLOYMENT", "kube-state-metrics")}
	var d appsv1.Deployment
	must(t, cl.C.Get(ctx(t), key, &d))
	orig := int32(1)
	if d.Spec.Replicas != nil && *d.Spec.Replicas > 0 {
		orig = *d.Spec.Replicas
	}
	scaleBack := func(c context.Context) error {
		var cur appsv1.Deployment
		if err := cl.C.Get(c, key, &cur); err != nil {
			return err
		}
		cur.Spec.Replicas = &orig
		delete(cur.Annotations, scaledFromAnnotation)
		if err := cl.C.Update(c, &cur); err != nil {
			return fmt.Errorf("scale %s back to %d: %w", key, orig, err)
		}
		return releaseFields(c, cl, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}})
	}
	restored := false
	t.Cleanup(func() { // fallback when the test fails before its own restore
		if restored {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := scaleBack(c); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	zero := int32(0)
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	d.Annotations[scaledFromAnnotation] = fmt.Sprint(orig)
	d.Spec.Replicas = &zero
	must(t, cl.C.Update(ctx(t), &d))
	t.Logf("scaled %s on %s to 0 (was %d)", key, cl.Name, orig)

	// Hub metrics checks run on a recheck; freshness fails once the series are 5 minutes stale.
	var r hubapi.ClusterReadiness
	var lastRecheck time.Time
	eventually(t, 12*time.Minute, 20*time.Second, "metrics.freshness fails", func() (bool, string) {
		if time.Since(lastRecheck) > 2*time.Minute {
			recheck(t, cl.Name)
			lastRecheck = time.Now()
		}
		r = readiness(t, cl.Name)
		c := check(r, rd.CheckFreshness)
		return c != nil && c.Status == "fail" && c.Severity == "blocking", fmt.Sprintf("state %s freshness %+v", r.State, c)
	})
	if r.Summary.Blocking == 0 {
		t.Errorf("summary.blocking is 0 with metrics.freshness failing: the banner would not show")
	}
	if rd.State(r.State).AtLeast(rd.StateMetricsOK) {
		t.Errorf("state %s with stale metrics, want below MetricsOK", r.State)
	}
	var list hubapi.ClusterList
	as(t, viewer).get(t, "/v1/clusters", &list)
	for _, c := range list.Items {
		if c.Name == cl.Name && (c.Readiness == nil || c.Readiness.Blocking == 0) {
			t.Errorf("cluster list readiness of %s: %+v, want blocking > 0 (banner)", c.Name, c.Readiness)
		}
	}

	// Restore and wait for AutomationReady, so later scenarios on spoke-2 don't hit the auto-patch
	// gate. The hub reruns metrics checks only every checkInterval (10m in dev) or on a recheck, and
	// freshness passes only once VictoriaMetrics has new KSM samples: recheck every minute.
	must(t, scaleBack(ctx(t)))
	restored = true
	t.Logf("scaled %s on %s back to %d", key, cl.Name, orig)
	lastRecheck = time.Time{}
	eventually(t, 10*time.Minute, 15*time.Second, cl.Name+" AutomationReady after the restore", func() (bool, string) {
		if time.Since(lastRecheck) > time.Minute {
			recheck(t, cl.Name)
			lastRecheck = time.Now()
		}
		r = readiness(t, cl.Name)
		return r.State == string(rd.StateAutomationReady), fmt.Sprintf("state %s blockedBy %v freshness %+v", r.State, r.BlockedBy, check(r, rd.CheckFreshness))
	})
}
