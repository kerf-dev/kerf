package report

import (
	"context"
	"math"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/recommender"
	"github.com/kerf-dev/kerf/tools/synth/synth"
)

// fixtureQuerier answers the recommender's queries from testdata/series fixtures, the way a
// Prometheus with kube-state-metrics and cAdvisor would for those workloads. Queries are matched
// by their text, built with recommender.Queries, so a change of the hub's queries shows up here.
type fixtureQuerier struct {
	mu      sync.Mutex
	rng     map[string]func(start, end time.Time) []recommender.Series
	instant map[string]func() []recommender.VectorSample
	// seen records every query, to check the matcher in the query text.
	seen []string
}

func (f *fixtureQuerier) Instant(_ context.Context, query string, _ time.Time) ([]recommender.VectorSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, query)
	if fn, ok := f.instant[query]; ok {
		return fn(), nil
	}
	return nil, nil // pricing series and anything else the fixtures do not have
}

func (f *fixtureQuerier) Range(_ context.Context, query string, start, end time.Time, _ time.Duration) ([]recommender.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, query)
	if fn, ok := f.rng[query]; ok {
		return fn(start, end), nil
	}
	return nil, nil
}

func clip(s []recommender.Sample, start, end time.Time) []recommender.Sample {
	var out []recommender.Sample
	for _, x := range s {
		if !x.T.Before(start) && !x.T.After(end) {
			out = append(out, x)
		}
	}
	return out
}

func samples(fx *synth.Fixture, v synth.Values, mul synth.Values) []recommender.Sample {
	var out []recommender.Sample
	for i, x := range v {
		if math.IsNaN(x) {
			continue
		}
		if mul != nil {
			x *= mul[i]
		}
		out = append(out, recommender.Sample{T: time.Unix(fx.Start+int64(i*fx.Step), 0).UTC(), V: x})
	}
	return out
}

func loadFixtures(t testing.TB) []*synth.Fixture {
	t.Helper()
	files, err := filepath.Glob("../../testdata/series/synth-*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in testdata/series: %v", err)
	}
	sort.Strings(files)
	var out []*synth.Fixture
	for _, p := range files {
		fx, err := synth.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fx)
	}
	return out
}

// newFixtureQuerier serves the fixtures for queries built with q (the cluster's matcher).
func newFixtureQuerier(t testing.TB, q recommender.Queries, cfg recommender.Config, window time.Duration, fxs []*synth.Fixture) *fixtureQuerier {
	t.Helper()
	f := &fixtureQuerier{rng: map[string]func(start, end time.Time) []recommender.Series{}, instant: map[string]func() []recommender.VectorSample{}}
	cluster := fxs[0].Cluster
	wlLabels := func(fx *synth.Fixture, container string) recommender.Labels {
		l := recommender.Labels{"cluster": cluster, "namespace": fx.Namespace, "workload_kind": fx.WorkloadKind, "workload": fx.Workload}
		if container != "" {
			l["container"] = container
		}
		return l
	}
	rangeOf := func(query string, gen func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.Sample, perContainer bool) {
		f.rng[query] = func(start, end time.Time) []recommender.Series {
			var out []recommender.Series
			for _, fx := range fxs {
				if !perContainer {
					out = append(out, recommender.Series{Labels: wlLabels(fx, ""), Samples: clip(gen(fx, synth.FixtureContainer{}), start, end)})
					continue
				}
				for _, c := range fx.Containers {
					if c.Init || c.CPU == nil {
						continue
					}
					out = append(out, recommender.Series{Labels: wlLabels(fx, c.Name), Samples: clip(gen(fx, c), start, end)})
				}
			}
			return out
		}
	}
	rangeOf(q.CPUUsage("max", nil), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.Sample {
		return samples(fx, c.CPU.Max, nil)
	}, true)
	rangeOf(q.MemoryUsage("max", nil, cfg.Step), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.Sample {
		return samples(fx, c.Memory.Max, nil)
	}, true)
	rangeOf(q.RunningPods(cfg.Step), func(fx *synth.Fixture, _ synth.FixtureContainer) []recommender.Sample {
		return samples(fx, fx.Replicas, nil)
	}, false)
	rangeOf(q.CPUUsage("sum", nil), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.Sample {
		return samples(fx, c.CPU.Avg, fx.Replicas)
	}, true)
	rangeOf(q.MemoryUsage("sum", nil, cfg.Step), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.Sample {
		return samples(fx, c.Memory.Avg, fx.Replicas)
	}, true)
	rangeOf(q.ThrottleRatio(), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.Sample {
		return samples(fx, c.ThrottleRatio, nil)
	}, true)

	end := time.Unix(fxs[0].Start+int64(fxs[0].Points*fxs[0].Step), 0).UTC()
	vs := func(l recommender.Labels, v float64) recommender.VectorSample {
		return recommender.VectorSample{Labels: l, Sample: recommender.Sample{T: end, V: v}}
	}
	perContainer := func(query string, gen func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.VectorSample) {
		f.instant[query] = func() []recommender.VectorSample {
			var out []recommender.VectorSample
			for _, fx := range fxs {
				for _, c := range fx.Containers {
					out = append(out, gen(fx, c)...)
				}
			}
			return out
		}
	}
	perContainer(q.OOMKills(cfg.RiskWindow), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.VectorSample {
		if c.Init || c.CPU == nil {
			return nil
		}
		return []recommender.VectorSample{vs(wlLabels(fx, c.Name), float64(c.OOMKills))}
	})
	perContainer(q.Restarts(cfg.RiskWindow), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.VectorSample {
		if c.Init || c.CPU == nil {
			return nil
		}
		return []recommender.VectorSample{vs(wlLabels(fx, c.Name), float64(c.Restarts))}
	})
	perContainer(q.ContainerImages(cfg.InventoryLookback), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.VectorSample {
		l := wlLabels(fx, c.Name)
		l["image"] = c.Image
		return []recommender.VectorSample{vs(l, 1)}
	})
	perContainer(q.InitContainers(cfg.InventoryLookback), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.VectorSample {
		if !c.Init {
			return nil
		}
		return []recommender.VectorSample{vs(wlLabels(fx, c.Name), 1)}
	})
	for _, metric := range []string{"requests", "limits"} {
		for _, init := range []bool{false, true} {
			for _, lb := range []time.Duration{0, cfg.InventoryLookback} {
				perContainer(q.Resources(metric, init, lb, nil), func(fx *synth.Fixture, c synth.FixtureContainer) []recommender.VectorSample {
					if c.Init != init {
						return nil
					}
					vals := c.Requests
					if metric == "limits" {
						vals = c.Limits
					}
					var out []recommender.VectorSample
					for _, res := range []string{"cpu", "memory"} {
						if v, ok := vals[res]; ok {
							l := wlLabels(fx, c.Name)
							l["resource"] = res
							out = append(out, vs(l, v))
						}
					}
					return out
				})
			}
		}
	}

	hpaLabels := func(fx *synth.Fixture) recommender.Labels {
		return recommender.Labels{"cluster": cluster, "namespace": fx.Namespace, "horizontalpodautoscaler": fx.Workload + "-hpa",
			"scaletargetref_kind": fx.WorkloadKind, "scaletargetref_name": fx.Workload}
	}
	perHPA := func(query string, gen func(fx *synth.Fixture) *recommender.VectorSample) {
		f.instant[query] = func() []recommender.VectorSample {
			var out []recommender.VectorSample
			for _, fx := range fxs {
				if fx.HPA == nil {
					continue
				}
				if s := gen(fx); s != nil {
					out = append(out, *s)
				}
			}
			return out
		}
	}
	perHPA(q.HPAInfo(cfg.InventoryLookback), func(fx *synth.Fixture) *recommender.VectorSample {
		s := vs(hpaLabels(fx), 1)
		return &s
	})
	perHPA(q.HPAMetrics(cfg.InventoryLookback), func(fx *synth.Fixture) *recommender.VectorSample {
		l := hpaLabels(fx)
		l["metric_name"], l["metric_target_type"] = fx.HPA.Metric, "utilization"
		s := vs(l, float64(fx.HPA.Target))
		return &s
	})
	perHPA(q.HPAReplicaBounds("min", cfg.InventoryLookback), func(fx *synth.Fixture) *recommender.VectorSample {
		s := vs(hpaLabels(fx), float64(fx.HPA.Min))
		return &s
	})
	perHPA(q.HPAReplicaBounds("max", cfg.InventoryLookback), func(fx *synth.Fixture) *recommender.VectorSample {
		s := vs(hpaLabels(fx), float64(fx.HPA.Max))
		return &s
	})
	perHPA(q.HPAPeakReplicas(window), func(fx *synth.Fixture) *recommender.VectorSample {
		peak := 0.0
		for _, r := range fx.Replicas {
			if !math.IsNaN(r) {
				peak = math.Max(peak, r)
			}
		}
		s := vs(hpaLabels(fx), peak)
		return &s
	})
	return f
}

// fixtureEnd is the end of the fixtures' history.
func fixtureEnd(fxs []*synth.Fixture) time.Time {
	return time.Unix(fxs[0].Start+int64(fxs[0].Points*fxs[0].Step), 0).UTC()
}

// fixtureNow is an arbitrary time for tests that do not depend on the data.
var fixtureNow = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// fixedLoader returns canned cluster data.
type fixedLoader struct{ d *recommender.ClusterData }

func (f fixedLoader) Load(context.Context, string, time.Time, time.Duration) (*recommender.ClusterData, error) {
	return f.d, nil
}
