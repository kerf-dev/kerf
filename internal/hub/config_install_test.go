package hub

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kerf-dev/kerf/internal/hub/auth"
)

// TestInstallConfigFromCharts: the hub config the charts render (EKS example, dev values) loads
// strictly and yields the umbrella chart for the wizard's install snippets. Needs helm.
func TestInstallConfigFromCharts(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("INSTALL_TEST_REQUIRE_HELM") != "" {
			t.Fatal("helm not found")
		}
		t.Skip("helm not found")
	}
	const root = "../../deploy"
	for name, tc := range map[string]struct {
		args                  []string
		chart, version, store string
		saml                  *auth.SAMLConfig // the SAML files the chart sets
	}{
		"eks example": {
			args:  []string{filepath.Join(root, "charts/kerf"), "-f", filepath.Join(root, "examples/eks/values-hub.yaml"), "--show-only", "charts/hub/templates/configmap.yaml"},
			chart: "oci://123456789012.dkr.ecr.eu-west-1.amazonaws.com/kerf/charts/kerf", version: "0.1.0", store: "aws-secrets-manager",
		},
		"dev": {
			args:  []string{filepath.Join(root, "charts/hub"), "-f", filepath.Join(root, "charts/hub/values-dev.yaml"), "--show-only", "templates/configmap.yaml"},
			chart: "deploy/charts/kerf", version: "",
		},
		"saml from secrets": {
			args: []string{filepath.Join(root, "charts/hub"), "-f", filepath.Join(root, "charts/hub/values-dev.yaml"), "--show-only", "templates/configmap.yaml",
				"--set", "auth.saml.idpMetadataURL=", "--set", "auth.saml.idpMetadataSecret.existingSecret=kerf-saml-idp",
				"--set", "auth.saml.spKeySecret.existingSecret=kerf-saml-sp"},
			chart: "deploy/charts/kerf", version: "",
			saml: &auth.SAMLConfig{IdPMetadataFile: "/etc/hub/saml-idp/metadata.xml", SPKeyFile: "/etc/hub/saml-sp/tls.key", SPCertFile: "/etc/hub/saml-sp/tls.crt"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(helm, append([]string{"template", "kerf"}, tc.args...)...)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, stderr.String())
			}
			var cm struct {
				Data map[string]string `yaml:"data"`
			}
			if err := yaml.Unmarshal(out, &cm); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(t.TempDir(), "hub.yaml")
			if err := os.WriteFile(p, []byte(cm.Data["hub.yaml"]), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HUB_DATABASE_URL", "postgres://test@db/test")
			cfg, err := LoadConfig(p)
			if err != nil {
				t.Fatal(err)
			}
			o := cfg.Onboarding
			if o.InstallChart() != tc.chart || o.InstallChartVersion() != tc.version {
				t.Errorf("chart %q version %q", o.InstallChart(), o.InstallChartVersion())
			}
			store := ""
			if o.AgentTokenExternalSecret != nil {
				store = o.AgentTokenExternalSecret.SecretStoreRef.Name
			}
			if store != tc.store {
				t.Errorf("token store %q", store)
			}
			if tc.saml != nil {
				sc := cfg.Auth.SAML
				if !sc.Enabled || sc.IdPMetadataURL != "" || sc.IdPMetadataFile != tc.saml.IdPMetadataFile || sc.SPKeyFile != tc.saml.SPKeyFile ||
					sc.SPCertFile != tc.saml.SPCertFile {
					t.Errorf("saml config %+v", sc)
				}
			}
		})
	}
}
