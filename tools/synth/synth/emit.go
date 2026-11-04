package synth

import (
	"math"
	"sort"
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
