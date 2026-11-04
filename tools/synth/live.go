package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tomer1983/resource-optimizer/tools/synth/synth"
)

// liveState is what `live` needs to continue a backfill: the backfill's timeline start, its
// anchor, its seed, and the last sample already written.
type liveState struct {
	start, anchor, last time.Time
	seed                int64
}

// live keeps synthetic clusters current: every interval it extends the backfilled timeline to
// now and writes only the new samples. The simulation is deterministic and anchored to the
// backfill, so the continuation is seamless (no counter resets, same pods).
func live(args []string) error {
	fs := flag.NewFlagSet("live", flag.ExitOnError)
	scenario := fs.String("scenario", "tools/synth/scenarios/fleet-default.yaml", "scenario YAML file (the one that was backfilled)")
	vmURL := fs.String("vm-url", "http://localhost:18428", "VictoriaMetrics base URL")
	interval := fs.Duration("interval", time.Minute, "how often to write new samples")
	fs.Parse(args)
	sc, err := synth.LoadScenario(*scenario)
	if err != nil {
		return err
	}
	q := &prom{base: strings.TrimRight(*vmURL, "/"), client: &http.Client{Timeout: time.Minute}}
	st, err := readLiveState(q, sc)
	if err != nil {
		return err
	}
	return runLive(sc, *vmURL, st, *interval)
}

func readLiveState(q *prom, sc *synth.Scenario) (liveState, error) {
	var st liveState
	now := time.Now()
	for i, c := range sc.Clusters {
		sel := fmt.Sprintf(`cluster=%q, scenario=%q`, c.Name, sc.Name)
		start, err := q.scalar(fmt.Sprintf(`max(last_over_time(synth_scenario_start_timestamp_seconds{%s}[35d]))`, sel), now)
		if err != nil || math.IsNaN(start) {
			return st, fmt.Errorf("cluster %s: no backfill of scenario %q found in VictoriaMetrics (run `make synth-backfill` first): %v", c.Name, sc.Name, err)
		}
		anchor, _ := q.scalar(fmt.Sprintf(`max(last_over_time(synth_scenario_anchor_timestamp_seconds{%s}[35d]))`, sel), now)
		seedStr, err := q.label(fmt.Sprintf(`last_over_time(synth_scenario_start_timestamp_seconds{%s}[35d])`, sel), "seed", now)
		if err != nil {
			return st, err
		}
		last, err := q.scalar(fmt.Sprintf(`max(tlast_over_time(kube_node_labels{cluster=%q}[35d]))`, c.Name), now)
		if err != nil || math.IsNaN(last) {
			return st, fmt.Errorf("cluster %s: cannot find the last sample: %v", c.Name, err)
		}
		cur := liveState{start: time.Unix(int64(start), 0), anchor: time.Unix(int64(anchor), 0), last: time.Unix(int64(last), 0)}
		cur.seed, err = strconv.ParseInt(seedStr, 10, 64)
		if err != nil {
			return st, fmt.Errorf("cluster %s: seed label %q: %w", c.Name, seedStr, err)
		}
		if i == 0 {
			st = cur
			continue
		}
		if cur.start != st.start || cur.anchor != st.anchor || cur.seed != st.seed {
			return st, fmt.Errorf("clusters of %q come from different backfills; run `make synth-backfill` again", sc.Name)
		}
		if cur.last.Before(st.last) {
			st.last = cur.last
		}
	}
	return st, nil
}

func runLive(sc *synth.Scenario, vmURL string, st liveState, interval time.Duration) error {
	// Each tick allocates the whole simulation and drops it; a soft limit makes the GC give the
	// memory back between ticks instead of holding ~1GB for a long-running background process.
	debug.SetMemoryLimit(384 << 20)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	step := sc.Step.D()
	slog.Info("live", "scenario", sc.Name, "start", st.start.UTC().Format(time.RFC3339), "anchor", st.anchor.UTC().Format(time.RFC3339),
		"seed", st.seed, "lastSample", st.last.UTC().Format(time.RFC3339), "interval", interval)
	for {
		end := time.Now().Truncate(step)
		if end.After(st.last) {
			t0 := time.Now()
			n, err := extend(sc, vmURL, st, end)
			if err != nil {
				slog.Error("live update failed; retrying next interval", "err", err)
			} else {
				slog.Info("live update", "from", st.last.Add(step).UTC().Format(time.RFC3339), "to", end.UTC().Format(time.RFC3339),
					"samples", n, "took", time.Since(t0).Round(time.Millisecond))
				st.last = end
			}
		}
		select {
		case <-ctx.Done():
			slog.Info("live stopped")
			return nil
		case <-time.After(time.Until(time.Now().Truncate(interval).Add(interval + 2*time.Second))):
		}
	}
}

// extend writes the samples in (st.last, end].
func extend(sc *synth.Scenario, vmURL string, st liveState, end time.Time) (int64, error) {
	seed := st.seed
	g, err := synth.New(sc, synth.Options{Start: st.start, Anchor: st.anchor, End: end, Seed: &seed})
	if err != nil {
		return 0, err
	}
	tl := g.Timeline
	from := int(st.last.Sub(tl.Start)/tl.Step) + 1
	vw := synth.NewVMWriter(vmURL, tl, synth.VMOptions{Workers: 4})
	emit := synth.Clip(vw.Emit, from, tl.N)
	for _, cm := range g.Clusters {
		if err := g.EmitNodes(cm, emit); err != nil {
			vw.Close()
			return 0, err
		}
	}
	err = g.Each(4, func(wt *synth.WorkloadTrace) error { return g.EmitWorkload(wt, emit) })
	if cerr := vw.Close(); err == nil {
		err = cerr
	}
	// No cache reset: these samples are at "now", which VictoriaMetrics never caches.
	return vw.Samples.Load(), err
}
