// Package synth generates realistic synthetic Kubernetes usage history (cAdvisor and
// kube-state-metrics series) for the Kerf hub, dashboards and UI.
//
// A Scenario (YAML) describes clusters, node pools and workloads, either listed explicitly or
// generated from a weighted profile mix. Run expands the scenario, simulates every pod and
// container over the time window and hands the result to a Sink: VictoriaMetrics import,
// JSON fixtures, or a counter for dry runs.
package synth

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Scenario is the top-level YAML document.
type Scenario struct {
	Name string `yaml:"name"`
	// Seed makes the whole scenario deterministic. The CLI --seed flag overrides it.
	Seed int64 `yaml:"seed"`
	// Days of history ending at the end time (default 30). The CLI --days flag overrides it.
	Days float64 `yaml:"days"`
	// Step is the sample interval (default 60s). Real scrapes are 30s; 60s halves the volume and
	// still gives 5 samples per rate(...[5m]) window.
	Step Duration `yaml:"step"`
	// WorkloadLabel adds convenience `workload` and `workload_kind` labels to cAdvisor series.
	// Real cAdvisor has neither, so consumers must not depend on them; keep it off for fleets
	// other tracks develop against.
	WorkloadLabel bool `yaml:"workloadLabel"`
	// GroundTruth emits synth_workload_info{...,profile,cpu_expect,memory_expect} per workload,
	// so tests and dashboards can check what the generator intended. Default true.
	GroundTruth *bool `yaml:"groundTruth"`
	// ImageRegistry prefixes generated application images.
	ImageRegistry string        `yaml:"imageRegistry"`
	Clusters      []ClusterSpec `yaml:"clusters"`
}

// QuickstartCluster is the one cluster name besides synth-* that synth writes: the demo cluster of the laptop
// quickstart (hack/quickstart.sh), whose workloads are generated history. Any other name, ro-* in particular,
// is refused.
const QuickstartCluster = "quickstart"

// IsSyntheticCluster reports whether synth may write or delete the series of a cluster of this name.
func IsSyntheticCluster(name string) bool {
	return strings.HasPrefix(name, "synth-") || name == QuickstartCluster
}

// ClusterSpec describes one synthetic cluster.
type ClusterSpec struct {
	// Name is the `cluster` label value. Must start with "synth-" (never ro-*, the real dev clusters), or be
	// QuickstartCluster, the demo cluster of the laptop quickstart (see IsSyntheticCluster).
	Name   string `yaml:"name"`
	Region string `yaml:"region"`
	// NodePools; pools with Count 0 are sized automatically so requests fill RequestPacking of
	// allocatable capacity, split by Weight.
	NodePools      []NodePoolSpec `yaml:"nodePools"`
	RequestPacking float64        `yaml:"requestPacking"`
	// Fleet generates workloads from a weighted profile mix.
	Fleet *FleetSpec `yaml:"fleet"`
	// Workloads are listed explicitly (in addition to the generated fleet).
	Workloads []WorkloadSpec `yaml:"workloads"`
}

// NodePoolSpec describes a group of identical nodes.
type NodePoolSpec struct {
	Name         string   `yaml:"name"`
	InstanceType string   `yaml:"instanceType"`
	CapacityType string   `yaml:"capacityType"` // on-demand | spot
	Zones        []string `yaml:"zones"`
	Count        int      `yaml:"count"`
	Weight       float64  `yaml:"weight"`
}

// FleetSpec generates Workloads workloads spread over Namespaces.
type FleetSpec struct {
	Workloads  int      `yaml:"workloads"`
	Namespaces []string `yaml:"namespaces"`
	// Profiles maps profile name to weight. Profiles: over-provisioned, right-sized,
	// throttled, oom-prone, hpa-coupled, jvm, batch.
	Profiles map[string]float64 `yaml:"profiles"`
	// Fractions of generated workloads that get a modifier.
	NewWorkloads float64 `yaml:"newWorkloads"` // created 6h–3d before the end (low-confidence)
	Sidecars     float64 `yaml:"sidecars"`     // envoy sidecar container
	StepChanges  float64 `yaml:"stepChanges"`  // usage step change at a release
	Restarting   float64 `yaml:"restarting"`   // non-OOM crash restarts
	StatefulSets float64 `yaml:"statefulSets"` // share of long-running services that are StatefulSets
	// DaemonSets are added to the first namespace named "platform" (or the last namespace).
	DaemonSets int `yaml:"daemonSets"`
	// RolloutEveryDays is the mean interval between releases of a service (default 7).
	RolloutEveryDays float64 `yaml:"rolloutEveryDays"`
}

// WorkloadSpec is one workload. Generated workloads are expanded into this form too.
type WorkloadSpec struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
	Kind      string `yaml:"kind"` // Deployment | StatefulSet | DaemonSet | CronJob
	// Profile is informational (ground truth); Pattern names the dominant signal shape.
	Profile  string `yaml:"profile"`
	Replicas int    `yaml:"replicas"`
	// Age is how long before the end the workload was created; 0 = before the window.
	Age Duration `yaml:"age"`
	// Rollouts lists release times as offsets before the end ("10d", "36h").
	Rollouts []Duration `yaml:"rollouts"`
	// Evictions: mean pod lifetime before a reschedule (spot interruptions, node drains);
	// 0 = default by capacity type, negative = never.
	MeanPodLifetime Duration        `yaml:"meanPodLifetime"`
	HPA             *HPASpec        `yaml:"hpa"`
	Schedule        *ScheduleSpec   `yaml:"schedule"`
	Containers      []ContainerSpec `yaml:"containers"`
	InitContainers  []ContainerSpec `yaml:"initContainers"`
	Expect          *Expectation    `yaml:"expect"`
	// Fixture names the testdata/series file (without .json) written for this workload by
	// `synth fixtures` when a scenario other than fleet-small lists it (fleet-gpu.yaml).
	Fixture string `yaml:"fixture"`
}

// HPASpec attaches a utilization HPA to a Deployment.
type HPASpec struct {
	Min    int    `yaml:"min" json:"min"`
	Max    int    `yaml:"max" json:"max"`
	Metric string `yaml:"metric" json:"metric"` // cpu | memory
	Target int    `yaml:"target" json:"target"` // percent utilization
}

// ScheduleSpec drives CronJob runs: one Job every Every, starting at Offset after midnight UTC,
// each running for Duration (±20%).
type ScheduleSpec struct {
	Every    Duration `yaml:"every"`
	Offset   Duration `yaml:"offset"`
	Duration Duration `yaml:"duration"`
}

// ContainerSpec is one container. CPU and Memory describe usage per pod.
type ContainerSpec struct {
	Name     string    `yaml:"name"`
	Image    string    `yaml:"image"`
	Sidecar  bool      `yaml:"sidecar"`
	Requests Resources `yaml:"requests"`
	Limits   Resources `yaml:"limits"`
	CPU      Signal    `yaml:"cpu"`
	Memory   Signal    `yaml:"memory"`
	// CrashEvery: mean time between non-OOM crashes (reason Error); 0 = never.
	CrashEvery Duration `yaml:"crashEvery"`
	// GPU makes the container request nvidia.com/gpu and gives its GPUs DCGM series.
	GPU *GPUSpec `yaml:"gpu"`
}

// GPUSpec describes the NVIDIA GPUs of a container: the kube-state-metrics requests/limits, and
// (unless DCGM is false) one NVIDIA DCGM exporter series per GPU and pod. The pods run on GPU
// nodes (g5.xlarge, g5.12xlarge), one pod per node.
type GPUSpec struct {
	// PerPod is nvidia.com/gpu requested (and limited) by one pod (default 1).
	PerPod int `yaml:"perPod"`
	// Model is the DCGM modelName label (default "NVIDIA A10G").
	Model string `yaml:"model"`
	// MemoryMiB is the framebuffer of one GPU: DCGM_FI_DEV_FB_USED + DCGM_FI_DEV_FB_FREE (default 24576).
	MemoryMiB float64 `yaml:"memoryMiB"`
	// Utilization is DCGM_FI_DEV_GPU_UTIL, in percent.
	Utilization GPUUtilization `yaml:"utilization"`
	// MemoryUsed is the framebuffer in use; default depends on the utilization pattern.
	MemoryUsed GPUMemoryUsed `yaml:"memoryUsed"`
	// LabelStyle: "pod" (default) puts the workload's namespace/pod/container on the DCGM series
	// (honor_labels, or the exporter's own pod mapping); "exported" is what a Prometheus scrape
	// without honor_labels produces: namespace/pod/container are the exporter pod's, the
	// workload's are exported_namespace/exported_pod/exported_container.
	LabelStyle string `yaml:"labelStyle"`
	// DCGM false emits no DCGM series at all (exporter missing on the GPU nodes); the requests remain.
	DCGM *bool `yaml:"dcgm"`
	// Expect is the ground truth a correct recommender reaches for the GPU.
	Expect *GPUExpectation `yaml:"expect"`
}

// GPUUtilization shapes the GPU utilisation of a workload.
type GPUUtilization struct {
	// Pattern: idle (~0-2 % with rare small blips), light (diurnal, p95 ~25 %), busy (diurnal,
	// p95 ~85 %). Default light.
	Pattern string `yaml:"pattern"`
	// P95 is the 95th percentile of the utilisation (percent) the pattern is scaled to; default
	// 1 (idle), 25 (light), 85 (busy).
	P95 float64 `yaml:"p95"`
}

// GPUMemoryUsed is the framebuffer used, either as a fraction of MemoryMiB or in MiB.
type GPUMemoryUsed struct {
	Fraction float64 `yaml:"fraction"`
	MiB      float64 `yaml:"mib"`
}

// GPUExpectation is the GPU status and suggestion kinds a correct recommender reaches.
type GPUExpectation struct {
	Status      string   `yaml:"status" json:"status"` // idle | under-used | ok | no-data
	Suggestions []string `yaml:"suggestions" json:"suggestions"`
}

// GPU utilisation patterns, label styles and the DCGM exporter's identity.
const (
	GPUIdle  = "idle"
	GPULight = "light"
	GPUBusy  = "busy"

	GPULabelPod      = "pod"
	GPULabelExported = "exported"

	// dcgmNamespace is where the exporter pods run when the labels are `exported`.
	dcgmNamespace = "gpu-operator"
)

// DCGMEnabled reports whether the container's GPUs have DCGM series.
func (g *GPUSpec) DCGMEnabled() bool { return g.DCGM == nil || *g.DCGM }

// gpuContainers returns the indexes of the containers that request GPUs.
func (w *WorkloadSpec) gpuContainers() []int {
	var out []int
	for i := range w.Containers {
		if w.Containers[i].GPU != nil {
			out = append(out, i)
		}
	}
	return out
}

// GPUsPerPod is the number of GPUs one pod of the workload requests (all containers).
func (w *WorkloadSpec) GPUsPerPod() int {
	n := 0
	for _, c := range w.Containers {
		if c.GPU != nil {
			n += c.GPU.PerPod
		}
	}
	return n
}

// applyDefaults fills the unset fields.
func (g *GPUSpec) applyDefaults() {
	if g.PerPod == 0 {
		g.PerPod = 1
	}
	if g.Model == "" {
		g.Model = "NVIDIA A10G"
	}
	if g.MemoryMiB == 0 {
		g.MemoryMiB = 24576
	}
	if g.Utilization.Pattern == "" {
		g.Utilization.Pattern = GPULight
	}
	if g.Utilization.P95 == 0 {
		g.Utilization.P95 = map[string]float64{GPUIdle: 1, GPULight: 25, GPUBusy: 85}[g.Utilization.Pattern]
	}
	if g.MemoryUsed.Fraction == 0 && g.MemoryUsed.MiB == 0 {
		g.MemoryUsed.Fraction = map[string]float64{GPUIdle: 0.04, GPULight: 0.2, GPUBusy: 0.75}[g.Utilization.Pattern]
	}
	if g.LabelStyle == "" {
		g.LabelStyle = GPULabelPod
	}
}

func (g *GPUSpec) validate() error {
	maxGPUs := 0
	for _, it := range InstanceTypes {
		maxGPUs = max(maxGPUs, it.GPUs)
	}
	switch {
	case g.PerPod < 1 || g.PerPod > maxGPUs:
		return fmt.Errorf("gpu.perPod must be in [1, %d], got %d", maxGPUs, g.PerPod)
	case g.MemoryMiB < 0 || g.MemoryMiB > 1<<20:
		return fmt.Errorf("gpu.memoryMiB %v out of range", g.MemoryMiB)
	case g.Utilization.Pattern != GPUIdle && g.Utilization.Pattern != GPULight && g.Utilization.Pattern != GPUBusy:
		return fmt.Errorf("unknown gpu.utilization.pattern %q (known: idle, light, busy)", g.Utilization.Pattern)
	case g.Utilization.P95 < 0 || g.Utilization.P95 > 100:
		return fmt.Errorf("gpu.utilization.p95 must be a percentage in (0, 100], got %v", g.Utilization.P95)
	case g.MemoryUsed.Fraction < 0 || g.MemoryUsed.Fraction > 1 || g.MemoryUsed.MiB < 0 || g.MemoryUsed.MiB > g.MemoryMiB:
		return fmt.Errorf("gpu.memoryUsed must be a fraction in (0, 1] or MiB up to gpu.memoryMiB (%v)", g.MemoryMiB)
	case g.MemoryUsed.Fraction > 0 && g.MemoryUsed.MiB > 0:
		return fmt.Errorf("gpu.memoryUsed: set either fraction or mib")
	case g.LabelStyle != GPULabelPod && g.LabelStyle != GPULabelExported:
		return fmt.Errorf("unknown gpu.labelStyle %q (known: pod, exported)", g.LabelStyle)
	}
	if e := g.Expect; e != nil {
		switch e.Status {
		case "idle", "under-used", "ok", "no-data":
		default:
			return fmt.Errorf("gpu.expect.status %q must be idle, under-used, ok or no-data", e.Status)
		}
		for _, k := range e.Suggestions {
			switch k {
			case "time-slicing", "mig", "smaller-gpu", "scale-to-zero":
			default:
				return fmt.Errorf("gpu.expect.suggestions: unknown kind %q", k)
			}
		}
	}
	return nil
}

// memoryUsedMiB is the framebuffer in use at the mean.
func (g *GPUSpec) memoryUsedMiB() float64 {
	if g.MemoryUsed.MiB > 0 {
		return g.MemoryUsed.MiB
	}
	return g.MemoryUsed.Fraction * g.MemoryMiB
}

// Resources holds cpu (cores) and memory (bytes); 0 = unset.
type Resources struct {
	CPU    Quantity `yaml:"cpu"`
	Memory Quantity `yaml:"memory"`
}

// Expectation is what a correct recommender should conclude (ground truth).
type Expectation struct {
	CPU       string   `yaml:"cpu" json:"cpu"`       // decrease | keep | increase | any
	Memory    string   `yaml:"memory" json:"memory"` // decrease | keep | increase | any
	RiskFlags []string `yaml:"riskFlags" json:"riskFlags"`
}

// Signal describes a usage time series. Pattern fills defaults for the shape fields that are
// left zero; any field can be set explicitly. Base is cores (CPU) or bytes (memory).
//
// Patterns: flat, diurnal, weekly, spiky, leak, step, sawtooth, throttled (alias of diurnal,
// meant to be combined with a CPU limit below the peak), sparse (alias of flat; sparseness
// comes from CronJob scheduling).
type Signal struct {
	Pattern  string   `yaml:"pattern"`
	Base     Quantity `yaml:"base"`
	Diurnal  float64  `yaml:"diurnal"`  // relative amplitude of the daily cycle
	PeakHour float64  `yaml:"peakHour"` // UTC hour of the daily peak (default 12)
	Weekend  float64  `yaml:"weekend"`  // multiplier on Saturday/Sunday (default 1)
	Noise    float64  `yaml:"noise"`    // relative noise (default 0.08 CPU, 0.02 memory)
	// Spikes: SpikesPerDay bursts of SpikeMinutes, multiplying the value by SpikeMult.
	SpikesPerDay float64 `yaml:"spikesPerDay"`
	SpikeMult    float64 `yaml:"spikeMult"`
	SpikeMinutes float64 `yaml:"spikeMinutes"`
	// Step change: multiply by StepFactor from StepAt (before the end) onwards. For Deployments a
	// rollout is added at that moment.
	StepAt     Duration `yaml:"stepAt"`
	StepFactor float64  `yaml:"stepFactor"`
	// LeakPerHour grows the value by LeakPerHour*Base per hour of container uptime.
	LeakPerHour float64 `yaml:"leakPerHour"`
	// Startup: extra StartupMult*Base right after container start, decaying over StartupMinutes.
	StartupMult    float64 `yaml:"startupMult"`
	StartupMinutes float64 `yaml:"startupMinutes"`
	// Sawtooth: relative amplitude and period (GC-like memory pattern).
	Sawtooth        float64 `yaml:"sawtooth"`
	SawtoothMinutes float64 `yaml:"sawtoothMinutes"`
}

// Patterns lists the supported Signal patterns.
var Patterns = []string{"flat", "diurnal", "weekly", "spiky", "leak", "step", "sawtooth", "throttled", "sparse"}

// Quantity is a Kubernetes quantity ("250m", "512Mi", 2) as a float64.
type Quantity float64

// UnmarshalYAML accepts numbers and Kubernetes quantity strings.
func (q *Quantity) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseQuantity(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*q = Quantity(v)
	return nil
}

// ParseQuantity parses a Kubernetes quantity into a float64.
func ParseQuantity(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, fmt.Errorf("invalid quantity %q: %w", s, err)
	}
	return q.AsApproximateFloat64(), nil
}

// Duration is a time.Duration that also accepts a "d" (day) suffix, e.g. "30d" or "1d12h".
type Duration time.Duration

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML parses durations like "90s", "36h", "14d".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// ParseDuration is time.ParseDuration plus a leading "<n>d" day component.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	var days time.Duration
	if i := strings.IndexByte(s, 'd'); i > 0 {
		f, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		days = time.Duration(f * float64(24*time.Hour))
		s = s[i+1:]
		if s == "" {
			return days, nil
		}
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return days + v, nil
}

// LoadScenario reads and validates a scenario file.
func LoadScenario(path string) (*Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sc, err := ParseScenario(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sc, nil
}

// ParseScenario parses and validates a scenario document.
func ParseScenario(b []byte) (*Scenario, error) {
	var sc Scenario
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&sc); err != nil {
		return nil, err
	}
	sc.applyDefaults()
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	return &sc, nil
}

func (sc *Scenario) applyDefaults() {
	if sc.Days == 0 {
		sc.Days = 30
	}
	if sc.Step == 0 {
		sc.Step = Duration(time.Minute)
	}
	if sc.GroundTruth == nil {
		t := true
		sc.GroundTruth = &t
	}
	if sc.ImageRegistry == "" {
		sc.ImageRegistry = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
	}
	for i := range sc.Clusters {
		c := &sc.Clusters[i]
		if c.Region == "" {
			c.Region = "eu-west-1"
		}
		if c.RequestPacking == 0 {
			c.RequestPacking = 0.7
		}
		if len(c.NodePools) == 0 {
			c.NodePools = []NodePoolSpec{{Name: "default", InstanceType: "m6i.xlarge", CapacityType: "on-demand", Weight: 1}}
		}
		for j := range c.NodePools {
			p := &c.NodePools[j]
			if p.Name == "" {
				p.Name = fmt.Sprintf("%s-%s", strings.ReplaceAll(p.InstanceType, ".", "-"), p.CapacityType)
			}
			if p.CapacityType == "" {
				p.CapacityType = "on-demand"
			}
			if len(p.Zones) == 0 {
				p.Zones = []string{c.Region + "a", c.Region + "b", c.Region + "c"}
			}
			if p.Weight == 0 {
				p.Weight = 1
			}
		}
		if f := c.Fleet; f != nil {
			if len(f.Namespaces) == 0 {
				f.Namespaces = []string{"payments", "cards", "ledger", "onboarding", "notifications", "risk", "platform"}
			}
			if len(f.Profiles) == 0 {
				f.Profiles = DefaultProfileMix()
			}
			if f.RolloutEveryDays == 0 {
				f.RolloutEveryDays = 7
			}
		}
	}
}

// DefaultProfileMix is the profile weighting used when a fleet lists none.
func DefaultProfileMix() map[string]float64 {
	return map[string]float64{
		ProfileOverProvisioned: 50,
		ProfileRightSized:      14,
		ProfileThrottled:       8,
		ProfileOOMProne:        5,
		ProfileHPACoupled:      8,
		ProfileJVM:             8,
		ProfileBatch:           7,
	}
}

// Validate checks names, kinds and references.
func (sc *Scenario) Validate() error {
	if sc.Step.D() < time.Second {
		return fmt.Errorf("step must be at least 1s")
	}
	if sc.Days <= 0 || sc.Days > 35 {
		return fmt.Errorf("days must be in (0, 35] (VictoriaMetrics retention is 35d)")
	}
	if len(sc.Clusters) == 0 {
		return fmt.Errorf("no clusters")
	}
	seen := map[string]bool{}
	for ci := range sc.Clusters {
		c := &sc.Clusters[ci]
		if !IsSyntheticCluster(c.Name) {
			return fmt.Errorf("cluster %q: synthetic cluster names must start with \"synth-\" (or be %q)", c.Name, QuickstartCluster)
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate cluster %q", c.Name)
		}
		seen[c.Name] = true
		for _, p := range c.NodePools {
			if _, ok := InstanceTypes[p.InstanceType]; !ok {
				return fmt.Errorf("cluster %s: unknown instance type %q (known: %s)", c.Name, p.InstanceType, knownInstanceTypes())
			}
			if p.CapacityType != "on-demand" && p.CapacityType != "spot" {
				return fmt.Errorf("cluster %s: capacityType must be on-demand or spot, got %q", c.Name, p.CapacityType)
			}
		}
		if c.Fleet != nil {
			for p := range c.Fleet.Profiles {
				if !isProfile(p) {
					return fmt.Errorf("cluster %s: unknown profile %q", c.Name, p)
				}
			}
		}
		names := map[string]bool{}
		for j := range c.Workloads {
			w := &c.Workloads[j]
			if err := w.validate(); err != nil {
				return fmt.Errorf("cluster %s: %w", c.Name, err)
			}
			key := w.Namespace + "/" + w.Name
			if names[key] {
				return fmt.Errorf("cluster %s: duplicate workload %s", c.Name, key)
			}
			names[key] = true
		}
		if err := c.validateGPUPools(); err != nil {
			return fmt.Errorf("cluster %s: %w", c.Name, err)
		}
	}
	return nil
}

// validateGPUPools checks that GPU workloads have a node pool with enough GPUs per node, and
// that CPU-only workloads have a pool without GPUs (they never run on GPU nodes).
func (c *ClusterSpec) validateGPUPools() error {
	maxGPUs, cpuPools := 0, 0
	for _, p := range c.NodePools {
		if n := InstanceTypes[p.InstanceType].GPUs; n > 0 {
			maxGPUs = max(maxGPUs, n)
		} else {
			cpuPools++
		}
	}
	cpuWorkloads := c.Fleet != nil
	for i := range c.Workloads {
		w := &c.Workloads[i]
		if n := w.GPUsPerPod(); n > 0 {
			if n > maxGPUs {
				return fmt.Errorf("workload %s requests %d GPUs per pod but no node pool has a GPU instance type with that many (max %d)", w.Name, n, maxGPUs)
			}
		} else {
			cpuWorkloads = true
		}
	}
	if cpuWorkloads && cpuPools == 0 {
		return fmt.Errorf("workloads without GPUs need a node pool without GPUs (GPU nodes only run GPU workloads)")
	}
	return nil
}

func (w *WorkloadSpec) validate() error {
	if w.Name == "" || w.Namespace == "" {
		return fmt.Errorf("workload needs name and namespace")
	}
	switch w.Kind {
	case "":
		w.Kind = KindDeployment
	case KindDeployment, KindStatefulSet, KindDaemonSet, KindCronJob:
	default:
		return fmt.Errorf("workload %s: unknown kind %q", w.Name, w.Kind)
	}
	if w.Kind == KindCronJob && (w.Schedule == nil || w.Schedule.Every == 0 || w.Schedule.Duration == 0) {
		return fmt.Errorf("workload %s: CronJob needs schedule.every and schedule.duration", w.Name)
	}
	if w.HPA != nil {
		if w.Kind != KindDeployment {
			return fmt.Errorf("workload %s: hpa is only supported on Deployments", w.Name)
		}
		if w.HPA.Min < 1 || w.HPA.Max < w.HPA.Min || w.HPA.Target <= 0 {
			return fmt.Errorf("workload %s: hpa needs 1 <= min <= max and target > 0", w.Name)
		}
		if w.HPA.Metric == "" {
			w.HPA.Metric = "cpu"
		}
	}
	if len(w.Containers) == 0 {
		return fmt.Errorf("workload %s: no containers", w.Name)
	}
	for i := range w.Containers {
		if g := w.Containers[i].GPU; g != nil {
			g.applyDefaults()
			if err := g.validate(); err != nil {
				return fmt.Errorf("workload %s container %s: %w", w.Name, w.Containers[i].Name, err)
			}
			if w.Kind == KindDaemonSet {
				return fmt.Errorf("workload %s: gpu is not supported on DaemonSets", w.Name)
			}
		}
	}
	for _, c := range w.InitContainers {
		if c.GPU != nil {
			return fmt.Errorf("workload %s: init container %s: gpu is not supported on init containers", w.Name, c.Name)
		}
	}
	for _, c := range append(append([]ContainerSpec{}, w.Containers...), w.InitContainers...) {
		if c.Name == "" {
			return fmt.Errorf("workload %s: container without name", w.Name)
		}
		for _, s := range []Signal{c.CPU, c.Memory} {
			if s.Pattern != "" && !isPattern(s.Pattern) {
				return fmt.Errorf("workload %s container %s: unknown pattern %q (known: %s)", w.Name, c.Name, s.Pattern, strings.Join(Patterns, ", "))
			}
		}
	}
	if w.Replicas == 0 {
		w.Replicas = 1
	}
	return nil
}

func isPattern(p string) bool {
	for _, x := range Patterns {
		if x == p {
			return true
		}
	}
	return false
}

// Workload kinds.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
	KindCronJob     = "CronJob"
)
