package synth

import "math"

// gpuUtilSignal is the workload-level GPU utilisation (percent) of a pattern: idle sits at ~0-2 %
// with rare small blips, light is a gentle daily cycle, busy a high one. The shape is scaled so its
// 95th percentile is about Utilization.P95 (with the per-pod and per-GPU noise of simGPU).
func gpuUtilSignal(g *GPUSpec) Signal {
	p95 := g.Utilization.P95
	// 0.9877 is cos at the 95th percentile of a uniform phase; 1.03 allows for the noise (measured).
	const cosP95, noise = 0.9877, 1.03
	switch g.Utilization.Pattern {
	case GPUIdle:
		return Signal{Pattern: "spiky", Base: Quantity(p95 / 2), SpikesPerDay: 0.4, SpikeMult: 8, SpikeMinutes: 5}
	case GPUBusy:
		return Signal{Pattern: "diurnal", Base: Quantity(p95 / (1 + 0.15*cosP95) / noise), Diurnal: 0.15, PeakHour: 14}
	default:
		return Signal{Pattern: "diurnal", Base: Quantity(p95 / (1 + 0.4*cosP95) / noise), Diurnal: 0.4, PeakHour: 14}
	}
}

// simGPU simulates the GPUs of one container of one pod over n samples: per GPU the utilisation
// in percent and the framebuffer used in MiB (both integers, as DCGM reports them). shape is the
// workload-level utilisation (shared by all pods, like traffic). Every GPU draws from its own
// stream (rnd of the pod's GPUs, keyed by index), so a longer timeline only appends samples.
func (g *Generator) simGPU(pod *rnd, gpu func(d int) *rnd, spec *GPUSpec, n int, shape []float64) (util, used [][]float64) {
	stepMin := g.Timeline.Step.Minutes()
	noise := 0.08
	if spec.Utilization.Pattern == GPUIdle {
		noise = 0.35
	}
	thetaU, thetaM := math.Min(1, stepMin/3), math.Min(1, stepMin/90)
	podU, podM := pod.uniform(0.92, 1.08), pod.uniform(0.97, 1.03)
	total := spec.MemoryMiB
	mean := spec.memoryUsedMiB()
	util, used = make([][]float64, spec.PerPod), make([][]float64, spec.PerPod)
	for d := range util {
		r := gpu(d)
		u, m := make([]float64, n), make([]float64, n)
		devU, devM := r.uniform(0.97, 1.03), r.uniform(0.98, 1.02)
		ouU, ouM := 1.0, 1.0
		for k := 0; k < n; k++ {
			ouU += thetaU*(1-ouU) + noise*math.Sqrt(2*thetaU)*r.NormFloat64()
			ouU = clamp(ouU, 0.2, 3)
			ouM += thetaM*(1-ouM) + 0.01*math.Sqrt(2*thetaM)*r.NormFloat64()
			ouM = clamp(ouM, 0.9, 1.1)
			u[k] = math.Round(clamp(shape[k]*podU*devU*ouU, 0, 100))
			m[k] = math.Round(clamp(mean*podM*devM*ouM, 0, total-1))
		}
		util[d], used[d] = u, m
	}
	return util, used
}
