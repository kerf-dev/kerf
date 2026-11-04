package health

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/apply"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

var (
	k8s      client.Client
	testCtx  context.Context
	skipEnv  string
	emitter  = &recEmitter{}
	throttle = &switchThrottle{}
)

type recEmitter struct {
	mu     sync.Mutex
	events []hubclient.Event
}

func (r *recEmitter) Emit(_ *v1alpha1.RightsizingRecommendation, obj client.Object, e hubclient.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Workload.Name = obj.GetName()
	r.events = append(r.events, e)
}

func (r *recEmitter) find(name, typ string) *hubclient.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.events {
		if r.events[i].Workload.Name == name && r.events[i].Type == typ {
			e := r.events[i]
			return &e
		}
	}
	return nil
}

type switchThrottle struct {
	mu     sync.Mutex
	ratios map[string]float64 // namespace/first pod prefix -> ratio
}

func (s *switchThrottle) Name() string { return "test" }
func (s *switchThrottle) set(workload string, r float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ratios == nil {
		s.ratios = map[string]float64{}
	}
	s.ratios[workload] = r
}
func (s *switchThrottle) Ratio(_ context.Context, q ThrottleQuery) (float64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for w, r := range s.ratios {
		for _, p := range q.Pods {
			if len(p.Name) > len(w) && p.Name[:len(w)] == w {
				return r, true, nil
			}
		}
	}
	return 0, false, nil
}

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	if assets == "" {
		matches, _ := filepath.Glob(filepath.Join(root, "bin", "envtest", "k8s", "1.35*-"+runtime.GOOS+"-"+runtime.GOARCH))
		if len(matches) > 0 {
			assets = matches[len(matches)-1]
		}
	}
	if assets == "" {
		skipEnv = "envtest binaries not found: run `make agent-envtest` or set KUBEBUILDER_ASSETS"
		return m.Run()
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(root, "api", "crds")}, ErrorIfCRDPathMissing: true, BinaryAssetsDirectory: assets}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest:", err)
		return 1
	}
	defer func() { _ = env.Stop() }()
	lvl := slog.LevelError
	if os.Getenv("HEALTH_TEST_DEBUG") != "" {
		lvl = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))
	scheme := k8sruntime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "manager:", err)
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	testCtx = ctx
	if err := mgr.GetFieldIndexer().IndexField(ctx, &v1alpha1.RightsizingRecommendation{}, v1alpha1.IndexTargetRef, func(o client.Object) []string {
		r := o.(*v1alpha1.RightsizingRecommendation)
		return []string{v1alpha1.TargetRefIndexValue(r.Spec.TargetRef.Kind, r.Spec.TargetRef.Name)}
	}); err != nil {
		fmt.Fprintln(os.Stderr, "index:", err)
		return 1
	}
	w := &Watcher{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Throttle: throttle, Emitter: emitter, Log: log,
		Cfg: Config{ClusterName: "envtest", CheckInterval: 300 * time.Millisecond, MinThrottlingWindow: time.Millisecond}}
	if err := w.SetupWithManager(mgr); err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return 1
	}
	go func() { _ = mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		return 1
	}
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		return 1
	}
	return m.Run()
}

func needEnv(t *testing.T) {
	t.Helper()
	if skipEnv != "" {
		t.Skip(skipEnv)
	}
}

func eventually(t *testing.T, timeout time.Duration, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: %v", what, err)
}

type fixture struct {
	name      string
	strategy  v1alpha1.Strategy
	template  corev1.ResourceRequirements // pod template values (Git, or applied for rollout)
	podValues corev1.ResourceRequirements // running pod values
	previous  v1alpha1.ContainerResources
	target    v1alpha1.ContainerRecommendation
	labels    map[string]string
}

// setup creates the workload as the agent would have left it after applying a change: Deployment,
// ReplicaSet, one pod created after the change, and the RSR in phase Applied.
func setup(t *testing.T, f fixture) (*appsv1.Deployment, *corev1.Pod) {
	t.Helper()
	lbl := map[string]string{"app": f.name}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: f.name, Namespace: "default", Labels: f.labels},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: lbl},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: lbl}, Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "busybox", Resources: f.template}}}},
		},
	}
	if err := k8s.Create(testCtx, d, client.FieldOwner("argocd-controller")); err != nil {
		t.Fatal(err)
	}
	if f.strategy == v1alpha1.StrategyRollout {
		own := map[corev1.ResourceName]bool{corev1.ResourceCPU: true, corev1.ResourceMemory: true}
		if err := apply.ApplyTemplate(testCtx, k8s, d, v1alpha1.KindDeployment, []v1alpha1.ContainerRecommendation{f.target}, own,
			map[string]string{v1alpha1.AnnotationManaged: "true"}); err != nil {
			t.Fatal(err)
		}
	}
	rsr := &v1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: f.name, Namespace: "default"},
		Spec: v1alpha1.RightsizingRecommendationSpec{
			TargetRef:  v1alpha1.TargetRef{APIVersion: "apps/v1", Kind: v1alpha1.KindDeployment, Name: f.name},
			Containers: []v1alpha1.ContainerRecommendation{f.target}, Strategy: f.strategy,
			Source: v1alpha1.RecommendationSource{RecommendationID: "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b7c10", GeneratedAt: metav1.Now()},
		},
	}
	if err := k8s.Create(testCtx, rsr); err != nil {
		t.Fatal(err)
	}
	applied := metav1.NewTime(time.Now().Add(-2 * time.Second).Truncate(time.Second))
	rsr.Status = v1alpha1.RightsizingRecommendationStatus{Phase: v1alpha1.PhaseApplied, AppliedAt: &applied,
		AppliedRecommendationID: "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b7c10", Previous: []v1alpha1.ContainerResources{f.previous}}
	if err := k8s.Status().Update(testCtx, rsr); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(testCtx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: f.name + "-5d4c3b2a1", Namespace: "default", Labels: lbl,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: d.Name, UID: d.UID, Controller: ptr.To(true)}}},
		Spec: appsv1.ReplicaSetSpec{Selector: d.Spec.Selector, Template: d.Spec.Template},
	}
	if err := k8s.Create(testCtx, rs); err != nil {
		t.Fatal(err)
	}
	podLbl := map[string]string{"app": f.name, "pod-template-hash": "5d4c3b2a1"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: f.name + "-5d4c3b2a1-abcde", Namespace: "default", Labels: podLbl,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: ptr.To(true)}}},
		Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "app", Image: "busybox", Resources: f.podValues,
			ResizePolicy: []corev1.ContainerResizePolicy{{ResourceName: corev1.ResourceCPU, RestartPolicy: corev1.NotRequired}, {ResourceName: corev1.ResourceMemory, RestartPolicy: corev1.NotRequired}}}}},
	}
	if err := k8s.Create(testCtx, pod); err != nil {
		t.Fatal(err)
	}
	setStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodRunning
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Image: "busybox", ImageID: "x", Ready: true,
			State:     corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}},
			Resources: p.Spec.Containers[0].Resources.DeepCopy()}}
	})
	return d, pod
}

func setStatus(t *testing.T, name string, mut func(*corev1.Pod)) {
	t.Helper()
	eventually(t, 5*time.Second, "pod status", func() error {
		var p corev1.Pod
		if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name}, &p); err != nil {
			return err
		}
		mut(&p)
		return k8s.Status().Update(testCtx, &p)
	})
}

func rsrOf(t *testing.T, name string) *v1alpha1.RightsizingRecommendation {
	t.Helper()
	var r v1alpha1.RightsizingRecommendation
	if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: name}, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func wantRolledBack(t *testing.T, name, reason string) *v1alpha1.RightsizingRecommendation {
	t.Helper()
	eventually(t, 15*time.Second, "rolled back", func() error {
		r := rsrOf(t, name)
		c := meta.FindStatusCondition(r.Status.Conditions, v1alpha1.ConditionHealthWatch)
		if r.Status.Phase != v1alpha1.PhaseRolledBack || r.Status.RolledBackAt == nil || c == nil || c.Reason != reason {
			return fmt.Errorf("phase %s, conditions %+v", r.Status.Phase, r.Status.Conditions)
		}
		return nil
	})
	return rsrOf(t, name)
}

func req(cpu, mem string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: rl(cpu, mem), Limits: rl("", mem)}
}

func rec(name, cpu, mem string) v1alpha1.ContainerRecommendation {
	return v1alpha1.ContainerRecommendation{ContainerResources: v1alpha1.ContainerResources{Name: name, Requests: rl(cpu, mem), Limits: rl("", mem)}}
}

// TestRolloutOOMRollback: a memory decrease applied by rollout, then an OOM kill: the template's
// memory goes back to the previous value (cpu stays), phase RolledBack, rolled_back OOMKills.
func TestRolloutOOMRollback(t *testing.T) {
	needEnv(t)
	const name = "oom-rollout"
	d, pod := setup(t, fixture{name: name, strategy: v1alpha1.StrategyRollout,
		template: req("500m", "256Mi"), podValues: req("250m", "64Mi"),
		previous: v1alpha1.ContainerResources{Name: "app", Requests: rl("500m", "256Mi"), Limits: rl("", "256Mi")},
		target:   rec("app", "250m", "64Mi")})
	eventually(t, 10*time.Second, "watching", func() error {
		c := meta.FindStatusCondition(rsrOf(t, name).Status.Conditions, v1alpha1.ConditionHealthWatch)
		if c == nil || c.Status != metav1.ConditionTrue || c.Reason != ReasonWatching {
			return fmt.Errorf("HealthWatch %+v", c)
		}
		return nil
	})
	setStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].RestartCount = 1
		p.Status.ContainerStatuses[0].LastTerminationState.Terminated = oom(time.Now())
	})
	r := wantRolledBack(t, name, v1alpha1.ReasonOOMKills)
	if c := meta.FindStatusCondition(r.Status.Conditions, v1alpha1.ConditionApplied); c == nil || c.Reason != v1alpha1.ReasonOOMKills {
		t.Fatalf("Applied condition %+v", c)
	}
	if err := k8s.Get(testCtx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	res := d.Spec.Template.Spec.Containers[0].Resources
	if res.Requests.Memory().String() != "256Mi" || res.Limits.Memory().String() != "256Mi" || res.Requests.Cpu().String() != "250m" {
		t.Fatalf("template after rollback: %+v", res)
	}
	owned := apply.OwnedResources(d)
	if !owned[corev1.ResourceCPU] || !owned[corev1.ResourceMemory] {
		t.Fatalf("rollback must keep owning the fields (SSA): %v", owned)
	}
	e := emitter.find(name, hubclient.EventRolledBack)
	if e == nil || e.Reason != v1alpha1.ReasonOOMKills || e.Strategy != "rollout" || len(e.Resources) != 1 || e.Resources[0] != "memory" ||
		e.Details["oomKills"] != "1" || len(e.Containers) != 1 || e.Containers[0].From.Requests.Memory != "64Mi" || e.Containers[0].To.Requests.Memory != "256Mi" {
		t.Fatalf("rolled_back event %+v", e)
	}
}

// TestInPlaceRestartsRollback: a cpu change applied in place, then 3 restarts: the pod is resized
// back through pods/resize.
func TestInPlaceRestartsRollback(t *testing.T) {
	needEnv(t)
	const name = "restarts-inplace"
	_, pod := setup(t, fixture{name: name, strategy: v1alpha1.StrategyInPlace,
		template: req("500m", "256Mi"), podValues: req("250m", "256Mi"),
		previous: v1alpha1.ContainerResources{Name: "app", Requests: rl("500m", "256Mi"), Limits: rl("", "256Mi")},
		target:   v1alpha1.ContainerRecommendation{ContainerResources: v1alpha1.ContainerResources{Name: "app", Requests: rl("250m", "")}}})
	setStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].RestartCount = 3
		p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.Now()}
	})
	wantRolledBack(t, name, v1alpha1.ReasonRestarts)
	var p corev1.Pod
	if err := k8s.Get(testCtx, client.ObjectKeyFromObject(pod), &p); err != nil {
		t.Fatal(err)
	}
	if got := p.Spec.Containers[0].Resources.Requests.Cpu().String(); got != "500m" {
		t.Fatalf("pod cpu after rollback = %s", got)
	}
	e := emitter.find(name, hubclient.EventRolledBack)
	if e == nil || e.Strategy != "in-place" || e.Details["restarts"] != "3" || len(e.Resources) != 1 || e.Resources[0] != "cpu" {
		t.Fatalf("rolled_back event %+v", e)
	}
}

// TestThrottlingRollback: the throttling ratio exceeds the threshold after a cpu decrease.
func TestThrottlingRollback(t *testing.T) {
	needEnv(t)
	const name = "throttled-rollout"
	throttle.set(name, 0.6)
	d, _ := setup(t, fixture{name: name, strategy: v1alpha1.StrategyRollout,
		template: req("500m", "256Mi"), podValues: req("100m", "256Mi"),
		previous: v1alpha1.ContainerResources{Name: "app", Requests: rl("500m", "256Mi"), Limits: rl("", "256Mi")},
		target:   rec("app", "100m", "256Mi")})
	wantRolledBack(t, name, v1alpha1.ReasonThrottling)
	if err := k8s.Get(testCtx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	if got := d.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String(); got != "500m" {
		t.Fatalf("cpu after rollback = %s", got)
	}
	if e := emitter.find(name, hubclient.EventRolledBack); e == nil || e.Details["throttlingRatio"] != "0.600" {
		t.Fatalf("event %+v", e)
	}
}

// TestWatchCompletes: an OOM kill after a cpu-only change is not caused by it; the watch ends
// Healthy after watchDuration and nothing is rolled back.
func TestWatchCompletes(t *testing.T) {
	needEnv(t)
	const name = "healthy"
	p := &v1alpha1.RightsizingPolicy{ObjectMeta: metav1.ObjectMeta{Name: "short-watch"}, Spec: v1alpha1.RightsizingPolicySpec{
		Selector:   v1alpha1.PolicySelector{MatchLabels: map[string]string{"watch": "short"}},
		Guardrails: &v1alpha1.Guardrails{Rollback: &v1alpha1.RollbackPolicy{WatchDuration: &metav1.Duration{Duration: 4 * time.Second}}},
	}}
	if err := k8s.Create(testCtx, p); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k8s.Delete(testCtx, p) }()
	_, pod := setup(t, fixture{name: name, strategy: v1alpha1.StrategyRollout, labels: map[string]string{"watch": "short"},
		template: req("500m", "256Mi"), podValues: req("400m", "256Mi"),
		previous: v1alpha1.ContainerResources{Name: "app", Requests: rl("500m", "256Mi"), Limits: rl("", "256Mi")},
		target:   rec("app", "400m", "256Mi")})
	setStatus(t, pod.Name, func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].RestartCount = 1
		p.Status.ContainerStatuses[0].LastTerminationState.Terminated = oom(time.Now())
	})
	eventually(t, 15*time.Second, "watch completed", func() error {
		r := rsrOf(t, name)
		c := meta.FindStatusCondition(r.Status.Conditions, v1alpha1.ConditionHealthWatch)
		if r.Status.Phase != v1alpha1.PhaseApplied || c == nil || c.Status != metav1.ConditionFalse || c.Reason != ReasonHealthy {
			return fmt.Errorf("phase %s HealthWatch %+v", r.Status.Phase, c)
		}
		return nil
	})
	if e := emitter.find(name, hubclient.EventRolledBack); e != nil {
		t.Fatalf("unexpected rollback: %+v", e)
	}
}
