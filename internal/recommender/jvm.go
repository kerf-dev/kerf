package recommender

// JVM-aware memory recommendations (D-007).
//
// A JVM sizes its maximum heap at start-up: from -Xmx (fixed), or as MaxRAMPercentage of the
// container memory limit (default 25 %). Memory below what the heap may grow to gets the container
// OOM-killed or the JVM thrashing in GC, so for JVM containers:
//   - fixed heap (-Xmx): memory >= max heap + non-heap estimate; memory is audit-only (flag
//     jvm-fixed-heap): extra memory is unused and less needs an -Xmx change, which the agent cannot do.
//   - percentage / default: memory follows the usage rule, plus a floor so that the maximum heap
//     after the change (pct x limit) still covers the heap need. Without heap metrics, the heap need
//     is the observed peak working set (an upper bound of the heap in use) capped at the current
//     maximum heap (the heap cannot have used more).
// Heap settings come from the agent's workload-status report. For clusters without an agent report
// (synthetic data) a JVM is recognised by its image only and the JVM defaults are assumed.

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// JVMConfig configures JVM-aware memory recommendations.
type JVMConfig struct {
	// NonHeapMinBytes / NonHeapRatio: non-heap memory (metaspace, code cache, threads, GC
	// structures, direct buffers) is estimated as max(NonHeapMinBytes, NonHeapRatio x heap).
	NonHeapMinBytes float64 `json:"nonHeapMinBytes"`
	NonHeapRatio    float64 `json:"nonHeapRatio"`
	// DefaultMaxRAMPercentage is the JVM default (25) used when no heap flag is set.
	DefaultMaxRAMPercentage float64 `json:"defaultMaxRAMPercentage"`
	// DetectByImage recognises JVMs by image name when the agent has not reported the workload.
	DetectByImage bool `json:"detectByImage"`
}

// DefaultJVMConfig returns the JVM defaults.
func DefaultJVMConfig() JVMConfig {
	return JVMConfig{NonHeapMinBytes: 64 * MiB, NonHeapRatio: 0.10, DefaultMaxRAMPercentage: 25, DetectByImage: true}
}

func (c JVMConfig) withDefaults() JVMConfig {
	d := DefaultJVMConfig()
	if c.NonHeapMinBytes <= 0 {
		c.NonHeapMinBytes = d.NonHeapMinBytes
	}
	if c.NonHeapRatio <= 0 {
		c.NonHeapRatio = d.NonHeapRatio
	}
	if c.DefaultMaxRAMPercentage <= 0 || c.DefaultMaxRAMPercentage > 100 {
		c.DefaultMaxRAMPercentage = d.DefaultMaxRAMPercentage
	}
	return c
}

// JVMInfo are the heap settings of a JVM container; 0 means "not set".
type JVMInfo struct {
	// Source is hubapi.JVMSourceAgent (reported) or hubapi.JVMSourceImage (image name only).
	Source     string
	DetectedBy []string
	// MaxHeap, InitialHeap and MaxRAM in bytes; MaxRAMPercentage in percent.
	MaxHeap, InitialHeap, MaxRAM float64
	MaxRAMPercentage             float64
}

// JVMFromReport converts an agent-reported JVM container; invalid quantities are ignored.
func JVMFromReport(r hubapi.JVMContainer) *JVMInfo {
	j := &JVMInfo{Source: hubapi.JVMSourceAgent, DetectedBy: slices.Clone(r.DetectedBy), MaxRAMPercentage: r.MaxRAMPercentage}
	for _, f := range []struct {
		s   string
		dst *float64
	}{{r.MaxHeap, &j.MaxHeap}, {r.InitialHeap, &j.InitialHeap}, {r.MaxRAM, &j.MaxRAM}} {
		if f.s == "" {
			continue
		}
		if v, err := ParseMemory(f.s); err == nil && v > 0 {
			*f.dst = v
		}
	}
	if j.MaxRAMPercentage < 0 || j.MaxRAMPercentage > 100 {
		j.MaxRAMPercentage = 0
	}
	return j
}

// jvmImage matches JVM base images by name or tag token (same heuristic as the agent).
var jvmImage = regexp.MustCompile(`(?i)(^|[/:_.@-])(java|openjdk|jdk|jre|temurin|corretto|amazoncorretto|zulu|graalvm|semeru|liberica|adoptopenjdk)[0-9]*($|[/:_.@-])`)

// IsJVMImage reports whether an image name looks like a JVM image.
func IsJVMImage(image string) bool { return jvmImage.MatchString(image) }

// ContainerImages returns the images of each workload container (kube_pod_container_info).
func (q Queries) ContainerImages(lookback time.Duration) string {
	wrap := lastOver(lookback)
	perPod := fmt.Sprintf("max by (cluster, namespace, pod, container, image) (%s)", q.rel(wrap("kube_pod_container_info"+q.sel())))
	return fmt.Sprintf("max by (%s, container, image) (\n%s\n* on (cluster, namespace, pod) group_left (workload_kind, workload)\n%s\n)",
		WorkloadLabels, perPod, q.PodWorkload(nil, wrap))
}

// containerImages collects images per workload container; the result of ContainerImages.
type containerImages map[hubapi.WorkloadKey]map[string][]string

func (m containerImages) add(cluster string, l Labels) {
	k := hubapi.WorkloadKey{Cluster: cluster, Namespace: l["namespace"], Kind: l["workload_kind"], Name: l["workload"]}
	if l["container"] == "" || l["image"] == "" {
		return
	}
	if m[k] == nil {
		m[k] = map[string][]string{}
	}
	m[k][l["container"]] = append(m[k][l["container"]], l["image"])
}

// apply sets RawContainer.Images on containers already in the inventory (look-up only).
func (m containerImages) apply(d *ClusterData) {
	for k, cs := range m {
		w := d.Workloads[k]
		if w == nil {
			continue
		}
		for name, imgs := range cs {
			if c := w.Containers[name]; c != nil {
				sort.Strings(imgs)
				c.Images = slices.Compact(imgs)
			}
		}
	}
}

// attachJVM sets ContainerData.JVM: from the agent report when the workload was reported
// (reported is true, even with no JVM containers), else from the image when enabled.
func attachJVM(data *WorkloadData, raw *RawWorkload, report []hubapi.JVMContainer, reported bool, cfg JVMConfig) {
	for i := range data.Containers {
		c := &data.Containers[i]
		if reported {
			for _, r := range report {
				if r.Name == c.Name {
					c.JVM = JVMFromReport(r)
				}
			}
			continue
		}
		rc := raw.Containers[c.Name]
		if !cfg.withDefaults().DetectByImage || rc == nil {
			continue
		}
		if slices.ContainsFunc(rc.Images, IsJVMImage) {
			c.JVM = &JVMInfo{Source: hubapi.JVMSourceImage, DetectedBy: []string{hubapi.JVMDetectedByImage}}
		}
	}
}

// jvmPlan is the JVM floor for one container.
type jvmPlan struct {
	info   *JVMInfo
	sizing string
	// pct is the heap fraction of the memory limit (percentage/default sizing).
	pct float64
	// ram caps the memory the JVM sizes from (-XX:MaxRAM); 0 = none.
	ram float64
	// heapNeed, nonHeap and maxHeapBefore in bytes (0 = unknown / not applicable).
	heapNeed, nonHeap, maxHeapBefore float64
	// limitFloor is the minimum memory limit; requestFloor the minimum request.
	limitFloor, requestFloor float64
}

func planJVM(c ContainerData, cfg JVMConfig) *jvmPlan {
	j := c.JVM
	if j == nil {
		return nil
	}
	cfg = cfg.withDefaults()
	nonHeap := func(heap float64) float64 { return math.Max(cfg.NonHeapMinBytes, cfg.NonHeapRatio*heap) }
	p := &jvmPlan{info: j, ram: j.MaxRAM}
	if j.MaxHeap > 0 {
		p.sizing = hubapi.JVMHeapFixed
		p.maxHeapBefore = j.MaxHeap
		p.nonHeap = nonHeap(j.MaxHeap)
		p.limitFloor = ceilMiB(math.Max(j.MaxHeap, j.InitialHeap) + p.nonHeap)
		p.requestFloor = p.limitFloor
		return p
	}
	p.sizing, p.pct = hubapi.JVMHeapDefault, cfg.DefaultMaxRAMPercentage/100
	if j.MaxRAMPercentage > 0 {
		p.sizing, p.pct = hubapi.JVMHeapPercentage, j.MaxRAMPercentage/100
	}
	p.maxHeapBefore = p.maxHeap(c.Current.MemoryLimit)
	p.heapNeed = math.Max(c.MemoryPeak, c.Memory.Max)
	if p.maxHeapBefore > 0 {
		p.heapNeed = math.Min(p.heapNeed, p.maxHeapBefore)
	}
	p.nonHeap = nonHeap(p.heapNeed)
	// pct x min(limit, MaxRAM) >= heapNeed. heapNeed <= the current max heap <= pct x MaxRAM, so
	// the MaxRAM cap never makes this unreachable.
	// A kept limit is always above this floor (heapNeed <= pct x current limit); it binds when the
	// limit follows the request.
	p.limitFloor = ceilMiB(p.heapNeed / p.pct)
	if j.InitialHeap > 0 {
		// The heap is committed up to -Xms at start-up.
		p.requestFloor = ceilMiB(j.InitialHeap + nonHeap(j.InitialHeap))
	}
	return p
}

// maxHeap returns the maximum heap the JVM sizes for a memory limit (0 = unknown: no limit and no
// MaxRAM, the heap then follows the node's memory).
func (p *jvmPlan) maxHeap(limit float64) float64 {
	if p.sizing == hubapi.JVMHeapFixed {
		return p.info.MaxHeap
	}
	ram := limit
	if p.ram > 0 && (ram <= 0 || p.ram < ram) {
		ram = p.ram
	}
	return p.pct * ram
}

// raiseRequest applies the floor to the unrounded memory request v. limitFollows is true when the
// limit will equal the request (so the request also sizes the heap).
func (p *jvmPlan) raiseRequest(v float64, limitFollows bool, e *hubapi.ResourceExplanation) float64 {
	switch {
	case p.sizing == hubapi.JVMHeapFixed && v < p.requestFloor:
		e.Notes = append(e.Notes, "raised to "+FormatMemory(p.requestFloor)+": JVM heap "+FormatMemory(math.Max(p.info.MaxHeap, p.info.InitialHeap))+
			" + non-heap "+FormatMemory(p.nonHeap))
		return p.requestFloor
	case limitFollows && v < p.limitFloor && p.limitFloor >= p.requestFloor:
		e.Notes = append(e.Notes, "raised to "+FormatMemory(p.limitFloor)+": JVM max heap ("+trimFloat(round(p.pct*100, 2))+
			"% of the limit) must cover the heap need "+FormatMemory(p.heapNeed))
		return p.limitFloor
	case v < p.requestFloor:
		e.Notes = append(e.Notes, "raised to "+FormatMemory(p.requestFloor)+": JVM initial heap -Xms "+FormatMemory(p.info.InitialHeap)+
			" + non-heap "+FormatMemory(math.Max(0, p.requestFloor-p.info.InitialHeap)))
		return p.requestFloor
	}
	return v
}

// floor is the smallest memory the JVM needs for a final limit (0 = no limit).
func (p *jvmPlan) floor(limit float64) float64 {
	if limit <= 0 {
		return p.requestFloor
	}
	return math.Max(p.limitFloor, p.requestFloor)
}

func ceilMiB(v float64) float64 { return math.Ceil(v/MiB) * MiB }

// explain finishes the explanation for the final limit and returns the container's JVM flags.
func (p *jvmPlan) explain(limit float64, e *hubapi.ResourceExplanation) []string {
	x := &hubapi.JVMExplanation{
		HeapSizing: p.sizing, DetectedBy: slices.Clone(p.info.DetectedBy), Source: p.info.Source,
		NonHeap: FormatMemory(p.nonHeap),
	}
	if x.DetectedBy == nil {
		x.DetectedBy = []string{}
	}
	if p.pct > 0 {
		x.MaxRAMPercentage = round(p.pct*100, 2)
	}
	if p.maxHeapBefore > 0 {
		x.MaxHeapBefore = FormatMemory(p.maxHeapBefore)
	}
	after := p.maxHeap(limit)
	if after > 0 {
		x.MaxHeapAfter = FormatMemory(after)
	}
	if p.heapNeed > 0 {
		x.HeapNeed = FormatMemory(p.heapNeed)
	}
	if f := p.floor(limit); f > 0 {
		x.Floor = FormatMemory(f)
	}
	e.JVM = x

	if p.info.Source == hubapi.JVMSourceImage {
		e.Notes = append(e.Notes, "JVM detected from the image, heap settings unknown (no agent report): assuming the JVM default MaxRAMPercentage="+trimFloat(round(p.pct*100, 2)))
	}
	switch p.sizing {
	case hubapi.JVMHeapFixed:
		e.Notes = append(e.Notes, "JVM with a fixed max heap "+FormatMemory(p.info.MaxHeap)+": memory is audit-only (jvm-fixed-heap) and never applied by the agent; "+
			"it cannot use more memory and needs at least heap + non-heap "+FormatMemory(p.limitFloor)+". Lower -Xmx together with the memory to save more")
		return []string{hubapi.FlagJVMFixedHeap}
	default:
		if limit <= 0 {
			e.Notes = append(e.Notes, "JVM without a memory limit: the max heap follows the node's memory")
		} else {
			e.Notes = append(e.Notes, "JVM max heap after the change: "+trimFloat(round(p.pct*100, 2))+"% of "+FormatMemory(limit)+" = "+FormatMemory(after)+
				", covers the heap need "+FormatMemory(p.heapNeed)+" (observed peak, capped at the current max heap)")
		}
	}
	return nil
}
