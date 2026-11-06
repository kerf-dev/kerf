package synth

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

// FixtureSchemaVersion is bumped on incompatible changes to Fixture.
const FixtureSchemaVersion = 1

// Defaults used to generate testdata/series (`make synth-fixtures`). The end time is fixed so
// fixtures are reproducible.
var (
	FixtureEnd      = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	FixtureDays     = 14.0
	FixtureBucket   = 5 * time.Minute
	FixtureCluster  = "synth-a"
	FixtureScenario = "tools/synth/scenarios/fleet-small.yaml"
	// The GPU fixtures come from the workloads of FixtureGPUScenario that name a `fixture:`.
	FixtureGPUScenario = "tools/synth/scenarios/fleet-gpu.yaml"
	FixtureGPUCluster  = "synth-gpu"
)

// Fixture is a compact, pre-aggregated history of one workload for unit tests (the
// recommender table tests). Arrays are aligned: element i is the bucket starting at
// Start + i*Step seconds. null (NaN in Go) means no pod was running.
type Fixture struct {
	SchemaVersion int          `json:"schemaVersion"`
	Cluster       string       `json:"cluster"`
	Namespace     string       `json:"namespace"`
	WorkloadKind  string       `json:"workloadKind"`
	Workload      string       `json:"workload"`
	Profile       string       `json:"profile,omitempty"`
	Pattern       string       `json:"pattern,omitempty"`
	Expect        *Expectation `json:"expect,omitempty"`
	HPA           *HPASpec     `json:"hpa,omitempty"`
	Start         int64        `json:"start"` // unix seconds
	Step          int          `json:"step"`  // seconds
	Points        int          `json:"points"`
	// Replicas is the desired replica count (spec or HPA current); for CronJobs, running pods.
	Replicas   Values             `json:"replicas"`
	Containers []FixtureContainer `json:"containers"`
	// GPU is set for workloads whose pods request GPUs.
	GPU *FixtureGPU `json:"gpu,omitempty"`
}

// FixtureGPU is the GPU part of a fixture: what the NVIDIA DCGM exporter reports for the GPUs of
// the workload's pods, per bucket, aggregated over all GPUs of all running pods. The arrays have
// Fixture.Points elements; null means no pod (or no DCGM series) in that bucket. Without DCGM
// series (DCGM false) they are absent.
type FixtureGPU struct {
	Container  string  `json:"container"`  // the container that requests the GPUs
	PerPod     int     `json:"perPod"`     // nvidia.com/gpu requested by one pod
	Model      string  `json:"model"`      // DCGM modelName
	MemoryMiB  float64 `json:"memoryMiB"`  // framebuffer of one GPU
	LabelStyle string  `json:"labelStyle"` // pod | exported
	DCGM       bool    `json:"dcgm"`       // false: the exporter has no series for the pods
	// Node is the GPU node of the first pod: the inputs of the GPU price.
	Node FixtureGPUNode `json:"node"`
	// Devices is the number of GPU series reporting in the bucket (running pods x PerPod).
	Devices Values `json:"devices,omitempty"`
	// Util is DCGM_FI_DEV_GPU_UTIL in percent: per GPU the mean over the bucket, then the
	// minimum, maximum and mean across GPUs (avg is what the hub averages over a workload's GPUs).
	Util *GPUAgg `json:"util,omitempty"`
	// FBUsedMiB is DCGM_FI_DEV_FB_USED: per GPU the maximum over the bucket, then across GPUs.
	FBUsedMiB *GPUAgg `json:"fbUsedMiB,omitempty"`
	// FBFreeMiB is DCGM_FI_DEV_FB_FREE: per GPU the minimum over the bucket, then across GPUs.
	FBFreeMiB *GPUAgg `json:"fbFreeMiB,omitempty"`
	// Expect is the GPU status and suggestion kinds a correct recommender reaches.
	Expect *GPUExpectation `json:"expect,omitempty"`
}

// FixtureGPUNode describes the node of a GPU pod.
type FixtureGPUNode struct {
	InstanceType string `json:"instanceType"` // e.g. g5.xlarge
	CapacityType string `json:"capacityType"` // on-demand | spot
	Region       string `json:"region"`
	GPUs         int    `json:"gpus"` // GPUs of the node
}

// GPUAgg holds min, max and mean across the GPUs of a workload.
type GPUAgg struct {
	Min Values `json:"min"`
	Max Values `json:"max"`
	Avg Values `json:"avg"`
}

// FixtureContainer is one container of the workload. Init containers are listed with
// init=true and carry no usage.
type FixtureContainer struct {
	Name     string             `json:"name"`
	Init     bool               `json:"init,omitempty"`
	Sidecar  bool               `json:"sidecar,omitempty"`
	Image    string             `json:"image"`
	Requests map[string]float64 `json:"requests"` // cpu cores, memory bytes; missing = unset
	Limits   map[string]float64 `json:"limits"`
	// CPU: per pod, average cores over the bucket (= rate(container_cpu_usage_seconds_total[step]));
	// then max and average across running pods.
	CPU *Agg `json:"cpu,omitempty"`
	// Memory: per pod, max working set bytes in the bucket; then max and average across pods.
	Memory *Agg `json:"memory,omitempty"`
	// ThrottleRatio: throttled/total CFS periods in the bucket, max across pods (only with a CPU limit).
	ThrottleRatio Values `json:"throttleRatio,omitempty"`
	Restarts      int    `json:"restarts"`
	OOMKills      int    `json:"oomKills"`
}

// Agg holds max and mean across pods.
type Agg struct {
	Max Values `json:"max"`
	Avg Values `json:"avg"`
}

// Values marshals NaN as null and rounds to 6 significant digits.
type Values []float64

// MarshalJSON implements json.Marshaler.
func (v Values) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, len(v)*8+2)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		if math.IsNaN(f) {
			b = append(b, "null"...)
		} else {
			b = strconv.AppendFloat(b, f, 'g', 6, 64)
		}
	}
	return append(b, ']'), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Values) UnmarshalJSON(b []byte) error {
	var raw []*float64
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*v = make(Values, len(raw))
	for i, p := range raw {
		if p == nil {
			(*v)[i] = math.NaN()
		} else {
			(*v)[i] = *p
		}
	}
	return nil
}

// LoadFixture reads a fixture file.
func LoadFixture(path string) (*Fixture, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Fixture
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.SchemaVersion != FixtureSchemaVersion {
		return nil, fmt.Errorf("%s: schemaVersion %d, want %d", path, f.SchemaVersion, FixtureSchemaVersion)
	}
	return &f, nil
}

// BuildFixture aggregates a trace into buckets of `step` (a multiple of the timeline step).
func (g *Generator) BuildFixture(wt *WorkloadTrace, step time.Duration) (*Fixture, error) {
	tl := g.Timeline
	if step < tl.Step || step%tl.Step != 0 {
		return nil, fmt.Errorf("fixture step %s must be a multiple of the sample step %s", step, tl.Step)
	}
	m := int(step / tl.Step)
	nb := tl.N / m
	w := wt.Spec
	f := &Fixture{
		SchemaVersion: FixtureSchemaVersion, Cluster: wt.Cluster.Spec.Name, Namespace: w.Namespace,
		WorkloadKind: w.Kind, Workload: w.Name, Profile: w.Profile, Expect: w.Expect, HPA: w.HPA,
		Start: tl.Start.Unix(), Step: int(step.Seconds()), Points: nb,
		Replicas: make(Values, nb),
	}
	if len(w.Containers) > 0 {
		f.Pattern = w.Containers[0].CPU.Pattern
		if f.Pattern == "" || f.Pattern == "flat" {
			if p := w.Containers[0].Memory.Pattern; p != "" {
				f.Pattern = p
			}
		}
	}
	for b := 0; b < nb; b++ {
		f.Replicas[b] = wt.Replicas[b*m]
		if w.Kind == KindCronJob {
			f.Replicas[b] = 0
			for _, p := range wt.Pods {
				if p.Start <= b*m && b*m < p.End {
					f.Replicas[b]++
				}
			}
		}
	}
	for _, c := range w.InitContainers {
		f.Containers = append(f.Containers, FixtureContainer{Name: c.Name, Init: true, Image: c.Image,
			Requests: resMap(c.Requests), Limits: resMap(c.Limits)})
	}
	for ci := range w.Containers {
		c := &w.Containers[ci]
		fc := FixtureContainer{Name: c.Name, Sidecar: c.Sidecar, Image: c.Image,
			Requests: resMap(c.Requests), Limits: resMap(c.Limits),
			CPU: &Agg{Max: nanValues(nb), Avg: nanValues(nb)}, Memory: &Agg{Max: nanValues(nb), Avg: nanValues(nb)}}
		if c.Limits.CPU > 0 {
			fc.ThrottleRatio = nanValues(nb)
		}
		cnt := make([]float64, nb)
		for _, p := range wt.Pods {
			ct := p.Containers[ci]
			fc.Restarts += len(ct.Instances) - 1
			for _, in := range ct.Instances {
				if in.EndReason == "OOMKilled" {
					fc.OOMKills++
				}
			}
			for b := p.Start / m; b < nb && b*m < p.End; b++ {
				lo, hi := max(b*m, p.Start), min((b+1)*m, p.End)
				if lo >= hi {
					continue
				}
				var cpu, mem, per, thr float64
				for i := lo; i < hi; i++ {
					k := i - p.Start
					cpu += ct.CPU[k]
					mem = math.Max(mem, ct.Mem[k])
					per += ct.Periods[k]
					thr += ct.Periods[k] * ct.Throttled[k]
				}
				cpu /= float64(hi - lo)
				fc.CPU.Max[b] = nanMax(fc.CPU.Max[b], cpu)
				fc.CPU.Avg[b] = nanAdd(fc.CPU.Avg[b], cpu)
				fc.Memory.Max[b] = nanMax(fc.Memory.Max[b], mem)
				fc.Memory.Avg[b] = nanAdd(fc.Memory.Avg[b], mem)
				if fc.ThrottleRatio != nil && per > 0 {
					fc.ThrottleRatio[b] = nanMax(fc.ThrottleRatio[b], thr/per)
				}
				cnt[b]++
			}
		}
		for b := range cnt {
			if cnt[b] > 0 {
				fc.CPU.Avg[b] /= cnt[b]
				fc.Memory.Avg[b] /= cnt[b]
			}
		}
		f.Containers = append(f.Containers, fc)
	}
	f.GPU = g.gpuFixture(wt, m, nb)
	return f, nil
}

// gpuFixture aggregates the DCGM series of the workload's first GPU container, nil without GPUs.
func (g *Generator) gpuFixture(wt *WorkloadTrace, m, nb int) *FixtureGPU {
	idx := wt.Spec.gpuContainers()
	if len(idx) == 0 || len(wt.Pods) == 0 {
		return nil
	}
	ci := idx[0]
	spec := &wt.Spec.Containers[ci]
	gs := spec.GPU
	node := wt.Pods[0].Node
	out := &FixtureGPU{Container: spec.Name, PerPod: gs.PerPod, Model: gs.Model, MemoryMiB: gs.MemoryMiB,
		LabelStyle: gs.LabelStyle, DCGM: gs.DCGMEnabled(), Expect: gs.Expect,
		Node: FixtureGPUNode{InstanceType: node.InstanceType, CapacityType: node.CapacityType, Region: node.Region, GPUs: node.Type.GPUs}}
	if !out.DCGM {
		return out
	}
	newAgg := func() *GPUAgg { return &GPUAgg{Min: nanValues(nb), Max: nanValues(nb), Avg: nanValues(nb)} }
	out.Devices = make(Values, nb)
	out.Util, out.FBUsedMiB, out.FBFreeMiB = newAgg(), newAgg(), newAgg()
	cnt := make([]float64, nb)
	add := func(a *GPUAgg, b int, v float64) {
		a.Min[b] = nanMin(a.Min[b], v)
		a.Max[b] = nanMax(a.Max[b], v)
		a.Avg[b] = nanAdd(a.Avg[b], v)
	}
	for _, p := range wt.Pods {
		ct := p.Containers[ci]
		for d := range ct.GPUUtil {
			for b := p.Start / m; b < nb && b*m < p.End; b++ {
				lo, hi := max(b*m, p.Start), min((b+1)*m, p.End)
				if lo >= hi {
					continue
				}
				var u float64
				used, free := 0.0, math.Inf(1)
				for i := lo; i < hi; i++ {
					k := i - p.Start
					u += ct.GPUUtil[d][k]
					used = math.Max(used, ct.GPUUsedMiB[d][k])
					free = math.Min(free, gs.MemoryMiB-ct.GPUUsedMiB[d][k])
				}
				add(out.Util, b, u/float64(hi-lo))
				add(out.FBUsedMiB, b, used)
				add(out.FBFreeMiB, b, free)
				cnt[b]++
			}
		}
	}
	for b := range cnt {
		out.Devices[b] = cnt[b]
		if cnt[b] > 0 {
			out.Util.Avg[b] /= cnt[b]
			out.FBUsedMiB.Avg[b] /= cnt[b]
			out.FBFreeMiB.Avg[b] /= cnt[b]
		} else {
			out.Devices[b] = math.NaN()
		}
	}
	return out
}

func nanMin(a, b float64) float64 {
	if math.IsNaN(a) {
		return b
	}
	return math.Min(a, b)
}

func resMap(r Resources) map[string]float64 {
	m := map[string]float64{}
	if r.CPU > 0 {
		m["cpu"] = float64(r.CPU)
	}
	if r.Memory > 0 {
		m["memory"] = float64(r.Memory)
	}
	return m
}

func nanValues(n int) Values {
	v := make(Values, n)
	for i := range v {
		v[i] = math.NaN()
	}
	return v
}

func nanMax(a, b float64) float64 {
	if math.IsNaN(a) {
		return b
	}
	return math.Max(a, b)
}

func nanAdd(a, b float64) float64 {
	if math.IsNaN(a) {
		return b
	}
	return a + b
}
