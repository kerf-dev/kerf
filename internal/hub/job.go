package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kerf-dev/kerf/internal/hub/config"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/recommender"
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

	wakeOnce sync.Once
	wake     chan struct{} // a recompute request was queued (RecomputeLoop polls now)

	// Per cluster, guarded by mu: the last accepted regular run and since when runs look incomplete.
	baseline   map[string]clusterBaseline
	incomplete map[string]time.Time

	// The admin settings in effect for the current run, guarded by mu.
	cur            config.Recommender
	pricing        hubapi.PricingSettings
	pricingVersion int

	resetOnce sync.Once
	reset     chan time.Duration // a new run interval from PUT /v1/admin/settings/recommender
}

// baseRecommender is the recommender configuration of the hub configuration file (settings
// version 0).
func (j *Job) baseRecommender() config.Recommender {
	return config.Recommender{Config: j.Engine.Cfg, Interval: j.Cfg.Recommender.Interval.D(), Retention: j.Cfg.History.Retention.D()}
}

// loadSettings reads the recommender and pricing settings sections (hot reload: every run uses
// the stored ones). A section that fails to load or apply keeps the configuration file's values.
func (j *Job) loadSettings(ctx context.Context) (config.Recommender, hubapi.PricingSettings, int) {
	rec := j.baseRecommender()
	var pricing hubapi.PricingSettings
	pv := 0
	if d, err := j.Store.GetSettings(ctx, hubapi.SettingsRecommender); err != nil {
		j.Log.WarnContext(ctx, "recommender settings", "err", err)
	} else if d.Version > 0 {
		var v hubapi.RecommenderSettings
		if err := json.Unmarshal(d.Value, &v); err != nil {
			j.Log.WarnContext(ctx, "recommender settings", "version", d.Version, "err", err)
		} else if applied, errs := config.ApplyRecommender(rec, v); errs != nil {
			j.Log.WarnContext(ctx, "recommender settings ignored", "version", d.Version, "errors", errs)
		} else {
			rec = applied
		}
	}
	if d, err := j.Store.GetSettings(ctx, hubapi.SettingsPricing); err != nil {
		j.Log.WarnContext(ctx, "pricing settings", "err", err)
	} else if d.Version > 0 {
		if err := json.Unmarshal(d.Value, &pricing); err != nil {
			j.Log.WarnContext(ctx, "pricing settings", "version", d.Version, "err", err)
		} else {
			pv = d.Version
		}
	}
	return rec, pricing, pv
}

func (j *Job) resetChan() chan time.Duration {
	j.resetOnce.Do(func() { j.reset = make(chan time.Duration, 1) })
	return j.reset
}

// SetInterval changes the interval of the regular runs (from the next tick on).
func (j *Job) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	ch := j.resetChan()
	select {
	case <-ch:
	default:
	}
	ch <- d
}

type clusterBaseline struct{ inventory, withRequests int }

// minGuardedInventory: smaller clusters are not checked for incomplete metrics (a var for tests).
var minGuardedInventory = 10

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// Loop runs until ctx is done. With a backfill configured, the backfill runs are spread between
// the regular runs (one backfill run whenever no regular run is due), newest first.
//
// With several hub replicas only the leader runs Loop; it starts again on every replica
// that becomes leader. The run at start is skipped when a regular run succeeded within the last
// min(interval, 5m), so a rolling update that moves the leadership twice runs once; the next
// run then comes one interval after that run.
func (j *Job) Loop(ctx context.Context) {
	rc := j.Cfg.Recommender
	reset := j.resetChan()
	select { // a change signalled while another replica led is in the settings loaded below
	case <-reset:
	default:
	}
	rec, _, _ := j.loadSettings(ctx)
	interval := rec.Interval
	first := interval
	if rc.RunOnStart == nil || *rc.RunOnStart {
		if last, ok := j.recentRegularRun(ctx, interval); ok {
			first = max(last.Add(interval).Sub(j.now()), time.Second)
			j.Log.InfoContext(ctx, "recommender: a recent run exists; skipping the run at start", "lastRunAsOf", last.UTC(), "nextIn", first.Round(time.Second))
		} else if err := j.RunOnce(ctx, j.now(), false); err != nil {
			j.Log.ErrorContext(ctx, "recommender run failed", "err", err)
		}
	}
	plan, err := j.PlanBackfill(ctx)
	if err != nil {
		j.Log.WarnContext(ctx, "backfill plan", "err", err)
	} else if len(plan.Missing) > 0 || len(plan.Stale) > 0 {
		j.Log.InfoContext(ctx, "backfill planned", "runs", len(plan.Missing), "replacing", len(plan.Stale))
	}
	// A timer re-armed after each tick: the first tick may be shorter (skipped start run).
	t := time.NewTimer(first)
	defer t.Stop()
	tick := func() {
		t.Reset(interval)
		if err := j.RunOnce(ctx, j.now(), false); err != nil {
			j.Log.ErrorContext(ctx, "recommender run failed", "err", err)
		}
	}
	setInterval := func(d time.Duration) {
		j.Log.InfoContext(ctx, "recommender interval changed", "interval", d)
		interval = d
		t.Reset(d)
	}
	for {
		if plan != nil && (len(plan.Missing) > 0 || len(plan.Stale) > 0) {
			select {
			case <-ctx.Done():
				return
			case d := <-reset:
				setInterval(d)
			case <-t.C:
				tick()
			default:
				j.backfillStep(ctx, plan)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case d := <-reset:
			setInterval(d)
		case <-t.C:
			tick()
		}
	}
}

// recentRegularRun returns the as-of time of the last successful regular run when it is younger
// than min(interval, 5m).
func (j *Job) recentRegularRun(ctx context.Context, interval time.Duration) (time.Time, bool) {
	last, ok, err := j.Store.LastRegularRunAsOf(ctx)
	if err != nil {
		j.Log.WarnContext(ctx, "recommender: last run", "err", err)
		return time.Time{}, false
	}
	if !ok || j.now().Sub(last) >= min(interval, 5*time.Minute) {
		return time.Time{}, false
	}
	return last, true
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
		if r.AsOf.Unix()%int64(every/time.Second) == 0 && (r.Status == "succeeded" || r.Status == "partial") && !r.RepricingNeeded {
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

// Trigger queues a recommender run now (POST /v1/admin/recompute) for cluster, or for all
// clusters when cluster is "". The request is stored in the database, so any replica
// can take it; the leader's RecomputeLoop runs it once no other run is in progress, as of that
// moment. A request whose scope already has a queued run that has not started gets that run
// (coalesced).
func (j *Job) Trigger(ctx context.Context, cluster, by string) (id uuid.UUID, requestedAt time.Time, coalesced bool, err error) {
	id, _ = uuid.NewV7()
	req, coalesced, err := j.Store.QueueRecompute(ctx, store.RecomputeRequest{ID: id, Cluster: cluster, RequestedAt: j.now(), RequestedBy: by})
	if err != nil {
		return uuid.Nil, time.Time{}, false, err
	}
	if !coalesced {
		j.Wake()
		if err := j.Store.Notify(ctx, store.NotifyRecompute); err != nil {
			j.Log.WarnContext(ctx, "notify recompute", "err", err) // the leader's poll finds it anyway
		}
	}
	return req.ID, req.RequestedAt, coalesced, nil
}

func (j *Job) wakeChan() chan struct{} {
	j.wakeOnce.Do(func() { j.wake = make(chan struct{}, 1) })
	return j.wake
}

// Wake makes RecomputeLoop look for queued requests now.
func (j *Job) Wake() {
	select {
	case j.wakeChan() <- struct{}{}:
	default:
	}
}

// RecomputeLoop runs the queued recompute requests (leader only) until ctx is done: when woken
// (Wake, a notification from another replica) and every poll interval.
func (j *Job) RecomputeLoop(ctx context.Context) {
	poll := j.Cfg.HA.RecomputePoll.D()
	if poll <= 0 {
		poll = 2 * time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		j.drainRecomputes(ctx)
		if time.Since(lastPrune) > time.Hour {
			lastPrune = time.Now()
			if n, err := j.Store.PruneRecomputes(ctx, time.Now().Add(-7*24*time.Hour)); err != nil {
				j.Log.WarnContext(ctx, "prune recompute requests", "err", err)
			} else if n > 0 {
				j.Log.InfoContext(ctx, "old recompute requests pruned", "requests", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-j.wakeChan():
		}
	}
}

// drainRecomputes runs queued requests one by one, each claimed only once the run lock is held
// (until then it coalesces new requests for its scope).
func (j *Job) drainRecomputes(ctx context.Context) {
	for ctx.Err() == nil {
		if !j.runClaimed(ctx) {
			return
		}
	}
}

func (j *Job) runClaimed(ctx context.Context) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	req, ok, err := j.Store.ClaimRecompute(ctx)
	if err != nil {
		if ctx.Err() == nil {
			j.Log.WarnContext(ctx, "claim recompute request", "err", err)
		}
		return false
	}
	if !ok {
		return false
	}
	j.Log.InfoContext(ctx, "recompute run started", "run", req.ID, "cluster", req.Cluster, "requestedBy", req.RequestedBy)
	msg := ""
	if err := j.runLocked(ctx, req.ID, j.now(), false, req.Cluster); err != nil {
		msg = err.Error()
		j.Log.ErrorContext(ctx, "recompute run failed", "run", req.ID, "cluster", req.Cluster, "err", err)
	}
	if ctx.Err() != nil {
		// Leadership lost mid-run: the next leader marks it interrupted and queues it again.
		return false
	}
	if err := j.Store.FinishRecompute(ctx, req.ID, msg); err != nil {
		j.Log.WarnContext(ctx, "finish recompute request", "run", req.ID, "err", err)
	}
	return true
}

// WaitTriggered waits until no recompute request is queued or running (tests; needs a running
// RecomputeLoop).
func (j *Job) WaitTriggered() {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		n, err := j.Store.UnfinishedRecomputes(context.Background())
		if err == nil && n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	j.Log.Error("WaitTriggered: recompute requests still unfinished after 1m")
}

// Takeover prepares a new leader: the runs and recompute requests the previous leader
// left unfinished are marked failed (interrupted recompute requests are queued again), and the
// per-cluster run gauges start from the database.
func (j *Job) Takeover(ctx context.Context) error {
	got, err := j.Store.RecoverInterrupted(ctx, func() uuid.UUID { id, _ := uuid.NewV7(); return id })
	if err != nil {
		return err
	}
	if got.Runs > 0 || got.Recomputes > 0 {
		j.Log.WarnContext(ctx, "runs interrupted by a leader change marked failed", "runs", got.Runs, "recomputesQueuedAgain", got.Recomputes)
	}
	return j.Metrics.seedRunGauges(ctx, j.Store)
}

// ReloadInterval applies the stored recommender interval (another replica changed the settings).
func (j *Job) ReloadInterval(ctx context.Context) {
	rec, _, _ := j.loadSettings(ctx)
	j.SetInterval(rec.Interval)
}

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
	j.cur, j.pricing, j.pricingVersion = j.loadSettings(ctx)
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
		if n, perr := j.Store.PruneHistory(ctx, j.now(), h.FullResolution.D(), j.cur.Retention); perr != nil {
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
	eng.Cfg = j.cur.Config
	pricing, pv := j.pricing, j.pricingVersion
	eng.AdjustPricing = func(cluster string, p recommender.Pricing) recommender.Pricing {
		return config.AdjustPricing(pricing, pv, cluster, p)
	}
	eng.ProfilesInUse = func(string) []string {
		var names []string
		for _, s := range reported {
			if s != nil && s.Profile != "" {
				names = append(names, s.Profile)
			}
		}
		return names
	}
	// JVM heap settings the agent reported; unreported workloads fall back to image detection.
	jvm, err := j.Store.ReportedJVM(ctx, cluster)
	if err != nil {
		return err
	}
	eng.JVM = func(k hubapi.WorkloadKey) ([]hubapi.JVMContainer, bool) { c, ok := jvm[k]; return c, ok }
	eng.PricingFallback = j.pricingFallback
	res, err := eng.RunCluster(ctx, cluster, asOf, func(k hubapi.WorkloadKey) *hubapi.ValueSettings { return reported[k] })
	if err != nil {
		return err
	}
	if !backfill {
		if err := j.checkComplete(ctx, cluster, res); err != nil {
			return err
		}
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
		// Approve-once grants apply to one recommendation; a newer one supersedes them.
		if n, err := j.Store.SupersedeStaleApprovals(ctx, cluster, j.now()); err != nil {
			j.Log.WarnContext(ctx, "supersede approvals", "cluster", cluster, "err", err)
		} else if n > 0 {
			j.Log.InfoContext(ctx, "approvals superseded by new recommendations", "cluster", cluster, "approvals", n)
		}
	}
	// Live workloads of the cluster with their team.
	views, err := j.Store.LoadWorkloads(ctx, store.WorkloadFilter{Cluster: cluster})
	if err != nil {
		return err
	}
	perTeam := map[string]hubapi.Savings{}
	if !backfill {
		// The stored total is what GET /v1/summary shows for the cluster from now on (live
		// workloads, latest recommendation each, including ones this run had no data for), so the
		// savings trend ends exactly at the summary.
		savings = clusterSavings(views)
		byTeam := map[string][]*store.WorkloadView{}
		for _, v := range views {
			byTeam[v.Team] = append(byTeam[v.Team], v)
		}
		for t, vs := range byTeam {
			perTeam[t] = clusterSavings(vs)
		}
	} else {
		teamOfKey := map[hubapi.WorkloadKey]string{}
		for _, v := range views {
			teamOfKey[v.Key] = v.Team
		}
		for _, r := range res.Results {
			w := r.Recommendation.Workload
			t := teamOfKey[hubapi.WorkloadKey{Cluster: cluster, Namespace: w.Namespace, Kind: w.Kind, Name: w.Name}]
			perTeam[t] = perTeam[t].Add(r.Recommendation.MonthlySavingsUSD)
		}
	}
	if err := j.Store.RecordRunCluster(ctx, store.RunCluster{
		RunID: runID, Cluster: cluster, AsOf: res.End, Status: "succeeded", Workloads: len(res.Inventory),
		Recommendations: len(res.Results), Savings: savings, Pricing: res.Pricing, FinishedAt: j.now(),
	}); err != nil {
		return err
	}
	// Per team: the source of the trend by team and of team-scoped callers.
	if err := j.Store.RecordRunTeams(ctx, runID, cluster, perTeam); err != nil {
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

// pricingFallback supplies rates for a cluster without pricing series at the end of a run's
// window: a backfill before the series existed uses today's series; otherwise (a gap in
// the series) the rates of the cluster's last run that had them, if not older than a day. Both
// keep source "metrics".
func (j *Job) pricingFallback(ctx context.Context, cluster string, end time.Time) (recommender.Pricing, bool) {
	now := j.now()
	if pl, ok := j.Engine.Loader.(interface {
		PricingAt(ctx context.Context, cluster string, t time.Time) (recommender.Pricing, bool, error)
	}); ok && now.Sub(end) > time.Hour {
		if p, found, err := pl.PricingAt(ctx, cluster, now); err == nil && found {
			j.Log.InfoContext(ctx, "no pricing series at the run's time; using the current rates", "cluster", cluster, "asOf", end)
			return p, true
		}
	}
	p, found, err := j.Store.LastMetricsPricing(ctx, cluster, now.Add(-24*time.Hour))
	if err != nil || !found {
		return recommender.Pricing{}, false
	}
	j.Log.WarnContext(ctx, "no pricing series; using the rates of the cluster's last run", "cluster", cluster)
	return p, true
}

// checkComplete refuses a regular run whose inventory, or number of workloads with request series,
// fell below half of the last accepted one: that is incomplete metrics (a scrape gap, a backend
// reload, regenerated data) far more often than half a cluster disappearing within minutes.
// Refused, the cluster's run fails and the previous results stay current. A drop that lasts
// longer than recommender.incompleteMetricsGrace is accepted as real.
func (j *Job) checkComplete(ctx context.Context, cluster string, res *recommender.ClusterResult) error {
	grace := j.Cfg.Recommender.IncompleteMetricsGrace.D()
	if grace == 0 {
		grace = 30 * time.Minute
	}
	if j.baseline == nil {
		j.baseline, j.incomplete = map[string]clusterBaseline{}, map[string]time.Time{}
	}
	cur := clusterBaseline{inventory: len(res.Inventory), withRequests: res.WithRequests}
	accept := func() error {
		j.baseline[cluster] = cur
		delete(j.incomplete, cluster)
		return nil
	}
	if grace < 0 {
		return accept()
	}
	base, ok := j.baseline[cluster]
	if !ok {
		// After a restart: the stored live inventory; request counts are unknown.
		n, err := j.Store.LiveInventory(ctx, cluster)
		if err != nil {
			return err
		}
		base = clusterBaseline{inventory: n, withRequests: cur.withRequests}
	}
	dropped := base.inventory >= minGuardedInventory &&
		(2*cur.inventory < base.inventory || 2*cur.withRequests < base.withRequests)
	if !dropped {
		return accept()
	}
	since, seen := j.incomplete[cluster]
	if !seen {
		since = j.now()
		j.incomplete[cluster] = since
	}
	if j.now().Sub(since) >= grace {
		j.Log.WarnContext(ctx, "inventory drop persisted; accepting it", "cluster", cluster,
			"workloads", cur.inventory, "previous", base.inventory, "since", since)
		return accept()
	}
	return fmt.Errorf("metrics look incomplete: %d workloads (%d with requests), previously %d (%d); keeping the previous results (accepted after %s)",
		cur.inventory, cur.withRequests, base.inventory, base.withRequests, grace)
}
