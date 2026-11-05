package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

// envtestAssets finds envtest binaries: $KUBEBUILDER_ASSETS or ./bin/envtest/k8s/1.35*.
func envtestAssets() string {
	if d := os.Getenv("KUBEBUILDER_ASSETS"); d != "" {
		return d
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	m, _ := filepath.Glob(filepath.Join(root, "bin", "envtest", "k8s", "1.35*-"+runtime.GOOS+"-"+runtime.GOARCH))
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

// TestEnvtest runs the webhook behind a real kube-apiserver: the API server calls it on pod
// CREATE through a MutatingWebhookConfiguration equivalent to the chart's.
func TestEnvtest(t *testing.T) {
	assets := envtestAssets()
	if assets == "" {
		t.Skip("envtest binaries not found: run `make webhook-envtest` or set KUBEBUILDER_ASSETS")
	}
	_, file, _, _ := runtime.Caller(0)
	ignore := admissionregistrationv1.Ignore
	none := admissionregistrationv1.SideEffectClassNone
	timeout := int32(2)
	path := Path
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(filepath.Dir(file), "..", "..", "api", "crds")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: assets,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			MutatingWebhooks: []*admissionregistrationv1.MutatingWebhookConfiguration{{
				ObjectMeta: metav1.ObjectMeta{Name: "kerf-webhook"},
				Webhooks: []admissionregistrationv1.MutatingWebhook{{
					Name:                    "pods.kerf.getkerf.dev",
					AdmissionReviewVersions: []string{"v1"},
					SideEffects:             &none,
					FailurePolicy:           &ignore,
					TimeoutSeconds:          &timeout,
					ClientConfig:            admissionregistrationv1.WebhookClientConfig{Service: &admissionregistrationv1.ServiceReference{Name: "webhook", Namespace: "default", Path: &path}},
					Rules: []admissionregistrationv1.RuleWithOperations{{
						Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
						Rule:       admissionregistrationv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}},
					}},
					MatchConditions: []admissionregistrationv1.MatchCondition{{Name: "has-controller",
						Expression: "has(object.metadata.ownerReferences) && object.metadata.ownerReferences.exists(r, has(r.controller) && r.controller)"}},
					ObjectSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key: v1alpha1.LabelWebhookSkip, Operator: metav1.LabelSelectorOpNotIn, Values: []string{"true"}}}},
				}},
			}},
		},
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()

	scheme := testScheme(t)
	wo := env.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		WebhookServer:          webhook.NewServer(webhook.Options{Host: wo.LocalServingHost, Port: wo.LocalServingPort, CertDir: wo.LocalServingCertDir}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	metrics := NewMetrics(prometheus.NewRegistry())
	h, err := Setup(ctx, mgr, metrics, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for !h.Synced() {
		if time.Now().After(deadline) {
			t.Fatal("caches did not sync")
		}
		time.Sleep(50 * time.Millisecond)
	}
	k, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	const tns = "default"
	tru := true
	owner := func(apiVersion, kind, name string, uid string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: typesUID(uid), Controller: &tru}}
	}
	mustCreate := func(o client.Object) {
		t.Helper()
		if err := k.Create(ctx, o); err != nil {
			t.Fatalf("create %T %s: %v", o, o.GetName(), err)
		}
	}
	// Workload chain: Deployment web -> ReplicaSet web-abc (no controllers run in envtest).
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: tns, Name: "web-abc", OwnerReferences: owner("apps/v1", "Deployment", "web", "d1")},
		Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "x"}}}}},
	}
	mustCreate(rs)

	rec := &v1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Namespace: tns, Name: "web"},
		Spec: v1alpha1.RightsizingRecommendationSpec{
			TargetRef: v1alpha1.TargetRef{APIVersion: "apps/v1", Kind: v1alpha1.KindDeployment, Name: "web"},
			Containers: []v1alpha1.ContainerRecommendation{{
				ContainerResources: v1alpha1.ContainerResources{Name: "app", Requests: rl("cpu", "150m", "memory", "200Mi"), Limits: rl("memory", "200Mi")},
				RemoveLimits:       []corev1.ResourceName{corev1.ResourceCPU},
			}},
			Strategy: v1alpha1.StrategyInPlace,
			Source:   v1alpha1.RecommendationSource{RecommendationID: "11111111-2222-3333-4444-555555555555", GeneratedAt: metav1.Now()},
		},
	}
	mustCreate(rec)
	setPhase := func(phase v1alpha1.Phase) {
		t.Helper()
		if err := k.Get(ctx, client.ObjectKeyFromObject(rec), rec); err != nil {
			t.Fatal(err)
		}
		rec.Status.Phase = phase
		rec.Status.ObservedGeneration = rec.Generation
		rec.Status.AppliedRecommendationID = rec.Spec.Source.RecommendationID
		if err := k.Status().Update(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	waitCache := func(phase v1alpha1.Phase) {
		t.Helper()
		for i := 0; i < 100; i++ {
			var got v1alpha1.RightsizingRecommendation
			if err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(rec), &got); err == nil && got.Status.Phase == phase {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("cache never saw phase %s", phase)
	}
	n := 0
	newPod := func(ownerRefs []metav1.OwnerReference, labels map[string]string) *corev1.Pod {
		t.Helper()
		n++
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: tns, Name: "p" + string(rune('a'+n)), Labels: labels, OwnerReferences: ownerRefs},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "x", Resources: corev1.ResourceRequirements{
				Requests: rl("cpu", "1", "memory", "1Gi"), Limits: rl("cpu", "2", "memory", "1Gi")}}}},
		}
		mustCreate(p)
		return p
	}
	rsOwner := owner("apps/v1", "ReplicaSet", "web-abc", "r1")

	t.Run("pending: template values", func(t *testing.T) {
		setPhase(v1alpha1.PhasePending)
		waitCache(v1alpha1.PhasePending)
		p := newPod(rsOwner, nil)
		if c := p.Spec.Containers[0].Resources; c.Requests.Cpu().String() != "1" || p.Annotations[v1alpha1.AnnotationInjectedResources] != "" {
			t.Fatalf("pod mutated while Pending: %+v %v", c, p.Annotations)
		}
	})

	setPhase(v1alpha1.PhaseApplied)
	waitCache(v1alpha1.PhaseApplied)

	t.Run("applied in-place: recommended values", func(t *testing.T) {
		p := newPod(rsOwner, nil)
		c := p.Spec.Containers[0].Resources
		if c.Requests.Cpu().String() != "150m" || c.Requests.Memory().String() != "200Mi" || c.Limits.Memory().String() != "200Mi" {
			t.Fatalf("resources = %+v", c)
		}
		if _, ok := c.Limits[corev1.ResourceCPU]; ok {
			t.Fatalf("cpu limit not removed: %+v", c.Limits)
		}
		var inj []v1alpha1.ContainerResources
		if err := json.Unmarshal([]byte(p.Annotations[v1alpha1.AnnotationInjectedResources]), &inj); err != nil || len(inj) != 1 || inj[0].Name != "app" {
			t.Fatalf("injected-resources = %q (%v)", p.Annotations[v1alpha1.AnnotationInjectedResources], err)
		}
		if p.Annotations[v1alpha1.AnnotationRecommendationID] != rec.Spec.Source.RecommendationID {
			t.Fatalf("recommendation-id = %q", p.Annotations[v1alpha1.AnnotationRecommendationID])
		}
	})

	t.Run("replicaset created just before its pod (cache miss, live read)", func(t *testing.T) {
		rs2 := rs.DeepCopy()
		rs2.ResourceVersion, rs2.UID, rs2.Name = "", "", "web-new"
		mustCreate(rs2)
		p := newPod(owner("apps/v1", "ReplicaSet", "web-new", "r2"), nil)
		if p.Spec.Containers[0].Resources.Requests.Cpu().String() != "150m" {
			t.Fatalf("resources = %+v", p.Spec.Containers[0].Resources)
		}
	})

	t.Run("bare pod is not sent to the webhook (matchConditions)", func(t *testing.T) {
		before := testutil.ToFloat64(metrics.Admissions.WithLabelValues(ResultSkipped, ReasonNoOwner))
		p := newPod(nil, nil)
		if p.Spec.Containers[0].Resources.Requests.Cpu().String() != "1" {
			t.Fatal("bare pod was mutated")
		}
		if after := testutil.ToFloat64(metrics.Admissions.WithLabelValues(ResultSkipped, ReasonNoOwner)); after != before {
			t.Fatal("bare pod reached the webhook")
		}
	})

	t.Run("opt-out label (objectSelector)", func(t *testing.T) {
		p := newPod(rsOwner, map[string]string{v1alpha1.LabelWebhookSkip: "true"})
		if p.Spec.Containers[0].Resources.Requests.Cpu().String() != "1" {
			t.Fatal("opted-out pod was mutated")
		}
	})

	t.Run("fallback to rollout stops injection", func(t *testing.T) {
		if err := k.Get(ctx, client.ObjectKeyFromObject(rec), rec); err != nil {
			t.Fatal(err)
		}
		rec.Spec.Strategy = v1alpha1.StrategyRollout
		if err := k.Update(ctx, rec); err != nil {
			t.Fatal(err)
		}
		setPhase(v1alpha1.PhaseApplied)
		for i := 0; i < 100; i++ {
			var got v1alpha1.RightsizingRecommendation
			if err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(rec), &got); err == nil && got.Spec.Strategy == v1alpha1.StrategyRollout {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		p := newPod(rsOwner, nil)
		if p.Spec.Containers[0].Resources.Requests.Cpu().String() != "1" {
			t.Fatal("pod mutated after fallback to rollout")
		}
	})

	if got := testutil.ToFloat64(metrics.Admissions.WithLabelValues(ResultMutated, "")); got != 2 {
		t.Fatalf("mutated admissions = %v, want 2", got)
	}
	if got := testutil.ToFloat64(metrics.Admissions.WithLabelValues(ResultSkipped, ReasonNotApplied)); got != 1 {
		t.Fatalf("skipped not-applied = %v, want 1", got)
	}
}

func typesUID(s string) types.UID { return types.UID(s) }
