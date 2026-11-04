package recommender

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"
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
	avg, count := q.Pricing("ro_cpu_core_hourly_cost_usd", time.Hour)
	add("Pricing/avg", avg)
	add("Pricing/count", count)
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
// byte-identical to the queries before D-025.
func TestQueriesDefaultGolden(t *testing.T) {
	golden(t, "testdata/queries-default.golden", allQueries(Queries{Cluster: "ro-spoke-1", RateWindow: 5 * time.Minute}))
}
