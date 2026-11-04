package selfcheck

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultInterval is how often the agent re-runs its self-checks.
const DefaultInterval = 10 * time.Minute

// Runner runs the self-checks at start, every Interval and when the hub asks for a re-check
// (ClusterConfig.flags.recheckRequestedAt newer than the last run).
type Runner struct {
	Checker  *Checker
	Interval time.Duration
	Log      *slog.Logger
	// OnResult is called after every run (e.g. to send the checks with the next status batch).
	OnResult func(Result)

	mu       sync.Mutex
	latest   Result
	has      bool
	lastRun  time.Time
	initOnce sync.Once
	kick     chan struct{}
}

func (r *Runner) init() { r.initOnce.Do(func() { r.kick = make(chan struct{}, 1) }) }

// Latest returns the last result; ok is false before the first run finished.
func (r *Runner) Latest() (Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.latest, r.has
}

// Recheck schedules a run if requestedAt is newer than the start of the last run. It reports
// whether a run was scheduled.
func (r *Runner) Recheck(requestedAt time.Time) bool {
	r.init()
	r.mu.Lock()
	newer := requestedAt.After(r.lastRun)
	r.mu.Unlock()
	if !newer {
		return false
	}
	select {
	case r.kick <- struct{}{}:
	default: // a run is already queued
	}
	return true
}

// RunOnce runs the checks now and stores the result.
func (r *Runner) RunOnce(ctx context.Context) Result {
	start := time.Now()
	if r.Checker.Now != nil {
		start = r.Checker.Now()
	}
	r.mu.Lock()
	r.lastRun = start
	r.mu.Unlock()
	res := r.Checker.Run(ctx)
	r.mu.Lock()
	r.latest, r.has = res, true
	r.mu.Unlock()
	if r.OnResult != nil {
		r.OnResult(res)
	}
	return res
}

// Run blocks until ctx is done.
func (r *Runner) Run(ctx context.Context) {
	r.init()
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	once := func() {
		res := r.RunOnce(ctx)
		var failing []string
		for _, c := range res.Checks {
			if c.Status != "pass" {
				failing = append(failing, c.ID+"="+c.Status)
			}
		}
		log.InfoContext(ctx, "self-checks done", "checks", len(res.Checks), "notPassing", failing)
	}
	once()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			once()
		case <-r.kick:
			once()
		}
	}
}
