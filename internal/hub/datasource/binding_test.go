package datasource

import (
	"strings"
	"testing"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

func TestNewMatcher(t *testing.T) {
	for _, tc := range []struct {
		name     string
		b        *hubapi.MetricsBinding
		sel      string
		relabels bool
		def      bool
		err      string
	}{
		{name: "nil", sel: `{cluster="ro-spoke-2", x="1"}`, def: true},
		{name: "default", b: &hubapi.MetricsBinding{ClusterLabel: "cluster"}, sel: `{cluster="ro-spoke-2", x="1"}`, def: true},
		{name: "same value", b: &hubapi.MetricsBinding{ClusterLabel: "cluster", ClusterLabelValue: "ro-spoke-2"}, sel: `{cluster="ro-spoke-2", x="1"}`, def: true},
		{name: "renamed", b: &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod"}, sel: `{k8s_cluster="prod", x="1"}`, relabels: true},
		{name: "renamed key, default value", b: &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster"}, sel: `{k8s_cluster="ro-spoke-2", x="1"}`, relabels: true},
		{name: "other value", b: &hubapi.MetricsBinding{ClusterLabel: "cluster", ClusterLabelValue: "spoke2"}, sel: `{cluster="spoke2", x="1"}`, relabels: true},
		{name: "no label", b: &hubapi.MetricsBinding{}, sel: `{x="1"}`, relabels: true},
		{name: "extra", b: &hubapi.MetricsBinding{ClusterLabel: "cluster", ExtraMatchers: []string{` env = "prod" `, `team=~"a|b"`}}, sel: `{cluster="ro-spoke-2", env="prod", team=~"a|b", x="1"}`},
		{name: "escaped", b: &hubapi.MetricsBinding{ClusterLabel: "cluster", ExtraMatchers: []string{`path="a\"b"`}}, sel: `{cluster="ro-spoke-2", path="a\"b", x="1"}`},
		{name: "bad label", b: &hubapi.MetricsBinding{ClusterLabel: "k8s-cluster"}, err: "not a valid label name"},
		{name: "value without label", b: &hubapi.MetricsBinding{ClusterLabelValue: "x"}, err: "needs a clusterLabel"},
		{name: "bad matcher", b: &hubapi.MetricsBinding{ClusterLabel: "cluster", ExtraMatchers: []string{`env=prod`}}, err: "want <label><op>"},
		{name: "injection", b: &hubapi.MetricsBinding{ClusterLabel: "cluster", ExtraMatchers: []string{`env="a"} or vector(1) or up{x="`}}, err: "want <label><op>"},
		{name: "bad regex", b: &hubapi.MetricsBinding{ClusterLabel: "cluster", ExtraMatchers: []string{`env=~"(("`}}, err: "invalid regex"},
		{name: "matcher on cluster label", b: &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ExtraMatchers: []string{`k8s_cluster="a"`}}, err: "use clusterLabel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewMatcher("ro-spoke-2", tc.b)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := m.Sel(`x="1"`); got != tc.sel {
				t.Errorf("Sel = %s, want %s", got, tc.sel)
			}
			if m.Relabels() != tc.relabels {
				t.Errorf("Relabels = %v", m.Relabels())
			}
			if m.IsDefault() != tc.def {
				t.Errorf("IsDefault = %v", m.IsDefault())
			}
			const e = `rate(x[5m])`
			want := e
			if tc.relabels {
				want = `label_replace(rate(x[5m]), "cluster", "ro-spoke-2", "", "")`
			}
			if got := m.Relabel(e); got != want {
				t.Errorf("Relabel = %s", got)
			}
		})
	}
}

func TestSelEmpty(t *testing.T) {
	m, _ := NewMatcher("a", &hubapi.MetricsBinding{})
	if s := m.Sel(); s != "" {
		t.Errorf("Sel() = %q, want empty", s)
	}
}

func TestIsDefaultBinding(t *testing.T) {
	if !IsDefaultBinding("a", hubapi.DefaultMetricsBinding("a")) {
		t.Error("DefaultMetricsBinding is not default")
	}
	if !IsDefaultBinding("a", hubapi.MetricsBinding{ClusterLabel: "cluster"}) {
		t.Error("empty datasource/value is default")
	}
	for _, b := range []hubapi.MetricsBinding{
		{DatasourceID: "x", ClusterLabel: "cluster"},
		{ClusterLabel: "k8s_cluster"},
		{ClusterLabel: "cluster", ClusterLabelValue: "b"},
		{ClusterLabel: "cluster", ExtraMatchers: []string{`a="b"`}},
		{ClusterLabel: "cluster", JobNames: &hubapi.MetricsJobNames{CAdvisor: []string{"k"}}},
	} {
		if IsDefaultBinding("a", b) {
			t.Errorf("%+v is not the default", b)
		}
	}
}
