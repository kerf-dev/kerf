package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// Job runs the recommender: once at startup, then every interval.
type Job struct {
	Store   *store.Store
	Engine  *recommender.Engine
	Metrics *Metrics
	Cfg     Config
	Log     *slog.Logger
	Now     func() time.Time

	mu sync.Mutex
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// Loop runs until ctx is done.
func (j *Job) Loop(ctx context.Context) {
	rc := j.Cfg.Recommender
	if rc.Backfill.Runs > 0 && rc.Backfill.Every.D() > 0 {
		if ok, err := j.Store.HasSucceededRun(ctx); err == nil && !ok {
			for i := rc.Backfill.Runs; i >= 1; i-- {
				asOf := j.now().Add(-time.Duration(i) * rc.Backfill.Every.D())
				if err := j.RunOnce(ctx, asOf, true); err != nil {
					j.Log.WarnContext(ctx, "backfill run failed", "asOf", asOf, "err", err)
				}
			}
		}
	}
	if rc.RunOnStart == nil || *rc.RunOnStart {
		if err := j.RunOnce(ctx, j.now(), false); err != nil {
			j.Log.ErrorContext(ctx, "recommender run failed", "err", err)
		}
	}
	t := time.NewTicker(rc.Interval.D())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := j.RunOnce(ctx, j.now(), false); err != nil {
				j.Log.ErrorContext(ctx, "recommender run failed", "err", err)
			}
		}
	}
}

// RunOnce computes and stores recommendations for every registered cluster as of asOf.
// Backfill runs (as of a past time) only add history: they never mark workloads deleted.
func (j *Job) RunOnce(ctx context.Context, asOf time.Time, backfill bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	start := time.Now()
	runID, _ := uuid.NewV7()
	if err := j.Store.StartRun(ctx, runID, asOf, backfill); err != nil {
		return err
	}
	clusters, err := j.Store.Clusters(ctx)
	if err != nil {
		_ = j.Store.FinishRun(ctx, runID, "failed", err.Error())
		return err
	}
	var errs []error
	ok := 0
	for _, c := range clusters {
		if err := j.runCluster(ctx, runID, c.Name, asOf, backfill); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
			j.Log.ErrorContext(ctx, "recommender cluster failed", "cluster", c.Name, "err", err)
			_ = j.Store.RecordRunCluster(ctx, store.RunCluster{RunID: runID, Cluster: c.Name, AsOf: asOf, Status: "failed", Error: err.Error()})
			continue
		}
		ok++
	}
	status := "succeeded"
	switch {
	case len(errs) > 0 && ok == 0:
		status = "failed"
	case len(errs) > 0:
		status = "partial"
	}
	err = errors.Join(errs...)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if ferr := j.Store.FinishRun(ctx, runID, status, msg); ferr != nil {
		return ferr
	}
	j.Metrics.runs.WithLabelValues(status).Inc()
	j.Metrics.runDuration.Observe(time.Since(start).Seconds())
	j.Log.InfoContext(ctx, "recommender run finished", "run", runID, "asOf", asOf.UTC(), "backfill", backfill, "status", status,
		"clusters", len(clusters), "duration", time.Since(start).Round(time.Millisecond))
	if !backfill {
		h := j.Cfg.History
		if n, perr := j.Store.PruneHistory(ctx, j.now(), h.FullResolution.D(), h.Retention.D()); perr != nil {
			j.Log.WarnContext(ctx, "history prune", "err", perr)
		} else if n > 0 {
			j.Log.InfoContext(ctx, "history pruned", "recommendations", n)
		}
	}
	if rerr := j.Metrics.Refresh(ctx, j.Store); rerr != nil {
		j.Log.WarnContext(ctx, "metrics refresh", "err", rerr)
	}
	return err
}

func (j *Job) runCluster(ctx context.Context, runID uuid.UUID, cluster string, asOf time.Time, backfill bool) error {
	reported, err := j.Store.ReportedSettings(ctx, cluster)
	if err != nil {
		return err
	}
	// Load only as much history as the profiles in use need (the default profile plus any profile
	// an agent reported for this cluster).
	eng := *j.Engine
	eng.ProfilesInUse = func(string) []string {
		var names []string
		for _, s := range reported {
			if s != nil && s.Profile != "" {
				names = append(names, s.Profile)
			}
		}
		return names
	}
	res, err := eng.RunCluster(ctx, cluster, asOf, func(k hubapi.WorkloadKey) *hubapi.ValueSettings { return reported[k] })
	if err != nil {
		return err
	}
	seenAt := j.now()
	if backfill {
		seenAt = res.End
	}
	// An empty inventory usually means missing metrics, not a cluster without workloads: keep the
	// previous inventory instead of marking everything deleted.
	markDeleted := !backfill && len(res.Inventory) > 0
	ids, err := j.Store.UpsertInventoryAt(ctx, cluster, res.Inventory, seenAt, markDeleted, res.End)
	if err != nil {
		return err
	}
	var savings hubapi.Savings
	for i := range res.Results {
		id, _ := uuid.NewV7()
		res.Results[i].Recommendation.ID = id.String()
		savings = savings.Add(res.Results[i].Recommendation.MonthlySavingsUSD)
	}
	if err := j.Store.InsertRecommendations(ctx, runID, ids, cluster, res.Results); err != nil {
		return err
	}
	if !backfill {
		// The stored total is what GET /v1/summary shows for the cluster from now on (live
		// workloads, latest recommendation each, including ones this run had no data for), so the
		// savings trend ends exactly at the summary.
		views, err := j.Store.LoadWorkloads(ctx, store.WorkloadFilter{Cluster: cluster})
		if err != nil {
			return err
		}
		savings = clusterSavings(views)
	}
	if err := j.Store.RecordRunCluster(ctx, store.RunCluster{
		RunID: runID, Cluster: cluster, AsOf: res.End, Status: "succeeded", Workloads: len(res.Inventory),
		Recommendations: len(res.Results), Savings: savings, Pricing: res.Pricing, FinishedAt: j.now(),
	}); err != nil {
		return err
	}
	if !backfill {
		fb := 0.0
		if res.Pricing.Fallback() {
			fb = 1
			j.Log.WarnContext(ctx, "no pricing series for cluster; savings use the default rate", "cluster", cluster,
				"cpuCoreHourUSD", res.Pricing.CPUCoreHour, "memoryGiBHourUSD", res.Pricing.MemoryGiBHour)
		}
		j.Metrics.pricingFB.WithLabelValues(cluster).Set(fb)
		j.Metrics.lastRun.WithLabelValues(cluster).Set(float64(j.now().Unix()))
		j.Metrics.clusterRecs.WithLabelValues(cluster).Set(float64(len(res.Results)))
	}
	j.Log.InfoContext(ctx, "recommender cluster done", "cluster", cluster, "asOf", res.End, "workloads", len(res.Inventory),
		"recommendations", len(res.Results), "pricing", res.Pricing.Source, "savingsUSD", math.Round(savings.Total*100)/100)
	return nil
}
