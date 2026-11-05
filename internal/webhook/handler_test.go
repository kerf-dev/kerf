package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

func recFor(name string, kind v1alpha1.WorkloadKind, target string, phase v1alpha1.Phase, strategy v1alpha1.Strategy,
	containers ...v1alpha1.ContainerRecommendation) *v1alpha1.RightsizingRecommendation {
	r := &v1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 1},
		Spec: v1alpha1.RightsizingRecommendationSpec{
			TargetRef:  v1alpha1.TargetRef{Kind: kind, Name: target},
			Containers: containers,
			Strategy:   strategy,
			Source:     v1alpha1.RecommendationSource{RecommendationID: "rec-" + name},
		},
		Status: v1alpha1.RightsizingRecommendationStatus{Phase: phase, ObservedGeneration: 1, AppliedRecommendationID: "applied-" + name},
	}
	return r
}

func admissionReq(t *testing.T, op admissionv1.Operation, pod *corev1.Pod) admission.Request {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID: "u1", Operation: op, Namespace: ns,
		Kind:     metav1.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Resource: metav1.GroupVersionResource{Version: "v1", Resource: "pods"},
		Object:   runtime.RawExtension{Raw: raw},
	}}
}

func appPod(owner metav1.OwnerReference, req, lim corev1.ResourceList) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "web-7d9f-", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{container("app", req, lim)}},
	}
}

// applyPatch applies the response's JSON patch to the request pod.
func applyPatch(t *testing.T, req admission.Request, resp admission.Response) *corev1.Pod {
	t.Helper()
	b, err := json.Marshal(resp.Patches)
	if err != nil {
		t.Fatal(err)
	}
	p, err := jsonpatch.DecodePatch(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Apply(req.Object.Raw)
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{}
	if err := json.Unmarshal(out, pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

func newHandler(t *testing.T, c client.Client, reg *prometheus.Registry) *Handler {
	t.Helper()
	return &Handler{
		Cache:    c,
		Resolver: &Resolver{Cache: c},
		Decoder:  admission.NewDecoder(testScheme(t)),
		Metrics:  NewMetrics(reg),
		Log:      slog.New(slog.DiscardHandler),
		Synced:   func() bool { return true },
	}
}

func TestHandle(t *testing.T) {
	rsOwner := ctrlRef("apps/v1", "ReplicaSet", "web-7d9f")
	stsOwner := ctrlRef("apps/v1", "StatefulSet", "db")
	podLevel := appPod(rsOwner, rl("cpu", "1"), nil)
	podLevel.Spec.Resources = &corev1.ResourceRequirements{Limits: rl("cpu", "2")}
	optOut := appPod(rsOwner, rl("cpu", "1"), nil)
	optOut.Labels = map[string]string{v1alpha1.LabelWebhookSkip: "true"}

	apply := []v1alpha1.ContainerRecommendation{crec("app", rl("cpu", "250m", "memory", "256Mi"), rl("memory", "256Mi"))}
	base := []client.Object{
		meta(ReplicaSetGVK, "web-7d9f", ref(ctrlRef("apps/v1", "Deployment", "web"))),
		meta(JobGVK, "one-off", nil),
	}
	tests := []struct {
		name       string
		objs       []client.Object
		op         admissionv1.Operation
		pod        *corev1.Pod
		synced     bool
		wantResult string
		wantReason string
		wantReq    corev1.ResourceList
		wantLim    corev1.ResourceList
		wantAnnot  string
	}{
		{
			name: "applied in-place deployment: injected",
			objs: []client.Object{recFor("web", v1alpha1.KindDeployment, "web", v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, apply...)},
			pod:  appPod(rsOwner, rl("cpu", "1", "memory", "1Gi"), rl("memory", "1Gi")), wantResult: ResultMutated,
			wantReq: rl("cpu", "250m", "memory", "256Mi"), wantLim: rl("memory", "256Mi"),
			wantAnnot: `[{"name":"app","requests":{"cpu":"250m","memory":"256Mi"},"limits":{"memory":"256Mi"}}]`,
		},
		{
			name: "looked up by targetRef, not by name",
			objs: []client.Object{
				recFor("web", v1alpha1.KindStatefulSet, "web", v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, apply...),
				recFor("web-deployment", v1alpha1.KindDeployment, "web", v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace,
					crec("app", rl("cpu", "100m"), nil)),
			},
			pod: appPod(rsOwner, rl("cpu", "1"), nil), wantResult: ResultMutated, wantReq: rl("cpu", "100m"),
			wantAnnot: `[{"name":"app","requests":{"cpu":"100m"}}]`,
		},
		{
			name: "statefulset",
			objs: []client.Object{recFor("db", v1alpha1.KindStatefulSet, "db", v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, apply...)},
			pod:  appPod(stsOwner, rl("cpu", "1"), nil), wantResult: ResultMutated, wantReq: rl("cpu", "250m", "memory", "256Mi"), wantLim: rl("memory", "256Mi"),
		},
		{
			name: "rollout strategy (after fallback): skipped",
			objs: []client.Object{recFor("web", v1alpha1.KindDeployment, "web", v1alpha1.PhaseApplied, v1alpha1.StrategyRollout, apply...)},
			pod:  appPod(rsOwner, rl("cpu", "1"), nil), wantResult: ResultSkipped, wantReason: ReasonNotInPlace,
		},
		{
			name: "pending: skipped",
			objs: []client.Object{recFor("web", v1alpha1.KindDeployment, "web", v1alpha1.PhasePending, v1alpha1.StrategyInPlace, apply...)},
			pod:  appPod(rsOwner, rl("cpu", "1"), nil), wantResult: ResultSkipped, wantReason: ReasonNotApplied,
		},
		{
			name: "no recommendation", pod: appPod(rsOwner, rl("cpu", "1"), nil),
			wantResult: ResultSkipped, wantReason: ReasonNoRecommendation,
		},
		{
			name: "already at recommended values", objs: []client.Object{recFor("web", v1alpha1.KindDeployment, "web", v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, apply...)},
			pod:        appPod(rsOwner, rl("cpu", "250m", "memory", "256Mi"), rl("memory", "256Mi")),
			wantResult: ResultSkipped, wantReason: ReasonNoChange,
		},
		{
			name: "pod-level resources", objs: []client.Object{recFor("web", v1alpha1.KindDeployment, "web", v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, apply...)},
			pod: podLevel, wantResult: ResultSkipped, wantReason: ReasonPodLevelResources,
		},
		{
			name: "opt-out label", objs: []client.Object{recFor("web", v1alpha1.KindDeployment, "web", v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, apply...)},
			pod: optOut, wantResult: ResultSkipped, wantReason: ReasonOptOut,
		},
		{
			name: "caches not synced", objs: []client.Object{recFor("web", v1alpha1.KindDeployment, "web", v1alpha1.PhaseApplied, v1alpha1.StrategyInPlace, apply...)},
			pod: appPod(rsOwner, rl("cpu", "1"), nil), synced: false, wantResult: ResultSkipped, wantReason: ReasonCacheNotSynced,
		},
		{
			name: "bare pod", pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare"}},
			wantResult: ResultSkipped, wantReason: ReasonNoOwner,
		},
		{
			name: "job without cronjob", pod: appPod(ctrlRef("batch/v1", "Job", "one-off"), nil, nil),
			wantResult: ResultSkipped, wantReason: ReasonUnmanagedOwner,
		},
		{
			name: "update is not handled", op: admissionv1.Update, pod: appPod(rsOwner, nil, nil),
			wantResult: ResultSkipped, wantReason: ReasonNotPodCreate,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(append(base, tt.objs...)...).
				WithIndex(&v1alpha1.RightsizingRecommendation{}, v1alpha1.IndexTargetRef, IndexTargetRef).Build()
			reg := prometheus.NewRegistry()
			h := newHandler(t, c, reg)
			if tt.wantReason == ReasonCacheNotSynced {
				h.Synced = func() bool { return false }
			}
			op := tt.op
			if op == "" {
				op = admissionv1.Create
			}
			req := admissionReq(t, op, tt.pod)
			resp := h.Handle(context.Background(), req)
			if !resp.Allowed {
				t.Fatalf("pod denied: %+v", resp.Result)
			}
			if got := testutil.ToFloat64(h.Metrics.Admissions.WithLabelValues(tt.wantResult, tt.wantReason)); got != 1 {
				t.Fatalf("kerf_webhook_admissions_total{result=%q,reason=%q} = %v", tt.wantResult, tt.wantReason, got)
			}
			if tt.wantResult != ResultMutated {
				if len(resp.Patches) != 0 {
					t.Fatalf("unexpected patches %v", resp.Patches)
				}
				return
			}
			got := applyPatch(t, req, resp)
			c0 := got.Spec.Containers[0]
			if !equalRL(c0.Resources.Requests, tt.wantReq) || !equalRL(c0.Resources.Limits, tt.wantLim) {
				t.Fatalf("resources = %+v", c0.Resources)
			}
			if tt.wantAnnot != "" && got.Annotations[v1alpha1.AnnotationInjectedResources] != tt.wantAnnot {
				t.Fatalf("annotation = %s", got.Annotations[v1alpha1.AnnotationInjectedResources])
			}
			if got.Annotations[v1alpha1.AnnotationRecommendationID] == "" {
				t.Fatal("recommendation-id annotation missing")
			}
		})
	}
}

func TestHandleErrorsAllow(t *testing.T) {
	boom := errors.New("boom")
	failList := interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return boom }}
	failGet := interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return boom
	}}
	tests := []struct {
		name   string
		funcs  interceptor.Funcs
		pod    *corev1.Pod
		raw    []byte
		reason string
	}{
		{"recommendation lookup fails", failList, appPod(ctrlRef("apps/v1", "StatefulSet", "db"), nil, nil), nil, ReasonRecommendationRead},
		{"owner lookup fails", failGet, appPod(ctrlRef("apps/v1", "ReplicaSet", "web-7d9f"), nil, nil), nil, ReasonOwnerLookup},
		{"undecodable object", interceptor.Funcs{}, nil, []byte(`{"spec":`), ReasonDecode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithInterceptorFuncs(tt.funcs).
				WithIndex(&v1alpha1.RightsizingRecommendation{}, v1alpha1.IndexTargetRef, IndexTargetRef).Build()
			h := newHandler(t, c, prometheus.NewRegistry())
			var req admission.Request
			if tt.pod != nil {
				req = admissionReq(t, admissionv1.Create, tt.pod)
			} else {
				req = admissionReq(t, admissionv1.Create, &corev1.Pod{})
				req.Object.Raw = tt.raw
			}
			resp := h.Handle(context.Background(), req)
			if !resp.Allowed || len(resp.Patches) != 0 {
				t.Fatalf("want allowed without patch, got %+v", resp)
			}
			if got := testutil.ToFloat64(h.Metrics.Admissions.WithLabelValues(ResultError, tt.reason)); got != 1 {
				t.Fatalf("error metric %q = %v", tt.reason, got)
			}
		})
	}
}

func TestMetricsNil(t *testing.T) {
	var m *Metrics
	m.Observe(ResultMutated, "", 0) // must not panic
	if IndexTargetRef(&corev1.Pod{}) != nil {
		t.Fatal("IndexTargetRef of a non-RSR")
	}
}
