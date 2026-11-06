// Package realized computes realized savings: the requests Kerf actually released by the changes
// it applied, times the replicas that ran, priced at the rates of the time. The numbers are
// materialised per workload and hour in Postgres (realized_savings_hourly) by the hub leader and
// kept strictly apart from the estimated savings of the recommendations. The method is documented
// in docs/realized-savings.md.
package realized

import (
	"slices"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Audit event types that change a workload's requests (docs/contracts/events.md).
const (
	TypeApplied    = "applied"
	TypeRolledBack = "rolled_back"
	// TypeReleased means the workload left auto-patch and Kerf handed its fields back (GitOps then
	// restores the Git values). Kerf stops counting the workload's released requests from then on.
	TypeReleased = "released"
)

// LedgerTypes are the event types the ledger reads.
var LedgerTypes = []string{TypeApplied, TypeRolledBack, TypeReleased}

// GiB is 2^30 bytes.
const GiB = float64(1 << 30)

// Change is one stored audit event of a workload that changed (or handed back) its requests.
type Change struct {
	Time       time.Time
	Type       string
	Resources  []string
	Containers []hubapi.ContainerChange
}

// Released is the requests released per replica, summed over containers: CPU in cores, memory in
// bytes. Negative values mean Kerf raised the requests above where they were before its first change.
type Released struct {
	CPU    float64
	Memory float64
}

// IsZero reports whether nothing is released.
func (r Released) IsZero() bool { return r.CPU == 0 && r.Memory == 0 }

// Step is the released requests from Time until the next step.
type Step struct {
	Time     time.Time
	Released Released
}

// Ledger turns a workload's changes into a step function of its released requests.
//
// Every applied or rolled_back event moves each listed container's request by (from - to), so a
// decrease adds, a later increase subtracts and a rollback (from = applied, to = restored) takes
// the release back from the moment it happened. The running sum telescopes to "requests before
// Kerf's first change minus requests now", without trusting any single event to carry both. A
// released event (the workload left auto-patch) resets the listed resources (all when none are
// listed) to zero: Kerf no longer claims them, whatever values stay live. A container whose from
// or to request is missing or unparsable is skipped for that resource and counted in skipped.
func Ledger(changes []Change) (steps []Step, skipped int) {
	sorted := slices.Clone(changes)
	slices.SortStableFunc(sorted, func(a, b Change) int { return a.Time.Compare(b.Time) })
	state := map[string]*Released{}
	get := func(name string) *Released {
		if state[name] == nil {
			state[name] = &Released{}
		}
		return state[name]
	}
	for _, c := range sorted {
		switch c.Type {
		case TypeApplied, TypeRolledBack:
			for _, ct := range c.Containers {
				from, to := requests(ct.From), requests(ct.To)
				if d, ok, present := delta(from.CPU, to.CPU, recommender.ParseCPU); ok {
					get(ct.Name).CPU += d
				} else if present {
					skipped++
				}
				if d, ok, present := delta(from.Memory, to.Memory, recommender.ParseMemory); ok {
					get(ct.Name).Memory += d
				} else if present {
					skipped++
				}
			}
		case TypeReleased:
			cpu, mem := len(c.Resources) == 0, len(c.Resources) == 0
			for _, r := range c.Resources {
				cpu = cpu || r == "cpu"
				mem = mem || r == "memory"
			}
			for _, r := range state {
				if cpu {
					r.CPU = 0
				}
				if mem {
					r.Memory = 0
				}
			}
		default:
			continue
		}
		var sum Released
		for _, r := range state {
			sum.CPU += r.CPU
			sum.Memory += r.Memory
		}
		// Clean float noise so a full rollback is exactly zero.
		sum.CPU, sum.Memory = clean(sum.CPU), clean(sum.Memory)
		if n := len(steps); n > 0 && steps[n-1].Time.Equal(c.Time) {
			steps[n-1].Released = sum
		} else {
			steps = append(steps, Step{Time: c.Time, Released: sum})
		}
	}
	return steps, skipped
}

func requests(r *hubapi.Resources) hubapi.ResourceValues {
	if r == nil || r.Requests == nil {
		return hubapi.ResourceValues{}
	}
	return *r.Requests
}

// delta parses from and to; ok when both parse, present when either is set.
func delta(from, to string, parse func(string) (float64, error)) (d float64, ok, present bool) {
	present = from != "" || to != ""
	if from == "" || to == "" {
		return 0, false, present
	}
	f, err1 := parse(from)
	t, err2 := parse(to)
	if err1 != nil || err2 != nil {
		return 0, false, present
	}
	return f - t, true, present
}

func clean(v float64) float64 {
	if v > -1e-9 && v < 1e-9 {
		return 0
	}
	return v
}

// Average is the time-weighted mean of the step function over [from, to). Before the first step
// nothing is released.
func Average(steps []Step, from, to time.Time) Released {
	span := to.Sub(from)
	if span <= 0 {
		return Released{}
	}
	var acc Released
	for i, s := range steps {
		start := s.Time
		end := to
		if i+1 < len(steps) {
			end = steps[i+1].Time
		}
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			continue
		}
		w := float64(end.Sub(start)) / float64(span)
		acc.CPU += s.Released.CPU * w
		acc.Memory += s.Released.Memory * w
	}
	acc.CPU, acc.Memory = clean(acc.CPU), clean(acc.Memory)
	return acc
}

// At returns the released requests in effect at t.
func At(steps []Step, t time.Time) Released {
	var r Released
	for _, s := range steps {
		if s.Time.After(t) {
			break
		}
		r = s.Released
	}
	return r
}
