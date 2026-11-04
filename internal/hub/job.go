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

// Job runs the recommender: once at startup, then every interval, plus runs queued through
// POST /v1/admin/recompute and (dev) a history backfill.
type Job struct {
	Store   *store.Store
	Engine  *recommender.Engine
	Metrics *Metrics
	Cfg     Config
	Log     *slog.Logger
	Now     func() time.Time

	mu sync.Mutex // one run at a time

	pmu     sync.Mutex
	pending map[string]queuedRun // runs requested through Trigger that have not started, by cluster ("" = all)
	wg      sync.WaitGroup       // triggered runs (tests wait for them)
}

type queuedRun struct {
	id uuid.UUID
	at time.Time
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// Loop runs until ctx is done. With a backfill configured, the backfill runs are spread between
// the regular runs (one backfill run whenever no regular run is due), newest first.
func (j *Job) Loop(ctx context.Context) {
	rc := j.Cfg.Recommender
	if rc.RunOnStart == nil || *rc.RunOnStart {
		if err := j.RunOnce(ctx, j.now(), false); err != nil {
			j.Log.ErrorContext(ctx, "recommender run failed", "err", err)
		}
	}
	plan, err := j.PlanBackfill(ctx)
	if err != nil {
		j.Log.WarnContext(ctx, "backfill plan", "err", err)
	} else if len(plan.Missing) > 0 || len(plan.Stale) > 0 {
		j.Log.InfoContext(ctx, "backfill planned", "runs", len(plan.Missing), "replacing", len(plan.Stale))
	}
	t := time.NewTicker(rc.Interval.D())
	defer t.Stop()
	for {
		if plan != nil && (len(plan.Missing) > 0 || len(plan.Stale) > 0) {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := j.RunOnce(ctx, j.now(), false); err != nil {
					j.Log.ErrorContext(ctx, "recommender run failed", "err", err)
				}
			default:
				j.backfillStep(ctx, plan)
			}
			continue
		}
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

// BackfillPlan is the backfill work left: the as-of times to compute (newest first) and the
// backfill runs to delete once they are done.
type BackfillPlan struct {
	Missing []time.Time
	Stale   []uuid.UUID
}

// PlanBackfill computes the dev history backfill: one run as of each multiple of backfill.every
// (UTC) in the backfill.runs intervals before the first regular run (or now), within the history
// retention. Times that already have a successful backfill run are skipped. Backfill runs off
// that grid (a changed interval, or older hub versions that backfilled as of the start time) or
// failed ones are stale: they are deleted after the new runs are stored, so the history is
// recomputed from the metrics as they are now.
func (j *Job) PlanBackfill(ctx context.Context) (*BackfillPlan, error) {
	b := j.Cfg.Recommender.Backfill
	every := b.Every.D()
	if b.Runs <= 0 || every <= 0 {
		return &BackfillPlan{}, nil
	}
	now := j.now()
	limit := now
	if first, ok, err := j.Store.FirstRegularRun(ctx); err != nil {
		return nil, err
	} else if ok && first.Before(limit) {
		limit = first
	}
	oldest := time.Time{}
	if r := j.Cfg.History.Retention.D(); r > 0 {
		oldest = now.Add(-r)
	}
	runs, err := j.Store.BackfillRuns(ctx)
	if err != nil {
		return nil, err
	}
	have := map[int64]bool{}
	plan := &BackfillPlan{}
	for _, r := range runs {
		if r.AsOf.Unix()%int64(every/time.Second) == 0 && (r.Status == "succeeded" || r.Status == "partial") {
			have[r.AsOf.Unix()] = true
			continue
		}
		plan.Stale = append(plan.Stale, r.ID)
	}
	t := alignDown(limit, every)
	if !t.Before(limit) {
		t = t.Add(-every)
	}
	for i := 0; i < b.Runs && t.After(oldest); i, t = i+1, t.Add(-every) {
		if !have[t.Unix()] {
			plan.Missing = append(plan.Missing, t)
		}
	}
	return plan, nil
}

// backfillStep does one unit of plan: the next backfill run, or (once all ran) deleting the stale runs.
func (j *Job) backfillStep(ctx context.Context, plan *BackfillPlan) {
	if len(plan.Missing) > 0 {
		asOf := plan.Missing[0]
		plan.Missing = plan.Missing[1:]
		if err := j.RunOnce(ctx, asOf, true); err != nil {
			j.Log.WarnContext(ctx, "backfill run failed", "asOf", asOf, "err", err)
		}
		return
	}
	n, err := j.Store.DeleteRuns(ctx, plan.Stale)
	if err != nil {
		j.Log.WarnContext(ctx, "delete stale backfill runs", "err", err)
	} else {
		j.Log.InfoContext(ctx, "backfill done; stale backfill runs deleted", "runs", len(plan.Stale), "recommendations", n)
	}
	plan.Stale = nil
}

// RunBackfill runs a whole backfill plan synchronously (tests and tools).
func (j *Job) RunBackfill(ctx context.Context) error {
	plan, err := j.PlanBackfill(ctx)
	if err != nil {
		return err
	}
	for len(plan.Missing) > 0 || len(plan.Stale) > 0 {
		j.backfillStep(ctx, plan)
	}
	return nil
}

// alignDown rounds t down to a multiple of d since the Unix epoch.
func alignDown(t time.Time, d time.Duration) time.Time {
	sec := int64(d / time.Second)
	if sec <= 0 {
		return t
	}
	u := t.Unix()
	return time.Unix(u-((u%sec)+sec)%sec, 0).UTC()
}

// Trigger queues a recommender run now (POST /v1/admin/recompute, D-019) for cluster, or for all
// clusters when cluster is "". The run starts once no other run is in progress, as of that
// moment. A request whose scope already has a queued run that has not started gets that run
// (coalesced). ctx must outlive the request.
func (j *Job) Trigger(ctx context.Context, cluster string) (id uuid.UUID, requestedAt time.Time, coalesced bool) {
	j.pmu.Lock()
	defer j.pmu.Unlock()
	if q, ok := j.pending[cluster]; ok {
		return q.id, q.at, true
	}
	id, _ = uuid.NewV7()
	requestedAt = j.now()
	if j.pending == nil {
		j.pending = map[string]queuedRun{}
	}
	j.pending[cluster] = queuedRun{id: id, at: requestedAt}
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		j.mu.Lock()
		defer j.mu.Unlock()
		j.pmu.Lock()
		delete(j.pending, cluster)
		j.pmu.Unlock()
		j.Log.InfoContext(ctx, "recompute run started", "run", id, "cluster", cluster)
		if err := j.runLocked(ctx, id, j.now(), false, cluster); err != nil {
			j.Log.ErrorContext(ctx, "recompute run failed", "run", id, "cluster", cluster, "err", err)
		}
	}()
	return id, requestedAt, false
}

// WaitTriggered waits for the runs queued through Trigger (tests).
func (j *Job) WaitTriggered() { j.wg.Wait() }

// RunOnce computes and stores recommendations for every registered cluster as of asOf.
// Backfill runs (as of a past time) only add history: they never mark workloads deleted.
func (j *Job) RunOnce(ctx context.Context, asOf time.Time, backfill bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	runID, _ := uuid.NewV7()
	return j.runLocked(ctx, runID, asOf, backfill, "")
}

// runLocked is RunOnce with j.mu held, for one cluster (only) or all ("").
func (j *Job) runLocked(ctx context.Context, runID uuid.UUID, asOf time.Time, backfill bool, only string) error {
	start := time.Now()
	if err := j.Store.StartRun(ctx, runID, asOf, backfill); err != nil {
		return err
	}
	all, err := j.Store.Clusters(ctx)
	if err != nil {
		_ = j.Store.FinishRun(ctx, runID, "failed", err.Error())
		return err
	}
	clusters := all
	if only != "" {
		clusters = nil
		for _, c := range all {
			if c.Name == only {
				clusters = append(clusters, c)
			}
		}
		if len(clusters) == 0 {
			err := fmt.Errorf("unknown cluster %s", only)
			_ = j.Store.FinishRun(ctx, runID, "failed", err.Error())
			return err
		}
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
	// JVM heap settings the agent reported (WO-12); unreported workloads fall back to image detection.
	jvm, err := j.Store.ReportedJVM(ctx, cluster)
	if err != nil {
		return err
	}
	eng.JVM = func(k hubapi.WorkloadKey) ([]hubapi.JVMContainer, bool) { c, ok := jvm[k]; return c, ok }
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
