package recommender

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// GPUConfig holds the thresholds of the GPU view (docs/gpu.md). The zero value means the defaults.
type GPUConfig struct {
	// IdleUtilization: the 95th percentile of the GPU utilisation (percent) below this is idle.
	IdleUtilization float64
	// UnderUsedUtilization: below this the GPUs are under-used.
	UnderUsedUtilization float64
	// UnderUsedMemory: a 95th percentile of the framebuffer used (percent of the GPU's memory) below
	// this also makes the GPUs under-used.
	UnderUsedMemory float64
	// MemoryHeadroom is the factor applied to the memory used before it is compared with a smaller
	// GPU's memory (1.25 = 25 % headroom).
	MemoryHeadroom float64
}

// DefaultGPUConfig returns the default GPU thresholds.
func DefaultGPUConfig() GPUConfig {
	return GPUConfig{IdleUtilization: 5, UnderUsedUtilization: 40, UnderUsedMemory: 30, MemoryHeadroom: 1.25}
}

func (c GPUConfig) withDefaults() GPUConfig {
	d := DefaultGPUConfig()
	if c.IdleUtilization <= 0 {
		c.IdleUtilization = d.IdleUtilization
	}
	if c.UnderUsedUtilization <= 0 {
		c.UnderUsedUtilization = d.UnderUsedUtilization
	}
	if c.UnderUsedMemory <= 0 {
		c.UnderUsedMemory = d.UnderUsedMemory
	}
	if c.MemoryHeadroom <= 0 {
		c.MemoryHeadroom = d.MemoryHeadroom
	}
	return c
}

// GPUPod is a pod that requests GPUs now.
type GPUPod struct {
	Name string
	Node string
	// GPUs is the pod's `nvidia.com/gpu` request, summed over its containers.
	GPUs float64
}

// RawGPU holds the raw GPU data of one workload: the pods that request GPUs now and the DCGM series.
type RawGPU struct {
	Pods []GPUPod
	// Util is the GPU utilisation in percent per step, MemUsedMiB the framebuffer used in MiB per step
	// (the busiest GPU of the busiest pod); both over the longest window.
	Util, MemUsedMiB []Sample
	// MemTotalMiB is the framebuffer size of one GPU; 0 when unknown.
	MemTotalMiB float64
	// Model is the DCGM modelName label of the GPUs.
	Model string
}

const mibBytes = 1 << 20

// loadGPU adds the GPU data to d. GPU data is optional: a query that fails is logged and the
// workloads keep no (or partial) GPU data, it never fails the load.
func (s *Source) loadGPU(ctx context.Context, d *ClusterData, data Querier, q Queries, start, end time.Time) {
	log := slog.With("cluster", d.Cluster)
	var (
		mu                     sync.Mutex
		pods, memTotal, prices []VectorSample
		util, memUsed          []Series
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(2)
	inst := func(name string, src Querier, query string, dst *[]VectorSample) {
		g.Go(func() error {
			res, err := src.Instant(gctx, query, end)
			if err != nil {
				log.WarnContext(ctx, "GPU query failed; GPU data of the cluster is incomplete", "query", name, "err", err)
				return nil
			}
			mu.Lock()
			*dst = res
			mu.Unlock()
			return nil
		})
	}
	rng := func(name, query string, dst *[]Series) {
		g.Go(func() error {
			res, err := data.Range(gctx, query, start, end, s.Cfg.Step)
			if err != nil {
				log.WarnContext(ctx, "GPU query failed; GPU data of the cluster is incomplete", "query", name, "err", err)
				return nil
			}
			mu.Lock()
			*dst = res
			mu.Unlock()
			return nil
		})
	}
	inst("gpuPods", data, q.GPUPods(), &pods)
	inst("gpuPrice", s.Q, q.GPUPrice(time.Hour), &prices) // pricing series are in the hub's own store
	rng("gpuUtilization", q.GPUUtilization(), &util)
	rng("gpuMemoryUsed", q.GPUMemoryUsed(), &memUsed)
	inst("gpuMemoryTotal", data, q.GPUMemoryTotal(s.Cfg.InventoryLookback), &memTotal)
	_ = g.Wait()

	key := func(l Labels) hubapi.WorkloadKey {
		return hubapi.WorkloadKey{Cluster: d.Cluster, Namespace: l["namespace"], Kind: l["workload_kind"], Name: l["workload"]}
	}
	for _, r := range pods {
		if r.Sample.V <= 0 {
			continue
		}
		if w := d.Workloads[key(r.Labels)]; w != nil {
			if w.GPU == nil {
				w.GPU = &RawGPU{}
			}
			w.GPU.Pods = append(w.GPU.Pods, GPUPod{Name: r.Labels["pod"], Node: r.Labels["node"], GPUs: r.Sample.V})
		}
	}
	// DCGM series only matter for workloads that request GPUs now.
	for _, r := range util {
		if w := d.Workloads[key(r.Labels)]; w != nil && w.GPU != nil {
			w.GPU.Util = r.Samples
		}
	}
	for _, r := range memUsed {
		if w := d.Workloads[key(r.Labels)]; w != nil && w.GPU != nil {
			w.GPU.MemUsedMiB = r.Samples
		}
	}
	for _, r := range memTotal {
		if w := d.Workloads[key(r.Labels)]; w != nil && w.GPU != nil && r.Sample.V > w.GPU.MemTotalMiB {
			w.GPU.MemTotalMiB, w.GPU.Model = r.Sample.V, r.Labels["modelName"]
		}
	}
	d.GPUPrices = map[string]float64{}
	for _, r := range prices {
		if r.Sample.V > 0 {
			d.GPUPrices[r.Labels["node"]] = r.Sample.V
		}
	}
	for _, w := range d.Workloads {
		if w.GPU != nil {
			sort.Slice(w.GPU.Pods, func(i, j int) bool { return w.GPU.Pods[i].Name < w.GPU.Pods[j].Name })
		}
	}
}

// AnalyzeGPU turns a workload's raw GPU data into the API shape: utilisation and memory over the
// profile window, the cost of the GPUs held now, the status and suggestions. nil when the workload
// holds no GPUs. prices are USD per GPU-hour by node; discountPercent is the hub's pricing discount.
func (w *RawWorkload) AnalyzeGPU(p Profile, end time.Time, prices map[string]float64, discountPercent float64, cfg GPUConfig) *hubapi.GPUUsage {
	if w.GPU == nil || len(w.GPU.Pods) == 0 {
		return nil
	}
	cfg = cfg.withDefaults()
	raw := w.GPU
	from := end.Add(-p.Window)
	u := &hubapi.GPUUsage{Pods: len(raw.Pods), Model: raw.Model, Window: p.Window.String(), Suggestions: []hubapi.GPUSuggestion{}}
	var perPod float64
	var hourly float64
	for _, pod := range raw.Pods {
		perPod = math.Max(perPod, pod.GPUs)
		u.GPUs += int(math.Round(pod.GPUs))
		if price, ok := prices[pod.Node]; ok {
			hourly += pod.GPUs * price
			u.Priced = true
		}
	}
	u.GPUsPerPod = int(math.Round(perPod))
	if u.GPUsPerPod < 1 {
		return nil
	}
	hourly *= 1 - clampPercent(discountPercent)/100
	u.MonthlyCostUSD = round(hourly*HoursPerMonth, 2)
	u.PerPodMonthlyCostUSD = round(hourly*HoursPerMonth/float64(u.Pods), 2)

	util := StatsFromSamples(after(raw.Util, from), []float64{0.95})
	u.Samples = util.Samples
	if util.Empty() {
		u.Status = hubapi.GPUNoData
		return u
	}
	p95, _ := util.Q(0.95)
	u.UtilizationP95, u.UtilizationAvg = ptr(round(clampPercent(p95), 1)), ptr(round(clampPercent(util.Mean), 1))
	u.UnusedMonthlyCostUSD = round(u.MonthlyCostUSD*(1-clampPercent(util.Mean)/100), 2)

	var memUsedBytes, memTotalBytes float64
	if mem := StatsFromSamples(after(raw.MemUsedMiB, from), []float64{0.95}); !mem.Empty() && raw.MemTotalMiB > 0 {
		v, _ := mem.Q(0.95)
		memUsedBytes, memTotalBytes = v*mibBytes, raw.MemTotalMiB*mibBytes
		u.MemoryUsedP95Bytes, u.MemoryTotalBytes = ptr(round(memUsedBytes, 0)), ptr(memTotalBytes)
		u.MemoryUtilizationP95 = ptr(round(clampPercent(memUsedBytes/memTotalBytes*100), 1))
	}

	underByUtil := p95 < cfg.UnderUsedUtilization
	underByMem := u.MemoryUtilizationP95 != nil && *u.MemoryUtilizationP95 < cfg.UnderUsedMemory
	switch {
	case p95 < cfg.IdleUtilization:
		u.Status = hubapi.GPUIdle
	case underByUtil || underByMem:
		u.Status = hubapi.GPUUnderUsed
	default:
		u.Status = hubapi.GPUOK
	}
	u.Suggestions = gpuSuggestions(u, underByUtil, memUsedBytes, memTotalBytes, cfg)
	return u
}

func gpuSuggestions(u *hubapi.GPUUsage, underByUtil bool, memUsed, memTotal float64, cfg GPUConfig) []hubapi.GPUSuggestion {
	out := []hubapi.GPUSuggestion{}
	switch u.Status {
	case hubapi.GPUIdle:
		s := hubapi.GPUSuggestion{
			Kind: hubapi.GPUSuggestScaleToZero,
			Message: fmt.Sprintf("The GPUs were idle: 95th percentile utilisation %.1f %% over %s. Scale the workload to zero while it has nothing to do (a schedule or event-driven scaling) so the GPU nodes can go away.",
				*u.UtilizationP95, u.Window),
		}
		if u.Priced {
			s.MonthlySavingsUSD = ptr(u.MonthlyCostUSD)
			s.Message += fmt.Sprintf(" The GPUs cost $%.2f a month.", u.MonthlyCostUSD)
		}
		out = append(out, s)
	case hubapi.GPUUnderUsed:
		if underByUtil {
			out = append(out, hubapi.GPUSuggestion{
				Kind: hubapi.GPUSuggestTimeSlicing,
				Message: fmt.Sprintf("95th percentile utilisation is %.1f %%: share each GPU between several pods with NVIDIA time-slicing so one GPU does the work of %d.",
					*u.UtilizationP95, max(u.GPUs, 2)),
			})
			if supportsMIG(u.Model) {
				out = append(out, hubapi.GPUSuggestion{
					Kind:    hubapi.GPUSuggestMIG,
					Message: "The GPU supports Multi-Instance GPU (MIG): partition it into isolated slices and give this workload one slice instead of a whole GPU.",
				})
			}
		}
		if t, ok := smallerGPU(u.Model, memUsed, memTotal, cfg.MemoryHeadroom); ok {
			out = append(out, hubapi.GPUSuggestion{
				Kind:        hubapi.GPUSuggestSmallerGPU,
				TargetModel: t.Name,
				Message: fmt.Sprintf("95th percentile framebuffer use is %.1f GiB of %.1f GiB: the memory fits a %s (%d GiB). Check that its compute is enough before moving.",
					memUsed/GiB, memTotal/GiB, t.Name, t.MemoryGiB),
			})
		}
	}
	return out
}

// gpuModel is an NVIDIA GPU a workload could move to.
type gpuModel struct {
	Name      string
	MemoryGiB int
	MIG       bool
}

// gpuCatalog lists the GPUs offered by the major clouds, smallest memory first; of two with the same
// memory the cheaper one comes first.
var gpuCatalog = []gpuModel{
	{"NVIDIA T4", 16, false},
	{"NVIDIA L4", 24, false},
	{"NVIDIA A10G", 24, false},
	{"NVIDIA A100 40GB", 40, true},
	{"NVIDIA L40S", 48, false},
	{"NVIDIA A100 80GB", 80, true},
	{"NVIDIA H100", 80, true},
}

// usableMemory is the share of a GPU's nominal memory the framebuffer reports as total.
const usableMemory = 0.93

// supportsMIG reports whether the GPU model (a DCGM modelName) supports Multi-Instance GPU.
func supportsMIG(model string) bool {
	for _, m := range []string{"A100", "A30", "H100", "H200", "H800", "B200", "GB200"} {
		if strings.Contains(strings.ToUpper(model), m) {
			return true
		}
	}
	return false
}

// smallerGPU returns the smallest catalog GPU whose memory fits memUsed (with headroom) and is
// clearly smaller than the current GPU's memory.
func smallerGPU(current string, memUsed, memTotal, headroom float64) (gpuModel, bool) {
	if memUsed <= 0 || memTotal <= 0 {
		return gpuModel{}, false
	}
	for _, m := range gpuCatalog {
		usable := float64(m.MemoryGiB) * GiB * usableMemory
		if usable < memUsed*headroom || usable > memTotal*0.9 {
			continue
		}
		if cur := strings.ToUpper(current); cur != "" && strings.Contains(cur, strings.ToUpper(strings.TrimPrefix(m.Name, "NVIDIA "))) {
			continue
		}
		return m, true
	}
	return gpuModel{}, false
}

func ptr[T any](v T) *T { return &v }

func clampPercent(v float64) float64 { return math.Min(math.Max(v, 0), 100) }
