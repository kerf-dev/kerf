package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

const (
	testChart    = "oci://123456789012.dkr.ecr.eu-west-1.amazonaws.com/resource-optimizer/charts/resource-optimizer"
	testRegistry = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
	umbrellaDir  = "../../../deploy/charts/resource-optimizer"
	spokeExample = "../../../deploy/examples/eks/values-spoke.yaml"
)

func opts() Options {
	return Options{Cluster: "ro-spoke-2", HubURL: "http://hub-api.hub.svc:8080", Chart: testChart, ChartVersion: "0.1.0", Webhook: true}
}

func esOpts() Options {
	o := opts()
	o.TokenExternalSecret = &TokenExternalSecret{SecretStoreName: "aws-secrets-manager"}
	return o
}

const secretToken = "ro-agent-SECRET-never-shown"

func render(t *testing.T, format string, o Options) hubapi.InstallSnippet {
	t.Helper()
	s, err := Render(format, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// heredocValues returns the values the Helm script passes to `helm -f -`.
func heredocValues(t *testing.T, script string) map[string]any {
	t.Helper()
	_, rest, ok := strings.Cut(script, "<<'"+valuesDelimiter+"'\n")
	body, _, ok2 := strings.Cut(rest, "\n"+valuesDelimiter+"\n")
	if !ok || !ok2 {
		t.Fatalf("no values heredoc:\n%s", script)
	}
	return parse(t, body)
}

func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%v:\n%s", err, s)
	}
	return m
}

// get walks a dotted path through nested maps.
func get(m map[string]any, p string) any {
	var cur any = m
	for _, k := range strings.Split(p, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = mm[k]; !ok {
			return nil
		}
	}
	return cur
}

// wantValues: what every snippet sets for opts() (docs/install/eks.md step 5.3).
var wantValues = map[string]any{
	"global.mode":                          "spoke",
	"global.clusterName":                   "ro-spoke-2",
	"global.imageRegistry":                 testRegistry,
	"agent.hub.url":                        "http://hub-api.hub.svc:8080",
	"agent.hub.tokenSecret.name":           hubapi.AgentTokenSecretName,
	"agent.hub.tokenSecret.key":            "token",
	"agent.hub.tokenSecret.create":         false,
	"agent.replicaCount":                   2,
	"agent.leaderElection":                 true,
	"agent.readOnly":                       false,
	"webhook":                              nil, // part of mode spoke
	"global.externalSecrets":               nil,
	"agent.hub.tokenSecret.externalSecret": nil,
}

func checkValues(t *testing.T, what string, got map[string]any, want map[string]any) {
	t.Helper()
	for p, w := range want {
		if g := get(got, p); !reflect.DeepEqual(g, w) {
			t.Errorf("%s: %s = %#v, want %#v", what, p, g, w)
		}
	}
}

func merge(base map[string]any, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func TestRenderHelm(t *testing.T) {
	for name, tc := range map[string]struct {
		mut      func(*Options)
		contains []string
		absent   []string
		values   map[string]any
	}{
		"existing secret": {
			mut: func(*Options) {},
			contains: []string{
				`: "${RO_AGENT_TOKEN:?`,
				"kubectl create namespace resource-optimizer --dry-run=client",
				`create secret generic resource-optimizer-agent-token --from-literal=token="$RO_AGENT_TOKEN"`,
				"helm upgrade --install resource-optimizer " + testChart + " --version 0.1.0 \\\n  -n resource-optimizer --create-namespace --wait --timeout 10m -f - <<'RO_VALUES'\n",
			},
			absent: []string{"cert-manager", "helm upgrade --install agent", "helm upgrade --install webhook"},
			values: wantValues,
		},
		"external secret, read-only, image, no webhook": {
			mut: func(o *Options) {
				*o = esOpts()
				o.ReadOnly, o.ImageTag, o.Webhook = true, "main-ece1564", false
			},
			contains: []string{"store the token shown once", "as resource-optimizer/clusters/ro-spoke-2/agent-token, property token"},
			absent:   []string{"RO_AGENT_TOKEN", "kubectl create", "kubectl -n", "cert-manager"},
			values: merge(wantValues, map[string]any{
				"global.externalSecrets.secretStoreRef": map[string]any{"kind": "ClusterSecretStore", "name": "aws-secrets-manager"},
				"global.externalSecrets":                map[string]any{"secretStoreRef": map[string]any{"kind": "ClusterSecretStore", "name": "aws-secrets-manager"}},
				"agent.hub.tokenSecret.externalSecret":  map[string]any{"enabled": true, "remoteRef": map[string]any{"key": "resource-optimizer/clusters/ro-spoke-2/agent-token", "property": "token"}},
				"agent.readOnly":                        true,
				"agent.image":                           map[string]any{"tag": "main-ece1564"},
				"webhook":                               map[string]any{"enabled": false},
			}),
		},
		"cert-manager, health, repo chart, explicit registry": {
			mut: func(o *Options) {
				o.CertManager = true
				o.Chart, o.ImageRegistry = "https://charts.example.com|resource-optimizer", "registry.example.com"
				o.Health = &HealthMetrics{MetricsURL: "http://prom:9090", ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod", ExtraMatchers: []string{`env="prod"`}}
			},
			contains: []string{
				"helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager --version v1.19.1 -n cert-manager",
				"helm upgrade --install resource-optimizer resource-optimizer --repo https://charts.example.com --version 0.1.0",
			},
			values: merge(wantValues, map[string]any{
				"global.imageRegistry": "registry.example.com",
				"agent.health":         map[string]any{"metricsURL": "http://prom:9090", "clusterLabel": "k8s_cluster", "clusterLabelValue": "prod", "extraMatchers": []any{`env="prod"`}},
			}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := opts()
			tc.mut(&o)
			s := render(t, hubapi.InstallHelm, o)
			for _, w := range tc.contains {
				if !strings.Contains(s.Content, w) {
					t.Errorf("lacks %q:\n%s", w, s.Content)
				}
			}
			for _, w := range tc.absent {
				if strings.Contains(s.Content, w) {
					t.Errorf("contains %q:\n%s", w, s.Content)
				}
			}
			if n := strings.Count(s.Content, "helm upgrade --install resource-optimizer "); n != 1 {
				t.Errorf("%d umbrella installs", n)
			}
			checkValues(t, "heredoc", heredocValues(t, s.Content), tc.values)
			if s.Filename != "install.sh" || s.ArgoCDIgnoreDifferences != "resource.customizations.ignoreDifferences.all: |\n  managedFieldsManagers:\n  - resource-optimizer\n" {
				t.Errorf("snippet %+v", s)
			}
			if sh, err := exec.LookPath("sh"); err == nil {
				p := filepath.Join(t.TempDir(), "install.sh")
				_ = os.WriteFile(p, []byte(s.Content), 0o600)
				if out, err := exec.Command(sh, "-n", p).CombinedOutput(); err != nil {
					t.Errorf("sh -n: %v %s", err, out)
				}
			}
		})
	}
}

func TestRenderValues(t *testing.T) {
	s := render(t, hubapi.InstallValues, opts())
	checkValues(t, "values", parse(t, s.Content), wantValues)
	if s.Filename != "values-ro-spoke-2.yaml" {
		t.Errorf("filename %s", s.Filename)
	}
	notes := strings.Join(s.Notes, "\n")
	for _, w := range []string{
		"helm upgrade --install resource-optimizer " + testChart + " --version 0.1.0 -n resource-optimizer --create-namespace -f values-ro-spoke-2.yaml",
		"create the Secret resource-optimizer-agent-token", "values-spoke.yaml",
	} {
		if !strings.Contains(notes, w) {
			t.Errorf("notes lack %q:\n%s", w, notes)
		}
	}
	// the external secret variant says where the token goes
	s = render(t, hubapi.InstallValues, esOpts())
	if !strings.Contains(s.Content, "resource-optimizer/clusters/ro-spoke-2/agent-token") || !strings.Contains(strings.Join(s.Notes, " "), "in the secret store aws-secrets-manager") {
		t.Errorf("external secret values:\n%s\n%v", s.Content, s.Notes)
	}
}

// TestValuesMatchSpokeExample: rendered for the example's cluster, every value the wizard sets is
// the one deploy/examples/eks/values-spoke.yaml (docs/install/eks.md step 5.3) installs, or the
// chart default where the example leaves it out.
func TestValuesMatchSpokeExample(t *testing.T) {
	b, err := os.ReadFile(spokeExample)
	if err != nil {
		t.Fatal(err)
	}
	example := parse(t, string(b))
	defaults := umbrellaDefaults(t)
	o := Options{
		Cluster: "prod-eu1", HubURL: "https://hub.ro.example.com", Webhook: true,
		Chart:               "oci://123456789012.dkr.ecr.eu-west-1.amazonaws.com/resource-optimizer/charts/resource-optimizer",
		TokenExternalSecret: &TokenExternalSecret{SecretStoreName: "aws-secrets-manager"},
		Health:              &HealthMetrics{MetricsURL: "http://prometheus-operated.monitoring.svc:9090", ClusterLabel: ""},
	}
	s := render(t, hubapi.InstallValues, o)
	got := parse(t, s.Content)
	for p, v := range leaves("", got) {
		w, src := get(example, p), "example"
		if w == nil {
			w, src = get(defaults, p), "chart default"
		}
		if fmt.Sprint(w) != fmt.Sprint(v) {
			t.Errorf("%s = %v, %s %v", p, v, src, w)
		}
	}
}

// umbrellaDefaults: the umbrella's values over the agent and webhook charts' values.
func umbrellaDefaults(t *testing.T) map[string]any {
	t.Helper()
	read := func(p string) map[string]any {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return parse(t, string(b))
	}
	u := read(filepath.Join(umbrellaDir, "values.yaml"))
	for _, c := range []string{"agent", "webhook"} {
		sub := read(filepath.Join(umbrellaDir, "charts", c, "values.yaml"))
		u[c] = deepMerge(sub, u[c].(map[string]any))
	}
	return u
}

func deepMerge(base, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if bm, ok := out[k].(map[string]any); ok {
			if om, ok := v.(map[string]any); ok {
				out[k] = deepMerge(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func leaves(prefix string, m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if mm, ok := v.(map[string]any); ok && len(mm) > 0 {
			for lk, lv := range leaves(p, mm) {
				out[lk] = lv
			}
			continue
		}
		out[p] = v
	}
	return out
}

func TestRenderArgoCD(t *testing.T) {
	for name, tc := range map[string]struct {
		mut          func(*Options)
		repo, chart  string
		ignored      int
		webhookValue any
	}{
		"oci, webhook": {mut: func(*Options) {}, repo: "123456789012.dkr.ecr.eu-west-1.amazonaws.com/resource-optimizer/charts", chart: "resource-optimizer", ignored: 2},
		"repo, no webhook": {mut: func(o *Options) {
			o.Chart, o.ImageRegistry, o.Webhook = "https://charts.example.com|resource-optimizer", "r.example.com", false
		}, repo: "https://charts.example.com", chart: "resource-optimizer", ignored: 1, webhookValue: map[string]any{"enabled": false}},
	} {
		t.Run(name, func(t *testing.T) {
			o := opts()
			tc.mut(&o)
			s := render(t, hubapi.InstallArgoCD, o)
			dec := yaml.NewDecoder(strings.NewReader(s.Content))
			var docs []map[string]any
			for {
				var m map[string]any
				if err := dec.Decode(&m); err != nil {
					break
				}
				docs = append(docs, m)
			}
			if len(docs) != 1 {
				t.Fatalf("%d documents:\n%s", len(docs), s.Content)
			}
			app := docs[0]
			checkValues(t, "application", app, map[string]any{
				"kind":                                  "Application",
				"metadata.name":                         "resource-optimizer-ro-spoke-2",
				"metadata.namespace":                    "argocd",
				"metadata.labels":                       map[string]any{"app.kubernetes.io/part-of": "resource-optimizer"},
				"metadata.finalizers":                   nil, // deleting the Application keeps the CRDs
				"spec.source.repoURL":                   tc.repo,
				"spec.source.chart":                     tc.chart,
				"spec.source.targetRevision":            "0.1.0",
				"spec.source.helm.releaseName":          "resource-optimizer",
				"spec.destination.namespace":            "resource-optimizer",
				"spec.destination.server":               "https://kubernetes.default.svc",
				"spec.syncPolicy.automated":             map[string]any{"prune": true, "selfHeal": true},
				"spec.syncPolicy.syncOptions":           []any{"CreateNamespace=true", "ServerSideApply=true", "RespectIgnoreDifferences=true"},
				"spec.source.helm.valuesObject.webhook": tc.webhookValue,
			})
			vals := get(app, "spec.source.helm.valuesObject").(map[string]any)
			want := maps(wantValues, "webhook")
			if tc.webhookValue == nil {
				want["global.imageRegistry"] = testRegistry
			} else {
				want["global.imageRegistry"] = "r.example.com"
			}
			checkValues(t, "valuesObject", vals, want)
			ign := get(app, "spec.ignoreDifferences").([]any)
			if len(ign) != tc.ignored {
				t.Fatalf("ignoreDifferences %v", ign)
			}
			if !reflect.DeepEqual(ign[0], map[string]any{"group": "*", "kind": "*", "managedFieldsManagers": []any{"resource-optimizer"}}) {
				t.Errorf("ignoreDifferences[0] %v", ign[0])
			}
			if tc.ignored == 2 && !reflect.DeepEqual(ign[1], map[string]any{"group": "admissionregistration.k8s.io", "kind": "MutatingWebhookConfiguration", "jqPathExpressions": []any{".webhooks[]?.clientConfig.caBundle"}}) {
				t.Errorf("ignoreDifferences[1] %v", ign[1])
			}
			if s.Filename != "resource-optimizer-ro-spoke-2.yaml" {
				t.Errorf("filename %s", s.Filename)
			}
		})
	}
}

// maps copies m without the given keys.
func maps(m map[string]any, drop ...string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if !slices.Contains(drop, k) {
			out[k] = v
		}
	}
	return out
}

// TestSnippetsRenderWithChart: the umbrella chart accepts the rendered values (values.schema.json,
// templates) and installs the agent (two replicas) and the webhook in mode spoke. Needs helm;
// make charts-lint runs it with INSTALL_TEST_REQUIRE_HELM=1 and kubeconforms the Argo CD
// Applications it writes to INSTALL_SNIPPET_DIR.
func TestSnippetsRenderWithChart(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("INSTALL_TEST_REQUIRE_HELM") != "" {
			t.Fatal("helm not found")
		}
		t.Skip("helm not found")
	}
	for name, o := range map[string]Options{"existing secret": opts(), "external secret": esOpts()} {
		for _, format := range []string{hubapi.InstallHelm, hubapi.InstallValues, hubapi.InstallArgoCD} {
			t.Run(name+"/"+format, func(t *testing.T) {
				s := render(t, format, o)
				if dir := os.Getenv("INSTALL_SNIPPET_DIR"); dir != "" && format == hubapi.InstallArgoCD { // make charts-lint: kubeconform
					_ = os.WriteFile(filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+"-"+s.Filename), []byte(s.Content), 0o600)
				}
				var vals string
				switch format {
				case hubapi.InstallHelm:
					b, _ := yaml.Marshal(heredocValues(t, s.Content))
					vals = string(b)
				case hubapi.InstallValues:
					vals = s.Content
				default:
					b, _ := yaml.Marshal(get(parse(t, s.Content), "spec.source.helm.valuesObject"))
					vals = string(b)
				}
				cmd := exec.Command(helm, "template", ReleaseName, umbrellaDir, "-n", DefaultNamespace, "-f", "-")
				cmd.Stdin = strings.NewReader(vals)
				var stderr strings.Builder
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("helm template: %v\n%s\nvalues:\n%s", err, stderr.String(), vals)
				}
				dec := yaml.NewDecoder(strings.NewReader(string(out)))
				var deploys, externalSecrets []string
				replicas := map[string]int{}
				for {
					var d struct {
						Kind     string `yaml:"kind"`
						Metadata struct{ Name string }
						Spec     struct{ Replicas int }
					}
					if err := dec.Decode(&d); errors.Is(err, io.EOF) {
						break
					} else if err != nil {
						t.Fatal(err)
					}
					switch d.Kind {
					case "Deployment":
						deploys = append(deploys, d.Metadata.Name)
						replicas[d.Metadata.Name] = d.Spec.Replicas
					case "ExternalSecret":
						externalSecrets = append(externalSecrets, d.Metadata.Name)
					}
				}
				slices.Sort(deploys)
				if !reflect.DeepEqual(deploys, []string{"resource-optimizer-agent", "webhook"}) || replicas["resource-optimizer-agent"] != 2 {
					t.Errorf("deployments %v replicas %v", deploys, replicas)
				}
				wantES := []string(nil)
				if o.TokenExternalSecret != nil {
					wantES = []string{hubapi.AgentTokenSecretName}
				}
				if !reflect.DeepEqual(externalSecrets, wantES) {
					t.Errorf("external secrets %v, want %v", externalSecrets, wantES)
				}
				if !strings.Contains(string(out), "secretName: "+hubapi.AgentTokenSecretName) {
					t.Error("the agent does not mount the token Secret")
				}
			})
		}
	}
}

func TestRenderNeverContainsToken(t *testing.T) {
	t.Setenv(TokenEnv, secretToken)
	for _, o := range []Options{opts(), esOpts()} {
		for _, f := range []string{hubapi.InstallHelm, hubapi.InstallValues, hubapi.InstallArgoCD} {
			s := render(t, f, o)
			if strings.Contains(s.Content+strings.Join(s.Notes, ""), secretToken) {
				t.Errorf("%s contains the token", f)
			}
		}
	}
}

func TestRenderErrors(t *testing.T) {
	for name, mut := range map[string]func(*Options){
		"cluster":         func(o *Options) { o.Cluster = "Bad" },
		"hub":             func(o *Options) { o.HubURL = "hub:8080" },
		"chart":           func(o *Options) { o.Chart = "" },
		"registry":        func(o *Options) { o.Chart = "deploy/charts/resource-optimizer" },
		"injection":       func(o *Options) { o.ChartVersion = "1; rm -rf /" },
		"quote":           func(o *Options) { o.ImageRegistry = "r'example" },
		"namespace":       func(o *Options) { o.Namespace = "a_b" },
		"store":           func(o *Options) { o.TokenExternalSecret = &TokenExternalSecret{} },
		"store injection": func(o *Options) { o.TokenExternalSecret = &TokenExternalSecret{SecretStoreName: "s", Key: "a b"} },
	} {
		o := opts()
		mut(&o)
		if _, err := Render(hubapi.InstallHelm, o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Render("terraform", opts()); err == nil {
		t.Error("unknown format accepted")
	}
	// a local chart path renders with an explicit registry
	o := opts()
	o.Chart, o.ImageRegistry = "deploy/charts/resource-optimizer", "k3d-ro-registry:5005"
	if _, err := Render(hubapi.InstallHelm, o); err != nil {
		t.Errorf("local chart: %v", err)
	}
}

func TestChartRefs(t *testing.T) {
	for _, tc := range []struct{ component, umbrella, registry string }{
		{"oci://123456789012.dkr.ecr.eu-west-1.amazonaws.com/resource-optimizer/charts/agent", testChart, testRegistry},
		{"oci://localhost:5005/resource-optimizer/charts/agent", "oci://localhost:5005/resource-optimizer/charts/resource-optimizer", "localhost:5005"},
		{"https://charts.example.com|agent", "https://charts.example.com|resource-optimizer", ""},
		{"deploy/charts/agent", "deploy/charts/resource-optimizer", ""},
		{"", "", ""},
	} {
		u := UmbrellaFromComponent(tc.component)
		if u != tc.umbrella || RegistryOf(u) != tc.registry {
			t.Errorf("%q: umbrella %q registry %q, want %q %q", tc.component, u, RegistryOf(u), tc.umbrella, tc.registry)
		}
	}
}
