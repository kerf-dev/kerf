package webhook

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

// Skip reasons for a recommendation that must not be injected.
const (
	ReasonNotApplied  = "not-applied"
	ReasonNotInPlace  = "not-in-place"
	ReasonStaleStatus = "stale-status"
)

// Eligible returns "" when the recommendation's values are to be injected into new pods, otherwise
// the skip reason. Values are injected only while the recommendation is Applied with the in-place
// strategy (after an in-place fallback the agent sets spec.strategy to rollout, which stops us),
// and only when the status describes the current spec: the agent writes spec and status in two
// requests, and between them a new spec must not be treated as live.
func Eligible(r *v1alpha1.RightsizingRecommendation) string {
	switch {
	case r.Status.Phase != v1alpha1.PhaseApplied:
		return ReasonNotApplied
	case r.Spec.Strategy != v1alpha1.StrategyInPlace:
		return ReasonNotInPlace
	case r.Status.ObservedGeneration != r.Generation:
		return ReasonStaleStatus
	}
	return ""
}

// Mutation is the result of Mutate.
type Mutation struct {
	// Injected are the values from the recommendation the pod now carries (per container, only the
	// cpu/memory keys the recommendation sets). Recorded in AnnotationInjectedResources.
	Injected []v1alpha1.ContainerResources
	// Changed is true when the pod spec differs from the input.
	Changed bool
	// Skipped lists "<container>/<resource>: <why>" for recommended values that were not injected.
	Skipped []string
}

var managed = []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory}

// Mutate sets the recommended requests/limits of rec on the matching containers (regular, init and
// sidecar) of pod, in place. Only cpu and memory are touched; spec.containers carries values only for
// the resources in auto-patch, and other keys are left as the template set them. Limits listed in
// removeLimits are removed. A resource whose result would be invalid (request above limit) keeps the
// template's request and limit.
func Mutate(pod *corev1.Pod, rec []v1alpha1.ContainerRecommendation) Mutation {
	var m Mutation
	for _, cr := range rec {
		c := findContainer(&pod.Spec, cr.Name)
		if c == nil {
			m.Skipped = append(m.Skipped, cr.Name+": container not in pod")
			continue
		}
		inj := v1alpha1.ContainerResources{Name: cr.Name}
		removed := map[corev1.ResourceName]bool{}
		for _, res := range cr.RemoveLimits {
			removed[res] = true
		}
		for _, res := range managed {
			req, hasReq := cr.Requests[res]
			lim, hasLim := cr.Limits[res]
			rm := removed[res] && !hasLim
			if !hasReq && !hasLim && !rm {
				continue
			}
			newReq, okReq := c.Resources.Requests[res]
			if hasReq {
				newReq, okReq = req, true
			}
			newLim, okLim := c.Resources.Limits[res]
			if hasLim {
				newLim, okLim = lim, true
			}
			if rm {
				okLim = false
			}
			if okReq && okLim && newReq.Cmp(newLim) > 0 {
				m.Skipped = append(m.Skipped, fmt.Sprintf("%s/%s: request %s above limit %s", cr.Name, res, newReq.String(), newLim.String()))
				continue
			}
			if hasReq {
				if cur, ok := c.Resources.Requests[res]; !ok || cur.Cmp(req) != 0 {
					m.Changed = true
				}
				if c.Resources.Requests == nil {
					c.Resources.Requests = corev1.ResourceList{}
				}
				c.Resources.Requests[res] = req
				if inj.Requests == nil {
					inj.Requests = corev1.ResourceList{}
				}
				inj.Requests[res] = req
			}
			if hasLim {
				if cur, ok := c.Resources.Limits[res]; !ok || cur.Cmp(lim) != 0 {
					m.Changed = true
				}
				if c.Resources.Limits == nil {
					c.Resources.Limits = corev1.ResourceList{}
				}
				c.Resources.Limits[res] = lim
				if inj.Limits == nil {
					inj.Limits = corev1.ResourceList{}
				}
				inj.Limits[res] = lim
			}
			if rm {
				if _, ok := c.Resources.Limits[res]; ok {
					m.Changed = true
					delete(c.Resources.Limits, res)
				}
				if len(c.Resources.Limits) == 0 {
					c.Resources.Limits = nil
				}
			}
		}
		if inj.Requests != nil || inj.Limits != nil {
			m.Injected = append(m.Injected, inj)
		}
	}
	sort.Slice(m.Injected, func(i, j int) bool { return m.Injected[i].Name < m.Injected[j].Name })
	return m
}

func findContainer(spec *corev1.PodSpec, name string) *corev1.Container {
	for i := range spec.Containers {
		if spec.Containers[i].Name == name {
			return &spec.Containers[i]
		}
	}
	for i := range spec.InitContainers {
		if spec.InitContainers[i].Name == name {
			return &spec.InitContainers[i]
		}
	}
	return nil
}
