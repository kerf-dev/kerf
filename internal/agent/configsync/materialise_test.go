package configsync

import (
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

func hubPolicy(name, typ string, version int, prio int32, cpu v1alpha1.Mode) hubclient.ConfigPolicy {
	return hubclient.ConfigPolicy{ID: "id-" + name, Name: name, Type: typ, Version: version, Priority: prio,
		Spec: v1alpha1.RightsizingPolicySpec{Priority: prio, Selector: v1alpha1.PolicySelector{Namespaces: []string{"mat-" + name}},
			CPU: &v1alpha1.CPUPolicy{Mode: cpu}}}
}

func getPolicy(t *testing.T, name string) *v1alpha1.RightsizingPolicy {
	t.Helper()
	var p v1alpha1.RightsizingPolicy
	if err := k8s.Get(testCtx, client.ObjectKey{Name: name}, &p); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return &p
}

func item(inv []hubclient.PolicyInventoryItem, name, source string) *hubclient.PolicyInventoryItem {
	for i := range inv {
		if inv[i].Name == name && inv[i].Source == source {
			return &inv[i]
		}
	}
	return nil
}

func gitPolicy(t *testing.T, name string) *v1alpha1.RightsizingPolicy {
	t.Helper()
	p := &v1alpha1.RightsizingPolicy{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.RightsizingPolicySpec{Priority: 50, CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit}}}
	if err := k8s.Create(testCtx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8s.Delete(testCtx, p) })
	return p
}

// TestMaterialise covers create / update / delete of hub CRs, Git CRs left alone, a name
// collision, invalid hub policies, read-only and "no config yet".
func TestMaterialise(t *testing.T) {
	needEnv(t)
	git := gitPolicy(t, "mat-git")
	taken := gitPolicy(t, "mat-taken")

	a := hubPolicy("mat-a", v1alpha1.HubPolicyTypePolicy, 1, 100, v1alpha1.ModeAutoPatch)
	global := hubPolicy(v1alpha1.GlobalDefaultsPolicyName, v1alpha1.HubPolicyTypeGlobalDefault, 1, 0, v1alpha1.ModeAudit)
	global.Spec.Selector = v1alpha1.PolicySelector{}
	collide := hubPolicy("mat-taken", v1alpha1.HubPolicyTypePolicy, 1, 100, v1alpha1.ModeAutoPatch)
	invalid := hubPolicy("mat-bad", v1alpha1.HubPolicyTypePolicy, 1, 0, v1alpha1.ModeAutoPatch) // priority 0: not a policy band
	cfg := &hubclient.ClusterConfig{ConfigVersion: "c-1", Policies: []hubclient.ConfigPolicy{a, global, collide, invalid}}

	// No config yet / read-only: nothing is written.
	if res, err := Materialise(testCtx, k8s, nil, false, testLog); err != nil || len(res.Created) > 0 || item(res.Inventory, "mat-git", "git") == nil {
		t.Fatalf("nil config: %+v %v", res, err)
	}
	res, err := Materialise(testCtx, k8s, cfg, true, testLog)
	if err != nil || len(res.Created) > 0 {
		t.Fatalf("read-only: %+v %v", res, err)
	}
	if it := item(res.Inventory, "mat-a", "hub"); it == nil || it.Valid || it.Message != MsgReadOnly || it.Generation != 0 {
		t.Fatalf("read-only inventory: %+v", it)
	}

	res, err = Materialise(testCtx, k8s, cfg, false, testLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = Materialise(testCtx, k8s, &hubclient.ClusterConfig{ConfigVersion: "c-end"}, false, testLog)
	})
	if !slices.Equal(res.Created, []string{"mat-a", v1alpha1.GlobalDefaultsPolicyName}) || len(res.Deleted) > 0 {
		t.Fatalf("created %v deleted %v", res.Created, res.Deleted)
	}
	p := getPolicy(t, "mat-a")
	if p.Labels[v1alpha1.LabelManagedBy] != v1alpha1.ManagedByHub || p.Annotations[v1alpha1.AnnotationHubPolicyID] != a.ID ||
		p.Annotations[v1alpha1.AnnotationHubVersion] != "1" || p.Annotations[v1alpha1.AnnotationHubPolicyType] != "policy" ||
		p.Spec.Priority != 100 || p.Spec.CPU.Mode != v1alpha1.ModeAutoPatch {
		t.Fatalf("CR %+v %+v", p.ObjectMeta, p.Spec)
	}
	if !slices.ContainsFunc(p.ManagedFields, func(m metav1.ManagedFieldsEntry) bool {
		return m.Manager == v1alpha1.FieldManager && m.Operation == metav1.ManagedFieldsOperationApply
	}) {
		t.Fatalf("not server-side applied by %s: %+v", v1alpha1.FieldManager, p.ManagedFields)
	}
	if got := getPolicy(t, "mat-taken"); got.Labels[v1alpha1.LabelManagedBy] != "" || got.Generation != taken.Generation || got.Spec.CPU.Mode != v1alpha1.ModeAudit {
		t.Fatalf("unmanaged CR touched: %+v", got)
	}
	inv := res.Inventory
	if it := item(inv, "mat-a", "hub"); it == nil || !it.Valid || it.Generation != 1 || it.HubVersion != 1 || it.HubPolicyID != a.ID || it.Spec == nil {
		t.Fatalf("inventory mat-a %+v", it)
	}
	if it := item(inv, "mat-taken", "hub"); it == nil || it.Valid || it.Message != MsgNameTaken || it.Generation != 0 {
		t.Fatalf("collision %+v", it)
	}
	if it := item(inv, "mat-taken", "git"); it == nil || !it.Valid {
		t.Fatalf("git CR of the collision %+v", it)
	}
	if it := item(inv, "mat-bad", "hub"); it == nil || it.Valid || !strings.Contains(it.Message, "spec.priority") {
		t.Fatalf("invalid %+v", it)
	}
	if it := item(inv, "mat-git", "git"); it == nil || !it.Valid || it.Generation != git.Generation || it.HubPolicyID != "" {
		t.Fatalf("git %+v", it)
	}

	// Unchanged config: no writes.
	if res, err = Materialise(testCtx, k8s, cfg, false, testLog); err != nil || len(res.Created)+len(res.Updated)+len(res.Deleted) > 0 {
		t.Fatalf("second run wrote: %+v %v", res, err)
	}

	// Edited outside the hub (kubectl edit): put back.
	p.Spec.Memory = &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAutoPatch}
	if err := k8s.Update(testCtx, p, client.FieldOwner("kubectl-edit")); err != nil {
		t.Fatal(err)
	}
	if res, err = Materialise(testCtx, k8s, cfg, false, testLog); err != nil || !slices.Equal(res.Updated, []string{"mat-a"}) {
		t.Fatalf("drift: %+v %v", res, err)
	}
	if got := getPolicy(t, "mat-a"); got.Spec.Memory != nil {
		t.Fatalf("foreign field kept: %+v", got.Spec.Memory)
	}

	// New version.
	a2 := a
	a2.Version, a2.Spec.CPU = 2, &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit}
	cfg.Policies[0] = a2
	if res, err = Materialise(testCtx, k8s, cfg, false, testLog); err != nil || !slices.Equal(res.Updated, []string{"mat-a"}) {
		t.Fatalf("update: %+v %v", res, err)
	}
	if got := getPolicy(t, "mat-a"); got.Annotations[v1alpha1.AnnotationHubVersion] != "2" || got.Spec.CPU.Mode != v1alpha1.ModeAudit {
		t.Fatalf("not updated: %+v", got)
	}

	// An invalid new version is not applied: version 2 stays and is reported invalid.
	a3 := a2
	a3.Version, a3.Spec.Selector.Namespaces = 3, []string{"[bad"}
	cfg.Policies[0] = a3
	if res, err = Materialise(testCtx, k8s, cfg, false, testLog); err != nil || len(res.Updated) > 0 {
		t.Fatalf("invalid update: %+v %v", res, err)
	}
	if got := getPolicy(t, "mat-a"); got.Annotations[v1alpha1.AnnotationHubVersion] != "2" {
		t.Fatalf("invalid version applied: %+v", got.Annotations)
	}
	if it := item(res.Inventory, "mat-a", "hub"); it == nil || it.Valid || it.HubVersion != 2 || it.Generation == 0 || !strings.Contains(it.Message, "hub version 3") {
		t.Fatalf("invalid update inventory: %+v", it)
	}

	// Removed from the config: deleted; Git CRs stay.
	cfg.Policies = []hubclient.ConfigPolicy{global}
	if res, err = Materialise(testCtx, k8s, cfg, false, testLog); err != nil || !slices.Equal(res.Deleted, []string{"mat-a"}) {
		t.Fatalf("delete: %+v %v", res, err)
	}
	for _, n := range []string{"mat-git", "mat-taken", v1alpha1.GlobalDefaultsPolicyName} {
		getPolicy(t, n)
	}
	// policySource git: no hub policies at all.
	if res, err = Materialise(testCtx, k8s, &hubclient.ClusterConfig{ConfigVersion: "c-9", PolicySource: "git"}, false, testLog); err != nil ||
		!slices.Equal(res.Deleted, []string{v1alpha1.GlobalDefaultsPolicyName}) {
		t.Fatalf("git only: %+v %v", res, err)
	}
}
