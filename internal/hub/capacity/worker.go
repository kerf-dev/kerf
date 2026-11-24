package capacity

import (
	"context"
	"log/slog"
	"time"
)

// historyRetention is how long hourly capacity rows are kept.
const historyRetention = 400 * 24 * time.Hour

// Worker samples every cluster's latest capacity report once per hour on the hub leader and
// stores the per-node-pool totals as history.
type Worker struct {
	DB  *DB
	Log *slog.Logger
	// Now returns the current time (default time.Now).
	Now func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Worker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

// Tick stores the current hour for every cluster whose report is at most an hour old, then
// deletes history past the retention. A failing cluster is logged and does not stop the others.
func (w *Worker) Tick(ctx context.Context) error {
	now := w.now()
	snaps, unreadable, err := w.DB.Snapshots(ctx)
	if err != nil {
		return err
	}
	hour := now.Truncate(time.Hour)
	cutoff := now.Add(-historyRetention)
	for _, c := range unreadable {
		w.log().WarnContext(ctx, "capacity history: unreadable report", "cluster", c)
		if err := w.prune(ctx, c, cutoff); err != nil {
			return err
		}
	}
	for _, s := range snaps {
		if now.Sub(s.ReportedAt) <= time.Hour {
			if err := w.DB.SaveHour(ctx, s.Cluster, hour, Rollup(s.Report)); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				w.log().WarnContext(ctx, "capacity history: cluster", "cluster", s.Cluster, "err", err)
			}
		}
		if err := w.prune(ctx, s.Cluster, cutoff); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) prune(ctx context.Context, cluster string, before time.Time) error {
	if _, err := w.DB.PruneHistory(ctx, cluster, before); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.log().WarnContext(ctx, "capacity history: prune", "cluster", cluster, "err", err)
	}
	return nil
}

// nextRun is the first minute past an hour boundary (hh:01:00 UTC) after now.
func nextRun(now time.Time) time.Time {
	t := now.UTC().Truncate(time.Hour).Add(time.Minute)
	if !t.After(now) {
		t = t.Add(time.Hour)
	}
	return t
}

// Run ticks at start and at each hour boundary plus one minute until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			w.log().WarnContext(ctx, "capacity history", "err", err)
		}
		timer.Reset(nextRun(w.now()).Sub(w.now()))
	}
}
