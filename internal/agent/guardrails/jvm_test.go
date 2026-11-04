package guardrails

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// TestJVMFixedHeap: memory of a JVM with a fixed -Xmx is audit-only (WO-12); CPU still applies.
func TestJVMFixedHeap(t *testing.T) {
	restart := []corev1.ContainerResizePolicy{{ResourceName: corev1.ResourceMemory, RestartPolicy: corev1.RestartContainer}}
	xmx := func(in *Input) {
		in.Containers[0].Image = "eclipse-temurin:21-jre"
		in.Containers[0].Command = []string{"java", "-Xmx128m", "-jar", "app.jar"}
		in.Containers[0].ResizePolicy = restart
	}
	tests := []struct {
		name    string
		mut     func(*Input)
		outcome Outcome
		reason  string
		// memory request after guardrails; refused is the expected refusal reason ("" = none).
		mem, refused string
	}{
		{name: "fixed -Xmx: memory refused, cpu applied", mut: xmx,
			outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, mem: "256Mi", refused: v1alpha1.ReasonJVMFixedHeap},
		{name: "fixed -Xmx with rollout: still audit-only", mut: func(in *Input) { xmx(in); in.Strategy = v1alpha1.StrategyRollout },
			outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, mem: "256Mi", refused: v1alpha1.ReasonJVMFixedHeap},
		{name: "fixed -Xmx without RestartContainer: JVMFixedHeap wins over RestartPolicyRequired", mut: func(in *Input) {
			xmx(in)
			in.Containers[0].ResizePolicy = nil
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, mem: "256Mi", refused: v1alpha1.ReasonJVMFixedHeap},
		{name: "fixed -Xmx in JAVA_TOOL_OPTIONS, memory only: blocked", mut: func(in *Input) {
			in.Containers[0].Env = []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-Xmx1g"}}
			in.Containers[0].ResizePolicy = restart
			in.Effective.CPUMode = v1alpha1.ModeAudit
		}, outcome: Blocked, reason: v1alpha1.ReasonJVMFixedHeap, mem: "256Mi", refused: v1alpha1.ReasonJVMFixedHeap},
		{name: "memory increase on fixed -Xmx also refused", mut: func(in *Input) {
			xmx(in)
			in.Rec = rec("200m", "400Mi", "", "")
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, mem: "256Mi", refused: v1alpha1.ReasonJVMFixedHeap},
		{name: "hub flag alone refuses memory", mut: func(in *Input) {
			in.Rec.Containers[0].RiskFlags = []string{string(v1alpha1.RiskJVMFixedHeap)}
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, mem: "256Mi", refused: v1alpha1.ReasonJVMFixedHeap},
		{name: "MaxRAMPercentage with RestartContainer: memory applies", mut: func(in *Input) {
			in.Containers[0].Image = "eclipse-temurin:21-jre"
			in.Containers[0].Env = []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-XX:MaxRAMPercentage=75.0"}}
			in.Containers[0].ResizePolicy = restart
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, mem: "200Mi"},
		{name: "detection off: -Xmx not detected", mut: func(in *Input) {
			xmx(in)
			in.Effective.JVMDetect = false
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, mem: "200Mi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mut(&in)
			d := Evaluate(in)
			if d.Outcome != tt.outcome || d.Reason != tt.reason {
				t.Fatalf("decision = %s/%s (%s), want %s/%s", d.Outcome, d.Reason, d.Message, tt.outcome, tt.reason)
			}
			if got := target(d, corev1.ResourceMemory, false); d.Outcome == Apply && got != tt.mem {
				t.Errorf("memory request = %s, want %s", got, tt.mem)
			}
			var reasons []string
			for _, r := range d.Refusals {
				reasons = append(reasons, r.Reason)
				if r.Reason == v1alpha1.ReasonJVMFixedHeap && (r.Resource != "memory" || !strings.Contains(r.Message, "audit-only")) {
					t.Errorf("refusal %+v", r)
				}
			}
			if tt.refused == "" && len(reasons) > 0 || tt.refused != "" && (len(reasons) != 1 || reasons[0] != tt.refused) {
				t.Errorf("refusals = %v, want [%s]", reasons, tt.refused)
			}
		})
	}
}
