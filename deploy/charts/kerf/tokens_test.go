package umbrella

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Values every umbrella render needs (make/charts.mk UMBRELLA_REQUIRED).
var required = []string{
	"--set-string", "global.imageRegistry=123456789012.dkr.ecr.eu-west-1.amazonaws.com",
	"--set-string", "global.clusterName=lint",
	"--set-string", "hub.database.existingSecret=lint-db",
}

// TestTokenSources renders each mode with the defaults, with ExternalSecrets and with your own Secrets, and
// checks where pricing and the agent get their hub token, the Secret the hub generates for them, whether the
// bootstrap admin must change its password, and where pricing reads the node series. Needs helm; make
// charts-lint runs it with UMBRELLA_TEST_REQUIRE_HELM=1.
func TestTokenSources(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("UMBRELLA_TEST_REQUIRE_HELM") != "" {
			t.Fatal("helm not found")
		}
		t.Skip("helm not found")
	}
	eso := []string{
		"--set", "global.externalSecrets.secretStoreRef.kind=ClusterSecretStore",
		"--set", "global.externalSecrets.secretStoreRef.name=aws",
		"--set", "agent.hub.tokenSecret.externalSecret.enabled=true",
		"--set", "agent.hub.tokenSecret.externalSecret.remoteRef.key=kerf/clusters/lint/agent-token",
		"--set", "pricing.hub.tokenSecret.externalSecret.enabled=true",
		"--set", "pricing.hub.tokenSecret.externalSecret.remoteRef.key=kerf/hub/pricing-token",
		"--set", "hub.auth.bootstrapAdmin.externalSecret.enabled=true",
		"--set", "hub.auth.bootstrapAdmin.externalSecret.remoteRef.key=kerf/hub/bootstrap-admin",
	}
	own := []string{
		"--set", "agent.hub.tokenSecret.name=my-agent-token",
		"--set", "pricing.hub.tokenSecret.name=my-pricing-token",
		"--set", "hub.auth.bootstrapAdmin.existingSecret=my-admin",
		"--set", "pricing.vmURL=http://vmsingle.monitoring.svc:8428",
	}
	type want struct {
		localKeys      []string // keys of Secret hub-local-tokens; nil: no such Secret
		localCluster   string   // hub config localTokens.cluster
		agentSecret    string   // the agent's token volume: secret/key
		pricingSecret  string   // pricing's token volume: secret/key
		externalSecret []string // ExternalSecrets rendered
		admin          string   // Secret/key of the bootstrap admin ("" = none)
		mustChange     bool
		vmURL          string // pricing --vm-url
	}
	for _, tc := range []struct {
		name string
		args []string
		want want
	}{
		{"all, defaults", []string{"--set", "global.mode=all"}, want{
			localKeys: []string{"agent", "pricing"}, localCluster: "lint",
			agentSecret: "hub-local-tokens/agent", pricingSecret: "hub-local-tokens/pricing",
			admin: "hub-bootstrap-admin/password", mustChange: true, vmURL: "",
		}},
		{"all, ExternalSecrets", append([]string{"--set", "global.mode=all"}, eso...), want{
			localKeys: []string{"agent", "pricing"}, localCluster: "lint",
			agentSecret: "kerf-agent-token/token", pricingSecret: "pricing-hub-token/token",
			externalSecret: []string{"hub-bootstrap-admin", "kerf-agent-token", "pricing-hub-token"},
			admin:          "hub-bootstrap-admin/password", vmURL: "",
		}},
		{"all, own Secrets", append([]string{"--set", "global.mode=all"}, own...), want{
			localKeys: []string{"agent", "pricing"}, localCluster: "lint",
			agentSecret: "my-agent-token/token", pricingSecret: "my-pricing-token/token",
			admin: "my-admin/password", vmURL: "http://vmsingle.monitoring.svc:8428",
		}},
		{"all, the agent's default name set explicitly", []string{"--set", "global.mode=all", "--set", "agent.hub.tokenSecret.name=kerf-agent-token"}, want{
			localKeys: []string{"agent", "pricing"}, localCluster: "lint",
			agentSecret: "hub-local-tokens/agent", pricingSecret: "hub-local-tokens/pricing",
			admin: "hub-bootstrap-admin/password", mustChange: true,
		}},
		{"all, no local tokens", []string{"--set", "global.mode=all", "--set", "hub.localTokens.enabled=false", "--set", "agent.hub.tokenSecret.name=a", "--set", "pricing.hub.tokenSecret.name=p"}, want{
			agentSecret: "a/token", pricingSecret: "p/token", admin: "hub-bootstrap-admin/password", mustChange: true,
		}},
		{"hub, defaults", []string{"--set", "global.mode=hub"}, want{
			localKeys: []string{"pricing"}, pricingSecret: "hub-local-tokens/pricing",
			admin: "hub-bootstrap-admin/password", mustChange: true,
		}},
		{"hub, ExternalSecrets", append([]string{"--set", "global.mode=hub"}, eso...), want{
			localKeys: []string{"pricing"}, pricingSecret: "pricing-hub-token/token",
			externalSecret: []string{"hub-bootstrap-admin", "pricing-hub-token"}, admin: "hub-bootstrap-admin/password",
		}},
		{"hub, own Secrets", append([]string{"--set", "global.mode=hub"}, own...), want{
			localKeys: []string{"pricing"}, pricingSecret: "my-pricing-token/token", admin: "my-admin/password",
			vmURL: "http://vmsingle.monitoring.svc:8428",
		}},
		{"spoke, defaults", []string{"--set", "global.mode=spoke", "--set", "agent.hub.url=https://hub.example.com"}, want{
			agentSecret: "kerf-agent-token/token",
		}},
		{"spoke, ExternalSecret", append([]string{"--set", "global.mode=spoke", "--set", "agent.hub.url=https://hub.example.com"}, eso...), want{
			agentSecret: "kerf-agent-token/token", externalSecret: []string{"kerf-agent-token"},
		}},
		{"spoke, own Secret", append([]string{"--set", "global.mode=spoke", "--set", "agent.hub.url=https://hub.example.com"}, own...), want{
			agentSecret: "my-agent-token/token",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"template", "kerf", ".", "-n", "kerf"}, append(slices.Clone(required), tc.args...)...)
			out, err := exec.Command(helm, args...).Output()
			if err != nil {
				var stderr string
				if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
					stderr = string(ee.Stderr)
				}
				t.Fatalf("helm template: %v\n%s", err, stderr)
			}
			objs := parseDocs(t, string(out))
			var got want
			if s := find(objs, "Secret", "hub-local-tokens"); s != nil {
				got.localKeys = sortedKeys(s["data"])
			}
			if hub := find(objs, "ConfigMap", "hub-config"); hub != nil {
				var cfg map[string]any
				if err := yaml.Unmarshal([]byte(str(dig(hub, "data", "hub.yaml"))), &cfg); err != nil {
					t.Fatal(err)
				}
				got.localCluster = str(dig(cfg, "localTokens", "cluster"))
				got.mustChange, _ = dig(cfg, "auth", "bootstrapAdmin", "mustChangePassword").(bool)
				if dig(cfg, "auth", "bootstrapAdmin") != nil {
					got.admin = volumeSecret(find(objs, "Deployment", "hub"), "bootstrap-admin", false)
				}
			}
			got.agentSecret = volumeSecret(find(objs, "Deployment", "kerf-agent"), "hub-token", true)
			got.pricingSecret = volumeSecret(find(objs, "Deployment", "pricing"), "hub-token", true)
			if p := find(objs, "Deployment", "pricing"); p != nil {
				for _, a := range dig(p, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["args"].([]any) {
					if v, ok := strings.CutPrefix(a.(string), "--vm-url="); ok {
						got.vmURL = v
					}
				}
			}
			for _, o := range objs {
				if o["kind"] == "ExternalSecret" {
					got.externalSecret = append(got.externalSecret, str(dig(o, "metadata", "name")))
				}
			}
			slices.Sort(got.externalSecret)
			if !slices.Equal(got.localKeys, tc.want.localKeys) || got.localCluster != tc.want.localCluster ||
				got.agentSecret != tc.want.agentSecret || got.pricingSecret != tc.want.pricingSecret ||
				!slices.Equal(got.externalSecret, tc.want.externalSecret) || got.admin != tc.want.admin ||
				got.mustChange != tc.want.mustChange || got.vmURL != tc.want.vmURL {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func parseDocs(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, doc := range strings.Split(s, "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatalf("parse: %v\n%s", err, doc)
		}
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}

func find(objs []map[string]any, kind, name string) map[string]any {
	for _, o := range objs {
		if o["kind"] == kind && dig(o, "metadata", "name") == name {
			return o
		}
	}
	return nil
}

func dig(m any, path ...string) any {
	for _, p := range path {
		mm, ok := m.(map[string]any)
		if !ok {
			return nil
		}
		m = mm[p]
	}
	return m
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func sortedKeys(v any) []string {
	m, _ := v.(map[string]any)
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// volumeSecret: "<secret>/<first item's key>" of volume name of deployment d ("" = none). optional: the
// volume must be optional (a missing token is reported by the pod, not by ContainerCreating).
func volumeSecret(d map[string]any, name string, optional bool) string {
	if d == nil {
		return ""
	}
	vols, _ := dig(d, "spec", "template", "spec", "volumes").([]any)
	for _, v := range vols {
		vm := v.(map[string]any)
		if vm["name"] != name {
			continue
		}
		sec, _ := vm["secret"].(map[string]any)
		if opt, _ := sec["optional"].(bool); opt != optional {
			return "optional != " + map[bool]string{true: "true", false: "false"}[optional]
		}
		out := str(sec["secretName"])
		if items, ok := sec["items"].([]any); ok && len(items) > 0 {
			return out + "/" + str(items[0].(map[string]any)["key"])
		}
		// The agent mounts the whole Secret and reads the file named after the key (--hub-token-file).
		for _, c := range dig(d, "spec", "template", "spec", "containers").([]any) {
			for _, a := range c.(map[string]any)["args"].([]any) {
				if f, ok := strings.CutPrefix(a.(string), "--hub-token-file=/var/run/secrets/kerf/hub/"); ok {
					return out + "/" + f
				}
			}
		}
		return out
	}
	return ""
}
