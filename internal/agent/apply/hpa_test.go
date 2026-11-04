package apply

import (
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

func resMetric(name corev1.ResourceName, typ autoscalingv2.MetricTargetType, util int32) autoscalingv2.MetricSpec {
	t := autoscalingv2.MetricTarget{Type: typ}
	if typ == autoscalingv2.UtilizationMetricType {
		t.AverageUtilization = ptr.To(util)
	}
	return autoscalingv2.MetricSpec{Type: autoscalingv2.ResourceMetricSourceType,
		Resource: &autoscalingv2.ResourceMetricSource{Name: name, Target: t}}
}

func hpaWith(current, maxR int32, metrics ...autoscalingv2.MetricSpec) *autoscalingv2.HorizontalPodAutoscaler {
	h := &autoscalingv2.HorizontalPodAutoscaler{}
	h.Name = "web"
	h.Spec.MaxReplicas = maxR
	h.Spec.Metrics = metrics
	h.Status.CurrentReplicas = current
	return h
}

func TestCoupling(t *testing.T) {
	cpu60 := resMetric(corev1.ResourceCPU, autoscalingv2.UtilizationMetricType, 60)
	mem70 := resMetric(corev1.ResourceMemory, autoscalingv2.UtilizationMetricType, 70)
	cpuAvg := resMetric(corev1.ResourceCPU, autoscalingv2.AverageValueMetricType, 0)
	external := autoscalingv2.MetricSpec{Type: autoscalingv2.ExternalMetricSourceType}
	container := autoscalingv2.MetricSpec{Type: autoscalingv2.ContainerResourceMetricSourceType,
		ContainerResource: &autoscalingv2.ContainerResourceMetricSource{Name: corev1.ResourceCPU, Container: "app",
			Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr.To[int32](60)}}}
	for _, tc := range []struct {
		name     string
		hpa      *autoscalingv2.HorizontalPodAutoscaler
		coupled  bool
		resource corev1.ResourceName
		target   int32
		problem  string
	}{
		{"no HPA", nil, false, "", 0, ""},
		{"cpu utilization", hpaWith(2, 4, cpu60), true, corev1.ResourceCPU, 60, ""},
		{"memory utilization", hpaWith(2, 4, mem70), true, corev1.ResourceMemory, 70, ""},
		{"average value only", hpaWith(2, 4, cpuAvg), false, "", 0, ""},
		{"external only", hpaWith(2, 4, external), false, "", 0, ""},
		{"utilization plus external", hpaWith(2, 4, cpu60, external), true, "", 0, HPAProblemMetrics},
		{"cpu and memory utilization", hpaWith(2, 4, cpu60, mem70), true, "", 0, HPAProblemMetrics},
		{"container resource", hpaWith(2, 4, container), true, "", 0, HPAProblemContainer},
		{"at maxReplicas", hpaWith(4, 4, cpu60), true, corev1.ResourceCPU, 60, HPAProblemMaxReplicas},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Coupling(tc.hpa)
			if c.Coupled != tc.coupled || c.Resource != tc.resource || c.Target != tc.target || c.Problem != tc.problem {
				t.Errorf("got %+v", c)
			}
			if want := tc.coupled && tc.problem == ""; c.Coordinatable() != want {
				t.Errorf("coordinatable %v, want %v", c.Coordinatable(), want)
			}
		})
	}
}

func TestRequestSumAndTarget(t *testing.T) {
	spec := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup", Resources: corev1.ResourceRequirements{Requests: rl("1", "")}}},
		Containers: []corev1.Container{
			{Name: "app", Resources: corev1.ResourceRequirements{Requests: rl("500m", "256Mi")}},
			{Name: "proxy", Resources: corev1.ResourceRequirements{Requests: rl("100m", "")}},
		},
	}
	if s, ok := RequestSum(spec, nil, corev1.ResourceCPU); !ok || s != 0.6 {
		t.Errorf("live cpu sum %v %v, want 0.6 (init container excluded)", s, ok)
	}
	targets := []v1alpha1.ContainerRecommendation{{ContainerResources: v1alpha1.ContainerResources{Name: "app", Requests: rl("300m", "")}}}
	after, ok := RequestSum(spec, targets, corev1.ResourceCPU)
	if !ok || after != 0.4 {
		t.Errorf("target cpu sum %v %v, want 0.4", after, ok)
	}
	if _, ok := RequestSum(spec, nil, corev1.ResourceMemory); ok {
		t.Error("memory sum ok although proxy has no memory request")
	}
	if got := NewHPATarget(60, 0.6, after); got != 90 {
		t.Errorf("target %d, want 90", got)
	}
	if got := NewHPATarget(60, 0.6, 0); got != 60 {
		t.Errorf("target with no requests %d", got)
	}
	h := hpaWith(2, 4, resMetric(corev1.ResourceCPU, autoscalingv2.UtilizationMetricType, 60))
	if HPATargetOf(h, corev1.ResourceCPU) != 60 || HPATargetOf(h, corev1.ResourceMemory) != 0 {
		t.Errorf("HPATargetOf")
	}
}
