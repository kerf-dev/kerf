package synth

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Label is one name/value pair.
type Label struct{ Name, Value string }

// Series is one time series on the generator's timeline: samples Start..Start+Len-1. Values is
// nil for a constant series (every sample is Const).
type Series struct {
	Labels []Label // sorted by name; includes __name__
	Start  int
	Len    int
	Values []float64
	Const  float64
}

// Name returns the metric name.
func (s *Series) Name() string {
	for _, l := range s.Labels {
		if l.Name == "__name__" {
			return l.Value
		}
	}
	return ""
}

// Label returns a label value.
func (s *Series) Label(name string) string {
	for _, l := range s.Labels {
		if l.Name == name {
			return l.Value
		}
	}
	return ""
}

// Value returns sample k (0-based from Start).
func (s *Series) Value(k int) float64 {
	if s.Values == nil {
		return s.Const
	}
	return s.Values[k]
}

// Emit receives generated series. It may be called concurrently.
type Emit func(*Series) error

// Clip returns an Emit that forwards only the samples in [from, to) (sample indices).
func Clip(emit Emit, from, to int) Emit {
	return func(s *Series) error {
		lo, hi := max(s.Start, from), min(s.Start+s.Len, to)
		if lo >= hi {
			return nil
		}
		c := *s
		c.Start, c.Len = lo, hi-lo
		if s.Values != nil {
			c.Values = s.Values[lo-s.Start : hi-s.Start]
		}
		return emit(&c)
	}
}

const (
	ksmJob      = "kube-state-metrics"
	ksmInstance = "kube-state-metrics.monitoring.svc:8080"
	cadvisorJob = "cadvisor"
	dcgmJob     = "nvidia-dcgm-exporter"
	// gpuResource is how kube-state-metrics names the nvidia.com/gpu resource.
	gpuResource = "nvidia_com_gpu"
)

type labels []Label

func (ls labels) with(kv ...string) labels {
	out := make(labels, len(ls), len(ls)+len(kv)/2)
	copy(out, ls)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, Label{kv[i], kv[i+1]})
	}
	return out
}

func (ls labels) sorted() []Label {
	out := make([]Label, 0, len(ls))
	for _, l := range ls {
		if l.Value != "" || l.Name == "__name__" {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func constSeries(ls labels, start, end int, v float64) *Series {
	return &Series{Labels: ls.sorted(), Start: start, Len: end - start, Const: v}
}

func valueSeries(ls labels, start int, vals []float64) *Series {
	return &Series{Labels: ls.sorted(), Start: start, Len: len(vals), Values: vals}
}

// EmitNodes emits node-level kube-state-metrics series for a cluster.
func (g *Generator) EmitNodes(cm *ClusterModel, emit Emit) error {
	n := g.Timeline.N
	// Where this scenario's timeline starts and what it is anchored to, so `synth live` can
	// continue it later with identical results.
	sl := labels{{"cluster", cm.Spec.Name}, {"job", "synth"}, {"scenario", g.Scenario.Name}, {"seed", strconv.FormatInt(g.Scenario.Seed, 10)}}
	for _, s := range []*Series{
		constSeries(sl.with("__name__", "synth_scenario_start_timestamp_seconds"), 0, n, float64(g.Timeline.Start.Unix())),
		constSeries(sl.with("__name__", "synth_scenario_anchor_timestamp_seconds"), 0, n, float64(g.Anchor().Unix())),
	} {
		if err := emit(s); err != nil {
			return err
		}
	}
	base := labels{{"cluster", cm.Spec.Name}, {"job", ksmJob}, {"instance", ksmInstance}}
	for _, nd := range cm.Nodes {
		nl := base.with("node", nd.Name)
		out := []*Series{
			constSeries(nl.with("__name__", "kube_node_labels",
				"label_node_kubernetes_io_instance_type", nd.InstanceType,
				"label_karpenter_sh_capacity_type", nd.CapacityType,
				"label_topology_kubernetes_io_region", nd.Region,
				"label_topology_kubernetes_io_zone", nd.Zone), 0, n, 1),
			constSeries(nl.with("__name__", "kube_node_info", "internal_ip", nd.IP, "provider_id", nd.ProviderID,
				"kubelet_version", "v1.35.4-eks-4f4795d", "container_runtime_version", "containerd://2.1.4",
				"os_image", "Amazon Linux 2023.9.20260921", "kernel_version", "6.12.46-66.121.amzn2023.x86_64"), 0, n, 1),
		}
		for _, m := range []struct {
			name     string
			cpu, mem float64
		}{
			{"kube_node_status_allocatable", nd.Type.AllocatableCPU, nd.Type.AllocatableMemory},
			{"kube_node_status_capacity", nd.Type.CPU, nd.Type.Memory},
		} {
			out = append(out,
				constSeries(nl.with("__name__", m.name, "resource", "cpu", "unit", "core"), 0, n, m.cpu),
				constSeries(nl.with("__name__", m.name, "resource", "memory", "unit", "byte"), 0, n, m.mem),
				constSeries(nl.with("__name__", m.name, "resource", "pods", "unit", "integer"), 0, n, nd.Type.Pods))
		}
		if nd.Type.GPUs > 0 {
			for _, m := range []string{"kube_node_status_allocatable", "kube_node_status_capacity"} {
				out = append(out, constSeries(nl.with("__name__", m, "resource", gpuResource, "unit", "integer"), 0, n, float64(nd.Type.GPUs)))
			}
		}
		for _, s := range out {
			if err := emit(s); err != nil {
				return err
			}
		}
	}
	return nil
}

// EmitWorkload emits every series of a simulated workload.
func (g *Generator) EmitWorkload(wt *WorkloadTrace, emit Emit) error {
	e := &emitter{g: g, wt: wt, emit: emit}
	e.workload()
	for _, p := range wt.Pods {
		if e.err != nil {
			break
		}
		e.pod(p)
	}
	return e.err
}

type emitter struct {
	g    *Generator
	wt   *WorkloadTrace
	emit Emit
	err  error
}

func (e *emitter) out(s *Series) {
	if e.err == nil && s.Len > 0 {
		e.err = e.emit(s)
	}
}

func (e *emitter) ksm() labels {
	return labels{{"cluster", e.wt.Cluster.Spec.Name}, {"job", ksmJob}, {"instance", ksmInstance}, {"namespace", e.wt.Spec.Namespace}}
}

func (e *emitter) workload() {
	w, wt, n := e.wt.Spec, e.wt, e.g.Timeline.N
	ksm := e.ksm()
	switch w.Kind {
	case KindDeployment:
		for _, o := range wt.Owners {
			e.out(constSeries(ksm.with("__name__", "kube_replicaset_owner", "replicaset", o.Name,
				"owner_kind", KindDeployment, "owner_name", w.Name, "owner_is_controller", "true"), o.Start, o.End, 1))
		}
		e.out(valueSeries(ksm.with("__name__", "kube_deployment_spec_replicas", "deployment", w.Name), wt.Created, wt.Replicas[wt.Created:]))
	case KindStatefulSet:
		e.out(valueSeries(ksm.with("__name__", "kube_statefulset_replicas", "statefulset", w.Name), wt.Created, wt.Replicas[wt.Created:]))
	case KindDaemonSet:
		e.out(valueSeries(ksm.with("__name__", "kube_daemonset_status_desired_number_scheduled", "daemonset", w.Name), wt.Created, wt.Replicas[wt.Created:]))
	case KindCronJob:
		for _, o := range wt.Owners {
			e.out(constSeries(ksm.with("__name__", "kube_job_owner", "job_name", o.Name,
				"owner_kind", KindCronJob, "owner_name", w.Name, "owner_is_controller", "true"), o.Start, o.End, 1))
		}
	}
	if h := w.HPA; h != nil {
		hl := ksm.with("horizontalpodautoscaler", w.Name)
		e.out(constSeries(hl.with("__name__", "kube_horizontalpodautoscaler_info", "scaletargetref_api_version", "apps/v1",
			"scaletargetref_kind", KindDeployment, "scaletargetref_name", w.Name), wt.Created, n, 1))
		e.out(constSeries(hl.with("__name__", "kube_horizontalpodautoscaler_spec_target_metric", "metric_name", h.Metric,
			"metric_target_type", "utilization"), wt.Created, n, float64(h.Target)))
		e.out(constSeries(hl.with("__name__", "kube_horizontalpodautoscaler_spec_min_replicas"), wt.Created, n, float64(h.Min)))
		e.out(constSeries(hl.with("__name__", "kube_horizontalpodautoscaler_spec_max_replicas"), wt.Created, n, float64(h.Max)))
		e.out(valueSeries(hl.with("__name__", "kube_horizontalpodautoscaler_status_current_replicas"), wt.Created, wt.Replicas[wt.Created:]))
		e.out(valueSeries(hl.with("__name__", "kube_horizontalpodautoscaler_status_desired_replicas"), wt.Created, wt.Replicas[wt.Created:]))
	}
	if *e.g.Scenario.GroundTruth {
		exp := w.Expect
		if exp == nil {
			exp = &Expectation{}
		}
		e.out(constSeries(labels{{"cluster", wt.Cluster.Spec.Name}, {"job", "synth"}, {"namespace", w.Namespace},
			{"__name__", "synth_workload_info"}, {"workload_kind", w.Kind}, {"workload", w.Name}, {"profile", w.Profile},
			{"cpu_expect", exp.CPU}, {"memory_expect", exp.Memory}, {"risk_flags", strings.Join(exp.RiskFlags, ",")}},
			wt.Created, n, 1))
		e.gpuGroundTruth()
	}
}

// gpuGroundTruth emits synth_gpu_workload_info for workloads with GPUs (not part of the contract).
func (e *emitter) gpuGroundTruth() {
	w, wt := e.wt.Spec, e.wt
	for _, ci := range w.gpuContainers() {
		gs := w.Containers[ci].GPU
		var status, suggestions string
		if gs.Expect != nil {
			status, suggestions = gs.Expect.Status, strings.Join(gs.Expect.Suggestions, ",")
		}
		e.out(constSeries(labels{{"cluster", wt.Cluster.Spec.Name}, {"job", "synth"}, {"namespace", w.Namespace},
			{"__name__", "synth_gpu_workload_info"}, {"workload_kind", w.Kind}, {"workload", w.Name}, {"container", w.Containers[ci].Name},
			{"gpus_per_pod", strconv.Itoa(gs.PerPod)}, {"model", gs.Model}, {"label_style", gs.LabelStyle},
			{"dcgm", strconv.FormatBool(gs.DCGMEnabled())}, {"gpu_status", status}, {"gpu_suggestions", suggestions}},
			wt.Created, e.g.Timeline.N, 1))
	}
}

func qosPath(w *WorkloadSpec) string {
	guaranteed := true
	for _, c := range w.Containers {
		if c.Requests.CPU == 0 || c.Requests.Memory == 0 || c.Requests.CPU != c.Limits.CPU || c.Requests.Memory != c.Limits.Memory {
			guaranteed = false
		}
	}
	if guaranteed {
		return "/kubepods"
	}
	return "/kubepods/burstable"
}

func (e *emitter) pod(p *PodTrace) {
	w, tl := e.wt.Spec, e.g.Timeline
	stepSec := tl.Step.Seconds()
	pl := e.ksm().with("pod", p.Name, "uid", p.UID)
	e.out(constSeries(pl.with("__name__", "kube_pod_owner", "owner_kind", p.OwnerKind, "owner_name", p.OwnerName,
		"owner_is_controller", "true"), p.Start, p.End, 1))
	e.out(constSeries(pl.with("__name__", "kube_pod_info", "node", p.Node.Name, "host_ip", p.Node.IP, "pod_ip", p.PodIP,
		"created_by_kind", p.OwnerKind, "created_by_name", p.OwnerName, "host_network", "false"), p.Start, p.End, 1))

	resources := func(prefix string, c *ContainerSpec) {
		cl := pl.with("container", c.Name, "node", p.Node.Name)
		for _, x := range []struct {
			metric string
			r      Resources
		}{{prefix + "_resource_requests", c.Requests}, {prefix + "_resource_limits", c.Limits}} {
			if x.r.CPU > 0 {
				e.out(constSeries(cl.with("__name__", x.metric, "resource", "cpu", "unit", "core"), p.Start, p.End, float64(x.r.CPU)))
			}
			if x.r.Memory > 0 {
				e.out(constSeries(cl.with("__name__", x.metric, "resource", "memory", "unit", "byte"), p.Start, p.End, float64(x.r.Memory)))
			}
			if c.GPU != nil { // extended resources: requests equal limits
				e.out(constSeries(cl.with("__name__", x.metric, "resource", gpuResource, "unit", "integer"), p.Start, p.End, float64(c.GPU.PerPod)))
			}
		}
	}
	for i := range w.InitContainers {
		c := &w.InitContainers[i]
		img := revImage(c.Image, p.Revision, c.Sidecar)
		e.out(constSeries(pl.with("__name__", "kube_pod_init_container_info", "container", c.Name, "image", img, "image_spec", img,
			"image_id", imageID(img), "container_id", "containerd://"+hash64(p.UID+c.Name), "restart_policy", ""), p.Start, p.End, 1))
		resources("kube_pod_init_container", c)
	}

	qos := qosPath(w)
	cad := labels{{"cluster", e.wt.Cluster.Spec.Name}, {"job", cadvisorJob}, {"instance", p.Node.Name}, {"node", p.Node.Name},
		{"namespace", w.Namespace}, {"pod", p.Name}}
	if e.g.Scenario.WorkloadLabel {
		cad = cad.with("workload", w.Name, "workload_kind", w.Kind)
	}
	podCPU := make([]float64, p.End-p.Start)
	podMem := make([]float64, p.End-p.Start)
	podCum := 0.0
	for _, ct := range p.Containers {
		c := ct.Spec
		resources("kube_pod_container", c)
		cl := pl.with("container", c.Name)
		// Restarts and last terminated reason.
		restarts := make([]float64, p.End-p.Start)
		var lastReason []struct {
			reason string
			start  int
		}
		for k, in := range ct.Instances {
			for i := in.Start - p.Start; i < len(restarts); i++ {
				restarts[i] = float64(k)
			}
			if k > 0 {
				lastReason = append(lastReason, struct {
					reason string
					start  int
				}{ct.Instances[k-1].EndReason, in.Start})
			}
		}
		e.out(valueSeries(cl.with("__name__", "kube_pod_container_status_restarts_total"), p.Start, restarts))
		// Merge consecutive equal reasons into one series span.
		for k := 0; k < len(lastReason); {
			j := k
			for j+1 < len(lastReason) && lastReason[j+1].reason == lastReason[k].reason {
				j++
			}
			end := p.End
			if j+1 < len(lastReason) {
				end = lastReason[j+1].start
			}
			e.out(constSeries(cl.with("__name__", "kube_pod_container_status_last_terminated_reason", "reason", lastReason[k].reason),
				lastReason[k].start, end, 1))
			k = j + 1
		}
		for k, in := range ct.Instances {
			end := p.End
			if k+1 < len(ct.Instances) {
				end = ct.Instances[k+1].Start
			}
			a, b := in.Start-p.Start, end-p.Start
			e.out(constSeries(cl.with("__name__", "kube_pod_container_info", "image", ct.Image, "image_spec", ct.Image,
				"image_id", imageID(ct.Image), "container_id", "containerd://"+in.ContainerID), in.Start, end, 1))
			il := cad.with("container", c.Name, "image", ct.Image, "name", in.ContainerID,
				"id", qos+"/pod"+p.UID+"/"+in.ContainerID)
			cpu := make([]float64, b-a)
			mem := make([]float64, b-a)
			cum := 0.0
			for i := a; i < b; i++ {
				cum += ct.CPU[i] * stepSec
				cpu[i-a] = round3(cum)
				mem[i-a] = ct.Mem[i]
				podCPU[i] += ct.CPU[i] * stepSec
				podMem[i] += ct.Mem[i]
			}
			e.out(valueSeries(il.with("__name__", "container_cpu_usage_seconds_total", "cpu", "total"), in.Start, cpu))
			e.out(valueSeries(il.with("__name__", "container_memory_working_set_bytes"), in.Start, mem))
			if c.Limits.CPU > 0 {
				per := make([]float64, b-a)
				thr := make([]float64, b-a)
				cp, ctr := 0.0, 0.0
				for i := a; i < b; i++ {
					np := ct.Periods[i] * stepSec
					cp += np
					ctr += np * ct.Throttled[i]
					per[i-a], thr[i-a] = math.Round(cp), math.Round(ctr)
				}
				e.out(valueSeries(il.with("__name__", "container_cpu_cfs_periods_total"), in.Start, per))
				e.out(valueSeries(il.with("__name__", "container_cpu_cfs_throttled_periods_total"), in.Start, thr))
			}
			oom := make([]float64, b-a)
			if in.EndReason == "OOMKilled" && len(oom) > 0 {
				oom[len(oom)-1] = 1
			}
			e.out(valueSeries(il.with("__name__", "container_oom_events_total"), in.Start, oom))
		}
	}
	for _, ct := range p.Containers {
		if gs := ct.Spec.GPU; gs != nil && gs.DCGMEnabled() {
			e.dcgm(p, ct)
		}
	}
	// Pod-level cgroup (no container label), as real cAdvisor reports it.
	podl := cad.with("id", qos+"/pod"+p.UID)
	for i := range podCPU {
		podCum += podCPU[i]
		podCPU[i] = round3(podCum)
	}
	e.out(valueSeries(podl.with("__name__", "container_cpu_usage_seconds_total", "cpu", "total"), p.Start, podCPU))
	e.out(valueSeries(podl.with("__name__", "container_memory_working_set_bytes"), p.Start, podMem))
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

func imageID(img string) string {
	repo, _, _ := strings.Cut(img, ":")
	if i := strings.LastIndexByte(img, ':'); i > strings.LastIndexByte(img, '/') {
		repo = img[:i]
	}
	return repo + "@sha256:" + hash64(img)
}

// hash64 returns 64 deterministic hex chars derived from s.
func hash64(s string) string {
	r := newRand(0, s)
	return r.hex(64)
}

// dcgmDriver and dcgmBusIDs are what the exporter reports on g5 nodes (the PCI bus of GPU i of n
// is the last n of dcgmBusIDs, offset by i).
const dcgmDriver = "550.127.05"

var dcgmBusIDs = []string{"00000000:00:1B.0", "00000000:00:1C.0", "00000000:00:1D.0", "00000000:00:1E.0"}

// GPUUUID is the DCGM `UUID` label of GPU idx of a node: GPU-<8-4-4-4-12 hex>, derived from the
// seed, the cluster and the node, so it is the same for every pod that runs there.
func (g *Generator) GPUUUID(cluster string, nd *Node, idx int) string {
	r := newRand(g.Scenario.Seed, cluster, "gpu-uuid", nd.Name, strconv.Itoa(idx))
	return "GPU-" + r.hex(8) + "-" + r.hex(4) + "-" + r.hex(4) + "-" + r.hex(4) + "-" + r.hex(12)
}

// dcgm emits DCGM_FI_DEV_GPU_UTIL, DCGM_FI_DEV_FB_USED and DCGM_FI_DEV_FB_FREE of every GPU of a
// container of a pod, the way the NVIDIA DCGM exporter reports them: one series per GPU, with the
// pod it is allocated to, plus the target labels the scrape adds.
func (e *emitter) dcgm(p *PodTrace, ct *ContainerTrace) {
	cluster := e.wt.Cluster.Spec.Name
	gs, w := ct.Spec.GPU, e.wt.Spec
	er := newRand(e.g.Scenario.Seed, cluster, "dcgm-exporter", p.Node.Name)
	exporterPod := "nvidia-dcgm-exporter-" + er.suffix(5)
	exporterIP := fmt.Sprintf("10.%d.%d.%d", 100+er.IntN(100), er.IntN(256), 1+er.IntN(254))
	base := labels{{"cluster", cluster}, {"job", dcgmJob}, {"instance", exporterIP + ":9400"},
		{"modelName", gs.Model}, {"Hostname", p.Node.Name}, {"DCGM_FI_DRIVER_VERSION", dcgmDriver}}
	if gs.LabelStyle == GPULabelExported {
		base = base.with("namespace", dcgmNamespace, "pod", exporterPod, "container", "nvidia-dcgm-exporter",
			"exported_namespace", w.Namespace, "exported_pod", p.Name, "exported_container", ct.Spec.Name)
	} else {
		base = base.with("namespace", w.Namespace, "pod", p.Name, "container", ct.Spec.Name)
	}
	nGPU := p.Node.Type.GPUs
	for d := 0; d < gs.PerPod; d++ {
		gl := base.with("gpu", strconv.Itoa(d), "UUID", e.g.GPUUUID(cluster, p.Node, d), "device", "nvidia"+strconv.Itoa(d),
			"pci_bus_id", dcgmBusIDs[len(dcgmBusIDs)-nGPU+d])
		free := make([]float64, len(ct.GPUUsedMiB[d]))
		for k, u := range ct.GPUUsedMiB[d] {
			free[k] = gs.MemoryMiB - u
		}
		e.out(valueSeries(gl.with("__name__", "DCGM_FI_DEV_GPU_UTIL"), p.Start, ct.GPUUtil[d]))
		e.out(valueSeries(gl.with("__name__", "DCGM_FI_DEV_FB_USED"), p.Start, ct.GPUUsedMiB[d]))
		e.out(valueSeries(gl.with("__name__", "DCGM_FI_DEV_FB_FREE"), p.Start, free))
	}
}
