package hub

import (
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// getWorkloadUsage serves GET /v1/workloads/{id}/usage.
func (s *Server) getWorkloadUsage(w http.ResponseWriter, r *http.Request) {
	v := s.workloadFromPath(w, r)
	if v == nil {
		return
	}
	q := r.URL.Query()
	resource := q.Get("resource")
	if resource != "cpu" && resource != "memory" {
		badRequest(w, r, "invalid_resource", "resource must be cpu or memory.")
		return
	}
	from, to, err := s.timeRange(q.Get("from"), q.Get("to"))
	if err != nil {
		badRequest(w, r, "invalid_time_range", err.Error())
		return
	}
	step, err := chooseStep(q.Get("step"), from, to)
	if err != nil {
		badRequest(w, r, "invalid_step", err.Error())
		return
	}
	onlyContainer := q.Get("container")

	key := v.Key
	prom, binding, err := s.metricsFor(r.Context(), key.Cluster)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	qb := recommender.Queries{Cluster: key.Cluster, RateWindow: s.RecCfg.RateWindow, Binding: binding}
	f := &recommender.Filter{Namespace: key.Namespace}
	inner := s.RecCfg.Step
	var usageQ, avgQ string
	if resource == "cpu" {
		usageQ, avgQ = qb.CPUUsage("max", f), qb.CPUUsage("avg", f)
		if step > inner {
			// Keep peaks between steps: max/avg over the step at the recommender's resolution.
			usageQ = fmt.Sprintf("max_over_time((%s)[%s:%s])", usageQ, promDur(step), promDur(inner))
			avgQ = fmt.Sprintf("avg_over_time((%s)[%s:%s])", avgQ, promDur(step), promDur(inner))
		}
	} else {
		win := max(step, inner)
		usageQ, avgQ = qb.MemoryUsage("max", f, win), qb.MemoryUsage("avg", f, win)
	}

	type series = map[string][]hubapi.Point
	usage, avg, req, lim := series{}, series{}, series{}, series{}
	match := func(l recommender.Labels) (string, bool) {
		c := l["container"]
		return c, l["workload_kind"] == key.Kind && l["workload"] == key.Name && (onlyContainer == "" || c == onlyContainer)
	}
	var mu sync.Mutex
	g, ctx := errgroup.WithContext(r.Context())
	run := func(query string, dst series, filterResource bool) {
		g.Go(func() error {
			res, err := prom.Range(ctx, query, from, to, step)
			if err != nil {
				return err
			}
			for _, sr := range res {
				c, ok := match(sr.Labels)
				if !ok || (filterResource && sr.Labels["resource"] != resource) {
					continue
				}
				pts := make([]hubapi.Point, len(sr.Samples))
				for i, p := range sr.Samples {
					pts[i] = hubapi.Point{float64(p.T.Unix()), p.V}
				}
				// Init and app containers are separate queries writing the same map; never merge two
				// series per name.
				mu.Lock()
				if len(dst[c]) == 0 {
					dst[c] = pts
				}
				mu.Unlock()
			}
			return nil
		})
	}
	run(usageQ, usage, false)
	run(avgQ, avg, false)
	for _, init := range []bool{false, true} {
		run(qb.Resources("requests", init, 0, f), req, true)
		run(qb.Resources("limits", init, 0, f), lim, true)
	}
	if err := g.Wait(); err != nil {
		s.Log.WarnContext(r.Context(), "usage query", "workload", key.ID(), "err", err)
		writeProblem(w, r, http.StatusBadGateway, "metrics_unavailable", "Metrics backend query failed: "+err.Error())
		return
	}
	steps, err := s.Store.RecommendationSteps(r.Context(), v.DBID, from.Add(-step), to)
	if err != nil {
		s.internal(w, r, err)
		return
	}

	// Containers with usage, plus the recommended ones. Classic init containers (requests in KSM,
	// no usage samples, never rightsized) would only render as empty charts.
	names := map[string]bool{}
	for c := range usage {
		names[c] = true
	}
	if v.Rec != nil {
		for _, c := range v.Rec.Containers {
			if onlyContainer == "" || c.Name == onlyContainer {
				names[c.Name] = true
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	slices.Sort(sorted)

	out := hubapi.UsageSeries{WorkloadID: key.ID(), Resource: resource, Unit: "cores", From: from.UTC(), To: to.UTC(), Step: step.String(),
		Containers: []hubapi.ContainerSeries{}}
	if resource == "memory" {
		out.Unit = "bytes"
	}
	var origin *time.Time
	for _, set := range []series{usage, avg, req, lim} {
		for _, pts := range set {
			if len(pts) > 0 {
				t := time.Unix(int64(pts[0][0]), 0)
				origin = &t
				break
			}
		}
		if origin != nil {
			break
		}
	}
	grid := usageGrid(from, to, step, origin)
	for _, n := range sorted {
		out.Containers = append(out.Containers, hubapi.ContainerSeries{
			Name: n, Usage: nonNil(usage[n]), UsageAvg: nonNil(avg[n]), Request: nonNil(req[n]), Limit: nonNil(lim[n]),
			Recommendation: recommendationPoints(steps, n, resource, grid),
		})
	}
	writeJSON(w, out)
}

func nonNil(p []hubapi.Point) []hubapi.Point {
	if p == nil {
		return []hubapi.Point{}
	}
	return p
}

// usageGrid returns the timestamps for the recommendation series: the grid of the metric series
// (VictoriaMetrics aligns range queries to multiples of step, Prometheus starts at from), so the
// recommendation shares the x values of usage, request and limit. origin is any timestamp of
// the metric series, nil without any (then the grid is aligned like VictoriaMetrics). The grid
// covers (from - step, to): the first point may precede from, like the metric series' first point.
func usageGrid(from, to time.Time, step time.Duration, origin *time.Time) []time.Time {
	sec := int64(step / time.Second)
	if sec <= 0 {
		return nil
	}
	o := alignDown(from, step).Unix()
	if origin != nil {
		o = origin.Unix()
	}
	// First grid point after from - step.
	lo := from.Unix() - sec
	start := o + ((lo-o)/sec)*sec
	for start <= lo {
		start += sec
	}
	for start-sec > lo {
		start -= sec
	}
	var out []time.Time
	for t := start; t < to.Unix() || (t == to.Unix() && to.Nanosecond() > 0); t += sec {
		out = append(out, time.Unix(t, 0).UTC())
	}
	return out
}

// recommendationPoints is the step function of the recommended request of one container,
// sampled at grid: at each time, the latest recommendation generated at or before it. steps are
// oldest first; times before the first recommendation have no point.
func recommendationPoints(steps []storeStep, container, resource string, grid []time.Time) []hubapi.Point {
	pts := []hubapi.Point{}
	i := -1
	for _, t := range grid {
		for i+1 < len(steps) && !steps[i+1].At.After(t) {
			i++
		}
		if i < 0 {
			continue
		}
		for _, c := range steps[i].Containers {
			if c.Name != container {
				continue
			}
			cpu, mem := requestValues(c.Recommended)
			v := cpu
			if resource == "memory" {
				v = mem
			}
			if v > 0 {
				pts = append(pts, hubapi.Point{float64(t.Unix()), v})
			}
		}
	}
	return pts
}

func promDur(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", max(1, d/time.Second))
	}
}
