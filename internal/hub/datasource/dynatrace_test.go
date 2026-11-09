package datasource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// mountSecret writes a mounted datasource Secret <dir>/<name>/token.
func mountSecret(t *testing.T, dir, name, token string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "token"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDynatraceDatasourceValidation(t *testing.T) {
	dir := t.TempDir()
	mountSecret(t, dir, "dynatrace", "dt0s16.ONE.secret")
	dt := func(auth *hubapi.DatasourceAuthInput, ag string) hubapi.DatasourceInput {
		in := hubapi.DatasourceInput{Name: "dt", Type: hubapi.DatasourceDynatrace, URL: "https://abc12345.apps.dynatrace.com", Auth: auth, Reason: "test"}
		if ag != "" {
			in.Dynatrace = &hubapi.DynatraceSettings{ActiveGateURL: ag}
		}
		return in
	}
	for _, tc := range []struct {
		name string
		in   hubapi.DatasourceInput
		err  string
	}{
		{"mounted secret", dt(&hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, TokenSecret: "dynatrace"}, "https://activegate.example.internal:9999"), ""},
		{"pasted token", dt(&hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, Token: "dt0s16.TWO.secret"}, ""), ""},
		{"no auth", dt(nil, ""), "needs auth.type bearer"},
		{"basic auth", dt(&hubapi.DatasourceAuthInput{Type: hubapi.AuthBasic, Username: "u", Password: "p"}, ""), "needs auth.type bearer"},
		{"token and secret", dt(&hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, Token: "x", TokenSecret: "dynatrace"}, ""), "not both"},
		{"secret not mounted", dt(&hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, TokenSecret: "other"}, ""), "onboarding.datasourceSecrets"},
		{"secret name", dt(&hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, TokenSecret: "../key"}, ""), "DNS label"},
		{"activegate url", dt(&hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, TokenSecret: "dynatrace"}, "activegate:9999"), "activeGateURL"},
		{"dynatrace settings on prometheus", hubapi.DatasourceInput{Name: "p", Type: hubapi.DatasourcePrometheus, URL: "http://prometheus:9090", Reason: "test",
			Dynatrace: &hubapi.DynatraceSettings{ActiveGateURL: "https://ag.example.internal"}}, "only for a dynatrace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newRegistry(t, newMemRepo())
			reg.SecretsDir = dir
			d, err := reg.Create(context.Background(), tc.in, hubapi.Actor{Type: "user", Name: "admin"})
			if tc.err != "" {
				var ve *ValidationError
				if !errors.As(err, &ve) || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Type != hubapi.DatasourceDynatrace || d.Auth.Type != hubapi.AuthBearer {
				t.Errorf("datasource = %+v", d)
			}
			if tc.in.Auth.TokenSecret != "" && (d.Auth.TokenSecret != "dynatrace" || d.Auth.SecretConfigured) {
				t.Errorf("mounted secret: auth = %+v (nothing stored)", d.Auth)
			}
			if tc.in.Auth.Token != "" && !d.Auth.SecretConfigured {
				t.Errorf("pasted token not stored: %+v", d.Auth)
			}
			if tc.in.Dynatrace != nil && (d.Dynatrace == nil || d.Dynatrace.ActiveGateURL != "https://activegate.example.internal:9999") {
				t.Errorf("dynatrace settings = %+v", d.Dynatrace)
			}
		})
	}
}

// TestMountedTokenRotation: the client of a datasource with a mounted Secret uses the file's
// token and is rebuilt when the Secret changes; the token never shows in the API or String.
func TestMountedTokenRotation(t *testing.T) {
	dir := t.TempDir()
	mountSecret(t, dir, "dynatrace", "dt0s16.ONE.secret")
	reg := newRegistry(t, newMemRepo())
	reg.SecretsDir = dir
	d, err := reg.Create(context.Background(), hubapi.DatasourceInput{Name: "dt", Type: hubapi.DatasourceDynatrace, URL: "https://abc12345.apps.dynatrace.com",
		Auth: &hubapi.DatasourceAuthInput{Type: hubapi.AuthBearer, TokenSecret: "dynatrace"}, Reason: "test"}, hubapi.Actor{Type: "user", Name: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, hc1, err := reg.Client(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secrets.Token != "dt0s16.ONE.secret" || strings.Contains(cfg.String(), "secret") {
		t.Errorf("config token %q, String %q", cfg.Secrets.Token, cfg.String())
	}
	if _, hc2, _ := reg.Client(context.Background(), d.ID); hc2 != hc1 {
		t.Error("client rebuilt without a change")
	}
	mountSecret(t, dir, "dynatrace", "dt0s16.TWO.secret")
	cfg, hc3, err := reg.Client(context.Background(), d.ID)
	if err != nil || hc3 == hc1 || cfg.Secrets.Token != "dt0s16.TWO.secret" {
		t.Errorf("rotation: err %v, rebuilt %v, token %q", err, hc3 != hc1, cfg.Secrets.Token)
	}
	// keep the settings on an update that omits auth and dynatrace
	_, after, err := reg.Update(context.Background(), d.ID, d.Version, hubapi.DatasourceInput{Name: "dt", Type: hubapi.DatasourceDynatrace,
		URL: "https://abc12345.apps.dynatrace.com", Reason: "rename"}, hubapi.Actor{Type: "user", Name: "admin"})
	if err != nil || after.Auth.TokenSecret != "dynatrace" {
		t.Errorf("update: %v %+v", err, after.Auth)
	}
}
