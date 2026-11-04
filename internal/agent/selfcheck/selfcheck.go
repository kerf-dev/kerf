// Package selfcheck runs the agent's cluster readiness self-checks (D-025,
// docs/contracts/readiness.md §3): RBAC per capability (SelfSubjectAccessReview), version and CRD
// compatibility, in-place resize support, webhook health, cert-manager, nodes with swap or static
// managers, the Argo CD ignoreDifferences setting, namespace coverage and clock skew. Results go
// in the status batch (checks[], capabilities[]).
package selfcheck

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	admissionv1 "k8s.io/api/admissionregistration/v1"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	rd "github.com/tomer1983/resource-optimizer/api/v1alpha1/readiness"
	"github.com/tomer1983/resource-optimizer/internal/agent/apply"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

// Remediation types.
const (
	RemediationCommand = "command"
	RemediationYAML    = "yaml"
	RemediationText    = "text"
)

// Thresholds.
const (
	MaxClockSkew = 30 * time.Second
	// InPlaceMinVersion is the first Kubernetes version with in-place pod resize on by default
	// through the pods/resize subresource (D-007).
	InPlaceMinVersion = "1.33.0"
	// FieldManager is the agent's server-side apply field manager (D-006).
	FieldManager = "resource-optimizer"
	// IgnoreDifferencesKey is the argocd-cm key holding ignoreDifferences for every resource.
	IgnoreDifferencesKey = "resource.customizations.ignoreDifferences.all"
)

// Config tunes the checks.
type Config struct {
	// AgentVersion is this agent's version (e.g. 0.2.1; non-semver builds such as dev skip the
	// comparison).
	AgentVersion string
	// ReadOnly: the agent was installed with readOnly: true (no write RBAC).
	ReadOnly bool
	// WebhookConfigName is the MutatingWebhookConfiguration of the webhook chart; empty =
	// the first one with a webhook named *.webhook.resource-optimizer.oz.io.
	WebhookConfigName string
	// WebhookDryRun creates a pod with dryRun=All to see the webhook mutate it. It needs `create
	// pods`, which the agent chart does not grant by default.
	WebhookDryRun bool
	// ArgoCDNamespaces are searched for argocd-cm (default argocd).
	ArgoCDNamespaces []string
	// UnsupportedNodeSelectors mark nodes where in-place resize is not used (chart value).
	UnsupportedNodeSelectors []map[string]string
	// DocsBase is the hub docs base URL for docsURL.
	DocsBase string
}

// Inputs are values the agent learns from the hub at run time.
type Inputs struct {
	// HubVersion and MinAgentVersion come from the last config pull.
	HubVersion, MinAgentVersion string
	// HubDate is the hub's Date header of the last hub response and LocalAt the local time it was
	// received; zero = unknown.
	HubDate, LocalAt time.Time
	// Excluded are namespaces excluded by policy or label, Namespaces the total; nil Excluded =
	// unknown.
	Excluded   []string
	Namespaces int
}

// Checker runs the self-checks.
type Checker struct {
	// Client reads live objects (uncached) and creates SelfSubjectAccessReviews.
	Client    client.Client
	Discovery discovery.DiscoveryInterface
	Cfg       Config
	// Inputs returns the current hub-derived inputs; nil = none.
	Inputs func() Inputs
	Now    func() time.Time
}

// Result is one run's report.
type Result struct {
	Checks       []hubclient.ReadinessCheck
	Capabilities []hubclient.AgentCapability
	At           time.Time
}

type result struct {
	c      *Checker
	at     time.Time
	checks []hubclient.ReadinessCheck
	caps   []hubclient.AgentCapability
}

func (r *result) add(id string, st rd.Status, msg, remediation, remType string, details map[string]string) {
	cat, _ := rd.Lookup(id)
	c := hubclient.ReadinessCheck{
		ID: id, Category: string(cat.Category), Severity: string(cat.Severity), Source: string(rd.SourceAgent), Gates: string(cat.Gates),
		Status: string(st), Message: msg, CheckedAt: r.at, Details: details,
	}
	if st != rd.StatusPass {
		c.Remediation, c.RemediationType = remediation, remType
	}
	if r.c.Cfg.DocsBase != "" {
		c.DocsURL = strings.TrimRight(r.c.Cfg.DocsBase, "/") + "/readiness#" + id
	}
	r.checks = append(r.checks, c)
}

// Run executes every agent check.
func (c *Checker) Run(ctx context.Context) Result {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	var in Inputs
	if c.Inputs != nil {
		in = c.Inputs()
	}
	r := &result{c: c, at: now.UTC()}
	c.checkVersion(r, in)
	c.checkCRDs(ctx, r)
	c.checkClockSkew(r, in)
	c.checkRBAC(ctx, r)
	c.checkInPlace(r)
	c.checkWebhook(ctx, r)
	c.checkNodes(ctx, r)
	c.checkArgoCD(ctx, r)
	c.checkCoverage(r, in)
	// catalog order
	order := map[string]int{}
	for i, cat := range rd.Catalog {
		order[cat.ID] = i
	}
	sort.SliceStable(r.checks, func(i, j int) bool { return order[r.checks[i].ID] < order[r.checks[j].ID] })
	return Result{Checks: r.checks, Capabilities: r.caps, At: r.at}
}

// semver parses v (a leading v allowed); nil for non-semver builds (dev, main-<sha>).
func semver(v string) *version.Version {
	if v == "" {
		return nil
	}
	p, err := version.ParseSemantic(strings.TrimPrefix(v, "v"))
	if err != nil {
		return nil
	}
	return p
}

func (c *Checker) checkVersion(r *result, in Inputs) {
	agent := semver(c.Cfg.AgentVersion)
	d := map[string]string{"agentVersion": c.Cfg.AgentVersion}
	if in.MinAgentVersion != "" {
		d["minAgentVersion"] = in.MinAgentVersion
	}
	if in.HubVersion != "" {
		d["hubVersion"] = in.HubVersion
	}
	upgrade := "helm upgrade agent <chart> -n resource-optimizer --reuse-values --version <hub version>"
	if agent == nil {
		r.add(rd.CheckAgentVersion, rd.StatusPass, fmt.Sprintf("development build %q: version not compared", c.Cfg.AgentVersion), "", "", d)
		return
	}
	if minV := semver(in.MinAgentVersion); minV != nil && agent.LessThan(minV) {
		r.add(rd.CheckAgentVersion, rd.StatusFail, fmt.Sprintf("agent %s is older than the hub's minimum %s", c.Cfg.AgentVersion, in.MinAgentVersion), upgrade, RemediationCommand, d)
		return
	}
	if hub := semver(in.HubVersion); hub != nil && (agent.Major() > hub.Major() || agent.Major() == hub.Major() && agent.Minor() > hub.Minor()) {
		r.add(rd.CheckAgentVersion, rd.StatusFail, fmt.Sprintf("agent %s is newer than the hub %s supports: upgrade the hub first", c.Cfg.AgentVersion, in.HubVersion),
			"Upgrade the hub to at least the agent's minor version, or install the agent version that matches the hub.", RemediationText, d)
		return
	}
	r.add(rd.CheckAgentVersion, rd.StatusPass, "agent "+c.Cfg.AgentVersion+" is compatible with the hub", "", "", d)
}

// crdKinds are the CRDs the agent needs and their spec types.
var crdKinds = []struct {
	plural string
	spec   reflect.Type
}{
	{"rightsizingpolicies", reflect.TypeOf(v1alpha1.RightsizingPolicySpec{})},
	{"rightsizingrecommendations", reflect.TypeOf(v1alpha1.RightsizingRecommendationSpec{})},
}

func (c *Checker) checkCRDs(ctx context.Context, r *result) {
	gv := v1alpha1.GroupVersion.String()
	rem := "kubectl apply --server-side --force-conflicts -f api/crds/   # the CRDs of the agent chart's version (Helm installs crds/ only on first install)"
	res, err := c.Discovery.ServerResourcesForGroupVersion(gv)
	if err != nil && !apierrors.IsNotFound(err) {
		r.add(rd.CheckCRDs, rd.StatusUnknown, "discovery failed: "+err.Error(), "", "", nil)
		return
	}
	have := map[string]bool{}
	if res != nil {
		for _, a := range res.APIResources {
			have[a.Name] = true
		}
	}
	var missing []string
	for _, k := range crdKinds {
		if !have[k.plural] {
			missing = append(missing, k.plural+"."+v1alpha1.GroupVersion.Group)
		}
	}
	if len(missing) > 0 {
		r.add(rd.CheckCRDs, rd.StatusFail, "CRDs not installed: "+strings.Join(missing, ", "), rem, RemediationCommand, nil)
		return
	}
	// Schema currency: every spec field the agent knows must be in the installed CRD.
	var stale []string
	unreadable := false
	for _, k := range crdKinds {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
		err := c.Client.Get(ctx, client.ObjectKey{Name: k.plural + "." + v1alpha1.GroupVersion.Group}, crd)
		if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
			unreadable = true
			continue
		}
		if err != nil {
			r.add(rd.CheckCRDs, rd.StatusUnknown, "read CRD: "+err.Error(), "", "", nil)
			return
		}
		props := specSchema(crd, v1alpha1.GroupVersion.Version)
		if props == nil {
			stale = append(stale, k.plural+": no "+v1alpha1.GroupVersion.Version+" schema")
			continue
		}
		for _, p := range missingPaths(k.spec, props, "spec") {
			stale = append(stale, k.plural+" "+p)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		if len(stale) > 10 {
			stale = append(stale[:10], fmt.Sprintf("… %d more", len(stale)-10))
		}
		r.add(rd.CheckCRDs, rd.StatusFail, "CRDs older than the agent; missing fields: "+strings.Join(stale, ", "), rem, RemediationCommand, nil)
		return
	}
	msg := "CRDs installed and current"
	if unreadable {
		msg = "CRDs installed (schema not compared: no permission to read customresourcedefinitions)"
	}
	r.add(rd.CheckCRDs, rd.StatusPass, msg, "", "", nil)
}

// specSchema returns the openAPIV3Schema properties of spec for version.
func specSchema(crd *unstructured.Unstructured, ver string) map[string]any {
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for _, v := range versions {
		m, ok := v.(map[string]any)
		if !ok || m["name"] != ver {
			continue
		}
		props, _, _ := unstructured.NestedMap(m, "schema", "openAPIV3Schema", "properties", "spec")
		return props
	}
	return nil
}

// missingPaths lists json paths of t (a struct) absent from the schema node.
func missingPaths(t reflect.Type, node map[string]any, path string) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		if t.Kind() == reflect.Map {
			node, _ = node["additionalProperties"].(map[string]any)
		} else if t.Kind() != reflect.Pointer {
			node, _ = node["items"].(map[string]any)
		}
		t = t.Elem()
		if node == nil {
			return nil
		}
	}
	if t.Kind() != reflect.Struct || t.PkgPath() != reflect.TypeOf(v1alpha1.RightsizingPolicySpec{}).PkgPath() {
		return nil // leaf or foreign type (Quantity, Duration, metav1 types)
	}
	if v, _ := node["x-kubernetes-preserve-unknown-fields"].(bool); v {
		return nil
	}
	props, _ := node["properties"].(map[string]any)
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			if f.Anonymous {
				out = append(out, missingPaths(f.Type, node, path)...)
			}
			continue
		}
		child, ok := props[name].(map[string]any)
		if !ok {
			out = append(out, path+"."+name)
			continue
		}
		out = append(out, missingPaths(f.Type, child, path+"."+name)...)
	}
	return out
}

func (c *Checker) checkClockSkew(r *result, in Inputs) {
	if in.HubDate.IsZero() || in.LocalAt.IsZero() {
		r.add(rd.CheckClockSkew, rd.StatusUnknown, "no hub response with a Date header yet", "", "", nil)
		return
	}
	// The Date header has 1 s resolution; the request took part of a second.
	skew := in.LocalAt.Sub(in.HubDate)
	abs := skew
	if abs < 0 {
		abs = -abs
	}
	d := map[string]string{"skew": skew.Round(time.Second).String()}
	if abs > MaxClockSkew {
		r.add(rd.CheckClockSkew, rd.StatusWarn, fmt.Sprintf("the node clock differs from the hub by %s (limit 30s): windows and cooldowns are off", skew.Round(time.Second)),
			"Enable time synchronisation (chrony / NTP, e.g. the Amazon Time Sync Service) on the nodes and the hub.", RemediationText, d)
		return
	}
	r.add(rd.CheckClockSkew, rd.StatusPass, fmt.Sprintf("clock skew to the hub %s", skew.Round(time.Second)), "", "", d)
}

func (c *Checker) checkInPlace(r *result) {
	v, err := c.Discovery.ServerVersion()
	if err != nil {
		r.add(rd.CheckInPlaceResize, rd.StatusUnknown, "server version: "+err.Error(), "", "", nil)
		return
	}
	d := map[string]string{"serverVersion": v.GitVersion}
	sv, err := version.ParseGeneric(v.GitVersion)
	minV := version.MustParseGeneric(InPlaceMinVersion)
	res, rerr := c.Discovery.ServerResourcesForGroupVersion("v1")
	resize := false
	if rerr == nil {
		for _, a := range res.APIResources {
			if a.Name == "pods/resize" {
				resize = true
			}
		}
	}
	d["podsResize"] = strconv.FormatBool(resize)
	rem := "Upgrade the cluster to Kubernetes 1.33 or later; until then use apply.strategy: rollout."
	switch {
	case err != nil:
		r.add(rd.CheckInPlaceResize, rd.StatusUnknown, "cannot parse server version "+v.GitVersion, "", "", d)
	case sv.LessThan(minV):
		r.add(rd.CheckInPlaceResize, rd.StatusWarn, fmt.Sprintf("Kubernetes %s is older than 1.33: in-place falls back to rollout", v.GitVersion), rem, RemediationText, d)
	case !resize:
		r.add(rd.CheckInPlaceResize, rd.StatusWarn, "the pods/resize subresource is not served (InPlacePodVerticalScaling disabled?): in-place falls back to rollout", rem, RemediationText, d)
	default:
		r.add(rd.CheckInPlaceResize, rd.StatusPass, fmt.Sprintf("Kubernetes %s serves pods/resize", v.GitVersion), "", "", d)
	}
}

const webhookSuffix = ".webhook.resource-optimizer.oz.io"

func (c *Checker) findWebhook(ctx context.Context) (*admissionv1.MutatingWebhookConfiguration, error) {
	if c.Cfg.WebhookConfigName != "" {
		m := &admissionv1.MutatingWebhookConfiguration{}
		err := c.Client.Get(ctx, client.ObjectKey{Name: c.Cfg.WebhookConfigName}, m)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return m, err
	}
	var list admissionv1.MutatingWebhookConfigurationList
	if err := c.Client.List(ctx, &list); err != nil {
		return nil, err
	}
	for i := range list.Items {
		for _, w := range list.Items[i].Webhooks {
			if strings.HasSuffix(w.Name, webhookSuffix) {
				return &list.Items[i], nil
			}
		}
	}
	return nil, nil
}

func (c *Checker) checkWebhook(ctx context.Context, r *result) {
	installRem := "helm upgrade --install webhook <webhook chart> -n resource-optimizer --set-string clusterName=<cluster>   # needs cert-manager"
	m, err := c.findWebhook(ctx)
	switch {
	case apierrors.IsForbidden(err):
		r.add(rd.CheckWebhook, rd.StatusUnknown, "no permission to read mutatingwebhookconfigurations", "", "", nil)
		r.add(rd.CheckCertManager, rd.StatusUnknown, "webhook configuration unreadable", "", "", nil)
		return
	case err != nil:
		r.add(rd.CheckWebhook, rd.StatusUnknown, "read webhook configuration: "+err.Error(), "", "", nil)
		r.add(rd.CheckCertManager, rd.StatusUnknown, "webhook configuration unreadable", "", "", nil)
		return
	case m == nil:
		r.add(rd.CheckWebhook, rd.StatusWarn, "the resource-optimizer webhook is not installed: new pods start with their template values until the agent resizes them", installRem, RemediationCommand, nil)
		r.add(rd.CheckCertManager, rd.StatusPass, "no webhook, cert-manager not needed", "", "", nil)
		return
	}
	d := map[string]string{"configuration": m.Name}
	c.checkCertManager(r, m)

	var problems []string
	var svc *admissionv1.ServiceReference
	for _, w := range m.Webhooks {
		if len(w.ClientConfig.CABundle) == 0 {
			problems = append(problems, fmt.Sprintf("webhook %s has no caBundle (cert-manager CA injection pending?)", w.Name))
		}
		if w.ClientConfig.Service != nil {
			svc = w.ClientConfig.Service
		}
	}
	if svc != nil {
		d["service"] = svc.Namespace + "/" + svc.Name
		ready, err := c.readyEndpoints(ctx, svc.Namespace, svc.Name)
		switch {
		case err != nil:
			d["endpoints"] = "unknown: " + err.Error()
		case ready == 0:
			problems = append(problems, fmt.Sprintf("service %s/%s has no ready endpoints", svc.Namespace, svc.Name))
		default:
			d["readyEndpoints"] = strconv.Itoa(ready)
		}
	}
	if len(problems) == 0 && c.Cfg.WebhookDryRun {
		mutated, probe, err := c.dryRun(ctx)
		switch {
		case err != nil:
			d["dryRun"] = "error: " + err.Error()
		case probe == "":
			d["dryRun"] = "skipped: no pod of a workload with a recommendation to probe"
		case !mutated:
			d["dryRun"] = "not mutated: " + probe
			problems = append(problems, "a dry-run copy of pod "+probe+" was not mutated")
		default:
			d["dryRun"] = "mutated: " + probe
		}
	}
	if len(problems) > 0 {
		r.add(rd.CheckWebhook, rd.StatusWarn, strings.Join(problems, "; "),
			"kubectl -n resource-optimizer get pods,certificate -l app.kubernetes.io/name=webhook && kubectl -n resource-optimizer logs deploy/webhook", RemediationCommand, d)
		return
	}
	r.add(rd.CheckWebhook, rd.StatusPass, "webhook "+m.Name+" configured with a CA and ready endpoints", "", "", d)
}

func (c *Checker) readyEndpoints(ctx context.Context, ns, svc string) (int, error) {
	var slices discoveryv1.EndpointSliceList
	if err := c.Client.List(ctx, &slices, client.InNamespace(ns), client.MatchingLabels{discoveryv1.LabelServiceName: svc}); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				n++
			}
		}
	}
	return n, nil
}

// dryRun creates a dry-run copy of a pod of a workload with a recommendation and reports whether
// the webhook mutated it (annotation injected-resources). probe is the pod copied ("" = none).
func (c *Checker) dryRun(ctx context.Context) (mutated bool, probe string, err error) {
	var recs v1alpha1.RightsizingRecommendationList
	if err := c.Client.List(ctx, &recs); err != nil {
		return false, "", err
	}
	for _, rec := range recs.Items {
		if len(rec.Spec.Containers) == 0 {
			continue
		}
		var pods corev1.PodList
		if err := c.Client.List(ctx, &pods, client.InNamespace(rec.Namespace)); err != nil {
			return false, "", err
		}
		for _, p := range pods.Items {
			ctrl := metav1.GetControllerOf(&p)
			if ctrl == nil || p.Labels[v1alpha1.LabelWebhookSkip] == "true" || !ownedBy(p, rec.Spec.TargetRef.Name) {
				continue
			}
			cp := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				GenerateName: "ro-selfcheck-", Namespace: p.Namespace, Labels: p.Labels, OwnerReferences: p.OwnerReferences,
			}, Spec: *p.Spec.DeepCopy()}
			cp.Spec.NodeName = ""
			delete(cp.Labels, "pod-template-hash")
			if err := c.Client.Create(ctx, cp, client.DryRunAll); err != nil {
				return false, p.Namespace + "/" + p.Name, err
			}
			_, ok := cp.Annotations[v1alpha1.AnnotationInjectedResources]
			return ok, p.Namespace + "/" + p.Name, nil
		}
	}
	return false, "", nil
}

// ownedBy reports whether a pod's controller chain likely belongs to workload (ReplicaSet names
// start with the Deployment name; Job names with the CronJob name).
func ownedBy(p corev1.Pod, workload string) bool {
	ctrl := metav1.GetControllerOf(&p)
	return ctrl != nil && (ctrl.Name == workload || strings.HasPrefix(ctrl.Name, workload+"-"))
}

func (c *Checker) checkCertManager(r *result, m *admissionv1.MutatingWebhookConfiguration) {
	if _, uses := m.Annotations["cert-manager.io/inject-ca-from"]; !uses {
		r.add(rd.CheckCertManager, rd.StatusPass, "the webhook does not use cert-manager", "", "", nil)
		return
	}
	if _, err := c.Discovery.ServerResourcesForGroupVersion("cert-manager.io/v1"); err != nil {
		r.add(rd.CheckCertManager, rd.StatusFail, "the webhook uses cert-manager, but cert-manager.io/v1 is not served",
			"helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager -n cert-manager --create-namespace --set crds.enabled=true", RemediationCommand, nil)
		return
	}
	r.add(rd.CheckCertManager, rd.StatusPass, "cert-manager present", "", "", nil)
}

func (c *Checker) checkNodes(ctx context.Context, r *result) {
	var nodes corev1.NodeList
	if err := c.Client.List(ctx, &nodes); err != nil {
		r.add(rd.CheckNodesInPlaceMemory, rd.StatusUnknown, "list nodes: "+err.Error(), "", "", nil)
		return
	}
	var bad []string
	for i := range nodes.Items {
		if ok, _ := apply.NodeSupportsInPlace(&nodes.Items[i], c.Cfg.UnsupportedNodeSelectors, true); !ok {
			bad = append(bad, nodes.Items[i].Name)
		}
	}
	sort.Strings(bad)
	d := map[string]string{"total": strconv.Itoa(len(nodes.Items)), "unsupported": strconv.Itoa(len(bad))}
	if len(bad) == 0 {
		r.add(rd.CheckNodesInPlaceMemory, rd.StatusPass, fmt.Sprintf("in-place memory resize possible on all %d nodes", len(nodes.Items)), "", "", d)
		return
	}
	shown := bad
	if len(shown) > 20 {
		shown = append(shown[:20:20], fmt.Sprintf("… %d more", len(bad)-20))
	}
	d["nodes"] = strings.Join(shown, ",")
	r.add(rd.CheckNodesInPlaceMemory, rd.StatusWarn, fmt.Sprintf("%d of %d nodes have swap or a static CPU/memory manager: memory changes there use rollout", len(bad), len(nodes.Items)),
		"Nothing to do unless most workloads run there: memory changes on those nodes use the rollout strategy (D-007).", RemediationText, d)
}

// ArgoCDSnippet is the argocd-cm entry the Argo CD check asks for.
func ArgoCDSnippet() string {
	return IgnoreDifferencesKey + ": |\n  managedFieldsManagers:\n  - " + FieldManager + "\n"
}

func (c *Checker) checkArgoCD(ctx context.Context, r *result) {
	nss := c.Cfg.ArgoCDNamespaces
	if len(nss) == 0 {
		nss = []string{"argocd"}
	}
	rem := "kubectl -n " + nss[0] + " patch configmap argocd-cm --type merge -p '{\"data\":{\"" + IgnoreDifferencesKey + "\":\"managedFieldsManagers:\\n- " + FieldManager + "\\n\"}}'\n# or in argocd-cm data:\n" + ArgoCDSnippet()
	_, apiErr := c.Discovery.ServerResourcesForGroupVersion("argoproj.io/v1alpha1")
	installed := apiErr == nil
	var forbidden bool
	for _, ns := range nss {
		cm := &corev1.ConfigMap{}
		err := c.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "argocd-cm"}, cm)
		switch {
		case apierrors.IsNotFound(err):
			continue
		case apierrors.IsForbidden(err):
			forbidden = true
			continue
		case err != nil:
			r.add(rd.CheckArgoCD, rd.StatusUnknown, "read argocd-cm: "+err.Error(), rem, RemediationCommand, nil)
			return
		}
		d := map[string]string{"namespace": ns}
		if ignoresManager(cm.Data) {
			r.add(rd.CheckArgoCD, rd.StatusPass, "argocd-cm ignores fields managed by "+FieldManager, "", "", d)
			return
		}
		r.add(rd.CheckArgoCD, rd.StatusWarn, "argocd-cm in "+ns+" lacks managedFieldsManagers: ["+FieldManager+"]: Argo CD self-heal reverts the agent's changes", rem, RemediationCommand, d)
		return
	}
	switch {
	case forbidden || installed:
		r.add(rd.CheckArgoCD, rd.StatusUnknown, "Argo CD is installed but argocd-cm is not readable by the agent: check that it ignores fields managed by "+FieldManager, rem, RemediationCommand, nil)
	default:
		r.add(rd.CheckArgoCD, rd.StatusPass, "Argo CD not installed", "", "", nil)
	}
}

// ignoresManager reports whether argocd-cm ignores fields of the agent's field manager for every
// resource, or for each workload kind the agent changes.
func ignoresManager(data map[string]string) bool {
	has := func(key string) bool {
		var v struct {
			Managers []string `json:"managedFieldsManagers"`
		}
		if yaml.Unmarshal([]byte(data[key]), &v) != nil {
			return false
		}
		for _, m := range v.Managers {
			if m == FieldManager {
				return true
			}
		}
		return false
	}
	if has(IgnoreDifferencesKey) {
		return true
	}
	for _, k := range []string{"apps_Deployment", "apps_StatefulSet", "apps_DaemonSet", "batch_CronJob"} {
		if !has("resource.customizations.ignoreDifferences." + k) {
			return false
		}
	}
	return true
}

func (c *Checker) checkCoverage(r *result, in Inputs) {
	if in.Excluded == nil {
		r.add(rd.CheckPolicyNamespaceCovers, rd.StatusUnknown, "namespace coverage not computed yet", "", "", nil)
		return
	}
	d := map[string]string{"namespaces": strconv.Itoa(in.Namespaces), "excluded": strconv.Itoa(len(in.Excluded))}
	if len(in.Excluded) == 0 {
		r.add(rd.CheckPolicyNamespaceCovers, rd.StatusPass, fmt.Sprintf("all %d namespaces covered", in.Namespaces), "", "", d)
		return
	}
	ex := append([]string(nil), in.Excluded...)
	sort.Strings(ex)
	if len(ex) > 30 {
		ex = append(ex[:30:30], fmt.Sprintf("… %d more", len(in.Excluded)-30))
	}
	d["list"] = strings.Join(ex, ",")
	r.add(rd.CheckPolicyNamespaceCovers, rd.StatusWarn, fmt.Sprintf("%d of %d namespaces excluded by policy or label", len(in.Excluded), in.Namespaces),
		"Informational: excluded namespaces get no recommendations applied.", RemediationText, d)
}
