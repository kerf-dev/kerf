package guardrails

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/apply"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
	"github.com/kerf-dev/kerf/internal/agent/workload"
)

// singleReplicaUnsafe returns why the change would leave a single-replica workload without a
// Ready pod, or "": in place only without a container restart; a rollout only with
// maxSurge >= 1 and maxUnavailable 0. The allow-disruption annotation lifts the check.
func singleReplicaUnsafe(in Input, changes []hubclient.ContainerChange) string {
	d := in.Disruption
	if !d.Unsafe() || len(changes) == 0 {
		return ""
	}
	optIn := fmt.Sprintf("annotate %s=true to allow it", v1alpha1.AnnotationAllowDisruption)
	if in.Strategy == v1alpha1.StrategyInPlace {
		changed := changedResources(changes)
		var restarting []string
		for _, c := range in.Containers {
			res, ok := changed[c.Name]
			if !ok {
				continue
			}
			var hit []string
			for _, r := range res {
				if workload.ResizePolicy(c.Container, r) == corev1.RestartContainer {
					hit = append(hit, string(r))
				}
			}
			if len(hit) > 0 {
				restarting = append(restarting, fmt.Sprintf("%s (%s)", c.Name, strings.Join(hit, ", ")))
			}
		}
		if len(restarting) == 0 {
			return ""
		}
		return fmt.Sprintf("single replica (%s): the in-place resize restarts container %s (resizePolicy RestartContainer); %s",
			d.Why, strings.Join(restarting, ", "), optIn)
	}
	if d.RolloutSafe {
		return ""
	}
	return fmt.Sprintf("single replica (%s): a rollout would leave no Ready pod: %s; %s", d.Why, d.RolloutWhy, optIn)
}

// changedResources maps container name -> resources whose request or limit changes.
func changedResources(changes []hubclient.ContainerChange) map[string][]corev1.ResourceName {
	out := map[string][]corev1.ResourceName{}
	for _, c := range changes {
		var res []corev1.ResourceName
		for _, r := range resources {
			if valueChanged(c, r) {
				res = append(res, r)
			}
		}
		if len(res) > 0 {
			out[c.Name] = res
		}
	}
	return out
}

func valueChanged(c hubclient.ContainerChange, r corev1.ResourceName) bool {
	pick := func(v *hubclient.ResourceValues) string {
		if v == nil {
			return ""
		}
		if r == corev1.ResourceCPU {
			return v.CPU
		}
		return v.Memory
	}
	if c.From == nil || c.To == nil {
		return false
	}
	return pick(c.From.Requests) != pick(c.To.Requests) || pick(c.From.Limits) != pick(c.To.Limits)
}

// SingleReplicaUnsafe is the check for a change that is already decided, e.g. an in-place
// resize that has to fall back to a rollout: "" when the rollout is safe.
func SingleReplicaUnsafe(d *apply.Disruption, strategy v1alpha1.Strategy, containers []workload.Container,
	changes []hubclient.ContainerChange) string {
	return singleReplicaUnsafe(Input{Disruption: d, Strategy: strategy, Containers: containers}, changes)
}
