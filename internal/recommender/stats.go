package recommender

import (
	"math"
	"sort"
	"time"
)

// Sample is one point of a time series.
type Sample struct {
	T time.Time
	V float64
}

// StatsFromSamples computes Stats with the same semantics as the PromQL *_over_time functions
// used by the Prometheus source (quantile_over_time interpolates linearly, stddev is the
// population standard deviation). It is used by tests and by in-memory sources.
func StatsFromSamples(samples []Sample, quantiles []float64) Stats {
	s := Stats{Samples: len(samples), Quantiles: map[float64]float64{}}
	if len(samples) == 0 {
		return s
	}
	vals := make([]float64, len(samples))
	s.First, s.Last = samples[0].T, samples[0].T
	sum := 0.0
	s.Max = math.Inf(-1)
	for i, p := range samples {
		vals[i] = p.V
		sum += p.V
		if p.V > s.Max {
			s.Max = p.V
		}
		if p.T.Before(s.First) {
			s.First = p.T
		}
		if p.T.After(s.Last) {
			s.Last = p.T
		}
	}
	s.Mean = sum / float64(len(vals))
	sq := 0.0
	for _, v := range vals {
		sq += (v - s.Mean) * (v - s.Mean)
	}
	s.Stddev = math.Sqrt(sq / float64(len(vals)))
	sort.Float64s(vals)
	for _, q := range quantiles {
		s.Quantiles[q] = quantile(q, vals)
	}
	return s
}

// quantile implements PromQL's quantile over sorted values.
func quantile(q float64, sorted []float64) float64 {
	n := float64(len(sorted))
	rank := q * (n - 1)
	lo := math.Max(0, math.Floor(rank))
	hi := math.Min(n-1, lo+1)
	w := rank - math.Floor(rank)
	return sorted[int(lo)]*(1-w) + sorted[int(hi)]*w
}

// MaxSince returns the maximum of samples at or after t (0 when none).
func MaxSince(samples []Sample, t time.Time) float64 {
	m := 0.0
	for _, p := range samples {
		if !p.T.Before(t) && p.V > m {
			m = p.V
		}
	}
	return m
}
