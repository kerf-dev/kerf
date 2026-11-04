package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

func opts() Options {
	return Options{
		Cluster: "ro-spoke-2", HubURL: "http://hub-api.hub.svc:8080",
		AgentChart: "oci://ghcr.io/tomer1983/resource-optimizer/charts/agent", WebhookChart: "oci://ghcr.io/tomer1983/resource-optimizer/charts/webhook",
		ChartVersion: "0.1.0",
	}
}

const secretToken = "ro-agent-SECRET-never-shown"

func TestRenderHelm(t *testing.T) {
	o := opts()
	o.Webhook, o.CertManager, o.ReadOnly, o.ImageTag = true, true, true, "main-ece1564"
	s, err := Render(hubapi.InstallHelm, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`: "${RO_AGENT_TOKEN:?`,
		"create secret generic resource-optimizer-agent-token --from-literal=token=\"$RO_AGENT_TOKEN\"",
		"helm upgrade --install agent oci://ghcr.io/tomer1983/resource-optimizer/charts/agent --version 0.1.0 -n resource-optimizer",
		"--set-string clusterName=ro-spoke-2", "--set-string hub.url=http://hub-api.hub.svc:8080", "--set readOnly=true",
		"--set-string image.tag=main-ece1564", "helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager --version v1.19.1",
		"helm upgrade --install webhook oci://ghcr.io/tomer1983/resource-optimizer/charts/webhook --version 0.1.0",
	} {
		if !strings.Contains(s.Content, want) {
			t.Errorf("helm snippet lacks %q:\n%s", want, s.Content)
		}
	}
	if s.Filename != "install.sh" || s.ArgoCDIgnoreDifferences != "resource.customizations.ignoreDifferences.all: |\n  managedFieldsManagers:\n  - resource-optimizer\n" {
		t.Errorf("snippet %+v", s)
	}
	if !strings.Contains(strings.Join(s.Notes, " "), "readOnly") {
		t.Errorf("notes %v", s.Notes)
	}
	// valid shell
	if sh, err := exec.LookPath("sh"); err == nil {
		p := filepath.Join(t.TempDir(), "install.sh")
		_ = os.WriteFile(p, []byte(s.Content), 0o600)
		if out, err := exec.Command(sh, "-n", p).CombinedOutput(); err != nil {
			t.Errorf("sh -n: %v %s", err, out)
		}
	}
}

func TestRenderValues(t *testing.T) {
	s, err := Render(hubapi.InstallValues, opts())
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ClusterName string `yaml:"clusterName"`
		Hub         struct {
			URL         string `yaml:"url"`
			TokenSecret struct {
				Name, Key string
				Create    bool
			} `yaml:"tokenSecret"`
		} `yaml:"hub"`
		ReadOnly bool `yaml:"readOnly"`
		Image    any  `yaml:"image"`
	}
	if err := yaml.Unmarshal([]byte(s.Content), &v); err != nil {
		t.Fatal(err)
	}
	if v.ClusterName != "ro-spoke-2" || v.Hub.URL != "http://hub-api.hub.svc:8080" || v.Hub.TokenSecret.Name != hubapi.AgentTokenSecretName ||
		v.Hub.TokenSecret.Key != "token" || v.Hub.TokenSecret.Create || v.ReadOnly || v.Image != nil {
		t.Errorf("values %+v", v)
	}
	if s.Filename != "values-ro-spoke-2.yaml" || !strings.Contains(strings.Join(s.Notes, "\n"), "-f values-ro-spoke-2.yaml") {
		t.Errorf("snippet %+v", s)
	}
}

func TestRenderArgoCD(t *testing.T) {
	o := opts()
	o.Webhook = true
	o.AgentChart = "https://charts.example.com|agent"
	s, err := Render(hubapi.InstallArgoCD, o)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(strings.NewReader(s.Content))
	var apps []map[string]any
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}
		apps = append(apps, m)
	}
	if len(apps) != 2 {
		t.Fatalf("%d documents:\n%s", len(apps), s.Content)
	}
	src := apps[0]["spec"].(map[string]any)["source"].(map[string]any)
	if apps[0]["kind"] != "Application" || src["repoURL"] != "https://charts.example.com" || src["chart"] != "agent" || src["targetRevision"] != "0.1.0" {
		t.Errorf("agent app %v", src)
	}
	vals := src["helm"].(map[string]any)["valuesObject"].(map[string]any)
	if vals["clusterName"] != "ro-spoke-2" {
		t.Errorf("values %v", vals)
	}
	wsrc := apps[1]["spec"].(map[string]any)["source"].(map[string]any)
	if wsrc["repoURL"] != "ghcr.io/tomer1983/resource-optimizer/charts" || wsrc["chart"] != "webhook" {
		t.Errorf("webhook app %v", wsrc)
	}
	dst := apps[0]["spec"].(map[string]any)["destination"].(map[string]any)
	if dst["namespace"] != "resource-optimizer" || apps[0]["metadata"].(map[string]any)["namespace"] != "argocd" {
		t.Errorf("destination %v", dst)
	}
}

func TestRenderNeverContainsToken(t *testing.T) {
	t.Setenv(TokenEnv, secretToken)
	for _, f := range []string{hubapi.InstallHelm, hubapi.InstallValues, hubapi.InstallArgoCD} {
		s, err := Render(f, opts())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(s.Content+strings.Join(s.Notes, ""), secretToken) {
			t.Errorf("%s contains the token", f)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	for name, mut := range map[string]func(*Options){
		"cluster":   func(o *Options) { o.Cluster = "Bad" },
		"hub":       func(o *Options) { o.HubURL = "hub:8080" },
		"chart":     func(o *Options) { o.AgentChart = "" },
		"webhook":   func(o *Options) { o.Webhook, o.WebhookChart = true, "" },
		"injection": func(o *Options) { o.ChartVersion = "1; rm -rf /" },
		"namespace": func(o *Options) { o.Namespace = "a_b" },
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
	if q := shellQuote("a'b c"); q != `'a'"'"'b c'` {
		t.Errorf("quote %s", q)
	}
}
