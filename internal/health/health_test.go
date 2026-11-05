package health

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kerf-dev/kerf/api/v1alpha1"
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

func cstatus(name string, restarts int32, last, cur *corev1.ContainerStateTerminated) corev1.ContainerStatus {
	cs := corev1.ContainerStatus{Name: name, RestartCount: restarts}
	cs.LastTerminationState.Terminated = last
	cs.State.Terminated = cur
	if cur == nil {
		cs.State.Running = &corev1.ContainerStateRunning{}
	}
	return cs
}

func oom(at time.Time) *corev1.ContainerStateTerminated {
	return &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(at)}
}

func TestObserve(t *testing.T) {
	applied := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	old := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "old", CreationTimestamp: metav1.NewTime(applied.Add(-time.Hour))}}
	fresh := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "new", CreationTimestamp: metav1.NewTime(applied.Add(time.Minute))}}

	s := newTracker().state("rsr", applied)
	// First observation: the old pod's 5 restarts happened before the change.
	old.Status.ContainerStatuses = []corev1.ContainerStatus{cstatus("app", 5, oom(applied.Add(-10*time.Minute)), nil)}
	fresh.Status.ContainerStatuses = []corev1.ContainerStatus{cstatus("app", 1, &corev1.ContainerStateTerminated{Reason: "Error", FinishedAt: metav1.NewTime(applied.Add(2 * time.Minute))}, nil)}
	sig := s.observe([]corev1.Pod{old, fresh})
	if sig.Restarts != 1 || sig.OOMKills != 0 {
		t.Fatalf("first: %+v", sig)
	}
	// Old pod OOMs twice after the change; the status only keeps the last one.
	old.Status.ContainerStatuses = []corev1.ContainerStatus{cstatus("app", 6, oom(applied.Add(3*time.Minute)), nil)}
	if sig = s.observe([]corev1.Pod{old, fresh}); sig.OOMKills != 1 || sig.Restarts != 2 {
		t.Fatalf("second: %+v", sig)
	}
	old.Status.ContainerStatuses = []corev1.ContainerStatus{cstatus("app", 7, oom(applied.Add(5*time.Minute)), nil)}
	if sig = s.observe([]corev1.Pod{old, fresh}); sig.OOMKills != 2 || sig.Restarts != 3 {
		t.Fatalf("third: %+v", sig)
	}
	// Currently terminated by OOM counts too, once.
	fresh.Status.ContainerStatuses = []corev1.ContainerStatus{cstatus("app", 1, nil, oom(applied.Add(6*time.Minute)))}
	s.observe([]corev1.Pod{old, fresh})
	if sig = s.observe([]corev1.Pod{old, fresh}); sig.OOMKills != 3 {
		t.Fatalf("fourth: %+v", sig)
	}
	// A new change resets the state.
	if s2 := newTracker().state("rsr", applied.Add(time.Hour)); len(s2.ooms) != 0 {
		t.Fatal("state not reset")
	}
}

func TestChangedAndRestore(t *testing.T) {
	prev := []v1alpha1.ContainerResources{
		{Name: "app", Requests: rl("500m", "256Mi"), Limits: rl("1", "512Mi")},
		{Name: "side", Requests: rl("50m", "64Mi")},
	}
	targets := []v1alpha1.ContainerRecommendation{
		{ContainerResources: v1alpha1.ContainerResources{Name: "app", Requests: rl("250m", "128Mi"), Limits: rl("", "128Mi")},
			RemoveLimits: []corev1.ResourceName{corev1.ResourceCPU}},
		{ContainerResources: v1alpha1.ContainerResources{Name: "side", Requests: rl("50m", "64Mi"), Limits: rl("", "64Mi")}},
	}
	if got := ChangedResources(prev, targets); !slices.Equal(got, []string{"cpu", "memory"}) {
		t.Fatalf("changed = %v", got)
	}
	if got := changedContainers(prev, targets, "memory"); !slices.Equal(got, []string{"app", "side"}) {
		t.Fatalf("memory containers = %v", got)
	}
	if got := changedContainers(prev, targets, "cpu"); !slices.Equal(got, []string{"app"}) {
		t.Fatalf("cpu containers = %v", got)
	}

	mem := RestoreTargets(targets, prev, []string{"memory"})
	app, side := mem[0], mem[1]
	if app.Requests.Memory().String() != "256Mi" || app.Limits.Memory().String() != "512Mi" || app.Requests.Cpu().String() != "250m" {
		t.Errorf("app after memory restore: %+v", app)
	}
	if !slices.Equal(app.RemoveLimits, []corev1.ResourceName{corev1.ResourceCPU}) {
		t.Errorf("cpu limit removal must stay: %v", app.RemoveLimits)
	}
	if _, ok := side.Limits[corev1.ResourceMemory]; ok || !slices.Contains(side.RemoveLimits, corev1.ResourceMemory) {
		t.Errorf("the memory limit the change added must be removed: %+v", side)
	}
	cpu := RestoreTargets(targets, prev, []string{"cpu"})
	if cpu[0].Limits.Cpu().String() != "1" || len(cpu[0].RemoveLimits) != 0 || cpu[0].Requests.Cpu().String() != "500m" {
		t.Errorf("cpu restore must bring the limit back: %+v", cpu[0])
	}
	if targets[0].Requests.Cpu().String() != "250m" {
		t.Error("RestoreTargets modified its input")
	}
	ch := changes(targets, mem, []string{"memory"})
	if len(ch) != 2 || ch[0].From.Requests.Memory != "128Mi" || ch[0].To.Requests.Memory != "256Mi" || ch[0].To.Requests.CPU != "" {
		t.Errorf("changes: %+v", ch)
	}
}

func TestOwnerWorkloads(t *testing.T) {
	ctl := ptr.To(true)
	tests := []struct {
		ref  metav1.OwnerReference
		lbl  map[string]string
		kind v1alpha1.WorkloadKind
		name string
	}{
		{metav1.OwnerReference{Kind: "ReplicaSet", Name: "web-api-7c9f8d6b5", Controller: ctl}, map[string]string{"pod-template-hash": "7c9f8d6b5"}, v1alpha1.KindDeployment, "web-api"},
		{metav1.OwnerReference{Kind: "StatefulSet", Name: "db", Controller: ctl}, nil, v1alpha1.KindStatefulSet, "db"},
		{metav1.OwnerReference{Kind: "DaemonSet", Name: "agent", Controller: ctl}, nil, v1alpha1.KindDaemonSet, "agent"},
		{metav1.OwnerReference{Kind: "Job", Name: "nightly-batch-29895885", Controller: ctl}, nil, v1alpha1.KindCronJob, "nightly-batch"},
		{metav1.OwnerReference{Kind: "Job", Name: "one-off", Controller: ctl}, nil, "", ""},
	}
	for _, tt := range tests {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: tt.lbl, OwnerReferences: []metav1.OwnerReference{tt.ref}}}
		got := ownerWorkloads(p)
		if tt.kind == "" {
			if len(got) != 0 {
				t.Errorf("%s: got %v", tt.ref.Name, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Kind != tt.kind || got[0].Name != tt.name {
			t.Errorf("%s: got %v, want %s/%s", tt.ref.Name, got, tt.kind, tt.name)
		}
	}
}

func TestResourcesForReason(t *testing.T) {
	if !slices.Equal(ResourcesForReason(v1alpha1.ReasonOOMKills), []string{"memory"}) ||
		!slices.Equal(ResourcesForReason(v1alpha1.ReasonThrottling), []string{"cpu"}) ||
		ResourcesForReason(v1alpha1.ReasonRestarts) != nil {
		t.Fatal("mapping")
	}
}

func TestPromSource(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		if r.Header.Get("Authorization") != "" {
			t.Error("no token configured, none expected")
		}
		if strings.Contains(gotQuery, `pod=~"empty-.*"`) {
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1730000000,"0.42"]}]}}`)
	}))
	defer srv.Close()
	src := &PromSource{URL: srv.URL}
	q := ThrottleQuery{Cluster: "ro-spoke-1", Namespace: "default", Containers: []string{"app"}, Since: time.Now().Add(-5 * time.Minute),
		Pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "web-1"}}, {ObjectMeta: metav1.ObjectMeta{Name: "web.2"}}}}
	r, ok, err := src.Ratio(context.Background(), q)
	if err != nil || !ok || r != 0.42 {
		t.Fatalf("ratio %v %v %v", r, ok, err)
	}
	for _, want := range []string{`cluster="ro-spoke-1"`, `namespace="default"`, `pod=~"web-1|web\\.2"`, `container=~"app"`,
		"container_cpu_cfs_throttled_periods_total", "[300s]", ">= 300"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q lacks %q", gotQuery, want)
		}
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv2.Close()
	if _, ok, err := (&PromSource{URL: srv2.URL}).Ratio(context.Background(), q); ok || err != nil {
		t.Fatalf("no data must be ok=false without error: %v %v", ok, err)
	}
	if _, _, err := (&PromSource{URL: "http://127.0.0.1:1"}).Ratio(context.Background(), q); err == nil {
		t.Fatal("unreachable endpoint must fail")
	}
}

func TestParseCFS(t *testing.T) {
	raw := `# HELP container_cpu_cfs_periods_total Number of elapsed enforcement period intervals.
# TYPE container_cpu_cfs_periods_total counter
container_cpu_cfs_periods_total{container="app",id="/a",image="x",name="a",namespace="default",pod="web-1"} 1000 1730000000000
container_cpu_cfs_periods_total{container="sidecar",id="/b",image="x",name="b",namespace="default",pod="web-1"} 1000 1730000000000
container_cpu_cfs_periods_total{container="app",id="/c",image="x",name="c",namespace="other",pod="web-1"} 1000 1730000000000
# HELP container_cpu_cfs_throttled_periods_total Number of throttled period intervals.
# TYPE container_cpu_cfs_throttled_periods_total counter
container_cpu_cfs_throttled_periods_total{container="app",id="/a",image="x",name="a",namespace="default",pod="web-1"} 400 1730000000000
`
	got, err := parseCFS([]byte(raw), "default", []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "web-1"}}}, map[string]bool{"app": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["web-1/app"].periods != 1000 || got["web-1/app"].throttled != 400 {
		t.Fatalf("parsed %+v", got)
	}
}

func TestFallbackSource(t *testing.T) {
	bad := &PromSource{URL: "http://127.0.0.1:1"}
	good := fakeThrottle{ratio: 0.3, ok: true}
	r, ok, err := Sources(bad, nil, good).Ratio(context.Background(), ThrottleQuery{Pods: []corev1.Pod{{}}, Containers: []string{"app"}})
	if err != nil || !ok || r != 0.3 {
		t.Fatalf("fallback: %v %v %v", r, ok, err)
	}
	if Sources() != nil {
		t.Fatal("no sources must be nil")
	}
}

type fakeThrottle struct {
	ratio float64
	ok    bool
}

func (f fakeThrottle) Name() string { return "fake" }
func (f fakeThrottle) Ratio(context.Context, ThrottleQuery) (float64, bool, error) {
	return f.ratio, f.ok, nil
}
