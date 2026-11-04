package recommender

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// Request is the input for one workload recommendation.
type Request struct {
	Workload WorkloadData
	// Settings are the effective value settings (Config.EffectiveSettings).
	Settings hubapi.ValueSettings
	Profile  Profile
	Pricing  Pricing
	// End is the end of the window (the recommendation's generatedAt).
	End time.Time
}

// Result is a recommendation plus data the hub stores next to it.
type Result struct {
	Recommendation hubapi.WorkloadRecommendation
	Pricing        Pricing
}

// Recommend computes the recommendation for one workload. ok is false when no container has
// usage data (nothing to recommend). The returned recommendation has no ID.
func Recommend(req Request, strategy Strategy, cfg Config) (Result, bool) {
	w := req.Workload
	p := req.Profile
	rec := hubapi.WorkloadRecommendation{
		Workload:        w.Key.Ref(),
		GeneratedAt:     req.End.UTC(),
		StrategyName:    strategy.Name(),
		Settings:        req.Settings,
		AverageReplicas: round(w.AverageReplicas, 3),
		RiskFlags:       []string{},
	}
	workloadFlags := map[string]bool{}
	if w.HPA.Coupled() {
		workloadFlags[hubapi.FlagHPACoupled] = true
	}

	confidence := 1.0
	cov := coverage{window: p.Window, end: req.End, step: cfg.Step}
	for _, c := range w.Containers {
		if c.CPU.Empty() && c.Memory.Empty() {
			continue
		}
		cr, conf := recommendContainer(c, w, req, strategy, cfg)
		confidence = math.Min(confidence, conf)
		cov.add(c)
		for _, f := range cr.RiskFlags {
			workloadFlags[f] = true
		}
		rec.MonthlySavingsUSD = rec.MonthlySavingsUSD.Add(*cr.MonthlySavingsUSD)
		rec.Containers = append(rec.Containers, cr)
	}
	if len(rec.Containers) == 0 {
		return Result{}, false
	}
	sort.Slice(rec.Containers, func(i, j int) bool {
		a, b := rec.Containers[i], rec.Containers[j]
		if a.Init != b.Init {
			return !a.Init
		}
		return a.Name < b.Name
	})
	rec.Confidence = round(confidence, 2)
	if rec.Confidence < cfg.LowConfidence {
		workloadFlags[hubapi.FlagLowConfidence] = true
	}
	rec.RiskFlags = sortedFlags(workloadFlags)
	rec.MonthlySavingsUSD = roundSavings(rec.MonthlySavingsUSD)
	rec.DataCoverage = cov.result()
	rec.Pricing = &hubapi.RecommendationPricing{
		Source: req.Pricing.Source, CPUCoreHourUSD: req.Pricing.CPUCoreHour, MemoryGiBHourUSD: req.Pricing.MemoryGiBHour,
	}
	if rec.Pricing.Source == "" {
		rec.Pricing.Source = PricingSourceDefault
	}
	return Result{Recommendation: rec, Pricing: req.Pricing}, true
}

func recommendContainer(c ContainerData, w WorkloadData, req Request, strategy Strategy, cfg Config) (hubapi.ContainerRecommendation, float64) {
	p, s := req.Profile, req.Settings
	prop := strategy.Propose(c, p)
	cur := c.Current
	flags := map[string]bool{}

	throttled := c.ThrottleRatio > cfg.ThrottleThreshold
	if throttled {
		flags[hubapi.FlagThrottled] = true
	}
	if c.OOMKilled {
		flags[hubapi.FlagOOMKilled] = true
	}
	if c.Restarts >= cfg.RestartThreshold {
		flags[hubapi.FlagRestarting] = true
	}
	if c.Init || hubapi.Contains(cfg.SidecarContainers, c.Name) {
		flags[hubapi.FlagSidecar] = true
	}
	density := c.CPU.density(cfg.Step)
	if w.Key.Kind == hubapi.KindCronJob || (c.CPU.Samples > 1 && density < cfg.ShortLivedDensity) {
		flags[hubapi.FlagShortLived] = true
	}

	expl := &hubapi.ContainerExplanation{Profile: p.Name, StrategyName: strategy.Name(), Window: p.Window.String()}

	// CPU request.
	cpuReq, cpuLim := cur.CPURequest, cur.CPULimit
	var removeLimits []string
	if prop.CPUExplanation != nil {
		e := prop.CPUExplanation
		v := math.Max(prop.CPU, cfg.MinCPUCores)
		if prop.CPU < cfg.MinCPUCores {
			e.Notes = append(e.Notes, "raised to minimum "+FormatCPU(cfg.MinCPUCores))
		}
		if throttled {
			floor := cur.CPURequest * (1 + cfg.ThrottleIncrease)
			if cur.CPURequest > 0 && v < floor {
				v = floor
				e.Notes = append(e.Notes, "raised: throttled ("+pct(c.ThrottleRatio)+" of periods), at least +"+pct(cfg.ThrottleIncrease)+" over current")
			} else {
				e.Notes = append(e.Notes, "throttled ("+pct(c.ThrottleRatio)+" of periods); not decreased")
			}
		}
		cpuReq = roundUp(v, cfg.CPUStepCores)
		e.RoundedTo = FormatCPU(cfg.CPUStepCores)

		// CPU limit.
		switch s.CPULimits {
		case hubapi.CPULimitsRemove:
			cpuLim = 0
			e.LimitRule = "remove"
			if cur.CPULimit > 0 {
				removeLimits = append(removeLimits, "cpu")
			}
		case hubapi.CPULimitsRatio:
			r, _ := parseRatio(s.CPULimitRatio)
			cpuLim = roundUp(cpuReq*r, cfg.CPUStepCores)
			e.LimitRule = "ratio " + s.CPULimitRatio
		default:
			e.LimitRule = "keep"
			if cur.CPULimit > 0 {
				cpuLim = cur.CPULimit
				if throttled {
					cpuLim = roundUp(cur.CPULimit*(1+cfg.ThrottleIncrease), cfg.CPUStepCores)
					e.Notes = append(e.Notes, "limit raised: throttled")
				}
				if cpuLim < cpuReq {
					cpuLim = cpuReq
					e.Notes = append(e.Notes, "limit raised to the request")
				}
			}
		}
		expl.CPU = e
	}

	// Memory request.
	memReq, memLim := cur.MemoryRequest, cur.MemoryLimit
	if prop.MemoryExplanation != nil {
		e := prop.MemoryExplanation
		v := math.Max(prop.Memory, cfg.MinMemoryBytes)
		if prop.Memory < cfg.MinMemoryBytes {
			e.Notes = append(e.Notes, "raised to minimum "+FormatMemory(cfg.MinMemoryBytes))
		}
		if c.OOMKilled {
			ref := math.Max(cur.MemoryRequest, cur.MemoryLimit)
			if floor := ref * (1 + cfg.OOMIncrease); ref > 0 && v < floor {
				v = floor
				e.Notes = append(e.Notes, "raised: oom-killed, at least +"+pct(cfg.OOMIncrease)+" over current")
			} else {
				e.Notes = append(e.Notes, "oom-killed; not decreased")
			}
		}
		limitFollows := s.MemoryLimitEqualsRequest == nil || *s.MemoryLimitEqualsRequest
		jvm := planJVM(c, cfg.JVM)
		if jvm != nil {
			v = jvm.raiseRequest(v, limitFollows, e)
		}
		memReq = roundUp(v, cfg.MemoryStepBytes)
		e.RoundedTo = FormatMemory(cfg.MemoryStepBytes)
		if limitFollows {
			memLim = memReq
			e.LimitRule = "equals request"
		} else {
			e.LimitRule = "keep"
			if cur.MemoryLimit > 0 {
				memLim = cur.MemoryLimit
				if c.OOMKilled {
					memLim = roundUp(cur.MemoryLimit*(1+cfg.OOMIncrease), cfg.MemoryStepBytes)
					e.Notes = append(e.Notes, "limit raised: oom-killed")
				}
				if memLim < memReq {
					memLim = memReq
					e.Notes = append(e.Notes, "limit raised to the request")
				}
			}
		}
		if jvm != nil {
			for _, f := range jvm.explain(memLim, e) {
				flags[f] = true
			}
		}
		expl.Memory = e
	}

	// Savings: (current - recommended request) x rate x replicas x 730 h.
	reps := w.AverageReplicas
	sv := hubapi.Savings{}
	if cur.CPURequest > 0 || expl.CPU != nil {
		sv.CPU = (cur.CPURequest - cpuReq) * req.Pricing.CPUCoreHour * reps * HoursPerMonth
	}
	if cur.MemoryRequest > 0 || expl.Memory != nil {
		sv.Memory = (cur.MemoryRequest - memReq) / GiB * req.Pricing.MemoryGiBHour * reps * HoursPerMonth
	}
	sv.Total = sv.CPU + sv.Memory
	sv = roundSavings(sv)
	if req.Pricing.Fallback() {
		note := "savings use the default rate: no pricing series for this cluster"
		if expl.CPU != nil {
			expl.CPU.Notes = append(expl.CPU.Notes, note)
		} else if expl.Memory != nil {
			expl.Memory.Notes = append(expl.Memory.Notes, note)
		}
	}

	cr := hubapi.ContainerRecommendation{
		Name:              c.Name,
		Init:              c.Init,
		Current:           resources(cur.CPURequest, cur.MemoryRequest, cur.CPULimit, cur.MemoryLimit),
		Recommended:       resources(cpuReq, memReq, cpuLim, memLim),
		RemoveLimits:      removeLimits,
		RiskFlags:         sortedFlags(flags),
		MonthlySavingsUSD: &sv,
		Explanation:       expl,
	}
	f := confidenceFactors(c, p, cfg)
	expl.ConfidenceFactors = &f
	return cr, f.Confidence
}

// Confidence weights: coverage, samples, variance (sum to 1).
const (
	weightCoverage = 0.6
	weightSamples  = 0.2
	weightVariance = 0.2
)

// confidenceFactors: confidence = 0.6 x coverage + 0.2 x samples + 0.2 x variance, where coverage
// is the fraction of the window's steps with data, samples saturates at FullConfidenceSamples, and
// variance is the stability of CPU usage 1/(1+cv) (cv = stddev/mean).
func confidenceFactors(c ContainerData, p Profile, cfg Config) hubapi.ConfidenceFactors {
	st := c.CPU
	if st.Empty() {
		st = c.Memory
	}
	steps := float64(p.Window / cfg.Step)
	cov := math.Min(1, float64(st.Samples)/steps)
	samples := math.Min(1, float64(st.Samples)/float64(max(1, cfg.FullConfidenceSamples)))
	stability := 1.0
	if st.Mean > 0 {
		stability = 1 / (1 + st.Stddev/st.Mean)
	}
	return hubapi.ConfidenceFactors{
		Coverage:   hubapi.ConfidenceFactor{Score: round(cov, 4), Weight: weightCoverage},
		Samples:    hubapi.ConfidenceFactor{Score: round(samples, 4), Weight: weightSamples},
		Variance:   hubapi.ConfidenceFactor{Score: round(stability, 4), Weight: weightVariance},
		Confidence: round(clamp01(weightCoverage*cov+weightSamples*samples+weightVariance*stability), 4),
	}
}

// density is the fraction of steps with data between the first and the last sample.
func (s Stats) density(step time.Duration) float64 {
	if s.Samples < 2 {
		return 1
	}
	span := float64(s.Last.Sub(s.First)/step) + 1
	return math.Min(1, float64(s.Samples)/span)
}

type coverage struct {
	window      time.Duration
	end         time.Time
	step        time.Duration
	samples     int
	first, last time.Time
}

func (c *coverage) add(cd ContainerData) {
	for _, s := range []Stats{cd.CPU, cd.Memory} {
		if s.Empty() {
			continue
		}
		c.samples = max(c.samples, s.Samples)
		if c.first.IsZero() || s.First.Before(c.first) {
			c.first = s.First
		}
		if s.Last.After(c.last) {
			c.last = s.Last
		}
	}
}

func (c *coverage) result() hubapi.DataCoverage {
	steps := float64(c.window / c.step)
	d := hubapi.DataCoverage{
		Ratio:   round(clamp01(float64(c.samples)/steps), 4),
		Window:  c.window.String(),
		Samples: c.samples,
	}
	if !c.first.IsZero() {
		f, l := c.first.UTC(), c.last.UTC()
		d.FirstSample, d.LastSample = &f, &l
		d.Covered = c.last.Sub(c.first).Truncate(time.Second).String()
	}
	return d
}

func resources(cpuReq, memReq, cpuLim, memLim float64) hubapi.Resources {
	var r hubapi.Resources
	req := hubapi.ResourceValues{}
	if cpuReq > 0 {
		req.CPU = FormatCPU(cpuReq)
	}
	if memReq > 0 {
		req.Memory = FormatMemory(memReq)
	}
	if !req.IsZero() {
		r.Requests = &req
	}
	lim := hubapi.ResourceValues{}
	if cpuLim > 0 {
		lim.CPU = FormatCPU(cpuLim)
	}
	if memLim > 0 {
		lim.Memory = FormatMemory(memLim)
	}
	if !lim.IsZero() {
		r.Limits = &lim
	}
	return r
}

func sortedFlags(m map[string]bool) []string {
	out := []string{}
	for _, f := range hubapi.AllRiskFlags {
		if m[f] {
			out = append(out, f)
		}
	}
	return out
}

func roundSavings(s hubapi.Savings) hubapi.Savings {
	s.CPU, s.Memory = round(s.CPU, 2), round(s.Memory, 2)
	s.Total = round(s.CPU+s.Memory, 2)
	return s
}

func round(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	r := math.Round(v*p) / p
	if r == 0 {
		return 0 // no negative zero
	}
	return r
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func pct(f float64) string { return trimFloat(round(f*100, 1)) + "%" }

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(f, 'f', 3, 64), "0"), ".")
}

// shortDuration renders 168h0m0s as 7d and 36h as 36h.
func shortDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return strings.TrimSuffix(strings.TrimSuffix(d.String(), "0s"), "0m")
}
