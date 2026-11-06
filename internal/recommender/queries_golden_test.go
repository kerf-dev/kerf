package recommender

import (
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

// allQueries renders every query the hub builds for q, labelled by method.
func allQueries(q Queries) string {
	f := &Filter{Namespace: "shop"}
	day := 24 * time.Hour
	var b strings.Builder
	add := func(name, query string) {
		b.WriteString("## " + name + "\n" + query + "\n\n")
	}
	add("PodWorkload", q.PodWorkload(nil, nil))
	add("PodWorkload/filter+lastOver", q.PodWorkload(f, lastOver(day)))
	add("CPUPerPod", q.CPUPerPod(nil))
	add("CPUPerPod/filter", q.CPUPerPod(f))
	add("MemoryPerPod", q.MemoryPerPod(nil, 5*time.Minute))
	add("CPUUsage/max", q.CPUUsage("max", nil))
	add("CPUUsage/avg/filter", q.CPUUsage("avg", f))
	add("MemoryUsage/max", q.MemoryUsage("max", nil, 5*time.Minute))
	add("MemoryUsage/avg/filter", q.MemoryUsage("avg", f, time.Hour))
	add("ThrottleRatio", q.ThrottleRatio())
	add("OOMKills", q.OOMKills(day))
	add("Restarts", q.Restarts(day))
	add("RunningPods", q.RunningPods(5*time.Minute))
	add("Resources/requests", q.Resources("requests", false, 0, nil))
	add("Resources/limits/init/lookback/filter", q.Resources("limits", true, day, f))
	add("InitContainers", q.InitContainers(day))
	add("ContainerImages", q.ContainerImages(day))
	add("HPAInfo", q.HPAInfo(day))
	add("HPAMetrics", q.HPAMetrics(day))
	add("HPAReplicaBounds/min", q.HPAReplicaBounds("min", day))
	add("HPAPeakReplicas", q.HPAPeakReplicas(7*day))
	avg, count := q.Pricing("kerf_cpu_core_hourly_cost_usd", time.Hour)
	add("Pricing/avg", avg)
	add("Pricing/count", count)
	add("PricingSources", q.PricingSources())
	add("PricingCURPeriod/start", PricingCURPeriod("start"))
	add("GPUPods", q.GPUPods())
	add("GPUUtilization", q.GPUUtilization())
	add("GPUMemoryUsed", q.GPUMemoryUsed())
	add("GPUMemoryTotal", q.GPUMemoryTotal(day))
	add("GPUPrice", q.GPUPrice(time.Hour))
	return b.String()
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs (go test -run %s -update to rewrite):\n%s", path, t.Name(), firstDiff(string(want), got))
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < max(len(al), len(bl)); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return "line " + itoa(i+1) + ":\n- " + x + "\n+ " + y
		}
	}
	return ""
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

// TestQueriesDefaultGolden pins the queries of a cluster without a metrics binding: they must stay
// byte-identical to the queries without a binding.
func TestQueriesDefaultGolden(t *testing.T) {
	golden(t, "testdata/queries-default.golden", allQueries(Queries{Cluster: "ro-spoke-1", RateWindow: 5 * time.Minute}))
}

// TestQueriesBindingEquivalent checks that a bound cluster's queries are the default queries with
// the cluster matcher swapped and every raw vector relabelled: removing the relabelling and
// swapping the matcher back gives the default queries byte for byte (pricing reads the hub's own
// store and keeps the default selector).
func TestQueriesBindingEquivalent(t *testing.T) {
	def := allQueries(Queries{Cluster: "ro-spoke-1", RateWindow: 5 * time.Minute})
	for _, tc := range []struct {
		name    string
		b       hubapi.MetricsBinding
		matcher string
	}{
		{"renamed label", hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod-eu1"}, `k8s_cluster="prod-eu1"`},
		{"other value", hubapi.MetricsBinding{ClusterLabel: "cluster", ClusterLabelValue: "spoke1"}, `cluster="spoke1"`},
		{"no label", hubapi.MetricsBinding{ClusterLabel: ""}, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := datasource.NewMatcher("ro-spoke-1", &tc.b)
			if err != nil {
				t.Fatal(err)
			}
			got := allQueries(Queries{Cluster: "ro-spoke-1", RateWindow: 5 * time.Minute, Binding: &m})
			if !strings.Contains(got, `kerf_cpu_core_hourly_cost_usd{cluster="ro-spoke-1"}`) {
				t.Error("pricing queries must keep cluster=\"ro-spoke-1\"")
			}
			back := unrelabel(t, got, "ro-spoke-1")
			if tc.matcher == "" {
				back = strings.NewReplacer("{}", "", "{", `{cluster="ro-spoke-1", `).Replace(back)
				// raw selectors without matchers had no braces at all
				back = restoreBare(back)
				back = strings.ReplaceAll(back, `cluster="ro-spoke-1", cluster="ro-spoke-1"`, `cluster="ro-spoke-1"`) // pricing
			} else {
				back = strings.ReplaceAll(back, tc.matcher, `cluster="ro-spoke-1"`)
			}
			if back != def {
				t.Errorf("not equivalent to the default queries:\n%s", firstDiff(def, back))
			}
		})
	}
}

// unrelabel removes every relabel wrapper `label_replace(X, "cluster", "<name>", "", "")`, keeping X.
func unrelabel(t *testing.T, s, cluster string) string {
	t.Helper()
	suffix := `, "cluster", "` + cluster + `", "", "")`
	for {
		i := strings.Index(s, suffix)
		if i < 0 {
			return s
		}
		depth, k := 0, i-1
		for ; k >= 0; k-- {
			if s[k] == ')' {
				depth++
			} else if s[k] == '(' {
				if depth == 0 {
					break
				}
				depth--
			}
		}
		if k < 0 || !strings.HasSuffix(s[:k], "label_replace") {
			t.Fatalf("relabel suffix without label_replace at %d", i)
		}
		open := k - len("label_replace")
		s = s[:open] + s[k+1:i] + s[i+len(suffix):]
	}
}

// restoreBare adds the cluster matcher to metric names that had no selector in a no-label binding.
func restoreBare(s string) string {
	for _, m := range []string{"kube_pod_container_status_restarts_total", "kube_pod_init_container_info", "kube_pod_container_info",
		"kube_horizontalpodautoscaler_info", "kube_horizontalpodautoscaler_spec_target_metric", "kube_horizontalpodautoscaler_spec_min_replicas",
		"kube_horizontalpodautoscaler_status_current_replicas", DCGMUtilization, DCGMFramebufUse, DCGMFramebufFre} {
		s = regexpBare(m).ReplaceAllString(s, m+`{cluster="ro-spoke-1"}$1`)
	}
	return s
}

func regexpBare(metric string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(metric) + `([\[),])`)
}

func TestQueriesBindingGolden(t *testing.T) {
	m, err := datasource.NewMatcher("ro-spoke-2", &hubapi.MetricsBinding{ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod-eu1", ExtraMatchers: []string{`env="prod"`}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "testdata/queries-binding.golden", allQueries(Queries{Cluster: "ro-spoke-2", RateWindow: 5 * time.Minute, Binding: &m}))
}
