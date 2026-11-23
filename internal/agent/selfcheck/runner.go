package selfcheck

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultInterval is how often the agent re-runs its self-checks.
const DefaultInterval = 10 * time.Minute

// DefaultInputPoll is how often the runner looks for hub inputs that the last run did not have.
const DefaultInputPoll = 5 * time.Second

// Runner runs the self-checks at start, every Interval, when the hub asks for a re-check
// (ClusterConfig.flags.recheckRequestedAt newer than the last run), and when a hub input that
// the last run did not have arrives (see InputPoll).
type Runner struct {
	Checker  *Checker
	Interval time.Duration
	// InputPoll is how often Run reads the hub inputs (clock, namespace coverage) while the last
	// run did not have all of them. When one arrives, the checks run again at once: in listen
	// mode the hub connects after the first run, and these checks would stay unknown until the
	// next interval.
	InputPoll time.Duration
	Log       *slog.Logger
	// OnResult is called after every run (e.g. to send the checks with the next status batch).
	OnResult func(Result)

	mu       sync.Mutex
	latest   Result
	has      bool
	lastRun  time.Time
	hubSeen  hubInputs // the hub inputs the last run had
	initOnce sync.Once
	kick     chan struct{}
}

// hubInputs tells which hub-derived inputs are set.
type hubInputs struct{ clock, coverage bool }

func (r *Runner) hubInputs() hubInputs {
	if r.Checker.Inputs == nil {
		return hubInputs{true, true} // nothing to wait for
	}
	in := r.Checker.Inputs()
	return hubInputs{!in.HubDate.IsZero() && !in.LocalAt.IsZero(), in.Excluded != nil}
}

// newHubInput reports whether a hub input arrived after the last run, and whether the last run
// had all of them.
func (r *Runner) newHubInput() (arrived, all bool) {
	r.mu.Lock()
	seen := r.hubSeen
	r.mu.Unlock()
	if seen.clock && seen.coverage {
		return false, true
	}
	cur := r.hubInputs()
	return cur.clock && !seen.clock || cur.coverage && !seen.coverage, false
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
	seen := r.hubInputs()
	r.mu.Lock()
	r.lastRun, r.hubSeen = start, seen
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
	pollEvery := r.InputPoll
	if pollEvery <= 0 {
		pollEvery = DefaultInputPoll
	}
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
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
		case <-poll.C:
			arrived, all := r.newHubInput()
			if all {
				poll.Stop() // all hub inputs are known: the interval and the recheck flag do the rest
			} else if arrived {
				once()
			}
		}
	}
}
