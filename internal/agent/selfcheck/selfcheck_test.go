package selfcheck

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	rd "github.com/tomer1983/resource-optimizer/api/v1alpha1/readiness"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

type env struct {
	deny        map[string]bool // Permission.String() denied
	objs        []client.Object
	resources   []*metav1.APIResourceList
	version     string
	forbidCM    bool
	forbidCRD   bool
	dryRunMutes bool
	dryRuns     int
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme, apiextv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (e *env) checker(t *testing.T) *Checker {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(e.objs...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			switch o := obj.(type) {
			case *authv1.SelfSubjectAccessReview:
				a := o.Spec.ResourceAttributes
				p := Permission{Group: a.Group, Resource: a.Resource, Subresource: a.Subresource, Verb: a.Verb}
				o.Status.Allowed = !e.deny[p.String()]
				return nil
			case *corev1.Pod:
				e.dryRuns++
				co := &client.CreateOptions{}
				co.ApplyOptions(opts)
				if len(co.DryRun) == 0 {
					t.Error("pod created without dry run")
				}
				if !e.dryRunMutes {
					o.Annotations = map[string]string{v1alpha1.AnnotationInjectedResources: "[]"}
				}
				return nil
			}
			return cl.Create(ctx, obj, opts...)
		},
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok && e.forbidCM {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, nil)
			}
			if obj.GetObjectKind().GroupVersionKind().Kind == "CustomResourceDefinition" && e.forbidCRD {
				return apierrors.NewForbidden(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, key.Name, nil)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	}).Build()
	ver := e.version
	if ver == "" {
		ver = "v1.35.0+k3s1"
	}
	d := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{Resources: e.resources}, FakedServerVersion: &version.Info{GitVersion: ver}}
	return &Checker{Client: c, Discovery: d, Now: func() time.Time { return now },
		Cfg: Config{AgentVersion: "0.3.0", WebhookDryRun: true, DocsBase: "https://docs"},
		Inputs: func() Inputs {
			return Inputs{HubVersion: "0.3.1", MinAgentVersion: "0.2.0", HubDate: now.Add(-2 * time.Second), LocalAt: now, Excluded: []string{}, Namespaces: 12}
		}}
}

func coreResources(resize bool) *metav1.APIResourceList {
	l := &metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods"}, {Name: "nodes"}}}
	if resize {
		l.APIResources = append(l.APIResources, metav1.APIResource{Name: "pods/resize"})
	}
	return l
}

func crdResources() *metav1.APIResourceList {
	return &metav1.APIResourceList{GroupVersion: v1alpha1.GroupVersion.String(), APIResources: []metav1.APIResource{{Name: "rightsizingpolicies"}, {Name: "rightsizingrecommendations"}}}
}

// crd returns a CRD whose spec schema has every field of the Go type except drop.
func crd(t *testing.T, plural string, spec any, drop string) *apiextv1.CustomResourceDefinition {
	t.Helper()
	props := schemaOf(t, spec, drop)
	return &apiextv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: plural + "." + v1alpha1.GroupVersion.Group},
		Spec: apiextv1.CustomResourceDefinitionSpec{Group: v1alpha1.GroupVersion.Group, Names: apiextv1.CustomResourceDefinitionNames{Plural: plural, Kind: "X"},
			Scope: apiextv1.NamespaceScoped,
			Versions: []apiextv1.CustomResourceDefinitionVersion{{Name: v1alpha1.GroupVersion.Version, Served: true, Storage: true,
				Schema: &apiextv1.CustomResourceValidation{OpenAPIV3Schema: &apiextv1.JSONSchemaProps{Type: "object",
					Properties: map[string]apiextv1.JSONSchemaProps{"spec": props}}}}}},
	}
}

// schemaOf builds a permissive schema with the json fields of v (one level per struct, recursion
// through x-kubernetes-preserve-unknown-fields below the first level).
func schemaOf(t *testing.T, v any, drop string) apiextv1.JSONSchemaProps {
	t.Helper()
	props := map[string]apiextv1.JSONSchemaProps{}
	for k := range jsonFields(v) {
		if k == drop {
			continue
		}
		props[k] = apiextv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: ptr.To(true)}
	}
	return apiextv1.JSONSchemaProps{Type: "object", Properties: props}
}

func jsonFields(v any) map[string]bool {
	out := map[string]bool{}
	rt := reflectType(v)
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

func healthyEnv(t *testing.T) *env {
	ready := true
	return &env{
		deny:      map[string]bool{},
		resources: []*metav1.APIResourceList{coreResources(true), crdResources(), {GroupVersion: "cert-manager.io/v1"}, {GroupVersion: "argoproj.io/v1alpha1"}},
		objs: []client.Object{
			crd(t, "rightsizingpolicies", v1alpha1.RightsizingPolicySpec{}, ""),
			crd(t, "rightsizingrecommendations", v1alpha1.RightsizingRecommendationSpec{}, ""),
			&admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "resource-optimizer-webhook", Annotations: map[string]string{"cert-manager.io/inject-ca-from": "resource-optimizer/webhook"}},
				Webhooks: []admissionv1.MutatingWebhook{{Name: "pods.webhook.resource-optimizer.oz.io", ClientConfig: admissionv1.WebhookClientConfig{CABundle: []byte("ca"),
					Service: &admissionv1.ServiceReference{Namespace: "resource-optimizer", Name: "webhook"}}}}},
			&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "resource-optimizer", Name: "webhook-x", Labels: map[string]string{discoveryv1.LabelServiceName: "webhook"}},
				AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-cm"}, Data: map[string]string{IgnoreDifferencesKey: "managedFieldsManagers:\n- kube-controller-manager\n- resource-optimizer\n"}},
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n2"}},
			&v1alpha1.RightsizingRecommendation{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
				Spec: v1alpha1.RightsizingRecommendationSpec{TargetRef: v1alpha1.TargetRef{Kind: "Deployment", Name: "api"}, Containers: []v1alpha1.ContainerRecommendation{{ContainerResources: v1alpha1.ContainerResources{Name: "app"}}}}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7d9-x", Labels: map[string]string{"app": "api", "pod-template-hash": "7d9"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-7d9", UID: "u", Controller: ptr.To(true)}}},
				Spec: corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "app", Image: "x"}}}},
		},
	}
}

func byID(res Result) map[string]hubclient.ReadinessCheck {
	m := map[string]hubclient.ReadinessCheck{}
	for _, c := range res.Checks {
		m[c.ID] = c
	}
	return m
}

func TestHealthy(t *testing.T) {
	e := healthyEnv(t)
	res := e.checker(t).Run(context.Background())
	var agentIDs []string
	for _, c := range rd.Catalog {
		if c.Source == rd.SourceAgent {
			agentIDs = append(agentIDs, c.ID)
		}
	}
	if len(res.Checks) != len(agentIDs) {
		t.Errorf("%d checks, want %d (every agent check)", len(res.Checks), len(agentIDs))
	}
	for i, c := range res.Checks {
		if c.ID != agentIDs[i] {
			t.Errorf("check %d = %s, want catalog order %s", i, c.ID, agentIDs[i])
		}
		if c.Status != "pass" {
			t.Errorf("%s = %s: %s", c.ID, c.Status, c.Message)
		}
		cat, _ := rd.Lookup(c.ID)
		if c.Source != "agent" || c.Severity != string(cat.Severity) || c.Gates != string(cat.Gates) || c.DocsURL != "https://docs/readiness#"+c.ID || !c.CheckedAt.Equal(now) {
			t.Errorf("%s catalog fields %+v", c.ID, c)
		}
	}
	if len(res.Capabilities) != len(rd.Capabilities) {
		t.Fatalf("capabilities %+v", res.Capabilities)
	}
	for _, c := range res.Capabilities {
		if !c.Granted || len(c.Missing) != 0 {
			t.Errorf("capability %+v", c)
		}
	}
	if e.dryRuns != 1 {
		t.Errorf("dry runs %d", e.dryRuns)
	}
	if d := byID(res)[rd.CheckWebhook].Details; d["dryRun"] != "mutated: shop/api-7d9-x" || d["readyEndpoints"] != "1" {
		t.Errorf("webhook details %v", d)
	}
}

func TestRBACMissing(t *testing.T) {
	e := healthyEnv(t)
	e.deny["patch pods/resize"] = true
	e.deny["patch apps/deployments"] = true
	e.deny["patch apps/statefulsets"] = true
	res := e.checker(t).Run(context.Background())
	got := byID(res)
	rz := got[rd.CheckRBACResize]
	if rz.Status != "fail" || !strings.Contains(rz.Message, "denied patch pods/resize") || rz.RemediationType != "yaml" ||
		!strings.Contains(rz.Remediation, `resources: ["pods/resize"]`) {
		t.Errorf("resize %+v", rz)
	}
	if got[rd.CheckRBACRollout].Status != "fail" || got[rd.CheckRBACRead].Status != "pass" {
		t.Errorf("rollout/read %+v %+v", got[rd.CheckRBACRollout], got[rd.CheckRBACRead])
	}
	for _, c := range res.Capabilities {
		switch c.Name {
		case "resize":
			if c.Granted || len(c.Missing) != 1 || c.Missing[0] != `{"apiGroups":[""],"resources":["pods/resize"],"verbs":["patch"]}` {
				t.Errorf("resize capability %+v", c)
			}
		case "rollout":
			if c.Granted || len(c.Missing) != 2 {
				t.Errorf("rollout capability %+v", c)
			}
		default:
			if !c.Granted {
				t.Errorf("%s not granted", c.Name)
			}
		}
	}
	// readOnly install
	e = healthyEnv(t)
	e.deny["create pods/eviction"] = true
	ck := e.checker(t)
	ck.Cfg.ReadOnly = true
	ev := byID(ck.Run(context.Background()))[rd.CheckRBACEvict]
	if ev.Status != "fail" || !strings.Contains(ev.Message, "readOnly") || ev.RemediationType != "command" {
		t.Errorf("readOnly evict %+v", ev)
	}
}

func TestVersionAndCRDs(t *testing.T) {
	for _, tc := range []struct {
		agent, minV, hub, want string
	}{
		{"0.3.0", "0.2.0", "0.3.1", "pass"},
		{"0.1.0", "0.2.0", "0.3.1", "fail"},
		{"0.4.0", "", "0.3.9", "fail"},
		{"1.0.0", "", "0.9.0", "fail"},
		{"v0.3.2", "", "0.3.0", "pass"},
		{"main-ece1564", "0.2.0", "0.3.0", "pass"},
	} {
		e := healthyEnv(t)
		ck := e.checker(t)
		ck.Cfg.AgentVersion = tc.agent
		ck.Inputs = func() Inputs { return Inputs{MinAgentVersion: tc.minV, HubVersion: tc.hub} }
		got := byID(ck.Run(context.Background()))[rd.CheckAgentVersion]
		if got.Status != tc.want {
			t.Errorf("%s/%s/%s: %s (%s)", tc.agent, tc.minV, tc.hub, got.Status, got.Message)
		}
	}

	// stale CRD: the installed policy schema lacks spec.selector
	e := healthyEnv(t)
	e.objs[0] = crd(t, "rightsizingpolicies", v1alpha1.RightsizingPolicySpec{}, "selector")
	got := byID(e.checker(t).Run(context.Background()))[rd.CheckCRDs]
	if got.Status != "fail" || !strings.Contains(got.Message, "rightsizingpolicies spec.selector") || got.RemediationType != "command" {
		t.Errorf("stale crd %+v", got)
	}
	// CRD not readable: discovery only
	e = healthyEnv(t)
	e.forbidCRD = true
	if got := byID(e.checker(t).Run(context.Background()))[rd.CheckCRDs]; got.Status != "pass" || !strings.Contains(got.Message, "not compared") {
		t.Errorf("forbidden crd %+v", got)
	}
	// not installed
	e = healthyEnv(t)
	e.resources = []*metav1.APIResourceList{coreResources(true)}
	if got := byID(e.checker(t).Run(context.Background()))[rd.CheckCRDs]; got.Status != "fail" || !strings.Contains(got.Message, "not installed") {
		t.Errorf("missing crd %+v", got)
	}
}

func TestInPlaceAndClock(t *testing.T) {
	e := healthyEnv(t)
	e.version = "v1.32.4"
	ck := e.checker(t)
	ck.Inputs = func() Inputs { return Inputs{HubDate: now.Add(-45 * time.Second), LocalAt: now} }
	got := byID(ck.Run(context.Background()))
	if got[rd.CheckInPlaceResize].Status != "warn" || !strings.Contains(got[rd.CheckInPlaceResize].Message, "older than 1.33") {
		t.Errorf("old server %+v", got[rd.CheckInPlaceResize])
	}
	if got[rd.CheckClockSkew].Status != "warn" || got[rd.CheckClockSkew].Details["skew"] != "45s" {
		t.Errorf("skew %+v", got[rd.CheckClockSkew])
	}
	if got[rd.CheckPolicyNamespaceCovers].Status != "unknown" {
		t.Errorf("coverage without input %+v", got[rd.CheckPolicyNamespaceCovers])
	}

	e = healthyEnv(t)
	e.resources[0] = coreResources(false)
	ck = e.checker(t)
	ck.Inputs = func() Inputs { return Inputs{Excluded: []string{"kube-system", "monitoring"}, Namespaces: 10} }
	got = byID(ck.Run(context.Background()))
	if got[rd.CheckInPlaceResize].Status != "warn" || !strings.Contains(got[rd.CheckInPlaceResize].Message, "pods/resize") {
		t.Errorf("no resize %+v", got[rd.CheckInPlaceResize])
	}
	if got[rd.CheckClockSkew].Status != "unknown" {
		t.Errorf("no hub date %+v", got[rd.CheckClockSkew])
	}
	if c := got[rd.CheckPolicyNamespaceCovers]; c.Status != "warn" || c.Details["list"] != "kube-system,monitoring" {
		t.Errorf("coverage %+v", c)
	}
}

func TestWebhookProblems(t *testing.T) {
	// not installed
	e := healthyEnv(t)
	e.objs = append(e.objs[:2], e.objs[4:]...)
	got := byID(e.checker(t).Run(context.Background()))
	if got[rd.CheckWebhook].Status != "warn" || !strings.Contains(got[rd.CheckWebhook].Message, "not installed") || got[rd.CheckCertManager].Status != "pass" {
		t.Errorf("no webhook %+v %+v", got[rd.CheckWebhook], got[rd.CheckCertManager])
	}

	// no caBundle, no ready endpoints, cert-manager missing
	e = healthyEnv(t)
	m := e.objs[2].(*admissionv1.MutatingWebhookConfiguration)
	m.Webhooks[0].ClientConfig.CABundle = nil
	notReady := false
	e.objs[3].(*discoveryv1.EndpointSlice).Endpoints[0].Conditions.Ready = &notReady
	e.resources = []*metav1.APIResourceList{coreResources(true), crdResources()}
	got = byID(e.checker(t).Run(context.Background()))
	w := got[rd.CheckWebhook]
	if w.Status != "warn" || !strings.Contains(w.Message, "no caBundle") || !strings.Contains(w.Message, "no ready endpoints") || e.dryRuns != 0 {
		t.Errorf("broken webhook %+v (dry runs %d)", w, e.dryRuns)
	}
	if got[rd.CheckCertManager].Status != "fail" {
		t.Errorf("cert-manager %+v", got[rd.CheckCertManager])
	}

	// dry run not mutated
	e = healthyEnv(t)
	e.dryRunMutes = true
	if w := byID(e.checker(t).Run(context.Background()))[rd.CheckWebhook]; w.Status != "warn" || !strings.Contains(w.Message, "not mutated") {
		t.Errorf("not mutated %+v", w)
	}

	// dry run off: no pod create at all
	e = healthyEnv(t)
	ck := e.checker(t)
	ck.Cfg.WebhookDryRun = false
	if w := byID(ck.Run(context.Background()))[rd.CheckWebhook]; w.Status != "pass" || e.dryRuns != 0 {
		t.Errorf("no dry run %+v %d", w, e.dryRuns)
	}
}

func TestArgoCDAndNodes(t *testing.T) {
	// argocd-cm without the manager
	e := healthyEnv(t)
	e.objs[4].(*corev1.ConfigMap).Data = map[string]string{"url": "https://argocd"}
	sw := int64(1 << 30)
	e.objs[5].(*corev1.Node).Status.NodeInfo.Swap = &corev1.NodeSwapStatus{Capacity: &sw}
	got := byID(e.checker(t).Run(context.Background()))
	a := got[rd.CheckArgoCD]
	if a.Status != "warn" || !strings.Contains(a.Remediation, "managedFieldsManagers") || a.Details["namespace"] != "argocd" {
		t.Errorf("argocd %+v", a)
	}
	n := got[rd.CheckNodesInPlaceMemory]
	if n.Status != "warn" || n.Details["nodes"] != "n1" || n.Details["total"] != "2" {
		t.Errorf("nodes %+v", n)
	}

	// per-kind keys also count
	if !ignoresManager(map[string]string{
		"resource.customizations.ignoreDifferences.apps_Deployment":  "managedFieldsManagers: [resource-optimizer]",
		"resource.customizations.ignoreDifferences.apps_StatefulSet": "managedFieldsManagers: [resource-optimizer]",
		"resource.customizations.ignoreDifferences.apps_DaemonSet":   "managedFieldsManagers: [resource-optimizer]",
		"resource.customizations.ignoreDifferences.batch_CronJob":    "managedFieldsManagers: [resource-optimizer]",
	}) {
		t.Error("per-kind ignoreDifferences not accepted")
	}
	if ignoresManager(map[string]string{"resource.customizations.ignoreDifferences.apps_Deployment": "managedFieldsManagers: [resource-optimizer]"}) {
		t.Error("partial per-kind accepted")
	}

	// argocd-cm unreadable while Argo CD is installed
	e = healthyEnv(t)
	e.forbidCM = true
	if a := byID(e.checker(t).Run(context.Background()))[rd.CheckArgoCD]; a.Status != "unknown" || a.Remediation == "" {
		t.Errorf("forbidden %+v", a)
	}
	// no Argo CD API and no Role (no argocd namespace): the 403 on argocd-cm is not "unreadable"
	e = healthyEnv(t)
	e.forbidCM = true
	e.resources = []*metav1.APIResourceList{coreResources(true), crdResources(), {GroupVersion: "cert-manager.io/v1"}}
	if a := byID(e.checker(t).Run(context.Background()))[rd.CheckArgoCD]; a.Status != "pass" || !strings.Contains(a.Message, "not installed") {
		t.Errorf("no argocd, forbidden: %+v", a)
	}
	// no Argo CD at all
	e = healthyEnv(t)
	e.objs = append(e.objs[:4], e.objs[5:]...)
	e.resources = []*metav1.APIResourceList{coreResources(true), crdResources(), {GroupVersion: "cert-manager.io/v1"}}
	if a := byID(e.checker(t).Run(context.Background()))[rd.CheckArgoCD]; a.Status != "pass" || !strings.Contains(a.Message, "not installed") {
		t.Errorf("no argocd %+v", a)
	}
}

func TestRunner(t *testing.T) {
	e := healthyEnv(t)
	got := make(chan Result, 4)
	r := &Runner{Checker: e.checker(t), Interval: time.Hour, OnResult: func(res Result) { got <- res }}
	if _, ok := r.Latest(); ok {
		t.Error("latest before run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	<-got
	if res, ok := r.Latest(); !ok || len(res.Checks) == 0 {
		t.Error("no latest")
	}
	// a re-check requested before the last run is ignored, a newer one runs
	if r.Recheck(now.Add(-time.Minute)) {
		t.Error("old recheck scheduled")
	}
	if !r.Recheck(now.Add(time.Minute)) {
		t.Error("new recheck not scheduled")
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("recheck did not run")
	}
}

func reflectType(v any) reflect.Type { return reflect.TypeOf(v) }

// TestGeneratedCRDsCurrent runs the schema comparison against the CRDs generated from the Go
// types: they must never look stale.
func TestGeneratedCRDsCurrent(t *testing.T) {
	for _, k := range crdKinds {
		b, err := os.ReadFile("../../../api/crds/" + v1alpha1.GroupVersion.Group + "_" + k.plural + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{}
		if err := sigsyaml.Unmarshal(b, &u.Object); err != nil {
			t.Fatal(err)
		}
		props := specSchema(u, v1alpha1.GroupVersion.Version)
		if props == nil {
			t.Fatalf("%s: no spec schema", k.plural)
		}
		if m := missingPaths(k.spec, props, "spec"); len(m) != 0 {
			t.Errorf("%s: generated CRD reported stale: %v", k.plural, m)
		}
		if k.plural == "rightsizingpolicies" {
			// an older CRD without the WO-19 field selector.workloads is detected (nested)
			sel, _, _ := unstructured.NestedMap(props, "properties", "selector", "properties")
			delete(sel, "workloads")
			_ = unstructured.SetNestedMap(props, sel, "properties", "selector", "properties")
			if m := missingPaths(k.spec, props, "spec"); strings.Join(m, ",") != "spec.selector.workloads" {
				t.Errorf("nested missing field: %v", m)
			}
		}
	}
}
