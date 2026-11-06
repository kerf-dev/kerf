package realized

import "time"

// Rates are the hourly prices of a cluster: USD per CPU core and per GiB of memory.
type Rates struct {
	CPUCoreHour   float64
	MemoryGiBHour float64
	// Source is "metrics" (the pricing service, as stored by the recommender run of the time) or
	// "default" (no stored rates: the recommender's default rates).
	Source string
}

// Where an hour's replica count came from.
const (
	// ReplicasMetrics: the average number of scheduled pods over the hour (kube-state-metrics).
	ReplicasMetrics = "metrics"
	// ReplicasRecommendation: the metrics had no data for the cluster in that hour; the average
	// replicas of the workload's latest stored recommendation at the time.
	ReplicasRecommendation = "recommendation"
	// ReplicasNone: neither exists; the hour is not counted (zero USD).
	ReplicasNone = "none"
)

// HourInput is what pricing one workload-hour needs besides the ledger.
type HourInput struct {
	Start         time.Time
	Replicas      float64
	ReplicaSource string
	Rates         Rates
}

// Hour is one materialised workload-hour.
type Hour struct {
	Start time.Time
	// Released is the per-replica release, time-weighted over the hour.
	Released      Released
	Replicas      float64
	ReplicaSource string
	Rates         Rates
	CPUUSD        float64
	MemoryUSD     float64
}

// Price computes the realized saving of one workload-hour:
//
//	cpuUSD    = released cores (time-weighted over the hour) x replicas x USD per core-hour
//	memoryUSD = released GiB   (time-weighted over the hour) x replicas x USD per GiB-hour
func Price(steps []Step, in HourInput) Hour {
	h := Hour{
		Start: in.Start, Released: Average(steps, in.Start, in.Start.Add(time.Hour)),
		Replicas: in.Replicas, ReplicaSource: in.ReplicaSource, Rates: in.Rates,
	}
	if in.ReplicaSource == ReplicasNone {
		h.Replicas = 0
	}
	h.CPUUSD = h.Released.CPU * h.Replicas * in.Rates.CPUCoreHour
	h.MemoryUSD = h.Released.Memory / GiB * h.Replicas * in.Rates.MemoryGiBHour
	return h
}

// Hours prices every hour of [from, to) (both on hour boundaries) for which input returns ok and
// something was released, in order.
func Hours(steps []Step, from, to time.Time, input func(hour time.Time) (HourInput, bool)) []Hour {
	var out []Hour
	for t := from; t.Before(to); t = t.Add(time.Hour) {
		if Average(steps, t, t.Add(time.Hour)).IsZero() {
			continue
		}
		in, ok := input(t)
		if !ok {
			continue
		}
		in.Start = t
		out = append(out, Price(steps, in))
	}
	return out
}
