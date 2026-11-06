package synth

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Timeline is the sample grid: N samples, Step apart, the first at Start. Start is aligned to
// Step so repeated backfills land on the same timestamps.
type Timeline struct {
	Start time.Time
	Step  time.Duration
	N     int
}

// NewTimeline returns the grid of `window` ending at end (aligned down to step).
func NewTimeline(end time.Time, window, step time.Duration) Timeline {
	e := end.Truncate(step)
	n := int(window/step) + 1
	return Timeline{Start: e.Add(-time.Duration(n-1) * step), Step: step, N: n}
}

// Ms returns the unix-millisecond timestamp of sample i.
func (tl Timeline) Ms(i int) int64 { return tl.Start.Add(time.Duration(i) * tl.Step).UnixMilli() }

// End is the time of the last sample.
func (tl Timeline) End() time.Time { return tl.Start.Add(time.Duration(tl.N-1) * tl.Step) }

func (tl Timeline) steps(d time.Duration) int { return int(d / tl.Step) }

// Options control a run.
type Options struct {
	End  time.Time // default now
	Days float64   // overrides Scenario.Days when > 0
	Seed *int64    // overrides Scenario.Seed
	Step time.Duration
	// Clusters limits the run to these cluster names (all when empty).
	Clusters []string
	// Start fixes the first sample (live mode: continue an earlier backfill); Days is then ignored.
	Start time.Time
	// Anchor is the time scenario offsets (age, rollouts, stepAt) are measured back from.
	// Default: the end. Live mode keeps the backfill's end as anchor, so extending the timeline
	// reproduces the past exactly and only appends.
	Anchor time.Time
}

// Generator holds an expanded scenario.
type Generator struct {
	Scenario *Scenario
	Timeline Timeline
	Clusters []*ClusterModel
	anchor   int // sample index of Options.Anchor
}

// Anchor returns the time scenario offsets are measured back from.
func (g *Generator) Anchor() time.Time {
	return g.Timeline.Start.Add(time.Duration(g.anchor) * g.Timeline.Step)
}

// at returns the sample index d before the anchor.
func (g *Generator) at(d time.Duration) int { return g.anchor - g.Timeline.steps(d) }

// ClusterModel is an expanded cluster: nodes and fully specified workloads.
type ClusterModel struct {
	Spec      *ClusterSpec
	Nodes     []*Node
	Workloads []*WorkloadSpec
	nodeCPU   float64 // total allocatable CPU of the nodes without GPUs, for weighted placement
	// gpuSlots maps a GPU workload ("namespace/name") to the node of each of its pod slots; GPU
	// pods run one per GPU node.
	gpuSlots map[string][]*Node
}

// Node is one synthetic node.
type Node struct {
	Name, InstanceType, CapacityType, Region, Zone, IP, ProviderID string
	Type                                                           InstanceType
}

// New expands the scenario into a Generator.
func New(sc *Scenario, opt Options) (*Generator, error) {
	if opt.Seed != nil {
		sc.Seed = *opt.Seed
	}
	if opt.Days > 0 {
		sc.Days = opt.Days
	}
	if opt.Step > 0 {
		sc.Step = Duration(opt.Step)
	}
	end := opt.End
	if end.IsZero() {
		end = time.Now()
	}
	step := sc.Step.D()
	if !opt.Start.IsZero() {
		if opt.Start.Truncate(step) != opt.Start || !end.After(opt.Start) {
			return nil, fmt.Errorf("start %s must be aligned to the step and before the end", opt.Start)
		}
		sc.Days = math.Min(35, end.Sub(opt.Start).Hours()/24)
	}
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	window := time.Duration(sc.Days * float64(24*time.Hour))
	tl := NewTimeline(end, window, step)
	if !opt.Start.IsZero() {
		tl = Timeline{Start: opt.Start, Step: step, N: int(end.Truncate(step).Sub(opt.Start)/step) + 1}
		window = tl.End().Sub(tl.Start)
	}
	g := &Generator{Scenario: sc, Timeline: tl, anchor: tl.N - 1}
	if !opt.Anchor.IsZero() {
		g.anchor = int(opt.Anchor.Sub(tl.Start) / step)
		if g.anchor < 0 || g.anchor >= tl.N {
			return nil, fmt.Errorf("anchor %s outside the timeline", opt.Anchor)
		}
		window = time.Duration(g.anchor) * step
	}
	for i := range sc.Clusters {
		cs := &sc.Clusters[i]
		if len(opt.Clusters) > 0 && !contains(opt.Clusters, cs.Name) {
			continue
		}
		cm := &ClusterModel{Spec: cs}
		for j := range cs.Workloads {
			w := cs.Workloads[j]
			cm.Workloads = append(cm.Workloads, &w)
		}
		for _, w := range expandFleet(sc, cs, window) {
			if err := w.validate(); err != nil {
				return nil, fmt.Errorf("generated workload: %w", err)
			}
			cm.Workloads = append(cm.Workloads, &w)
		}
		g.sizeNodes(cm)
		if err := g.placeGPU(cm); err != nil {
			return nil, fmt.Errorf("cluster %s: %w", cs.Name, err)
		}
		g.Clusters = append(g.Clusters, cm)
	}
	if len(g.Clusters) == 0 {
		return nil, fmt.Errorf("no clusters selected")
	}
	return g, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// sizeNodes creates nodes for every pool; pools without a count are sized so that the average
// requests fill RequestPacking of allocatable capacity.
func (g *Generator) sizeNodes(cm *ClusterModel) {
	var cpu, mem, dsCPU, dsMem float64
	for _, w := range cm.Workloads {
		if w.GPUsPerPod() > 0 {
			continue // runs on GPU nodes, placed by placeGPU
		}
		var c, m float64
		for _, ct := range w.Containers {
			c += float64(ct.Requests.CPU)
			m += float64(ct.Requests.Memory)
		}
		switch {
		case w.Kind == KindDaemonSet:
			dsCPU += c
			dsMem += m
			continue
		case w.Kind == KindCronJob:
			c *= w.Schedule.Duration.D().Hours() / w.Schedule.Every.D().Hours()
			m *= w.Schedule.Duration.D().Hours() / w.Schedule.Every.D().Hours()
		case w.HPA != nil:
			avg := float64(w.HPA.Min+w.HPA.Max) / 2 * 0.7
			c, m = c*avg, m*avg
		default:
			c, m = c*float64(w.Replicas), m*float64(w.Replicas)
		}
		cpu += c
		mem += m
	}
	totalW := 0.0
	for _, p := range cm.Spec.NodePools {
		if InstanceTypes[p.InstanceType].GPUs == 0 {
			totalW += p.Weight
		}
	}
	r := newRand(g.Scenario.Seed, cm.Spec.Name, "nodes")
	for _, p := range cm.Spec.NodePools {
		it := InstanceTypes[p.InstanceType]
		if it.GPUs > 0 {
			continue // GPU pools: placeGPU
		}
		n := p.Count
		if n == 0 {
			share := p.Weight / totalW
			pack := cm.Spec.RequestPacking
			byCPU := cpu * share / ((it.AllocatableCPU - dsCPU) * pack)
			byMem := mem * share / ((it.AllocatableMemory - dsMem) * pack)
			n = int(math.Ceil(math.Max(byCPU, byMem)))
			if n < 2 {
				n = 2
			}
		}
		for i := 0; i < n; i++ {
			cm.addNode(r, p, i)
		}
	}
}

// addNode creates node i of pool p.
func (cm *ClusterModel) addNode(r *rnd, p NodePoolSpec, i int) *Node {
	it := InstanceTypes[p.InstanceType]
	zone := p.Zones[i%len(p.Zones)]
	ip := fmt.Sprintf("10.%d.%d.%d", 20+r.IntN(40), r.IntN(256), 1+r.IntN(254))
	node := &Node{
		Name:         fmt.Sprintf("ip-%s.%s.compute.internal", strings.ReplaceAll(ip, ".", "-"), cm.Spec.Region),
		InstanceType: p.InstanceType, CapacityType: p.CapacityType, Region: cm.Spec.Region, Zone: zone, IP: ip,
		ProviderID: fmt.Sprintf("aws:///%s/i-0%s", zone, r.hex(16)), Type: it,
	}
	cm.Nodes = append(cm.Nodes, node)
	if it.GPUs == 0 {
		cm.nodeCPU += it.AllocatableCPU
	}
	return node
}

// placeGPU creates the GPU nodes and assigns every pod slot of every GPU workload its own node:
// the pool with the fewest GPUs per node that fits the pod. Pools with a count provide that many
// nodes; pools without one grow on demand.
func (g *Generator) placeGPU(cm *ClusterModel) error {
	type pool struct {
		spec NodePoolSpec
		free []*Node
		made int
	}
	var pools []*pool
	r := newRand(g.Scenario.Seed, cm.Spec.Name, "gpu-nodes")
	for _, p := range cm.Spec.NodePools {
		if InstanceTypes[p.InstanceType].GPUs == 0 {
			continue
		}
		pl := &pool{spec: p}
		for i := 0; i < p.Count; i++ {
			pl.free = append(pl.free, cm.addNode(r, p, pl.made))
			pl.made++
		}
		pools = append(pools, pl)
	}
	cm.gpuSlots = map[string][]*Node{}
	for _, w := range cm.Workloads {
		perPod := w.GPUsPerPod()
		if perPod == 0 {
			continue
		}
		var best *pool
		for _, pl := range pools {
			if n := InstanceTypes[pl.spec.InstanceType].GPUs; n >= perPod && (best == nil || n < InstanceTypes[best.spec.InstanceType].GPUs) {
				best = pl
			}
		}
		if best == nil {
			return fmt.Errorf("workload %s: no node pool with %d GPUs per node", w.Name, perPod)
		}
		slots := w.Replicas
		switch {
		case w.HPA != nil:
			slots = w.HPA.Max
		case w.Kind == KindCronJob:
			slots = 1
		}
		for j := 0; j < slots; j++ {
			switch {
			case len(best.free) > 0:
			case best.spec.Count == 0:
				best.free = append(best.free, cm.addNode(r, best.spec, best.made))
				best.made++
			default:
				return fmt.Errorf("workload %s needs %d %s nodes for its GPU pods, pool has %d", w.Name, slots, best.spec.InstanceType, best.spec.Count)
			}
			cm.gpuSlots[w.Namespace+"/"+w.Name] = append(cm.gpuSlots[w.Namespace+"/"+w.Name], best.free[0])
			best.free = best.free[1:]
		}
	}
	return nil
}

// gpuNode returns the node of pod slot j of a GPU workload.
func (cm *ClusterModel) gpuNode(w *WorkloadSpec, j int) *Node {
	return cm.gpuSlots[w.Namespace+"/"+w.Name][j]
}

// pickNode chooses a node without GPUs, weighted by allocatable CPU.
func (cm *ClusterModel) pickNode(r *rnd) *Node {
	x := r.Float64() * cm.nodeCPU
	var last *Node
	for _, n := range cm.Nodes {
		if n.Type.GPUs > 0 {
			continue // GPU nodes only run GPU workloads
		}
		last = n
		x -= n.Type.AllocatableCPU
		if x < 0 {
			return n
		}
	}
	return last
}

// WorkloadTrace is the simulated history of one workload.
type WorkloadTrace struct {
	Cluster  *ClusterModel
	Spec     *WorkloadSpec
	Created  int       // first sample index the workload exists
	Replicas []float64 // desired replicas per sample (NaN before Created)
	Owners   []Owner   // ReplicaSets (Deployment) or Jobs (CronJob)
	Pods     []*PodTrace
}

// Owner is an intermediate controller object.
type Owner struct {
	Name       string
	Start, End int // [Start, End) sample indices
}

// PodTrace is one pod's life.
type PodTrace struct {
	Name, UID, PodIP     string
	Node                 *Node
	OwnerKind, OwnerName string
	Start, End           int // [Start, End) sample indices
	Revision             int
	Containers           []*ContainerTrace
}

// ContainerTrace holds per-sample values for one container of a pod; index k is sample
// Pod.Start+k.
type ContainerTrace struct {
	Spec      *ContainerSpec
	Image     string
	CPU       []float64 // cores used (average over the step)
	Throttled []float64 // fraction of CFS periods throttled
	Periods   []float64 // CFS periods per second
	Mem       []float64 // working set bytes
	Instances []Instance
	// GPU series per GPU of the pod (index = GPU of the node, 0..PerPod-1), nil without GPUs:
	// utilisation in percent (integers) and framebuffer used in MiB (integers). Index k is sample
	// Pod.Start+k like the other fields.
	GPUUtil, GPUUsedMiB [][]float64
}

// Instance is one run of a container (a restart starts a new one).
type Instance struct {
	Start       int    // absolute sample index
	ContainerID string // 64 hex chars
	EndReason   string // why it ended: OOMKilled, Error, or "" if it still runs at pod end
}

// Each simulates every workload of every cluster with `parallel` goroutines and calls fn for
// each trace. fn may be called concurrently.
func (g *Generator) Each(parallel int, fn func(*WorkloadTrace) error) error {
	if parallel < 1 {
		parallel = 1
	}
	type job struct {
		cm *ClusterModel
		w  *WorkloadSpec
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	stop := make(chan struct{})
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := fn(g.Simulate(j.cm, j.w)); err != nil {
					once.Do(func() { firstErr = err; close(stop) })
				}
			}
		}()
	}
outer:
	for _, cm := range g.Clusters {
		for _, w := range cm.Workloads {
			select {
			case jobs <- job{cm, w}:
			case <-stop:
				break outer
			}
		}
	}
	close(jobs)
	wg.Wait()
	return firstErr
}

// Simulate produces the trace of one workload.
func (g *Generator) Simulate(cm *ClusterModel, w *WorkloadSpec) *WorkloadTrace {
	tl := g.Timeline
	seed := g.Scenario.Seed
	r := newRand(seed, cm.Spec.Name, w.Namespace, w.Name, "workload")
	wt := &WorkloadTrace{Cluster: cm, Spec: w}
	if w.Age > 0 {
		wt.Created = max(0, g.at(w.Age.D()))
	}
	// Workload-level shapes per container (shared by all pods: traffic is shared).
	cpuShapes := make([][]float64, len(w.Containers))
	memShapes := make([][]float64, len(w.Containers))
	gpuShapes := make([][]float64, len(w.Containers))
	for i := range w.Containers {
		c := &w.Containers[i]
		if c.GPU != nil {
			gpuShapes[i] = g.shape(normalize(gpuUtilSignal(c.GPU), false), newRand(seed, cm.Spec.Name, w.Namespace, w.Name, c.Name, "gpu-util"))
		}
		cpuShapes[i] = g.shape(normalize(c.CPU, true), newRand(seed, cm.Spec.Name, w.Namespace, w.Name, c.Name, "cpu"))
		memShapes[i] = g.shape(normalize(c.Memory, false), newRand(seed, cm.Spec.Name, w.Namespace, w.Name, c.Name, "mem"))
	}

	// Releases: explicit rollouts plus step changes.
	revStarts := []int{wt.Created}
	for _, d := range w.Rollouts {
		revStarts = append(revStarts, g.at(d.D()))
	}
	for _, c := range w.Containers {
		if c.CPU.StepFactor != 0 && c.CPU.StepAt > 0 {
			revStarts = append(revStarts, g.at(c.CPU.StepAt.D()))
		}
		if c.Memory.StepFactor != 0 && c.Memory.StepAt > 0 {
			revStarts = append(revStarts, g.at(c.Memory.StepAt.D()))
		}
	}
	sort.Ints(revStarts)
	revs := revStarts[:0]
	for _, s := range revStarts {
		if s >= wt.Created && s < tl.N && (len(revs) == 0 || s > revs[len(revs)-1]) {
			revs = append(revs, s)
		}
	}
	revAt := func(i int) int { return sort.SearchInts(revs, i+1) - 1 }

	// Replicas.
	wt.Replicas = make([]float64, tl.N)
	for i := range wt.Replicas {
		wt.Replicas[i] = math.NaN()
	}
	var perPodLoad []float64 // HPA only: total load / replicas
	switch {
	case w.HPA != nil:
		perPodLoad = g.hpaReplicas(w, cpuShapes[0], wt)
	case w.Kind == KindDaemonSet:
		for i := wt.Created; i < tl.N; i++ {
			wt.Replicas[i] = float64(len(cm.Nodes))
		}
	case w.Kind == KindCronJob:
	default:
		for i := wt.Created; i < tl.N; i++ {
			wt.Replicas[i] = float64(w.Replicas)
		}
	}

	// ReplicaSet names per revision.
	rsNames := make([]string, len(revs))
	for k := range revs {
		rsNames[k] = w.Name + "-" + r.suffix(9+r.IntN(2))
		if w.Kind == KindDeployment {
			wt.Owners = append(wt.Owners, Owner{Name: rsNames[k], Start: revs[k], End: tl.N})
		}
	}

	// Each pod's random stream is keyed by its identity (slot and sequence, or Job), so a longer
	// timeline only appends pods and never changes earlier ones.
	newPod := func(key string, start, end, rev int, node *Node, name, ownerKind, ownerName string) {
		pr := newRand(seed, cm.Spec.Name, w.Namespace, w.Name, "pod", key)
		if node == nil {
			if w.GPUsPerPod() > 0 {
				node = cm.gpuNode(w, 0) // CronJob runs
			} else {
				node = cm.pickNode(pr)
			}
		}
		p := &PodTrace{Name: name, UID: pr.uid(), Node: node, OwnerKind: ownerKind, OwnerName: ownerName,
			Start: start, End: end, Revision: rev,
			PodIP: fmt.Sprintf("10.%d.%d.%d", 100+pr.IntN(100), pr.IntN(256), 1+pr.IntN(254))}
		for ci := range w.Containers {
			// Own stream per container: containers are simulated one after the other, so a shared
			// stream would make the second depend on how long the first ran.
			cr := newRand(seed, cm.Spec.Name, w.Namespace, w.Name, "pod", key, w.Containers[ci].Name)
			ct := g.simContainer(cr, w, ci, p, cpuShapes[ci], memShapes[ci], perPodLoad)
			if gs := w.Containers[ci].GPU; gs != nil {
				cname := w.Containers[ci].Name
				ct.GPUUtil, ct.GPUUsedMiB = g.simGPU(newRand(seed, cm.Spec.Name, w.Namespace, w.Name, "pod", key, cname, "gpu"),
					func(d int) *rnd {
						return newRand(seed, cm.Spec.Name, w.Namespace, w.Name, "pod", key, cname, "gpu", strconv.Itoa(d))
					}, gs, p.End-p.Start, gpuShapes[ci][p.Start:p.End])
			}
			p.Containers = append(p.Containers, ct)
		}
		wt.Pods = append(wt.Pods, p)
	}

	lifetime := func(n *Node, pr *rnd) int {
		mean := w.MeanPodLifetime.D()
		if mean < 0 {
			return math.MaxInt32
		}
		if mean == 0 {
			mean = 30 * 24 * time.Hour
			if n != nil && n.CapacityType == "spot" {
				mean = 7 * 24 * time.Hour
			}
		}
		return max(1, int(pr.expo(float64(mean))/float64(tl.Step)))
	}

	switch w.Kind {
	case KindCronJob:
		g.cronJobs(w, wt, newPod)
	default:
		slots := w.Replicas
		if w.HPA != nil {
			slots = w.HPA.Max
		}
		if w.Kind == KindDaemonSet {
			slots = len(cm.Nodes)
		}
		for j := 0; j < slots; j++ {
			sr := newRand(seed, cm.Spec.Name, w.Namespace, w.Name, "slot", strconv.Itoa(j))
			cur := -1 // start index of current pod
			curRev, deadline, seq := -1, 0, 0
			var curNode *Node
			flush := func(end int) {
				if cur < 0 {
					return
				}
				var name, ok, on string
				switch w.Kind {
				case KindDeployment:
					name, ok, on = rsNames[curRev]+"-"+sr.suffix(5), "ReplicaSet", rsNames[curRev]
				case KindStatefulSet:
					name, ok, on = fmt.Sprintf("%s-%d", w.Name, j), KindStatefulSet, w.Name
				case KindDaemonSet:
					name, ok, on = w.Name+"-"+sr.suffix(5), KindDaemonSet, w.Name
				}
				newPod(fmt.Sprintf("%d/%d", j, seq), cur, end, curRev, curNode, name, ok, on)
				seq++
				cur = -1
			}
			for i := wt.Created; i < tl.N; i++ {
				active := w.HPA == nil || wt.Replicas[i] > float64(j)
				// Rollouts replace pods one slot at a time.
				rev := revAt(max(wt.Created, i-j))
				if cur >= 0 && (!active || rev != curRev || i >= deadline) {
					flush(i)
				}
				if active && cur < 0 {
					cur, curRev = i, rev
					curNode = nil
					switch {
					case w.Kind == KindDaemonSet:
						curNode = cm.Nodes[j]
					case w.GPUsPerPod() > 0:
						curNode = cm.gpuNode(w, j)
					default:
						curNode = cm.pickNode(sr)
					}
					deadline = math.MaxInt32
					if w.Kind != KindDaemonSet {
						deadline = i + lifetime(curNode, sr)
					}
				}
			}
			flush(tl.N)
		}
	}
	return wt
}

// hpaReplicas fills wt.Replicas from the HPA rule and returns per-pod CPU demand.
func (g *Generator) hpaReplicas(w *WorkloadSpec, shape []float64, wt *WorkloadTrace) []float64 {
	tl := g.Timeline
	h := w.HPA
	req := float64(w.Containers[0].Requests.CPU)
	per := make([]float64, tl.N)
	window := max(1, tl.steps(5*time.Minute))
	desired := make([]float64, tl.N)
	cur := float64(h.Min)
	for i := wt.Created; i < tl.N; i++ {
		load := shape[i] * float64(h.Min)
		d := math.Ceil(load / (float64(h.Target) / 100 * req))
		desired[i] = math.Max(float64(h.Min), math.Min(float64(h.Max), d))
		if desired[i] > cur {
			cur = desired[i]
		} else {
			// Scale down only to the highest recommendation of the stabilization window.
			hi := 0.0
			for k := max(wt.Created, i-window+1); k <= i; k++ {
				hi = math.Max(hi, desired[k])
			}
			if hi < cur {
				cur = hi
			}
		}
		wt.Replicas[i] = cur
		per[i] = load / cur
	}
	return per
}

func (g *Generator) cronJobs(w *WorkloadSpec, wt *WorkloadTrace, newPod func(key string, start, end, rev int, node *Node, name, ok, on string)) {
	tl := g.Timeline
	every, off, dur := w.Schedule.Every.D(), w.Schedule.Offset.D(), w.Schedule.Duration.D()
	day := tl.Start.Truncate(24 * time.Hour)
	created := tl.Start.Add(time.Duration(wt.Created) * tl.Step)
	for t := day.Add(off); !t.After(tl.End()); t = t.Add(every) {
		if t.Before(created) {
			continue
		}
		job := fmt.Sprintf("%s-%d", w.Name, t.Unix()/60)
		jr := newRand(g.Scenario.Seed, wt.Cluster.Spec.Name, w.Namespace, job)
		start := int(t.Sub(tl.Start) / tl.Step)
		n := max(1, int(float64(dur)*jr.uniform(0.8, 1.2)/float64(tl.Step)))
		end := min(tl.N, start+n)
		wt.Owners = append(wt.Owners, Owner{Name: job, Start: start, End: end})
		newPod(job, start, end, 0, nil, job+"-"+jr.suffix(5), "Job", job)
	}
}

// normalize fills pattern defaults.
func normalize(s Signal, cpu bool) Signal {
	switch s.Pattern {
	case "diurnal", "throttled":
		if s.Diurnal == 0 {
			s.Diurnal = 0.5
		}
	case "weekly":
		if s.Diurnal == 0 {
			s.Diurnal = 0.4
		}
		if s.Weekend == 0 {
			s.Weekend = 0.5
		}
	case "spiky":
		if s.SpikesPerDay == 0 {
			s.SpikesPerDay = 6
		}
		if s.SpikeMult == 0 {
			s.SpikeMult = 3
		}
	case "leak":
		if s.LeakPerHour == 0 {
			s.LeakPerHour = 0.02
		}
	case "step":
		if s.StepFactor == 0 {
			s.StepFactor = 2
		}
	case "sawtooth":
		if s.Sawtooth == 0 {
			s.Sawtooth = 0.1
		}
	}
	if s.SpikesPerDay > 0 && s.SpikeMinutes == 0 {
		s.SpikeMinutes = 5
	}
	if s.Sawtooth > 0 && s.SawtoothMinutes == 0 {
		s.SawtoothMinutes = 15
	}
	if s.StartupMult > 0 && s.StartupMinutes == 0 {
		s.StartupMinutes = 5
	}
	if s.Diurnal > 0 && s.PeakHour == 0 {
		s.PeakHour = 12
	}
	if s.Weekend == 0 {
		s.Weekend = 1
	}
	if s.Noise == 0 {
		s.Noise = 0.02
		if cpu {
			s.Noise = 0.08
		}
	}
	if s.Noise < 0 {
		s.Noise = 0
	}
	return s
}

// shape returns the workload-level value of a signal at every sample (before per-pod noise,
// startup, leak and sawtooth, which depend on the container instance).
func (g *Generator) shape(s Signal, r *rnd) []float64 {
	tl := g.Timeline
	out := make([]float64, tl.N)
	base := float64(s.Base)
	stepAt := tl.N
	if s.StepFactor != 0 {
		at := s.StepAt.D()
		if at == 0 {
			at = time.Duration(g.anchor/2) * tl.Step
		}
		stepAt = g.at(at)
	}
	stepMin := tl.Step.Minutes()
	for i := range out {
		t := tl.Start.Add(time.Duration(i) * tl.Step).UTC()
		v := base
		if s.Diurnal != 0 {
			h := float64(t.Hour()) + float64(t.Minute())/60
			v *= 1 + s.Diurnal*math.Cos(2*math.Pi*(h-s.PeakHour)/24)
		}
		if wd := t.Weekday(); s.Weekend != 1 && (wd == time.Saturday || wd == time.Sunday) {
			v *= s.Weekend
		}
		if i >= stepAt {
			v *= s.StepFactor
		}
		out[i] = math.Max(v, base*0.02)
	}
	if s.SpikesPerDay > 0 {
		// Spikes are drawn day by day in time order, so extending the timeline keeps them.
		perDay := max(1, int(1440/stepMin))
		width := max(1, int(s.SpikeMinutes/stepMin))
		for d0 := 0; d0 < tl.N; d0 += perDay {
			n := r.poisson(s.SpikesPerDay)
			for k := 0; k < n; k++ {
				at := d0 + r.IntN(perDay)
				m := s.SpikeMult * r.uniform(0.8, 1.2)
				for d := 0; d < width && at+d < tl.N; d++ {
					out[at+d] *= m
				}
			}
		}
	}
	return out
}

// simContainer simulates one container of one pod, including OOM kills and crashes.
func (g *Generator) simContainer(r *rnd, w *WorkloadSpec, ci int, p *PodTrace, cpuShape, memShape, perPodLoad []float64) *ContainerTrace {
	tl := g.Timeline
	spec := &w.Containers[ci]
	cs, ms := normalize(spec.CPU, true), normalize(spec.Memory, false)
	n := p.End - p.Start
	ct := &ContainerTrace{Spec: spec, Image: revImage(spec.Image, p.Revision, spec.Sidecar),
		CPU: make([]float64, n), Throttled: make([]float64, n), Periods: make([]float64, n), Mem: make([]float64, n)}
	cpuLim, memLim := float64(spec.Limits.CPU), float64(spec.Limits.Memory)
	stepMin := tl.Step.Minutes()
	podFC, podFM := r.uniform(0.85, 1.15), r.uniform(0.93, 1.07)
	thetaC, thetaM := math.Min(1, stepMin/3), math.Min(1, stepMin/90)
	ouC, ouM := 1.0, 1.0
	crashP := 0.0
	if spec.CrashEvery > 0 {
		crashP = float64(tl.Step) / float64(spec.CrashEvery.D())
	}
	instStart := p.Start
	ct.Instances = []Instance{{Start: p.Start, ContainerID: r.hex(64)}}
	for i := p.Start; i < p.End; i++ {
		k := i - p.Start
		up := float64(i-instStart) * stepMin // minutes since container start
		ouC += thetaC*(1-ouC) + cs.Noise*math.Sqrt(2*thetaC)*r.NormFloat64()
		ouC = clamp(ouC, 0.2, 3)
		ouM += thetaM*(1-ouM) + ms.Noise*math.Sqrt(2*thetaM)*r.NormFloat64()
		ouM = clamp(ouM, 0.5, 2)

		demand := cpuShape[i]
		if perPodLoad != nil && ci == 0 {
			demand = perPodLoad[i]
		}
		demand *= podFC * ouC * (1 + 0.3*cs.Noise*r.NormFloat64())
		if cs.StartupMult > 0 && up < cs.StartupMinutes {
			demand += float64(cs.Base) * cs.StartupMult * (1 - up/cs.StartupMinutes)
		}
		demand = math.Max(demand, 0.0005)
		used, thr, per := demand, 0.0, 0.0
		if cpuLim > 0 {
			x := demand / cpuLim
			per = 10 * clamp(0.15+1.5*x, 0, 1)
			switch {
			case x >= 1:
				used = cpuLim * r.uniform(0.95, 0.995)
				thr = clamp(0.25+0.7*(x-1), 0.25, 0.95)
			case x > 0.6:
				y := (x - 0.6) / 0.4
				thr = 0.2 * y * y
			}
		}
		ct.CPU[k], ct.Throttled[k], ct.Periods[k] = used, thr, per

		mem := memShape[i] * podFM * ouM
		if ms.LeakPerHour > 0 {
			mem += float64(ms.Base) * ms.LeakPerHour * up / 60
		}
		if ms.Sawtooth > 0 {
			ph := math.Mod(up/ms.SawtoothMinutes+float64(ci)*0.37, 1)
			mem *= 1 - ms.Sawtooth/2 + ms.Sawtooth*ph
		}
		if ramp := math.Max(3, cs.StartupMinutes); up < ramp {
			mem *= 0.45 + 0.55*up/ramp
		}
		reason := ""
		if memLim > 0 && mem >= memLim*0.995 {
			mem = memLim * r.uniform(0.985, 0.998)
			reason = "OOMKilled"
		} else if crashP > 0 && r.Float64() < crashP {
			reason = "Error"
		}
		ct.Mem[k] = math.Round(mem/4096) * 4096
		if reason != "" && i+1 < p.End {
			ct.Instances[len(ct.Instances)-1].EndReason = reason
			ct.Instances = append(ct.Instances, Instance{Start: i + 1, ContainerID: r.hex(64)})
			instStart = i + 1
		}
	}
	return ct
}

func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }

// revImage bumps the patch version of an image tag for each revision.
func revImage(image string, rev int, fixed bool) string {
	if rev == 0 || fixed {
		return image
	}
	i := strings.LastIndexByte(image, ':')
	if i < 0 {
		return image
	}
	tag := image[i+1:]
	sem, rest, _ := strings.Cut(tag, "-")
	parts := strings.Split(sem, ".")
	if len(parts) != 3 {
		return image
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return image
	}
	parts[2] = strconv.Itoa(patch + rev)
	tag = strings.Join(parts, ".")
	if rest != "" {
		tag += "-" + rest
	}
	return image[:i+1] + tag
}
