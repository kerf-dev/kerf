package recommender

import (
	"math"
	"math/rand/v2"
	"time"
)

// Hand-written fixtures: usage patterns generated in Go (the testdata/series fixtures can be
// added to the table once they exist). Each generator returns per-step samples ending at end.

var testEnd = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

const testStep = 5 * time.Minute

// gen produces samples over [end-days, end] every step; f gets the step index and time.
func gen(days float64, f func(i int, t time.Time) (float64, bool)) []Sample {
	n := int(days * 24 * float64(time.Hour/testStep))
	out := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		t := testEnd.Add(-time.Duration(n-1-i) * testStep)
		if v, ok := f(i, t); ok {
			out = append(out, Sample{T: t, V: v})
		}
	}
	return out
}

func noise(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed*7+1)) }

func flat(days, v, jitter float64, seed uint64) []Sample {
	r := noise(seed)
	return gen(days, func(int, time.Time) (float64, bool) { return v * (1 + jitter*(r.Float64()*2-1)), true })
}

// diurnal: base + amp * max(0, sin) over a 24 h period (peak at 06:00 UTC).
func diurnal(days, base, amp float64) []Sample {
	return gen(days, func(_ int, t time.Time) (float64, bool) {
		h := float64(t.Hour()) + float64(t.Minute())/60
		return base + amp*math.Max(0, math.Sin(2*math.Pi*h/24)), true
	})
}

// weekly: busy on weekdays, quiet on weekends.
func weekly(days, weekday, weekend float64) []Sample {
	return gen(days, func(_ int, t time.Time) (float64, bool) {
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
			return weekend, true
		}
		return weekday, true
	})
}

// spiky: base with a spike every `every` steps.
func spiky(days, base, spike float64, every int) []Sample {
	return gen(days, func(i int, _ time.Time) (float64, bool) {
		if i%every == 0 {
			return spike, true
		}
		return base, true
	})
}

// leak: linear growth from start to end value, reset every period (restart).
func leak(days, from, to float64) []Sample {
	n := days * 24 * float64(time.Hour/testStep)
	return gen(days, func(i int, _ time.Time) (float64, bool) { return from + (to-from)*float64(i)/n, true })
}

// stepChange: before until the last `afterDays`, then after.
func stepChange(days, before, after, afterDays float64) []Sample {
	cut := testEnd.Add(-time.Duration(afterDays * float64(24*time.Hour)))
	return gen(days, func(_ int, t time.Time) (float64, bool) {
		if t.After(cut) {
			return after, true
		}
		return before, true
	})
}

// sparse: present only in the first `run` steps of every `period` steps (a CronJob).
func sparse(days, v float64, run, period int) []Sample {
	return gen(days, func(i int, _ time.Time) (float64, bool) { return v, i%period < run })
}

func podsConst(days, n float64) []Sample {
	return gen(days, func(int, time.Time) (float64, bool) { return n, true })
}
