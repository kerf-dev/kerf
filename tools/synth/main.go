// Command synth generates synthetic Kubernetes usage history for Kerf.
//
//	synth backfill --scenario tools/synth/scenarios/fleet-default.yaml --vm-url http://localhost:18428 --replace
//	synth fixtures --scenario tools/synth/scenarios/fleet-small.yaml --out testdata/series
//	synth describe --scenario tools/synth/scenarios/fleet-default.yaml [--manifest fleet.json]
//
// See tools/synth/README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kerf-dev/kerf/tools/synth/synth"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "backfill":
		err = backfill(os.Args[2:])
	case "fixtures":
		err = fixtures(os.Args[2:])
	case "describe":
		err = describe(os.Args[2:])
	case "delete":
		err = deleteCmd(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "live":
		err = live(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		slog.Error(os.Args[1]+" failed", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: synth <command> [flags]

commands:
  backfill   generate a scenario and import it into VictoriaMetrics (/api/v1/import)
  fixtures   write per-workload JSON fixtures (testdata/series) for unit tests
  describe   print what a scenario expands to (nodes, workloads, volume); --manifest writes ground truth
  live       keep a backfilled scenario current: append new samples every minute (seamless continuation)
  verify     check with PromQL that a backfilled scenario is in VictoriaMetrics with the expected shapes
  delete     delete all series of synthetic clusters (synth-*) from VictoriaMetrics

run "synth <command> -h" for flags`)
}

// common flags shared by all commands.
type common struct {
	scenario  string
	days      float64
	seed      int64
	end       string
	step      time.Duration
	clusters  stringList
	namespace string
	workload  string
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, strings.Split(v, ",")...); return nil }

func (c *common) register(fs *flag.FlagSet, defScenario string) {
	fs.StringVar(&c.scenario, "scenario", defScenario, "scenario YAML file")
	fs.Float64Var(&c.days, "days", 0, "days of history (default: scenario's, usually 30)")
	fs.Int64Var(&c.seed, "seed", -1, "random seed (default: scenario's)")
	fs.StringVar(&c.end, "end", "", "end time, RFC3339 (default: now)")
	fs.DurationVar(&c.step, "step", 0, "sample interval (default: scenario's, usually 60s)")
	fs.Var(&c.clusters, "cluster", "only these clusters (repeatable or comma separated)")
	fs.StringVar(&c.namespace, "namespace", "", "only workloads in this namespace")
	fs.StringVar(&c.workload, "workload", "", "only the workload with this name")
}

func (c *common) generator() (*synth.Generator, error) {
	sc, err := synth.LoadScenario(c.scenario)
	if err != nil {
		return nil, err
	}
	opt := synth.Options{Days: c.days, Step: c.step, Clusters: c.clusters}
	if c.seed >= 0 {
		opt.Seed = &c.seed
	}
	if c.end != "" {
		t, err := time.Parse(time.RFC3339, c.end)
		if err != nil {
			return nil, fmt.Errorf("--end: %w", err)
		}
		opt.End = t
	}
	g, err := synth.New(sc, opt)
	if err != nil {
		return nil, err
	}
	if c.namespace != "" || c.workload != "" {
		for _, cm := range g.Clusters {
			var keep []*synth.WorkloadSpec
			for _, w := range cm.Workloads {
				if (c.namespace == "" || w.Namespace == c.namespace) && (c.workload == "" || w.Name == c.workload) {
					keep = append(keep, w)
				}
			}
			cm.Workloads = keep
		}
	}
	return g, nil
}

func (c *common) filtered() bool { return c.namespace != "" || c.workload != "" }

func backfill(args []string) error {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	var c common
	c.register(fs, "tools/synth/scenarios/fleet-default.yaml")
	vmURL := fs.String("vm-url", "http://localhost:18428", "VictoriaMetrics base URL")
	replace := fs.Bool("replace", false, "delete existing series of the scenario's clusters first")
	workers := fs.Int("workers", 8, "concurrent import requests")
	parallel := fs.Int("parallel", 4, "workloads simulated concurrently")
	gz := fs.Bool("gzip", false, "gzip import requests")
	format := fs.String("format", "remote-write", "remote-write (/api/v1/write) or json (/api/v1/import)")
	batch := fs.Int("batch-kib", 2048, "import request size in KiB")
	maxRSS := fs.Float64("max-vm-memory", 0.8, "pause while VictoriaMetrics' Go memory is above this fraction of its memory limit (negative: off)")
	dry := fs.Bool("dry-run", false, "generate and count, do not import")
	follow := fs.Bool("follow", false, "after the backfill, keep appending samples like `synth live`")
	chunk := fs.Duration("chunk", 0, "import in time order, this much history at a time (0: whole window per series)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	g, err := c.generator()
	if err != nil {
		return err
	}
	tl := g.Timeline
	ctx := context.Background()
	admin := synth.VMAdmin{URL: *vmURL}
	var names []string
	for _, cm := range g.Clusters {
		names = append(names, cm.Spec.Name)
	}
	slog.Info("backfill", "scenario", g.Scenario.Name, "clusters", names, "from", tl.Start.UTC().Format(time.RFC3339),
		"to", tl.End().UTC().Format(time.RFC3339), "step", tl.Step, "seed", g.Scenario.Seed, "vm", *vmURL, "dryRun", *dry)
	if *replace && !*dry {
		if c.filtered() {
			return errors.New("--replace deletes whole clusters; do not combine it with --namespace/--workload")
		}
		slog.Info("deleting existing series", "clusters", names)
		if err := admin.DeleteClusters(ctx, names); err != nil {
			return err
		}
	}

	var emit synth.Emit
	var vw *synth.VMWriter
	var nSeries, nSamples atomic.Int64
	if *dry {
		emit = func(s *synth.Series) error { nSeries.Add(1); nSamples.Add(int64(s.Len)); return nil }
	} else {
		vw = synth.NewVMWriter(*vmURL, tl, synth.VMOptions{Format: *format, Workers: *workers, Gzip: *gz, BatchBytes: *batch << 10, MaxRSSFraction: *maxRSS})
		emit = vw.Emit
	}
	start := time.Now()
	var done atomic.Int64
	total := 0
	for _, cm := range g.Clusters {
		total += len(cm.Workloads)
	}
	// VictoriaMetrics ingests backfills with far less memory when data arrives roughly in time
	// order, so the window is imported chunk by chunk. Simulation is deterministic: every chunk
	// re-simulates each workload and keeps only its slice of samples.
	step := tl.N
	if *chunk > 0 {
		step = max(1, int(*chunk/tl.Step))
	}
	chunks := (tl.N + step - 1) / step
	total *= chunks
	stopProgress := progress(func() string {
		s, smp := nSeries.Load(), nSamples.Load()
		if vw != nil {
			s, smp = vw.Series.Load(), vw.Samples.Load()
		}
		return fmt.Sprintf("workload-chunks %d/%d, series-chunks %d, samples %.1fM", done.Load(), total, s, float64(smp)/1e6)
	})
	for from := 0; from < tl.N && err == nil; from += step {
		ce := synth.Clip(emit, from, from+step)
		if !c.filtered() {
			for _, cm := range g.Clusters {
				if err = g.EmitNodes(cm, ce); err != nil {
					break
				}
			}
		}
		if err == nil {
			err = g.Each(*parallel, func(wt *synth.WorkloadTrace) error {
				defer done.Add(1)
				return g.EmitWorkload(wt, ce)
			})
		}
	}
	if vw != nil {
		if cerr := vw.Close(); err == nil {
			err = cerr
		}
		nSeries.Store(vw.Series.Load())
		nSamples.Store(vw.Samples.Load())
	}
	stopProgress()
	if err != nil {
		return err
	}
	if !*dry {
		if err := admin.Flush(ctx); err != nil {
			slog.Warn("flush/reset cache failed (data is still imported)", "err", err)
		}
	}
	el := time.Since(start)
	attrs := []any{"workloads", total / chunks, "chunks", chunks, "seriesChunks", nSeries.Load(), "samples", nSamples.Load(), "elapsed", el.Round(time.Millisecond),
		"samplesPerSec", int(float64(nSamples.Load()) / el.Seconds())}
	if vw != nil {
		attrs = append(attrs, "importedMiB", vw.Bytes.Load()>>20, "wireMiB", vw.WireBytes.Load()>>20,
			"pausedForVMMemory", time.Duration(vw.Paused.Load()).Round(time.Second), "requestTime", time.Duration(vw.PostTime.Load()).Round(time.Second))
	}
	slog.Info("backfill done", attrs...)
	if *follow && !*dry {
		return runLive(g.Scenario, *vmURL, liveState{start: tl.Start, anchor: g.Anchor(), last: tl.End(), seed: g.Scenario.Seed}, time.Minute)
	}
	return nil
}

func progress(f func() string) (stop func()) {
	t := time.NewTicker(10 * time.Second)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-t.C:
				slog.Info("progress", "status", f())
			case <-done:
				return
			}
		}
	}()
	return func() { t.Stop(); close(done); wg.Wait() }
}

func fixtures(args []string) error {
	fs := flag.NewFlagSet("fixtures", flag.ExitOnError)
	var c common
	c.register(fs, synth.FixtureScenario)
	out := fs.String("out", "testdata/series", "output directory")
	bucket := fs.Duration("bucket", synth.FixtureBucket, "fixture resolution")
	all := fs.Bool("all", false, "include generated fleet workloads (default: only workloads listed explicitly)")
	clean := fs.Bool("clean", false, "remove existing *.json fixtures in --out first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *clean {
		old, _ := filepath.Glob(filepath.Join(*out, "*.json"))
		for _, f := range old {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
	}
	if c.end == "" {
		c.end = synth.FixtureEnd.Format(time.RFC3339) // fixed so fixtures are reproducible
	}
	if c.days == 0 {
		c.days = synth.FixtureDays
	}
	if len(c.clusters) == 0 {
		c.clusters = stringList{synth.FixtureCluster}
	}
	g, err := c.generator()
	if err != nil {
		return err
	}
	if !*all {
		for _, cm := range g.Clusters {
			explicit := map[string]bool{}
			for _, w := range cm.Spec.Workloads {
				explicit[w.Namespace+"/"+w.Name] = true
			}
			var keep []*synth.WorkloadSpec
			for _, w := range cm.Workloads {
				if explicit[w.Namespace+"/"+w.Name] {
					keep = append(keep, w)
				}
			}
			cm.Workloads = keep
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	var mu sync.Mutex
	var written []string
	err = g.Each(4, func(wt *synth.WorkloadTrace) error {
		f, err := g.BuildFixture(wt, *bucket)
		if err != nil {
			return err
		}
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("%s.json", wt.Spec.Name)
		if len(g.Clusters) > 1 {
			name = fmt.Sprintf("%s_%s.json", wt.Cluster.Spec.Name, wt.Spec.Name)
		}
		p := filepath.Join(*out, name)
		if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
			return err
		}
		mu.Lock()
		written = append(written, p)
		mu.Unlock()
		return nil
	})
	sort.Strings(written)
	for _, p := range written {
		fmt.Println(p)
	}
	return err
}

func describe(args []string) error {
	fs := flag.NewFlagSet("describe", flag.ExitOnError)
	var c common
	c.register(fs, "tools/synth/scenarios/fleet-default.yaml")
	manifest := fs.String("manifest", "", "write the expanded workload list with expectations as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g, err := c.generator()
	if err != nil {
		return err
	}
	tl := g.Timeline
	fmt.Printf("scenario %s: %s → %s, step %s, %d samples per full series, seed %d\n",
		g.Scenario.Name, tl.Start.UTC().Format(time.RFC3339), tl.End().UTC().Format(time.RFC3339), tl.Step, tl.N, g.Scenario.Seed)
	type mw struct {
		Cluster    string             `json:"cluster"`
		Namespace  string             `json:"namespace"`
		Kind       string             `json:"workloadKind"`
		Name       string             `json:"workload"`
		Profile    string             `json:"profile"`
		Replicas   int                `json:"replicas"`
		HPA        *synth.HPASpec     `json:"hpa,omitempty"`
		Expect     *synth.Expectation `json:"expect,omitempty"`
		Containers []string           `json:"containers"`
	}
	var list []mw
	for _, cm := range g.Clusters {
		pools := map[string]int{}
		for _, n := range cm.Nodes {
			pools[n.InstanceType+"/"+n.CapacityType]++
		}
		fmt.Printf("\ncluster %s: %d nodes %v, %d workloads\n", cm.Spec.Name, len(cm.Nodes), pools, len(cm.Workloads))
		byProfile, byKind, byNS := map[string]int{}, map[string]int{}, map[string]int{}
		for _, w := range cm.Workloads {
			byProfile[w.Profile]++
			byKind[w.Kind]++
			byNS[w.Namespace]++
			var cs []string
			for _, ct := range w.Containers {
				cs = append(cs, ct.Name)
			}
			list = append(list, mw{cm.Spec.Name, w.Namespace, w.Kind, w.Name, w.Profile, w.Replicas, w.HPA, w.Expect, cs})
		}
		fmt.Printf("  profiles:   %v\n  kinds:      %v\n  namespaces: %v\n", byProfile, byKind, byNS)
	}
	var series, samples atomic.Int64
	var mu sync.Mutex
	byName := map[string][2]int64{}
	count := func(s *synth.Series) error { //nolint:unparam // must match synth.Emit
		series.Add(1)
		samples.Add(int64(s.Len))
		mu.Lock()
		x := byName[s.Name()]
		byName[s.Name()] = [2]int64{x[0] + 1, x[1] + int64(s.Len)}
		mu.Unlock()
		return nil
	}
	for _, cm := range g.Clusters {
		if err := g.EmitNodes(cm, count); err != nil {
			return err
		}
	}
	if err := g.Each(4, func(wt *synth.WorkloadTrace) error { return g.EmitWorkload(wt, count) }); err != nil {
		return err
	}
	fmt.Printf("\nvolume: %d series, %.1fM samples\n", series.Load(), float64(samples.Load())/1e6)
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return byName[names[i]][1] > byName[names[j]][1] })
	for _, n := range names {
		fmt.Printf("  %-55s %8d series %8.1fM samples\n", n, byName[n][0], float64(byName[n][1])/1e6)
	}
	if *manifest != "" {
		b, _ := json.MarshalIndent(list, "", "  ")
		return os.WriteFile(*manifest, append(b, '\n'), 0o644)
	}
	return nil
}

func deleteCmd(args []string) error {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	vmURL := fs.String("vm-url", "http://localhost:18428", "VictoriaMetrics base URL")
	var clusters stringList
	fs.Var(&clusters, "cluster", "synthetic clusters to delete (default synth-a,synth-b)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(clusters) == 0 {
		clusters = stringList{"synth-a", "synth-b"}
	}
	admin := synth.VMAdmin{URL: *vmURL}
	ctx := context.Background()
	if err := admin.DeleteClusters(ctx, clusters); err != nil {
		return err
	}
	slog.Info("deleted", "clusters", []string(clusters))
	return admin.Flush(ctx)
}
