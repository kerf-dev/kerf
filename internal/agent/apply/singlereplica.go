package apply

import (
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/workload"
)

// Disruption describes how a workload tolerates its pods being restarted (WO-13, D-007).
type Disruption struct {
	// SingleReplica: a Deployment or StatefulSet with one replica, or a workload whose
	// PodDisruptionBudget allows no disruption. A change must then never leave it without a Ready pod.
	SingleReplica bool
	// Why explains SingleReplica ("replicas: 1", "PodDisruptionBudget web allows 0 disruptions").
	Why string
	// RolloutSafe: a template change keeps a Ready pod at every moment (Deployment or DaemonSet
	// RollingUpdate with maxSurge >= 1 and maxUnavailable 0).
	RolloutSafe bool
	// RolloutWhy explains why a rollout is not safe.
	RolloutWhy string
	// Allowed: the workload opted in with v1alpha1.AnnotationAllowDisruption.
	Allowed bool
	// PDBs are the names of the PodDisruptionBudgets that select the workload's pods.
	PDBs []string
}

// Unsafe reports whether disruption-free changes are required (single replica, no opt-in).
func (d *Disruption) Unsafe() bool { return d != nil && d.SingleReplica && !d.Allowed }

// AssessDisruption evaluates a workload against the PodDisruptionBudgets of its namespace.
func AssessDisruption(obj client.Object, pdbs []policyv1.PodDisruptionBudget) Disruption {
	d := Disruption{Allowed: strings.EqualFold(obj.GetAnnotations()[v1alpha1.AnnotationAllowDisruption], "true")}
	kind, _ := workload.KindOf(obj)
	if kind == v1alpha1.KindCronJob {
		// Every run starts new pods: nothing to disrupt.
		d.RolloutSafe = true
		return d
	}
	replicas := expectedPods(obj)
	if replicas == 0 {
		d.RolloutSafe = true
		return d
	}
	var why []string
	if (kind == v1alpha1.KindDeployment || kind == v1alpha1.KindStatefulSet) && replicas == 1 {
		why = append(why, "replicas: 1")
	}
	tmplLabels := labels.Set(templateLabels(obj))
	for _, p := range pdbs {
		if p.Namespace != obj.GetNamespace() || p.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil || sel.Empty() || !sel.Matches(tmplLabels) {
			continue
		}
		d.PDBs = append(d.PDBs, p.Name)
		if allowed := AllowedDisruptions(p.Spec, replicas); allowed <= 0 {
			why = append(why, fmt.Sprintf("PodDisruptionBudget %s allows 0 disruptions of %d pod(s)", p.Name, replicas))
		}
	}
	slices.Sort(d.PDBs)
	d.SingleReplica = len(why) > 0
	d.Why = strings.Join(why, "; ")
	d.RolloutSafe, d.RolloutWhy = rolloutSafe(obj, replicas)
	return d
}

// AllowedDisruptions is the number of pods a PodDisruptionBudget lets be down for the given
// number of expected pods (percentages round up, like the disruption controller).
func AllowedDisruptions(spec policyv1.PodDisruptionBudgetSpec, expected int32) int32 {
	switch {
	case spec.MaxUnavailable != nil:
		v, err := intstr.GetScaledValueFromIntOrPercent(spec.MaxUnavailable, int(expected), true)
		if err != nil {
			return 0
		}
		return int32(min(v, int(expected)))
	case spec.MinAvailable != nil:
		v, err := intstr.GetScaledValueFromIntOrPercent(spec.MinAvailable, int(expected), true)
		if err != nil {
			return 0
		}
		return expected - int32(v)
	}
	return expected
}

func expectedPods(obj client.Object) int32 {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		if o.Spec.Replicas == nil {
			return 1
		}
		return *o.Spec.Replicas
	case *appsv1.StatefulSet:
		if o.Spec.Replicas == nil {
			return 1
		}
		return *o.Spec.Replicas
	case *appsv1.DaemonSet:
		return o.Status.DesiredNumberScheduled
	}
	return 0
}

func templateLabels(obj client.Object) map[string]string {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return o.Spec.Template.Labels
	case *appsv1.StatefulSet:
		return o.Spec.Template.Labels
	case *appsv1.DaemonSet:
		return o.Spec.Template.Labels
	}
	return nil
}

// rolloutSafe reports whether a pod template change keeps a Ready pod at every moment.
func rolloutSafe(obj client.Object, replicas int32) (bool, string) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		if o.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
			return false, "Deployment strategy Recreate stops every pod before starting new ones"
		}
		var surge, unavailable *intstr.IntOrString
		if ru := o.Spec.Strategy.RollingUpdate; ru != nil {
			surge, unavailable = ru.MaxSurge, ru.MaxUnavailable
		}
		return surgeSafe("Deployment", surge, unavailable, replicas, intstr.FromString("25%"), intstr.FromString("25%"))
	case *appsv1.DaemonSet:
		if o.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
			return false, "DaemonSet updateStrategy OnDelete: pods are only replaced when deleted"
		}
		var surge, unavailable *intstr.IntOrString
		if ru := o.Spec.UpdateStrategy.RollingUpdate; ru != nil {
			surge, unavailable = ru.MaxSurge, ru.MaxUnavailable
		}
		return surgeSafe("DaemonSet", surge, unavailable, replicas, intstr.FromInt32(0), intstr.FromInt32(1))
	case *appsv1.StatefulSet:
		return false, "StatefulSet pods are replaced delete-first (no surge)"
	}
	return true, ""
}

// surgeSafe resolves maxSurge (rounded up) and maxUnavailable (rounded down) like the
// Deployment controller does; both 0 means maxUnavailable 1.
func surgeSafe(kind string, surge, unavailable *intstr.IntOrString, replicas int32, defSurge, defUnavailable intstr.IntOrString) (bool, string) {
	if surge == nil {
		surge = &defSurge
	}
	if unavailable == nil {
		unavailable = &defUnavailable
	}
	s, err1 := intstr.GetScaledValueFromIntOrPercent(surge, int(replicas), true)
	u, err2 := intstr.GetScaledValueFromIntOrPercent(unavailable, int(replicas), false)
	if err1 != nil || err2 != nil {
		return false, fmt.Sprintf("%s rolling update parameters cannot be parsed", kind)
	}
	if s == 0 && u == 0 {
		u = 1
	}
	if s >= 1 && u == 0 {
		return true, ""
	}
	return false, fmt.Sprintf("%s rolling update has maxSurge %s (%d) and maxUnavailable %s (%d); needs maxSurge >= 1 and maxUnavailable 0",
		kind, surge.String(), s, unavailable.String(), u)
}
