// Package install renders the install instructions of a workload cluster for the onboarding
// wizard: a Helm script, a values file, or an Argo CD Application, plus the Argo CD
// ignoreDifferences snippet for argocd-cm (D-006). All three install the umbrella chart
// kerf with global.mode spoke (D-028, docs/install/eks.md step 5.3): the agent, the
// admission webhook and the CRDs in one release.
//
// The token never appears: the snippets reference the Secret kerf-agent-token (key
// token), either an existing one (the Helm script creates it from $KERF_AGENT_TOKEN) or one the
// chart's ExternalSecret builds from a secret store. Its value is shown once at cluster creation or
// token creation. docs/contracts/readiness.md §4.
package install

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Defaults of Options.
const (
	DefaultNamespace       = "kerf"
	DefaultArgoCDNamespace = "argocd"
	DefaultArgoCDProject   = "default"
	DefaultCertManagerRef  = "oci://quay.io/jetstack/charts/cert-manager"
	DefaultCertManagerVer  = "v1.19.1"
	// DefaultTokenKey is the secret store key of a cluster's agent token ({cluster} = its name),
	// as in docs/install/eks.md step 5.2.
	DefaultTokenKey      = "kerf/clusters/{cluster}/agent-token"
	DefaultTokenProperty = "token"
	// ChartName and ReleaseName of the umbrella chart (deploy/charts/kerf).
	ChartName   = "kerf"
	ReleaseName = "kerf"
	// FieldManager is the agent's server-side apply field manager (D-006).
	FieldManager = "kerf"
	// TokenEnv is the shell variable the Helm snippet reads the agent token from.
	TokenEnv = "KERF_AGENT_TOKEN"
	// AgentReplicas: two agents with leader election (the umbrella chart's production default).
	AgentReplicas = 2
)

// Options describe what to render.
type Options struct {
	Cluster string
	// HubURL is the hub base URL as reachable from the cluster.
	HubURL string
	// Chart is the umbrella chart reference: oci://registry/path/kerf,
	// <helm repo URL>|kerf, or a local chart path.
	Chart string
	// ChartVersion is the chart version (empty = latest). The images default to it.
	ChartVersion string
	// ImageRegistry is global.imageRegistry, the registry host of the images (the chart requires
	// it). Empty: the host of an oci:// Chart.
	ImageRegistry string
	Namespace     string
	// ImageRepository and ImageTag override the chart's agent image when set.
	ImageRepository, ImageTag string
	// ReadOnly installs the agent without write RBAC (every mode capped at audit).
	ReadOnly bool
	// Webhook keeps the admission webhook (part of mode spoke; false sets webhook.enabled false).
	// CertManager adds cert-manager to the Helm script (the webhook needs it).
	Webhook, CertManager bool
	// CertManagerChart / CertManagerVersion default to DefaultCertManagerRef / Ver.
	CertManagerChart, CertManagerVersion string
	// Health is the cluster's metrics binding for the agent's throttling health watch (D-025);
	// nil = the chart defaults (no throttling rollback).
	Health *HealthMetrics
	// TokenExternalSecret makes the chart build the token Secret with the External Secrets
	// Operator; nil = an existing Secret (the Helm script creates it).
	TokenExternalSecret *TokenExternalSecret
	// ArgoCDNamespace and ArgoCDProject of the Application (defaults argocd, default);
	// DestinationServer defaults to the in-cluster API server.
	ArgoCDNamespace, ArgoCDProject, DestinationServer string
}

// HealthMetrics are the agent chart's health.* values selecting the cluster's series.
type HealthMetrics struct {
	// MetricsURL is set when the agent can query the datasource directly (no auth).
	MetricsURL        string   `yaml:"metricsURL,omitempty"`
	ClusterLabel      string   `yaml:"clusterLabel"`
	ClusterLabelValue string   `yaml:"clusterLabelValue,omitempty"`
	ExtraMatchers     []string `yaml:"extraMatchers,omitempty"`
}

// TokenExternalSecret: where the agent token is in the secret store.
type TokenExternalSecret struct {
	// SecretStoreKind (default ClusterSecretStore) and SecretStoreName: global.externalSecrets.secretStoreRef.
	SecretStoreKind, SecretStoreName string
	// Key (default DefaultTokenKey; {cluster} is replaced) and Property (default token).
	Key, Property string
}

var (
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	safeArg  = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,{}|-]*$`)
)

func (o *Options) defaults() error {
	var errs []error
	if !dnsLabel.MatchString(o.Cluster) {
		errs = append(errs, errors.New("cluster must be a DNS label"))
	}
	if u, err := url.Parse(o.HubURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, errors.New("hub URL must be an absolute http(s) URL"))
	}
	if o.Chart == "" {
		errs = append(errs, errors.New("chart reference is not configured"))
	}
	if o.ImageRegistry == "" {
		o.ImageRegistry = RegistryOf(o.Chart)
	}
	if o.ImageRegistry == "" && o.Chart != "" {
		errs = append(errs, errors.New("image registry is not configured (needed unless the chart is an oci:// reference)"))
	}
	if o.Namespace == "" {
		o.Namespace = DefaultNamespace
	}
	if !dnsLabel.MatchString(o.Namespace) {
		errs = append(errs, errors.New("namespace must be a DNS label"))
	}
	if o.ArgoCDNamespace == "" {
		o.ArgoCDNamespace = DefaultArgoCDNamespace
	}
	if o.ArgoCDProject == "" {
		o.ArgoCDProject = DefaultArgoCDProject
	}
	if o.DestinationServer == "" {
		o.DestinationServer = "https://kubernetes.default.svc"
	}
	if o.CertManagerChart == "" {
		o.CertManagerChart = DefaultCertManagerRef
	}
	if o.CertManagerVersion == "" {
		o.CertManagerVersion = DefaultCertManagerVer
	}
	args := []string{o.Chart, o.ChartVersion, o.ImageRegistry, o.ImageRepository, o.ImageTag, o.CertManagerChart, o.CertManagerVersion}
	if t := o.TokenExternalSecret; t != nil {
		t2 := *t
		if t2.SecretStoreKind == "" {
			t2.SecretStoreKind = "ClusterSecretStore"
		}
		if t2.Key == "" {
			t2.Key = DefaultTokenKey
		}
		t2.Key = strings.ReplaceAll(t2.Key, "{cluster}", o.Cluster)
		if t2.Property == "" {
			t2.Property = DefaultTokenProperty
		}
		if t2.SecretStoreName == "" {
			errs = append(errs, errors.New("token external secret: secret store name is not configured"))
		}
		o.TokenExternalSecret = &t2
		args = append(args, t2.SecretStoreKind, t2.SecretStoreName, t2.Key, t2.Property)
	}
	for _, c := range args {
		if !safeArg.MatchString(c) {
			errs = append(errs, fmt.Errorf("invalid characters in %q", c))
		}
	}
	return errors.Join(errs...)
}

// RegistryOf returns the registry host of an oci:// chart reference ("" otherwise).
func RegistryOf(ref string) string {
	rest, ok := strings.CutPrefix(ref, "oci://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	return host
}

// UmbrellaFromComponent derives the umbrella chart reference from a component chart reference
// (the older onboarding.agentChart): make release publishes it next to the components, e.g.
// oci://<registry>/charts/agent -> oci://<registry>/charts/kerf,
// <repo>|agent -> <repo>|kerf, deploy/charts/agent -> deploy/charts/kerf.
func UmbrellaFromComponent(ref string) string {
	if ref == "" {
		return ""
	}
	if repo, _, ok := strings.Cut(ref, "|"); ok {
		return repo + "|" + ChartName
	}
	rest, oci := strings.CutPrefix(ref, "oci://")
	out := path.Join(path.Dir(rest), ChartName)
	if oci {
		return "oci://" + out
	}
	return out
}

// ArgoCDIgnoreDifferences is the argocd-cm data entry that makes Argo CD ignore the fields the
// agent manages (D-006).
func ArgoCDIgnoreDifferences() string {
	return "resource.customizations.ignoreDifferences.all: |\n  managedFieldsManagers:\n  - " + FieldManager + "\n"
}

// Render returns the snippet in format helm, values or argocd.
func Render(format string, o Options) (hubapi.InstallSnippet, error) {
	if err := o.defaults(); err != nil {
		return hubapi.InstallSnippet{}, err
	}
	s := hubapi.InstallSnippet{Format: format, ArgoCDIgnoreDifferences: ArgoCDIgnoreDifferences()}
	var err error
	switch format {
	case hubapi.InstallHelm:
		s.Filename = "install.sh"
		s.Content, err = helmScript(o)
	case hubapi.InstallValues:
		s.Filename = valuesName(o)
		s.Content, err = valuesFile(o)
	case hubapi.InstallArgoCD:
		s.Filename = "kerf-" + o.Cluster + ".yaml"
		s.Content, err = argoCD(o)
	default:
		return s, fmt.Errorf("unknown format %q (want helm, values or argocd)", format)
	}
	if err != nil {
		return s, err
	}
	s.Notes = notes(format, o)
	return s, nil
}

func valuesName(o Options) string { return "values-" + o.Cluster + ".yaml" }

func notes(format string, o Options) []string {
	var n []string
	if t := o.TokenExternalSecret; t != nil {
		n = append(n, fmt.Sprintf("The agent token is not in this snippet: store the token shown once when the cluster was created (or a new agent token, POST /v1/admin/tokens) in the secret store %s as %s, property %s. The chart's ExternalSecret builds the Secret %s from it.",
			t.SecretStoreName, t.Key, t.Property, hubapi.AgentTokenSecretName))
	} else if format == hubapi.InstallHelm {
		n = append(n, fmt.Sprintf("The agent token is not in this snippet: set %s to the token shown once when the cluster was created (or a new agent token, POST /v1/admin/tokens); the script stores it in the Secret %s.",
			TokenEnv, hubapi.AgentTokenSecretName))
	} else {
		n = append(n, fmt.Sprintf("The agent token is not in this snippet: create the Secret %s (key %s) in namespace %s with the token shown once when the cluster was created, or create a new agent token (POST /v1/admin/tokens).",
			hubapi.AgentTokenSecretName, hubapi.AgentTokenSecretKey, o.Namespace))
	}
	n = append(n,
		"This installs the umbrella chart kerf in mode spoke with the settings of this cluster. docs/install/eks.md step 5.3 (deploy/examples/eks/values-spoke.yaml) adds the production extras: NetworkPolicies, ServiceMonitors, the webhook port and a private hub CA.",
		"If Argo CD manages the workloads, add the argocdIgnoreDifferences entry to argocd-cm so it does not revert the agent's changes.",
	)
	if o.ReadOnly {
		n = append(n, "readOnly: the agent gets no write permissions; every workload stays in audit and the cluster cannot reach AutomationReady.")
	}
	if o.Webhook && (!o.CertManager || format != hubapi.InstallHelm) {
		n = append(n, "The webhook needs cert-manager in the cluster (or set webhook.certManager.issuerRef to an existing issuer).")
	}
	if format == hubapi.InstallValues {
		n = append(n, fmt.Sprintf("helm upgrade --install %s %s -n %s --create-namespace -f %s --wait --timeout 10m",
			ReleaseName, chartCLI(o.Chart, o.ChartVersion), o.Namespace, valuesName(o)))
	}
	return n
}

// chartCLI renders a chart reference for helm: "oci://…/chart --version x" or "chart --repo url".
func chartCLI(ref, version string) string {
	out := ref
	if repo, chart, ok := strings.Cut(ref, "|"); ok {
		out = chart + " --repo " + repo
	}
	if version != "" {
		out += " --version " + version
	}
	return out
}

// valuesDelimiter ends the values heredoc of the Helm script. The values are YAML mappings of
// validated single-line strings, so no line equals it.
const valuesDelimiter = "KERF_VALUES"

func helmScript(o Options) (string, error) {
	vals, err := marshal(spokeVals(o))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -eu\n")
	fmt.Fprintf(&b, "# Kerf (chart %s, global.mode spoke) for cluster %s.\n# Run with kubectl and helm pointed at the cluster.\n", ChartName, o.Cluster)
	if t := o.TokenExternalSecret; t != nil {
		fmt.Fprintf(&b, "# The agent token comes from the secret store %s: store the token shown once when the\n# cluster was created (or POST /v1/admin/tokens) as %s, property %s, first.\n\n",
			t.SecretStoreName, t.Key, t.Property)
	} else {
		fmt.Fprintf(&b, "# %s: the agent token shown once when the cluster was created (or POST /v1/admin/tokens).\n", TokenEnv)
		fmt.Fprintf(&b, ": \"${%s:?set %s to the agent token}\"\n\n", TokenEnv, TokenEnv)
		fmt.Fprintf(&b, "kubectl create namespace %s --dry-run=client -o yaml | kubectl apply -f -\n", o.Namespace)
		fmt.Fprintf(&b, "kubectl -n %s create secret generic %s --from-literal=%s=\"$%s\" --dry-run=client -o yaml | kubectl apply -f -\n\n",
			o.Namespace, hubapi.AgentTokenSecretName, hubapi.AgentTokenSecretKey, TokenEnv)
	}
	if o.Webhook && o.CertManager {
		fmt.Fprintf(&b, "helm upgrade --install cert-manager %s -n cert-manager --create-namespace --set crds.enabled=true --wait\n\n",
			chartCLI(o.CertManagerChart, o.CertManagerVersion))
	}
	fmt.Fprintf(&b, "helm upgrade --install %s %s \\\n  -n %s --create-namespace --wait --timeout 10m -f - <<'%s'\n%s%s\n",
		ReleaseName, chartCLI(o.Chart, o.ChartVersion), o.Namespace, valuesDelimiter, vals, valuesDelimiter)
	return b.String(), nil
}

// ---- values (deploy/examples/eks/values-spoke.yaml, docs/install/eks.md step 5.3) ----

type secretStoreRef struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name"`
}

type globalValues struct {
	Mode            string `yaml:"mode"`
	ClusterName     string `yaml:"clusterName"`
	ImageRegistry   string `yaml:"imageRegistry"`
	ExternalSecrets *struct {
		SecretStoreRef secretStoreRef `yaml:"secretStoreRef"`
	} `yaml:"externalSecrets,omitempty"`
}

type remoteRef struct {
	Key      string `yaml:"key"`
	Property string `yaml:"property"`
}

type externalSecret struct {
	Enabled   bool      `yaml:"enabled"`
	RemoteRef remoteRef `yaml:"remoteRef"`
}

type tokenSecret struct {
	Name           string          `yaml:"name"`
	Key            string          `yaml:"key"`
	Create         bool            `yaml:"create"`
	ExternalSecret *externalSecret `yaml:"externalSecret,omitempty"`
}

type hubValues struct {
	URL         string      `yaml:"url"`
	TokenSecret tokenSecret `yaml:"tokenSecret"`
}

type imageValues struct {
	Repository string `yaml:"repository,omitempty"`
	Tag        string `yaml:"tag,omitempty"`
}

type agentValues struct {
	Hub            hubValues      `yaml:"hub"`
	ReplicaCount   int            `yaml:"replicaCount"`
	LeaderElection bool           `yaml:"leaderElection"`
	Image          *imageValues   `yaml:"image,omitempty"`
	ReadOnly       bool           `yaml:"readOnly"`
	Health         *HealthMetrics `yaml:"health,omitempty"`
}

type webhookValues struct {
	Enabled bool `yaml:"enabled"`
}

type spokeValues struct {
	Global  globalValues   `yaml:"global"`
	Agent   agentValues    `yaml:"agent"`
	Webhook *webhookValues `yaml:"webhook,omitempty"`
}

func spokeVals(o Options) spokeValues {
	v := spokeValues{
		Global: globalValues{Mode: "spoke", ClusterName: o.Cluster, ImageRegistry: o.ImageRegistry},
		Agent: agentValues{
			Hub:          hubValues{URL: o.HubURL, TokenSecret: tokenSecret{Name: hubapi.AgentTokenSecretName, Key: hubapi.AgentTokenSecretKey}},
			ReplicaCount: AgentReplicas, LeaderElection: true,
			ReadOnly: o.ReadOnly,
			Health:   o.Health,
		},
	}
	if t := o.TokenExternalSecret; t != nil {
		v.Global.ExternalSecrets = &struct {
			SecretStoreRef secretStoreRef `yaml:"secretStoreRef"`
		}{secretStoreRef{Kind: t.SecretStoreKind, Name: t.SecretStoreName}}
		v.Agent.Hub.TokenSecret.ExternalSecret = &externalSecret{Enabled: true, RemoteRef: remoteRef{Key: t.Key, Property: t.Property}}
	}
	if o.ImageRepository != "" || o.ImageTag != "" {
		v.Agent.Image = &imageValues{Repository: o.ImageRepository, Tag: o.ImageTag}
	}
	if !o.Webhook {
		v.Webhook = &webhookValues{Enabled: false}
	}
	return v
}

func marshal(docs ...any) (string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return "", err
		}
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func tokenComment(o Options) string {
	if t := o.TokenExternalSecret; t != nil {
		return fmt.Sprintf("# The token is read from the Secret %s, built by the chart's ExternalSecret from\n# %s (property %s) in the secret store %s. It is not in this file.\n",
			hubapi.AgentTokenSecretName, t.Key, t.Property, t.SecretStoreName)
	}
	return fmt.Sprintf("# The token is read from the Secret %s (key %s); it is not in this file.\n", hubapi.AgentTokenSecretName, hubapi.AgentTokenSecretKey)
}

func valuesFile(o Options) (string, error) {
	body, err := marshal(spokeVals(o))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# Kerf (chart %s, global.mode spoke) values for cluster %s.\n", ChartName, o.Cluster) + tokenComment(o) + body, nil
}

// ---- Argo CD (docs/install/eks.md steps 6 and 7) ----

type appSource struct {
	RepoURL        string `yaml:"repoURL"`
	Chart          string `yaml:"chart"`
	TargetRevision string `yaml:"targetRevision"`
	Helm           struct {
		ReleaseName  string `yaml:"releaseName"`
		ValuesObject any    `yaml:"valuesObject"`
	} `yaml:"helm"`
}

type ignoreDifference struct {
	Group                 string   `yaml:"group"`
	Kind                  string   `yaml:"kind"`
	ManagedFieldsManagers []string `yaml:"managedFieldsManagers,omitempty"`
	JQPathExpressions     []string `yaml:"jqPathExpressions,omitempty"`
}

type application struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		Project     string    `yaml:"project"`
		Source      appSource `yaml:"source"`
		Destination struct {
			Server    string `yaml:"server"`
			Namespace string `yaml:"namespace"`
		} `yaml:"destination"`
		IgnoreDifferences []ignoreDifference `yaml:"ignoreDifferences"`
		SyncPolicy        struct {
			Automated   map[string]bool `yaml:"automated"`
			SyncOptions []string        `yaml:"syncOptions"`
		} `yaml:"syncPolicy"`
	} `yaml:"spec"`
}

// argoSource splits a chart reference into Argo CD's repoURL and chart: OCI references drop the
// scheme and the chart name from the repository.
func argoSource(ref, version string) appSource {
	var s appSource
	if repo, chart, ok := strings.Cut(ref, "|"); ok {
		s.RepoURL, s.Chart = repo, chart
	} else {
		trimmed := strings.TrimPrefix(ref, "oci://")
		i := strings.LastIndex(trimmed, "/")
		s.RepoURL, s.Chart = trimmed[:max(i, 0)], trimmed[i+1:]
	}
	s.TargetRevision = version
	if s.TargetRevision == "" {
		s.TargetRevision = "*"
	}
	return s
}

// argoCD renders the Application of the umbrella chart, as one Application of
// deploy/examples/eks/applicationset.yaml: the agent's fields and the webhook's caBundle (written
// by cert-manager's CA injector) are ignored (D-006), and deleting the Application keeps the
// objects (no resources finalizer: the CRDs hold every policy).
func argoCD(o Options) (string, error) {
	var a application
	a.APIVersion, a.Kind = "argoproj.io/v1alpha1", "Application"
	a.Metadata.Name, a.Metadata.Namespace = "kerf-"+o.Cluster, o.ArgoCDNamespace
	a.Metadata.Labels = map[string]string{"app.kubernetes.io/part-of": "kerf"}
	a.Spec.Project = o.ArgoCDProject
	a.Spec.Source = argoSource(o.Chart, o.ChartVersion)
	a.Spec.Source.Helm.ReleaseName, a.Spec.Source.Helm.ValuesObject = ReleaseName, spokeVals(o)
	a.Spec.Destination.Server, a.Spec.Destination.Namespace = o.DestinationServer, o.Namespace
	a.Spec.IgnoreDifferences = []ignoreDifference{{Group: "*", Kind: "*", ManagedFieldsManagers: []string{FieldManager}}}
	if o.Webhook {
		a.Spec.IgnoreDifferences = append(a.Spec.IgnoreDifferences, ignoreDifference{
			Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration",
			JQPathExpressions: []string{".webhooks[]?.clientConfig.caBundle"},
		})
	}
	a.Spec.SyncPolicy.Automated = map[string]bool{"prune": true, "selfHeal": true}
	a.Spec.SyncPolicy.SyncOptions = []string{"CreateNamespace=true", "ServerSideApply=true", "RespectIgnoreDifferences=true"}
	body, err := marshal(a)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# Argo CD Application for cluster %s (chart %s, global.mode spoke).\n", o.Cluster, ChartName) + tokenComment(o) +
		"# Create that Secret (or the secret store entry) before the first sync.\n" + body, nil
}
