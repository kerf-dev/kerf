package readiness

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// rawSeries is one series' raw sample timestamps, oldest first.
type rawSeries struct {
	labels recommender.Labels
	ts     []time.Time
}

// series builds one container series sampled at the given offsets before now.
func series(i int, ts []time.Time) rawSeries {
	return rawSeries{labels: recommender.Labels{"namespace": "shop", "pod": fmt.Sprintf("api-%d", i), "container": "app"}, ts: ts}
}

// samplesEvery returns timestamps every step from start to end (inclusive of start).
func samplesEvery(start, end time.Time, step time.Duration) []time.Time {
	var out []time.Time
	for t := start; !t.After(end); t = t.Add(step) {
		out = append(out, t)
	}
	return out
}

// regularSeries is n series scraped every step for the last age, with per-series phase offsets.
func regularSeries(n int, step, age time.Duration) []rawSeries {
	out := make([]rawSeries, n)
	for i := range out {
		phase := time.Duration(i) * step / time.Duration(n)
		out[i] = series(i, samplesEvery(now.Add(-age).Add(phase), now, step))
	}
	return out
}

// promLookback is Prometheus' default lookback of an instant selector.
const promLookback = 5 * time.Minute

// coverageSamples answers the two scrape-interval queries over raw samples, with Prometheus
// semantics: count_over_time over (at-30m, at]; the span query's subquery evaluates
// timestamp(m) > start at every step (aligned to the step) in (at-30m, at], where an instant
// selector returns the newest sample at or before the step within the lookback.
func coverageSamples(all []rawSeries) (spans, counts []recommender.VectorSample) {
	at := now.Truncate(scrapeStep)
	start := at.Add(-scrapeWindow)
	for _, s := range all {
		n := 0
		for _, t := range s.ts {
			if t.After(start) && !t.After(at) {
				n++
			}
		}
		if n > 0 {
			counts = append(counts, recommender.VectorSample{Labels: s.labels, Sample: recommender.Sample{V: float64(n)}})
		}
		var lo, hi time.Time
		for step := start.Add(scrapeStep); !step.After(at); step = step.Add(scrapeStep) {
			var last time.Time
			for _, t := range s.ts {
				if t.After(step) {
					break
				}
				last = t
			}
			// lookback, then the "> start" filter (timestamps are whole seconds in the query)
			if last.IsZero() || step.Sub(last) > promLookback || last.Unix() <= start.Unix() {
				continue
			}
			if lo.IsZero() || last.Before(lo) {
				lo = last
			}
			if last.After(hi) {
				hi = last
			}
		}
		if !lo.IsZero() {
			spans = append(spans, recommender.VectorSample{Labels: s.labels, Sample: recommender.Sample{V: hi.Sub(lo).Seconds()}})
		}
	}
	return spans, counts
}

func scrapeCheck(t *testing.T, all []rawSeries) (status, interval, msg string) {
	t.Helper()
	spans, counts := coverageSamples(all)
	q := healthy()
	q.rules = append([]rule{
		{has: []string{"min_over_time("}, res: spans},
		{has: []string{"count_over_time(container_memory_working_set_bytes"}, res: counts},
	}, q.rules...)
	rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Now: func() time.Time { return now }}).Run(context.Background())
	c := byID(rep)[rd.CheckScrapeInterval]
	return c.Status, c.Details["interval"], c.Message
}

func TestScrapeIntervalCoverage(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	jitter := func(n int, base, spread, age time.Duration) []rawSeries {
		out := make([]rawSeries, n)
		for i := range out {
			var ts []time.Time
			for t := now.Add(-age); !t.After(now); {
				ts = append(ts, t)
				t = t.Add(base + time.Duration(rng.Int64N(int64(2*spread))) - spread)
			}
			out[i] = series(i, ts)
		}
		return out
	}
	// withGaps drops `missed` random samples of each series.
	withGaps := func(all []rawSeries, missed int) []rawSeries {
		for i := range all {
			for range missed {
				k := 1 + rng.IntN(len(all[i].ts)-2)
				all[i].ts = append(all[i].ts[:k], all[i].ts[k+1:]...)
			}
		}
		return all
	}
	longGap := func(all []rawSeries, every int) []rawSeries {
		for i := range all {
			if i%every != 0 {
				continue
			}
			var kept []time.Time
			for _, t := range all[i].ts {
				if t.Before(now.Add(-20*time.Minute)) || t.After(now.Add(-10*time.Minute)) {
					kept = append(kept, t)
				}
			}
			all[i].ts = kept
		}
		return all
	}
	mixed := append(regularSeries(10, 30*time.Second, 2*time.Hour), regularSeries(10, 30*time.Second, 5*time.Minute)...)
	for i := range mixed {
		mixed[i].labels = recommender.Labels{"pod": fmt.Sprintf("p-%d", i)}
	}
	ended := regularSeries(10, 30*time.Second, 2*time.Hour)
	for i := range ended {
		if i%2 == 0 { // pods deleted 20 minutes ago
			k := 0
			for k < len(ended[i].ts) && ended[i].ts[k].Before(now.Add(-20*time.Minute)) {
				k++
			}
			ended[i].ts = ended[i].ts[:k]
		}
	}

	tests := []struct {
		name     string
		series   []rawSeries
		status   string
		min, max time.Duration // interval bounds; 0 = no interval
	}{
		{"full window, 30 s", regularSeries(50, 30*time.Second, 2*time.Hour), "pass", 30 * time.Second, 30 * time.Second},
		{"full window, 15 s", regularSeries(20, 15*time.Second, 2*time.Hour), "pass", 15 * time.Second, 15 * time.Second},
		{"full window, 60 s", regularSeries(20, time.Minute, 2*time.Hour), "pass", time.Minute, time.Minute},
		{"full window, 2 min", regularSeries(20, 2*time.Minute, 2*time.Hour), "warn", 2 * time.Minute, 2 * time.Minute},
		// The job started 12 minutes ago: about 24 samples. 30 min / 24 would say 1m15s.
		{"job 12 minutes old", regularSeries(50, 30*time.Second, 12*time.Minute), "pass", 29 * time.Second, 30 * time.Second},
		{"job 12 minutes old, 2 min", regularSeries(20, 2*time.Minute, 12*time.Minute), "warn", 2 * time.Minute, 2 * time.Minute},
		{"old and new series", mixed, "pass", 29 * time.Second, 30 * time.Second},
		{"pods deleted mid-window", ended, "pass", 30 * time.Second, 30 * time.Second},
		{"a few missed scrapes", withGaps(regularSeries(30, 30*time.Second, 2*time.Hour), 3), "pass", 30 * time.Second, 32 * time.Second},
		{"a 10-minute gap on a third of the series", longGap(regularSeries(30, 30*time.Second, 2*time.Hour), 3), "pass", 30 * time.Second, 30 * time.Second},
		{"jitter 30 s ± 10 s", jitter(50, 30*time.Second, 10*time.Second, 2*time.Hour), "pass", 29 * time.Second, 31 * time.Second},
		{"jitter 30 s ± 10 s, job 12 minutes old", jitter(50, 30*time.Second, 10*time.Second, 12*time.Minute), "pass", 28 * time.Second, 31 * time.Second},
		{"two samples only", regularSeries(10, 30*time.Second, 40*time.Second), "unknown", 0, 0},
		{"no series", nil, "unknown", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, interval, msg := scrapeCheck(t, tc.series)
			if status != tc.status {
				t.Fatalf("status %s (%s), want %s", status, msg, tc.status)
			}
			if tc.max == 0 {
				if interval != "" {
					t.Errorf("interval %s, want none", interval)
				}
				return
			}
			d, err := time.ParseDuration(interval)
			if err != nil || d < tc.min || d > tc.max {
				t.Errorf("interval %q (%s), want %s..%s", interval, msg, tc.min, tc.max)
			}
		})
	}
}

func TestScrapeIntervalQueries(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	span, count := scrapeIntervalQueries(`{cluster="c", container!=""}`, at)
	start := at.Add(-30 * time.Minute).Unix()
	wantSpan := fmt.Sprintf(`max_over_time((timestamp(container_memory_working_set_bytes{cluster="c", container!=""}) > %d)[30m:15s]) - `+
		`min_over_time((timestamp(container_memory_working_set_bytes{cluster="c", container!=""}) > %d)[30m:15s])`, start, start)
	if span != wantSpan {
		t.Errorf("span query\n%s\nwant\n%s", span, wantSpan)
	}
	if count != `count_over_time(container_memory_working_set_bytes{cluster="c", container!=""}[30m])` {
		t.Errorf("count query %s", count)
	}
}

func TestEstimateScrapeInterval(t *testing.T) {
	tests := []struct {
		name   string
		in     []seriesCoverage
		want   time.Duration
		usedOK int
	}{
		{"empty", nil, 0, 0},
		{"too few samples", []seriesCoverage{{span: 30 * time.Second, samples: 2}}, 0, 0},
		{"one series", []seriesCoverage{{span: 29*time.Minute + 30*time.Second, samples: 60}}, 30 * time.Second, 1},
		{"even count takes the middle two", []seriesCoverage{{span: 10 * time.Minute, samples: 21}, {span: 10 * time.Minute, samples: 11}}, 45 * time.Second, 2},
		{"median ignores an outlier", []seriesCoverage{{span: 15 * time.Minute, samples: 31}, {span: 15 * time.Minute, samples: 31}, {span: 15 * time.Minute, samples: 4}}, 30 * time.Second, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, used := estimateScrapeInterval(tc.in)
			if got != tc.want || used != tc.usedOK {
				t.Errorf("got %s from %d series, want %s from %d", got, used, tc.want, tc.usedOK)
			}
		})
	}
}

func labelsNode(node, capacity string, ts float64) recommender.VectorSample {
	l := recommender.Labels{"node": node, "label_node_kubernetes_io_instance_type": "m8g.large", "label_topology_kubernetes_io_zone": "eu-west-1a", "label_topology_kubernetes_io_region": "eu-west-1"}
	if capacity != "" {
		l["label_eks_amazonaws_com_capacity_type"] = capacity
	}
	return recommender.VectorSample{Labels: l, Sample: recommender.Sample{V: ts}}
}

func TestNodeLabelsNewestSeries(t *testing.T) {
	old := float64(now.Add(-40 * time.Minute).Unix()) // before the allowlist change
	cur := float64(now.Add(-30 * time.Second).Unix())
	tests := []struct {
		name    string
		res     []recommender.VectorSample
		status  string
		msg     string
		missing string
	}{
		{
			name: "old series without capacity type, newest with it",
			res: []recommender.VectorSample{
				labelsNode("mng-1", "", old), labelsNode("mng-1", "ON_DEMAND", cur),
				labelsNode("mng-2", "", old), labelsNode("mng-2", "ON_DEMAND", cur),
				labelsNode("k-1", "SPOT", cur),
			},
			status: "pass", msg: "3 nodes with instance type",
		},
		{
			name: "newest series lacks the label",
			res: []recommender.VectorSample{
				labelsNode("mng-1", "ON_DEMAND", old), labelsNode("mng-1", "", cur),
				labelsNode("mng-2", "ON_DEMAND", cur), labelsNode("k-1", "SPOT", cur),
			},
			status: "warn", msg: "capacity type on 1 of 3 nodes", missing: "1",
		},
		{
			name:   "same timestamp: the series with more labels wins",
			res:    []recommender.VectorSample{labelsNode("n", "", cur), labelsNode("n", "SPOT", cur)},
			status: "pass", msg: "1 nodes",
		},
		{
			name:   "only old series",
			res:    []recommender.VectorSample{labelsNode("a", "", old), labelsNode("b", "", old), labelsNode("c", "SPOT", old)},
			status: "warn", msg: "capacity type on 2 of 3 nodes", missing: "2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := healthy()
			q.rules = append([]rule{{has: []string{"kube_node_labels"}, res: tc.res}}, q.rules...)
			rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Now: func() time.Time { return now }}).Run(context.Background())
			c := byID(rep)[rd.CheckNodeLabels]
			if c.Status != tc.status || !strings.Contains(c.Message, tc.msg) || c.Details["capacitytypeMissing"] != tc.missing {
				t.Errorf("%+v", c)
			}
			if tc.status == "warn" && (!strings.Contains(c.Remediation, NodeLabelsAllowlist) || !strings.Contains(c.Remediation, "# "+RecheckHint("c"))) {
				t.Errorf("remediation %q", c.Remediation)
			}
			var nodeQ string
			for _, qs := range q.queries {
				if strings.Contains(qs, "kube_node_labels") && !strings.Contains(qs, "__name__") {
					nodeQ = qs
				}
			}
			if !strings.Contains(nodeQ, "max_over_time(timestamp(kube_node_labels{") {
				t.Errorf("node labels query %s", nodeQ)
			}
		})
	}
}

func TestFreshnessAbsentAndStale(t *testing.T) {
	ts := func(age time.Duration) []recommender.VectorSample {
		return []recommender.VectorSample{{Sample: recommender.Sample{V: float64(now.Add(-age).Unix())}}}
	}
	tests := []struct {
		name              string
		cadvisor, owner   []recommender.VectorSample
		status, msg       string
		details           map[string]string
		remHas, remHasNot string
	}{
		{
			name: "fresh", cadvisor: ts(30 * time.Second), owner: ts(20 * time.Second), status: "pass", msg: "last sample 30s ago",
			details: map[string]string{"container_memory_working_set_bytes": "30s", "kube_pod_owner": "20s", "lastSampleAge": "30s"},
		},
		{
			name: "stale", cadvisor: ts(30 * time.Second), owner: ts(20 * time.Minute), status: "fail", msg: "last sample is 20m0s old",
			details: map[string]string{"kube_pod_owner": "20m0s", "lastSampleAge": "20m0s"},
		},
		{
			name: "both absent", status: "fail", msg: "no samples of container_memory_working_set_bytes or kube_pod_owner found in the last 1h",
			details: map[string]string{"container_memory_working_set_bytes": Absent, "kube_pod_owner": Absent, "lastSampleAge": Absent},
			remHas:  "scrape job for the kubelet's cAdvisor endpoint",
		},
		{
			name: "cadvisor absent", owner: ts(10 * time.Second), status: "fail", msg: "no samples of container_memory_working_set_bytes found in the last 1h",
			details: map[string]string{"container_memory_working_set_bytes": Absent, "kube_pod_owner": "10s", "lastSampleAge": "10s"},
			remHas:  "cAdvisor", remHasNot: "Install kube-state-metrics",
		},
		{
			name: "absent and stale", cadvisor: ts(25 * time.Minute), status: "fail",
			msg:     "no samples of kube_pod_owner found in the last 1h; the other series' last sample is 25m0s old",
			details: map[string]string{"kube_pod_owner": Absent, "lastSampleAge": "25m0s"},
			remHas:  "Install kube-state-metrics",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := healthy()
			q.rules = append([]rule{
				{has: []string{"max(max_over_time(timestamp(container_memory_working_set_bytes"}, res: tc.cadvisor},
				{has: []string{"max(max_over_time(timestamp(kube_pod_owner"}, res: tc.owner},
			}, q.rules...)
			rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Now: func() time.Time { return now }}).Run(context.Background())
			c := byID(rep)[rd.CheckFreshness]
			if c.Status != tc.status || !strings.HasPrefix(c.Message, tc.msg) {
				t.Fatalf("%s %q, want %s %q", c.Status, c.Message, tc.status, tc.msg)
			}
			for k, v := range tc.details {
				if c.Details[k] != v {
					t.Errorf("details[%s] = %q, want %q", k, c.Details[k], v)
				}
			}
			if strings.Contains(c.Message, "1h0m0s") || strings.Contains(c.Message, "over 1h old") {
				t.Errorf("absent reported as an age: %q", c.Message)
			}
			if tc.remHas != "" && !strings.Contains(c.Remediation, tc.remHas) {
				t.Errorf("remediation %q lacks %q", c.Remediation, tc.remHas)
			}
			if tc.remHasNot != "" && strings.Contains(c.Remediation, tc.remHasNot) {
				t.Errorf("remediation %q has %q", c.Remediation, tc.remHasNot)
			}
		})
	}
}

func TestCAdvisorRemediation(t *testing.T) {
	q := healthy()
	q.rules = append([]rule{
		{has: []string{"count(last_over_time(container_cpu_usage_seconds_total"}, res: nil},
		{has: []string{"count(last_over_time(container_memory_working_set_bytes"}, res: nil},
	}, q.rules...)
	rep := (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Now: func() time.Time { return now }}).Run(context.Background())
	c := byID(rep)[rd.CheckCAdvisor]
	if c.Status != "fail" || c.RemediationType != "yaml" {
		t.Fatalf("%+v", c)
	}
	for _, want := range []string{
		"ca_file: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
		"replacement: /api/v1/nodes/$1/proxy/metrics/cadvisor",
		"- job_name: cadvisor",
		`discovery.kubernetes "nodes"`, `prometheus.scrape "cadvisor"`, `ca_file = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"`,
		"resources: [nodes, nodes/proxy]", "verbs: [get, list, watch]",
		"# " + RecheckHint("c"),
	} {
		if !strings.Contains(c.Remediation, want) {
			t.Errorf("remediation lacks %q", want)
		}
	}
	if strings.Contains(c.Remediation, "insecure_skip_verify") {
		t.Error("remediation turns off TLS verification")
	}
	// A datasource test (preview) stores nothing: no re-check hint.
	rep = (&MetricsChecker{Q: q, M: datasource.DefaultMatcher("c"), Preview: true, Now: func() time.Time { return now }}).Run(context.Background())
	if c := byID(rep)[rd.CheckCAdvisor]; c.Status != "fail" || strings.Contains(c.Remediation, "re-check") {
		t.Errorf("preview remediation %q", c.Remediation)
	}
	if got := CAdvisorRemediation("kubelet"); !strings.Contains(got, "- job_name: kubelet") || !strings.Contains(got, `job_name          = "kubelet"`) {
		t.Errorf("job name not used: %s", got)
	}
}
