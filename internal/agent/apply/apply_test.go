package apply

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

func rl(cpu, mem string) corev1.ResourceList {
	l := corev1.ResourceList{}
	if cpu != "" {
		l[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if mem != "" {
		l[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	return l
}

func TestOwnedResources(t *testing.T) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{ManagedFields: []metav1.ManagedFieldsEntry{
		{Manager: "argocd-controller", FieldsV1: metav1.NewFieldsV1(`{"f:spec":{"f:template":{"f:spec":{"f:containers":{"k:{\"name\":\"app\"}":{"f:resources":{"f:requests":{"f:memory":{}}}}}}}}}`)},
		{Manager: FieldManager, Operation: metav1.ManagedFieldsOperationApply, FieldsV1: metav1.NewFieldsV1(`{"f:metadata":{"f:annotations":{"f:resource-optimizer.oz.io/managed":{}}},"f:spec":{"f:template":{"f:spec":{"f:containers":{"k:{\"name\":\"app\"}":{".":{},"f:name":{},"f:resources":{"f:limits":{"f:cpu":{}}}}}}}}}`)},
	}}}
	got := OwnedResources(d)
	if !got[corev1.ResourceCPU] || got[corev1.ResourceMemory] {
		t.Fatalf("owned = %v, want cpu only", got)
	}
}

func TestTemplateConfigOnlyOwnedResources(t *testing.T) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "n"}}
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app"}}
	d.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "side", RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways)}}
	targets := []v1alpha1.ContainerRecommendation{
		{ContainerResources: v1alpha1.ContainerResources{Name: "app", Requests: rl("100m", "64Mi"), Limits: rl("", "64Mi")}},
		{ContainerResources: v1alpha1.ContainerResources{Name: "side", Requests: rl("10m", "")}},
		{ContainerResources: v1alpha1.ContainerResources{Name: "gone", Requests: rl("10m", "")}},
	}
	u, err := TemplateConfig(d, v1alpha1.KindDeployment, targets, map[corev1.ResourceName]bool{corev1.ResourceCPU: true}, map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}
	cs := u.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	app := cs["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	if _, ok := app["limits"]; ok {
		t.Errorf("memory limit must not be applied when only cpu is owned: %v", app)
	}
	if app["requests"].(map[string]any)["cpu"] != "100m" || app["requests"].(map[string]any)["memory"] != nil {
		t.Errorf("requests = %v", app["requests"])
	}
	if len(cs["initContainers"].([]any)) != 1 || len(cs["containers"].([]any)) != 1 {
		t.Errorf("containers %v init %v", cs["containers"], cs["initContainers"])
	}
	if u.GetKind() != "Deployment" || u.GetAPIVersion() != "apps/v1" || u.GetAnnotations()["a"] != "b" {
		t.Errorf("object meta: %v", u.Object)
	}
}

func TestObserveResize(t *testing.T) {
	own := map[corev1.ResourceName]bool{corev1.ResourceCPU: true}
	targets := []v1alpha1.ContainerRecommendation{{ContainerResources: v1alpha1.ContainerResources{Name: "app", Requests: rl("250m", "")}}}
	pod := func(spec, actual string, conds ...corev1.PodCondition) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
		p.Spec.Containers = []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: rl(spec, "64Mi")}}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Resources: &corev1.ResourceRequirements{Requests: rl(actual, "64Mi")}}}
		p.Status.Conditions = conds
		return p
	}
	since := metav1.NewTime(time.Now().Add(-time.Hour))
	tests := []struct {
		name string
		pod  *corev1.Pod
		want ResizeState
	}{
		{"needed", pod("500m", "500m"), ResizeNeeded},
		{"in progress", pod("250m", "500m"), ResizeInProgress},
		{"done", pod("250m", "250m"), ResizeDone},
		{"deferred", pod("250m", "500m", corev1.PodCondition{Type: corev1.PodResizePending, Status: corev1.ConditionTrue, Reason: corev1.PodReasonDeferred, LastTransitionTime: since}), ResizeDeferred},
		{"infeasible", pod("250m", "500m", corev1.PodCondition{Type: corev1.PodResizePending, Status: corev1.ConditionTrue, Reason: corev1.PodReasonInfeasible}), ResizeInfeasible},
		{"kubelet busy", pod("250m", "250m", corev1.PodCondition{Type: corev1.PodResizeInProgress, Status: corev1.ConditionTrue}), ResizeInProgress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ObserveResize(tt.pod, targets, own)
			if got.State != tt.want {
				t.Fatalf("state %s, want %s", got.State, tt.want)
			}
			if tt.want == ResizeDeferred && !got.Since.Equal(since.Time) {
				t.Errorf("since %v", got.Since)
			}
		})
	}
}

func TestNodeSupportsInPlace(t *testing.T) {
	swap := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "s"}}
	swap.Status.NodeInfo.Swap = &corev1.NodeSwapStatus{Capacity: ptr.To[int64](1 << 30)}
	static := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "c", Labels: map[string]string{"cpu-manager": "static"}}}
	plain := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	sel := []map[string]string{{"cpu-manager": "static"}}
	tests := []struct {
		name string
		node *corev1.Node
		mem  bool
		want bool
	}{
		{"plain", plain, true, true},
		{"swap cpu only", swap, false, true},
		{"swap memory", swap, true, false},
		{"static cpu manager", static, false, false},
	}
	for _, tt := range tests {
		if got, why := NodeSupportsInPlace(tt.node, sel, tt.mem); got != tt.want {
			t.Errorf("%s: %v (%s), want %v", tt.name, got, why, tt.want)
		}
	}
}
