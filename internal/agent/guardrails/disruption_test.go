package guardrails

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/apply"
	"github.com/kerf-dev/kerf/internal/agent/workload"
)

func withResizePolicy(c workload.Container, res corev1.ResourceName, p corev1.ResourceResizeRestartPolicy) workload.Container {
	c.ResizePolicy = append(c.ResizePolicy, corev1.ContainerResizePolicy{ResourceName: res, RestartPolicy: p})
	return c
}

// TestSingleReplica: single-replica protection, one input changed per case.
func TestSingleReplica(t *testing.T) {
	single := &apply.Disruption{SingleReplica: true, Why: "replicas: 1", RolloutSafe: true}
	unsafeRollout := &apply.Disruption{SingleReplica: true, Why: "replicas: 1", RolloutWhy: "Deployment strategy Recreate stops every pod"}
	tests := []struct {
		name    string
		mut     func(*Input)
		outcome Outcome
		msg     string
	}{
		{name: "no disruption info", mut: func(*Input) {}, outcome: Apply},
		{name: "multi-replica", mut: func(in *Input) { in.Disruption = &apply.Disruption{RolloutSafe: false} }, outcome: Apply},
		{name: "in-place without restart", mut: func(in *Input) { in.Disruption = unsafeRollout }, outcome: Apply},
		{name: "in-place memory change with RestartContainer", mut: func(in *Input) {
			in.Disruption = single
			in.Containers[0] = withResizePolicy(in.Containers[0], corev1.ResourceMemory, corev1.RestartContainer)
		}, outcome: Blocked, msg: "restarts container app (memory)"},
		{name: "RestartContainer on a resource that does not change", mut: func(in *Input) {
			in.Disruption = single
			in.Rec = rec("200m", "256Mi", "", "") // memory unchanged
			in.Containers[0] = withResizePolicy(in.Containers[0], corev1.ResourceMemory, corev1.RestartContainer)
		}, outcome: Apply},
		{name: "rollout with surge", mut: func(in *Input) {
			in.Disruption, in.Strategy = single, v1alpha1.StrategyRollout
		}, outcome: Apply},
		{name: "rollout without surge", mut: func(in *Input) {
			in.Disruption, in.Strategy = unsafeRollout, v1alpha1.StrategyRollout
		}, outcome: Blocked, msg: "a rollout would leave no Ready pod: Deployment strategy Recreate"},
		{name: "opt-in", mut: func(in *Input) {
			d := *unsafeRollout
			d.Allowed = true
			in.Disruption, in.Strategy = &d, v1alpha1.StrategyRollout
		}, outcome: Apply},
		{name: "nothing to change wins", mut: func(in *Input) {
			in.Disruption, in.Strategy = unsafeRollout, v1alpha1.StrategyRollout
			in.Rec = rec("300m", "256Mi", "", "")
		}, outcome: Skipped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mut(&in)
			d := Evaluate(in)
			if d.Outcome != tc.outcome {
				t.Fatalf("outcome %s (%s: %s), want %s", d.Outcome, d.Reason, d.Message, tc.outcome)
			}
			if tc.outcome == Blocked {
				if d.Reason != v1alpha1.ReasonSingleReplicaUnsafe || !strings.Contains(d.Message, tc.msg) || !strings.Contains(d.Message, v1alpha1.AnnotationAllowDisruption) {
					t.Fatalf("reason %s message %q", d.Reason, d.Message)
				}
				if len(d.Targets) == 0 || len(d.Proposed) == 0 {
					t.Fatal("a blocked decision keeps the targets for the RSR")
				}
			}
		})
	}
}

func TestSingleReplicaUnsafeForFallback(t *testing.T) {
	changes := Evaluate(base()).Changes
	cs := base().Containers
	if why := SingleReplicaUnsafe(&apply.Disruption{SingleReplica: true, Why: "replicas: 1", RolloutWhy: "x"}, v1alpha1.StrategyRollout, cs, changes); why == "" {
		t.Fatal("unsafe fallback not detected")
	}
	if why := SingleReplicaUnsafe(&apply.Disruption{SingleReplica: true, RolloutSafe: true}, v1alpha1.StrategyRollout, cs, changes); why != "" {
		t.Fatalf("safe fallback refused: %s", why)
	}
	if why := SingleReplicaUnsafe(nil, v1alpha1.StrategyRollout, cs, changes); why != "" {
		t.Fatalf("nil disruption: %s", why)
	}
}
