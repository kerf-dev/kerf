package dynatrace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Usage is the usage chart data of one workload, by container.
type Usage struct {
	// Usage is the max across pods, Avg the average across pods.
	Usage, Avg, Request, Limit map[string][]recommender.Sample
}

// workloadFilter selects one workload's series (the kind in Dynatrace's and Kerf's spelling).
func workloadFilter(cluster, namespace, kind, name string) string {
	return ", filter: " + strings.Join([]string{
		DimCluster + " == " + dqlString(cluster),
		DimNamespace + " == " + dqlString(namespace),
		DimWorkload + " == " + dqlString(name),
		"in(" + DimKind + ", {" + dqlString(strings.ToLower(kind)) + ", " + dqlString(kind) + "})",
	}, " and ")
}

// WorkloadUsage reads the usage chart of one workload: CPU (cores) or memory (bytes) per
// container in [from, to] at step, with its requests and limits.
func (s *Source) WorkloadUsage(ctx context.Context, cluster, namespace, kind, name, resource string, from, to time.Time, step time.Duration) (Usage, error) {
	out := Usage{Usage: map[string][]recommender.Sample{}, Avg: map[string][]recommender.Sample{}, Request: map[string][]recommender.Sample{}, Limit: map[string][]recommender.Sample{}}
	key, agg, kind2, reqKey, limKey := KeyCPUUsage, "avg", unitCPU, KeyRequestsCPU, KeyLimitsCPU
	if resource == "memory" {
		key, agg, kind2, reqKey, limKey = KeyMemoryWorkingSet, "max", unitBytes, KeyRequestsMemory, KeyLimitsMemory
	}
	if step <= 0 {
		step = 5 * time.Minute
	}
	f := workloadFilter(cluster, namespace, kind, name)
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	add := func(dst map[string][]recommender.Sample, rec map[string]json.RawMessage, field string, factor float64) error {
		c := str(rec, DimContainer)
		if c == "" {
			return nil
		}
		sm, err := series(rec, field, factor)
		dst[c] = append(dst[c], sm...)
		return err
	}
	for _, c := range chunk(from, to, step) {
		w := ", from: " + dqlTime(c[0]) + ", to: " + dqlTime(c[1])
		g.Go(func() error {
			res, err := s.run(gctx, fmt.Sprintf("timeseries v = %s(%s), by: {%s, %s}%s%s, interval: %s\n| summarize {vmax = max(v[]), vavg = avg(v[])}, by: {timeframe, interval, %s}",
				agg, key, DimPod, DimContainer, f, w, dqlDuration(step), DimContainer))
			if err != nil {
				return fmt.Errorf("usage: %w", err)
			}
			fac := s.unitScale(gctx, res, key, kind2)
			mu.Lock()
			defer mu.Unlock()
			for _, rec := range res.Records {
				if err := add(out.Usage, rec, "vmax", fac); err != nil {
					return err
				}
				if err := add(out.Avg, rec, "vavg", fac); err != nil {
					return err
				}
			}
			return nil
		})
		for _, r := range []struct {
			key string
			dst map[string][]recommender.Sample
		}{{reqKey, out.Request}, {limKey, out.Limit}} {
			g.Go(func() error {
				res, err := s.run(gctx, fmt.Sprintf("timeseries v = max(%s), by: {%s}%s%s, interval: %s", r.key, DimContainer, f, w, dqlDuration(step)))
				if err != nil {
					return fmt.Errorf("%s: %w", r.key, err)
				}
				fac := s.unitScale(gctx, res, r.key, kind2)
				mu.Lock()
				defer mu.Unlock()
				for _, rec := range res.Records {
					if err := add(r.dst, rec, "v", fac); err != nil {
						return err
					}
				}
				return nil
			})
		}
	}
	if err := g.Wait(); err != nil {
		return Usage{}, err
	}
	for _, m := range []map[string][]recommender.Sample{out.Usage, out.Avg, out.Request, out.Limit} {
		for c, sm := range m {
			m[c] = mergeMax(sm)
		}
	}
	return out, nil
}

// HourlyReplicas is the average number of pods per workload over each hour in [from, to) (the
// realized savings): pods with a memory sample per 5-minute step, a step without one counts as
// zero. Hours are keyed by their start; covered are the hours with any data.
func (s *Source) HourlyReplicas(ctx context.Context, registered, cluster string, from, to time.Time) (map[hubapi.WorkloadKey]map[time.Time]float64, map[time.Time]bool, error) {
	const step = 5 * time.Minute
	perHour := float64(time.Hour / step)
	out := map[hubapi.WorkloadKey]map[time.Time]float64{}
	covered := map[time.Time]bool{}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	for _, c := range chunk(from, to, step) {
		g.Go(func() error {
			res, err := s.run(gctx, PodsQuery(cluster, c[0], c[1], step))
			if err != nil {
				return fmt.Errorf("pods: %w", err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, rec := range res.Records {
				k, ok := workloadKey(registered, rec)
				if !ok {
					continue
				}
				sm, err := series(rec, "pods", 1)
				if err != nil {
					return err
				}
				for _, p := range sm {
					// the sample stamped t covers [t-step, t)
					h := p.T.Add(-step).UTC().Truncate(time.Hour)
					if out[k] == nil {
						out[k] = map[time.Time]float64{}
					}
					out[k][h] += p.V / perHour
					covered[h] = true
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return out, covered, nil
}
