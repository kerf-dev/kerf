// Package install renders the agent (and optional webhook) install instructions of a cluster for
// the onboarding wizard: a Helm command, a values file, or an Argo CD Application, plus the Argo
// CD ignoreDifferences snippet (D-006). The token never appears: the snippets reference the Secret
// resource-optimizer-agent-token (key token), whose value is shown once at cluster creation or
// token creation. docs/contracts/readiness.md §4.
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// Defaults of Options.
const (
	DefaultNamespace       = "resource-optimizer"
	DefaultArgoCDNamespace = "argocd"
	DefaultArgoCDProject   = "default"
	DefaultCertManagerRef  = "oci://quay.io/jetstack/charts/cert-manager"
	DefaultCertManagerVer  = "v1.19.1"
	// FieldManager is the agent's server-side apply field manager (D-006).
	FieldManager = "resource-optimizer"
	// TokenEnv is the shell variable the Helm snippet reads the agent token from.
	TokenEnv = "RO_AGENT_TOKEN"
)

// Options describe what to render.
type Options struct {
	Cluster string
	// HubURL is the hub base URL as reachable from the cluster.
	HubURL string
	// AgentChart and WebhookChart are chart references: oci://registry/path/<chart> or
	// <helm repo URL>|<chart>, e.g. https://charts.example.com|agent.
	AgentChart, WebhookChart string
	// ChartVersion is the chart version (empty = latest).
	ChartVersion string
	Namespace    string
	// ImageRepository and ImageTag override the chart's agent image when set.
	ImageRepository, ImageTag string
	// ReadOnly installs the agent without write RBAC (every mode capped at audit).
	ReadOnly bool
	// Webhook adds the admission webhook; CertManager adds cert-manager (the webhook needs it).
	Webhook, CertManager bool
	// CertManagerChart / CertManagerVersion default to DefaultCertManagerRef / Ver.
	CertManagerChart, CertManagerVersion string
	// Health is the cluster's metrics binding for the agent's throttling health watch (D-025);
	// nil = the chart defaults (hub VictoriaMetrics, label cluster).
	Health *HealthMetrics
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

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

func (o *Options) defaults() error {
	var errs []error
	if !dnsLabel.MatchString(o.Cluster) {
		errs = append(errs, errors.New("cluster must be a DNS label"))
	}
	if u, err := url.Parse(o.HubURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, errors.New("hub URL must be an absolute http(s) URL"))
	}
	if o.AgentChart == "" {
		errs = append(errs, errors.New("agent chart reference is not configured"))
	}
	if o.Webhook && o.WebhookChart == "" {
		errs = append(errs, errors.New("webhook chart reference is not configured"))
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
	for _, c := range []string{o.AgentChart, o.WebhookChart, o.ChartVersion, o.ImageRepository, o.ImageTag, o.CertManagerChart, o.CertManagerVersion} {
		if strings.ContainsAny(c, "\n\r'\"`$\\ ") {
			errs = append(errs, fmt.Errorf("invalid characters in %q", c))
		}
	}
	return errors.Join(errs...)
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
		s.Content, s.Filename = helmScript(o), "install.sh"
	case hubapi.InstallValues:
		s.Filename = "values-" + o.Cluster + ".yaml"
		s.Content, err = valuesFile(o)
	case hubapi.InstallArgoCD:
		s.Filename = "resource-optimizer-" + o.Cluster + ".yaml"
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

func notes(format string, o Options) []string {
	n := []string{
		fmt.Sprintf("The agent token is not in this snippet: create the Secret %s (key %s) in namespace %s with the token shown once when the cluster was created, or create a new agent token (POST /v1/admin/tokens).",
			hubapi.AgentTokenSecretName, hubapi.AgentTokenSecretKey, o.Namespace),
		"If Argo CD manages the workloads, add the argocdIgnoreDifferences entry to argocd-cm so it does not revert the agent's changes.",
	}
	if o.ReadOnly {
		n = append(n, "readOnly: the agent gets no write permissions; every workload stays in audit and the cluster cannot reach AutomationReady.")
	}
	if o.Webhook && !o.CertManager {
		n = append(n, "The webhook needs cert-manager in the cluster (or set webhook issuerRef to an existing issuer).")
	}
	if format == hubapi.InstallValues {
		n = append(n, fmt.Sprintf("helm upgrade --install agent %s -n %s --create-namespace -f %s", chartCLI(o.AgentChart, o.ChartVersion), o.Namespace, "values-"+o.Cluster+".yaml"))
	}
	return n
}

// shellQuote quotes s for POSIX shells.
func shellQuote(s string) string {
	if s != "" && regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// chartCLI renders a chart reference for helm: "oci://…/agent --version x" or "agent --repo url".
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

func helmScript(o Options) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -eu\n")
	fmt.Fprintf(&b, "# resource-optimizer for cluster %s. Run with kubectl/helm pointed at the cluster.\n", o.Cluster)
	fmt.Fprintf(&b, "# %s: the agent token shown once when the cluster was created (or POST /v1/admin/tokens).\n", TokenEnv)
	fmt.Fprintf(&b, ": \"${%s:?set %s to the agent token}\"\n\n", TokenEnv, TokenEnv)
	fmt.Fprintf(&b, "kubectl create namespace %s --dry-run=client -o yaml | kubectl apply -f -\n", o.Namespace)
	fmt.Fprintf(&b, "kubectl -n %s create secret generic %s --from-literal=%s=\"$%s\" --dry-run=client -o yaml | kubectl apply -f -\n\n",
		o.Namespace, hubapi.AgentTokenSecretName, hubapi.AgentTokenSecretKey, TokenEnv)
	if o.Webhook && o.CertManager {
		fmt.Fprintf(&b, "helm upgrade --install cert-manager %s -n cert-manager --create-namespace --set crds.enabled=true --wait\n\n",
			chartCLI(o.CertManagerChart, o.CertManagerVersion))
	}
	fmt.Fprintf(&b, "helm upgrade --install agent %s -n %s --wait \\\n  --set-string clusterName=%s \\\n  --set-string hub.url=%s",
		chartCLI(o.AgentChart, o.ChartVersion), o.Namespace, shellQuote(o.Cluster), shellQuote(o.HubURL))
	fmt.Fprintf(&b, " \\\n  --set-string hub.tokenSecret.name=%s --set-string hub.tokenSecret.key=%s", hubapi.AgentTokenSecretName, hubapi.AgentTokenSecretKey)
	if o.ImageRepository != "" {
		fmt.Fprintf(&b, " \\\n  --set-string image.repository=%s", o.ImageRepository)
	}
	if o.ImageTag != "" {
		fmt.Fprintf(&b, " \\\n  --set-string image.tag=%s", o.ImageTag)
	}
	if o.ReadOnly {
		b.WriteString(" \\\n  --set readOnly=true")
	}
	if h := o.Health; h != nil {
		if h.MetricsURL != "" {
			fmt.Fprintf(&b, " \\\n  --set-string health.metricsURL=%s", shellQuote(h.MetricsURL))
		}
		fmt.Fprintf(&b, " \\\n  --set-string health.clusterLabel=%s", shellQuote(h.ClusterLabel))
		if h.ClusterLabelValue != "" {
			fmt.Fprintf(&b, " \\\n  --set-string health.clusterLabelValue=%s", shellQuote(h.ClusterLabelValue))
		}
		if len(h.ExtraMatchers) > 0 {
			j, _ := json.Marshal(h.ExtraMatchers)
			fmt.Fprintf(&b, " \\\n  --set-json health.extraMatchers=%s", shellQuote(string(j)))
		}
	}
	b.WriteString("\n")
	if o.Webhook {
		fmt.Fprintf(&b, "\nhelm upgrade --install webhook %s -n %s --wait \\\n  --set-string clusterName=%s\n",
			chartCLI(o.WebhookChart, o.ChartVersion), o.Namespace, shellQuote(o.Cluster))
	}
	return b.String()
}

type tokenSecret struct {
	Name   string `yaml:"name"`
	Key    string `yaml:"key"`
	Create bool   `yaml:"create"`
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
	ClusterName string         `yaml:"clusterName"`
	Hub         hubValues      `yaml:"hub"`
	Image       *imageValues   `yaml:"image,omitempty"`
	ReadOnly    bool           `yaml:"readOnly"`
	Health      *HealthMetrics `yaml:"health,omitempty"`
}

func agentVals(o Options) agentValues {
	v := agentValues{
		ClusterName: o.Cluster,
		Hub:         hubValues{URL: o.HubURL, TokenSecret: tokenSecret{Name: hubapi.AgentTokenSecretName, Key: hubapi.AgentTokenSecretKey}},
		ReadOnly:    o.ReadOnly,
		Health:      o.Health,
	}
	if o.ImageRepository != "" || o.ImageTag != "" {
		v.Image = &imageValues{Repository: o.ImageRepository, Tag: o.ImageTag}
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

func valuesFile(o Options) (string, error) {
	body, err := marshal(agentVals(o))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# resource-optimizer agent values for cluster %s.\n# The token is read from the Secret %s (key %s); it is not in this file.\n",
		o.Cluster, hubapi.AgentTokenSecretName, hubapi.AgentTokenSecretKey) + body, nil
}

type appSource struct {
	RepoURL        string `yaml:"repoURL"`
	Chart          string `yaml:"chart"`
	TargetRevision string `yaml:"targetRevision"`
	Helm           struct {
		ReleaseName  string `yaml:"releaseName"`
		ValuesObject any    `yaml:"valuesObject"`
	} `yaml:"helm"`
}

type application struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		Project     string    `yaml:"project"`
		Source      appSource `yaml:"source"`
		Destination struct {
			Server    string `yaml:"server"`
			Namespace string `yaml:"namespace"`
		} `yaml:"destination"`
		SyncPolicy struct {
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

func newApp(o Options, name, ref, release string, values any) application {
	var a application
	a.APIVersion, a.Kind = "argoproj.io/v1alpha1", "Application"
	a.Metadata.Name, a.Metadata.Namespace = name, o.ArgoCDNamespace
	a.Spec.Project = o.ArgoCDProject
	a.Spec.Source = argoSource(ref, o.ChartVersion)
	a.Spec.Source.Helm.ReleaseName, a.Spec.Source.Helm.ValuesObject = release, values
	a.Spec.Destination.Server, a.Spec.Destination.Namespace = o.DestinationServer, o.Namespace
	a.Spec.SyncPolicy.Automated = map[string]bool{"prune": true, "selfHeal": true}
	a.Spec.SyncPolicy.SyncOptions = []string{"CreateNamespace=true", "ServerSideApply=true"}
	return a
}

func argoCD(o Options) (string, error) {
	docs := []any{newApp(o, "resource-optimizer-agent", o.AgentChart, "agent", agentVals(o))}
	if o.Webhook {
		docs = append(docs, newApp(o, "resource-optimizer-webhook", o.WebhookChart, "webhook", map[string]string{"clusterName": o.Cluster}))
	}
	body, err := marshal(docs...)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# Argo CD Applications for cluster %s. Create the Secret %s (key %s) in namespace %s first;\n# the token is not in this manifest.\n",
		o.Cluster, hubapi.AgentTokenSecretName, hubapi.AgentTokenSecretKey, o.Namespace) + body, nil
}
