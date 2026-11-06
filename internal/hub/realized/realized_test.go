package realized

import (
	"math"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func day(n float64) time.Time { return t0.Add(time.Duration(n * 24 * float64(time.Hour))) }

func req(cpu, mem string) *hubapi.Resources {
	return &hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: cpu, Memory: mem}}
}

func change(at time.Time, typ string, containers ...hubapi.ContainerChange) Change {
	return Change{Time: at, Type: typ, Containers: containers}
}

func ct(name string, from, to *hubapi.Resources) hubapi.ContainerChange {
	return hubapi.ContainerChange{Name: name, From: from, To: to}
}

// rates: $0.04 per core-hour, $0.005 per GiB-hour.
var rates = Rates{CPUCoreHour: 0.04, MemoryGiBHour: 0.005, Source: "metrics"}

// total prices every hour of [from, to) with a constant replica count and the test rates.
func total(steps []Step, from, to time.Time, replicas float64) (cpu, mem float64) {
	for _, h := range Hours(steps, from, to, func(time.Time) (HourInput, bool) {
		return HourInput{Replicas: replicas, ReplicaSource: ReplicasMetrics, Rates: rates}, true
	}) {
		cpu += h.CPUUSD
		mem += h.MemoryUSD
	}
	return cpu, mem
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func TestRealizedAmounts(t *testing.T) {
	end := day(30)
	tests := []struct {
		name     string
		changes  []Change
		replicas float64
		cpuUSD   float64
		memUSD   float64
	}{
		{
			// The acceptance case: 1 core released for 30 days at $0.04/core-hour = 720 h x 0.04.
			name:     "one core for 30 days",
			changes:  []Change{change(day(0), TypeApplied, ct("app", req("2", ""), req("1", "")))},
			replicas: 1, cpuUSD: 28.80,
		},
		{
			name:     "one core for 30 days on three replicas",
			changes:  []Change{change(day(0), TypeApplied, ct("app", req("2", ""), req("1", "")))},
			replicas: 3, cpuUSD: 86.40,
		},
		{
			name:     "millicores and memory",
			changes:  []Change{change(day(0), TypeApplied, ct("app", req("1500m", "3Gi"), req("500m", "1Gi")))},
			replicas: 1, cpuUSD: 28.80, memUSD: 7.20, // 2 GiB x 720 h x 0.005
		},
		{
			// A rollback after 10 days stops the saving from that moment: 240 h x 0.04.
			name: "rollback stops the saving",
			changes: []Change{
				change(day(0), TypeApplied, ct("app", req("2", ""), req("1", ""))),
				change(day(10), TypeRolledBack, ct("app", req("1", ""), req("2", ""))),
			},
			replicas: 1, cpuUSD: 9.60,
		},
		{
			// Rolled back in the middle of an hour: that hour counts half.
			name: "rollback mid-hour",
			changes: []Change{
				change(day(0), TypeApplied, ct("app", req("2", ""), req("1", ""))),
				change(day(10).Add(30*time.Minute), TypeRolledBack, ct("app", req("1", ""), req("2", ""))),
			},
			replicas: 1, cpuUSD: 9.62, // 240.5 h x 0.04
		},
		{
			// A later increase of 0.5 core reduces it: 480 h x 1 core + 240 h x 0.5 core.
			name: "later increase reduces it",
			changes: []Change{
				change(day(0), TypeApplied, ct("app", req("2", ""), req("1", ""))),
				change(day(20), TypeApplied, ct("app", req("1", ""), req("1500m", ""))),
			},
			replicas: 1, cpuUSD: 24.00,
		},
		{
			// An increase beyond the original request costs money: -0.5 core for the last 10 days.
			name: "increase above the original request",
			changes: []Change{
				change(day(0), TypeApplied, ct("app", req("2", ""), req("1", ""))),
				change(day(20), TypeApplied, ct("app", req("1", ""), req("2500m", ""))),
			},
			replicas: 1, cpuUSD: 19.20 - 4.80,
		},
		{
			// Leaving auto-patch hands the fields back: Kerf stops counting from then on.
			name: "handed back after 15 days",
			changes: []Change{
				change(day(0), TypeApplied, ct("app", req("2", "2Gi"), req("1", "1Gi"))),
				{Time: day(15), Type: TypeReleased},
			},
			replicas: 1, cpuUSD: 14.40, memUSD: 1.80,
		},
		{
			name: "handed back for memory only",
			changes: []Change{
				change(day(0), TypeApplied, ct("app", req("2", "2Gi"), req("1", "1Gi"))),
				{Time: day(15), Type: TypeReleased, Resources: []string{"memory"}},
			},
			replicas: 1, cpuUSD: 28.80, memUSD: 1.80,
		},
		{
			// Two containers, one listed per event; a second decrease adds to the first.
			name: "two containers and two decreases",
			changes: []Change{
				change(day(0), TypeApplied, ct("app", req("2", ""), req("1", "")), ct("sidecar", req("500m", ""), req("250m", ""))),
				change(day(10), TypeApplied, ct("app", req("1", ""), req("500m", ""))),
			},
			replicas: 1, cpuUSD: 0.04 * (1.25*720 + 0.5*480), // 36 + 9.6
		},
		{
			name:     "change before the window counts from the window start",
			changes:  []Change{change(day(-5), TypeApplied, ct("app", req("2", ""), req("1", "")))},
			replicas: 1, cpuUSD: 28.80,
		},
		{
			name:     "no requests in the event: nothing counted",
			changes:  []Change{change(day(0), TypeApplied, ct("app", &hubapi.Resources{}, &hubapi.Resources{}))},
			replicas: 1,
		},
		{
			name:     "skipped events are ignored",
			changes:  []Change{{Time: day(0), Type: "skipped", Containers: []hubapi.ContainerChange{ct("app", req("2", ""), req("1", ""))}}},
			replicas: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			steps, _ := Ledger(tc.changes)
			cpu, mem := total(steps, day(0), end, tc.replicas)
			if round2(cpu) != round2(tc.cpuUSD) || round2(mem) != round2(tc.memUSD) {
				t.Fatalf("cpu %.4f memory %.4f, want %.2f and %.2f", cpu, mem, tc.cpuUSD, tc.memUSD)
			}
		})
	}
}

func TestLedgerSkipsUnparsable(t *testing.T) {
	steps, skipped := Ledger([]Change{
		change(day(0), TypeApplied, ct("app", req("2", "lots"), req("1", "1Gi")), ct("other", req("", "1Gi"), req("", ""))),
	})
	if skipped != 2 {
		t.Fatalf("skipped %d, want 2", skipped)
	}
	if got := At(steps, day(1)); got != (Released{CPU: 1}) {
		t.Fatalf("released %+v", got)
	}
}

func TestLedgerOrderAndRollbackIsExactlyZero(t *testing.T) {
	// Events arrive out of order (buffered by the agent); the ledger sorts by time.
	steps, _ := Ledger([]Change{
		change(day(1), TypeRolledBack, ct("app", req("300m", "100Mi"), req("1", "300Mi"))),
		change(day(0), TypeApplied, ct("app", req("1", "300Mi"), req("300m", "100Mi"))),
	})
	if len(steps) != 2 || !steps[0].Time.Equal(day(0)) {
		t.Fatalf("steps %+v", steps)
	}
	if got := At(steps, day(2)); !got.IsZero() {
		t.Fatalf("after the rollback %+v, want zero", got)
	}
	if got := At(steps, day(0).Add(-time.Second)); !got.IsZero() {
		t.Fatalf("before the change %+v", got)
	}
}

func TestPriceReplicaSources(t *testing.T) {
	steps, _ := Ledger([]Change{change(day(0), TypeApplied, ct("app", req("2", ""), req("1", "")))})
	tests := []struct {
		src  string
		reps float64
		want float64
	}{
		{ReplicasMetrics, 2.5, 0.10},
		{ReplicasRecommendation, 2, 0.08},
		{ReplicasNone, 3, 0},
	}
	for _, tc := range tests {
		h := Price(steps, HourInput{Start: day(1), Replicas: tc.reps, ReplicaSource: tc.src, Rates: rates})
		if round2(h.CPUUSD) != tc.want {
			t.Errorf("%s: %.4f, want %.2f", tc.src, h.CPUUSD, tc.want)
		}
	}
}

func TestHoursSkipsEmptyHours(t *testing.T) {
	steps, _ := Ledger([]Change{
		change(day(0).Add(2*time.Hour), TypeApplied, ct("app", req("2", ""), req("1", ""))),
		change(day(0).Add(4*time.Hour), TypeRolledBack, ct("app", req("1", ""), req("2", ""))),
	})
	hs := Hours(steps, day(0), day(1), func(time.Time) (HourInput, bool) {
		return HourInput{Replicas: 1, ReplicaSource: ReplicasMetrics, Rates: rates}, true
	})
	if len(hs) != 2 || !hs[0].Start.Equal(day(0).Add(2*time.Hour)) {
		t.Fatalf("hours %+v", hs)
	}
}
