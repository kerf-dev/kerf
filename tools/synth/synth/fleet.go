package synth

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Workload profiles of a generated fleet.
const (
	ProfileOverProvisioned = "over-provisioned"
	ProfileRightSized      = "right-sized"
	ProfileThrottled       = "throttled"
	ProfileOOMProne        = "oom-prone"
	ProfileHPACoupled      = "hpa-coupled"
	ProfileJVM             = "jvm"
	ProfileBatch           = "batch"
	ProfileDaemon          = "daemon" // DaemonSets added by FleetSpec.DaemonSets
)

func isProfile(p string) bool {
	switch p {
	case ProfileOverProvisioned, ProfileRightSized, ProfileThrottled, ProfileOOMProne,
		ProfileHPACoupled, ProfileJVM, ProfileBatch:
		return true
	}
	return false
}

// Expectation values.
const (
	ExpectDecrease = "decrease"
	ExpectKeep     = "keep"
	ExpectIncrease = "increase"
	ExpectAny      = "any"
)

var serviceBases = map[string][]string{
	"payments":      {"payment-gateway", "payment-coordinator", "sepa-transfer", "swift-adapter", "instant-payments", "fx-quote", "fee-engine", "settlement", "reconciliation", "direct-debit", "standing-orders", "payment-limits", "payment-status", "beneficiary", "mandate"},
	"cards":         {"card-authorization", "card-issuing", "card-lifecycle", "tokenization", "threeds-challenge", "card-controls", "pin-management", "chargeback", "card-statements", "clearing", "virtual-cards", "card-limits"},
	"ledger":        {"ledger-core", "posting-engine", "balance", "account-history", "interest-accrual", "journal", "gl-export", "end-of-day", "ledger-events", "statement-generator"},
	"onboarding":    {"kyc-coordinator", "identity-verification", "document-scan", "address-lookup", "customer-profile", "account-opening", "consent", "signup", "eligibility", "aml-screening-adapter"},
	"notifications": {"push-sender", "email-sender", "sms-gateway", "notification-router", "template-renderer", "in-app-inbox", "notification-preferences", "webhook-dispatcher"},
	"risk":          {"fraud-scoring", "transaction-monitoring", "aml-screening", "risk-rules", "device-fingerprint", "velocity-checks", "sanctions-screening", "credit-decisioning", "risk-features"},
	"platform":      {"api-gateway", "auth-service", "token-exchange", "feature-flags", "config-service", "audit-log", "rate-limiter", "secrets-sync", "event-bridge", "outbox-relay", "schema-registry", "service-catalog"},
}

var genericBases = []string{"core", "gateway", "events", "query", "command", "sync", "reporting", "search", "cache", "profile"}

var serviceSuffixes = []string{"", "", "", "-api", "-worker", "-consumer", "-processor", "-sync", "-adapter", "-reader", "-writer", "-grpc"}
var statefulSuffixes = []string{"-store", "-cache", "-index", "-state"}
var batchSuffixes = []string{"-batch", "-nightly", "-report", "-cleanup", "-export", "-recon", "-archiver"}
var daemonNames = []string{"log-shipper", "node-metrics-agent", "secrets-store-csi", "network-policy-agent", "ebs-csi-node", "dns-cache"}

type nameAllocator struct{ used map[string]bool }

func (a *nameAllocator) take(r *rnd, ns string, suffixes []string) string {
	bases := serviceBases[ns]
	if len(bases) == 0 {
		bases = make([]string, len(genericBases))
		for i, b := range genericBases {
			bases[i] = ns + "-" + b
		}
	}
	for try := 0; try < 200; try++ {
		n := r.choice(bases) + r.choice(suffixes)
		if !a.used[ns+"/"+n] {
			a.used[ns+"/"+n] = true
			return n
		}
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s%s-%d", r.choice(bases), r.choice(suffixes), i)
		if !a.used[ns+"/"+n] {
			a.used[ns+"/"+n] = true
			return n
		}
	}
}

// expandFleet turns a FleetSpec into explicit workloads.
func expandFleet(sc *Scenario, c *ClusterSpec, window time.Duration) []WorkloadSpec {
	f := c.Fleet
	if f == nil {
		return nil
	}
	alloc := &nameAllocator{used: map[string]bool{}}
	for _, w := range c.Workloads {
		alloc.used[w.Namespace+"/"+w.Name] = true
	}
	var out []WorkloadSpec
	days := window.Hours() / 24
	for i := 0; i < f.Workloads; i++ {
		r := newRand(sc.Seed, c.Name, "fleet", fmt.Sprint(i))
		profile := r.pick(f.Profiles)
		ns := f.Namespaces[r.IntN(len(f.Namespaces))]
		w := genWorkload(sc, r, profile, ns, alloc, f)
		applyModifiers(r, &w, f, days)
		out = append(out, w)
	}
	dsNS := f.Namespaces[len(f.Namespaces)-1]
	for _, ns := range f.Namespaces {
		if ns == "platform" {
			dsNS = ns
		}
	}
	for i := 0; i < f.DaemonSets && i < len(daemonNames); i++ {
		r := newRand(sc.Seed, c.Name, "daemonset", fmt.Sprint(i))
		w := genDaemonSet(sc, r, daemonNames[i], dsNS)
		w.Rollouts = randomRollouts(r, days, f.RolloutEveryDays*2)
		out = append(out, w)
	}
	return out
}

func genWorkload(sc *Scenario, r *rnd, profile, ns string, alloc *nameAllocator, f *FleetSpec) WorkloadSpec {
	w := WorkloadSpec{Namespace: ns, Kind: KindDeployment, Profile: profile}
	switch profile {
	case ProfileBatch:
		w.Kind = KindCronJob
		w.Name = alloc.take(r, ns, batchSuffixes)
	case ProfileHPACoupled:
		w.Name = alloc.take(r, ns, serviceSuffixes)
	default:
		if r.chance(f.StatefulSets) && profile != ProfileThrottled {
			w.Kind = KindStatefulSet
			w.Name = alloc.take(r, ns, statefulSuffixes)
		} else {
			w.Name = alloc.take(r, ns, serviceSuffixes)
		}
	}
	main := ContainerSpec{Name: w.Name, Image: appImage(sc, r, ns, w.Name, profile == ProfileJVM)}
	if r.chance(0.3) {
		main.Name = "app"
	}
	w.Replicas = int(r.pickF(1, 2, 2, 5, 3, 3, 4, 1, 6, 0.5))
	if w.Kind == KindStatefulSet {
		w.Replicas = int(r.pickF(1, 1, 3, 3))
	}
	exp := &Expectation{CPU: ExpectDecrease, Memory: ExpectDecrease}
	switch profile {
	case ProfileOverProvisioned:
		cpuReq := r.pickF(0.25, 2, 0.5, 4, 1, 3, 2, 1)
		main.Requests.CPU = Quantity(cpuReq)
		main.CPU = serviceCPU(r, cpuReq*r.uniform(0.05, 0.35))
		memReq := r.pickF(512*mib, 3, 1*gib, 4, 2*gib, 2, 4*gib, 1)
		main.Requests.Memory = Quantity(memReq)
		main.Memory = serviceMemory(r, memReq*r.uniform(0.15, 0.5))
		setLimits(r, &main, true)
	case ProfileRightSized:
		cpuReq := r.pickF(0.1, 2, 0.25, 3, 0.5, 3, 1, 2)
		main.Requests.CPU = Quantity(cpuReq)
		main.CPU = serviceCPU(r, cpuReq*r.uniform(0.6, 0.85))
		memReq := r.pickF(256*mib, 2, 512*mib, 3, 1*gib, 3, 2*gib, 1)
		main.Requests.Memory = Quantity(memReq)
		main.Memory = serviceMemory(r, memReq*r.uniform(0.6, 0.8))
		setLimits(r, &main, false)
		exp = &Expectation{CPU: ExpectKeep, Memory: ExpectKeep}
	case ProfileThrottled:
		lim := r.pickF(0.25, 2, 0.5, 3, 1, 2)
		main.Limits.CPU = Quantity(lim)
		main.Requests.CPU = Quantity(lim)
		if r.chance(0.4) {
			main.Requests.CPU = Quantity(lim / 2)
		}
		main.CPU = Signal{Pattern: "throttled", Base: Quantity(lim * r.uniform(0.8, 1.1)), Diurnal: r.uniform(0.5, 0.8), PeakHour: r.uniform(10, 16), Weekend: 0.7, Noise: 0.1}
		memReq := r.pickF(256*mib, 2, 512*mib, 3, 1*gib, 2)
		main.Requests.Memory = Quantity(memReq)
		main.Limits.Memory = Quantity(memReq)
		main.Memory = serviceMemory(r, memReq*r.uniform(0.4, 0.7))
		exp = &Expectation{CPU: ExpectIncrease, Memory: ExpectAny, RiskFlags: []string{"throttled"}}
	case ProfileOOMProne:
		cpuReq := r.pickF(0.25, 2, 0.5, 2, 1, 1)
		main.Requests.CPU = Quantity(cpuReq)
		main.CPU = serviceCPU(r, cpuReq*r.uniform(0.15, 0.4))
		lim := r.pickF(256*mib, 2, 512*mib, 3, 1*gib, 2)
		main.Requests.Memory = Quantity(lim)
		main.Limits.Memory = Quantity(lim)
		if r.chance(0.6) {
			frac := r.uniform(0.4, 0.6)
			hours := r.uniform(6, 48)
			main.Memory = Signal{Pattern: "leak", Base: Quantity(lim * frac), LeakPerHour: (0.99 - frac) / frac / hours, Noise: 0.015}
		} else {
			main.Memory = Signal{Pattern: "spiky", Base: Quantity(lim * r.uniform(0.7, 0.8)), SpikesPerDay: r.uniform(0.5, 3), SpikeMult: r.uniform(1.35, 1.6), SpikeMinutes: r.uniform(3, 10), Noise: 0.015}
		}
		exp = &Expectation{CPU: ExpectAny, Memory: ExpectIncrease, RiskFlags: []string{"oom-killed"}}
	case ProfileHPACoupled:
		cpuReq := r.pickF(0.25, 2, 0.5, 3, 1, 2)
		main.Requests.CPU = Quantity(cpuReq)
		target := int(r.pickF(60, 2, 70, 3, 75, 1, 80, 1))
		min := int(r.pickF(2, 3, 3, 2, 4, 1))
		w.HPA = &HPASpec{Min: min, Max: int(float64(min) * r.uniform(3, 5)), Metric: "cpu", Target: target}
		if r.chance(0.15) {
			w.HPA.Metric = "memory"
		}
		w.Replicas = min
		// Base is the demand per pod at min replicas at average load.
		main.CPU = Signal{Pattern: "diurnal", Base: Quantity(cpuReq * float64(target) / 100 * r.uniform(0.55, 0.85)), Diurnal: r.uniform(0.5, 0.9), PeakHour: r.uniform(10, 16), Weekend: r.uniform(0.5, 0.8), Noise: 0.08}
		memReq := r.pickF(512*mib, 3, 1*gib, 3, 2*gib, 1)
		main.Requests.Memory = Quantity(memReq)
		main.Limits.Memory = Quantity(memReq)
		main.Memory = serviceMemory(r, memReq*r.uniform(0.3, 0.6))
		if r.chance(0.5) {
			main.Limits.CPU = Quantity(cpuReq * 2)
		}
		exp = &Expectation{CPU: ExpectAny, Memory: ExpectAny, RiskFlags: []string{"hpa-coupled"}}
	case ProfileJVM:
		cpuReq := r.pickF(0.5, 2, 1, 3, 2, 2)
		main.Requests.CPU = Quantity(cpuReq)
		main.CPU = serviceCPU(r, cpuReq*r.uniform(0.1, 0.3))
		main.CPU.StartupMult = r.uniform(3, 6)
		main.CPU.StartupMinutes = r.uniform(3, 8)
		if r.chance(0.7) {
			main.Limits.CPU = Quantity(cpuReq * 2)
		}
		lim := r.pickF(1*gib, 2, 2*gib, 3, 4*gib, 2)
		main.Requests.Memory = Quantity(lim)
		main.Limits.Memory = Quantity(lim)
		main.Memory = Signal{Pattern: "sawtooth", Base: Quantity(lim * r.uniform(0.7, 0.82)), Sawtooth: r.uniform(0.06, 0.12), SawtoothMinutes: r.uniform(5, 30), Noise: 0.01}
		exp = &Expectation{CPU: ExpectDecrease, Memory: ExpectKeep}
	case ProfileBatch:
		every := time.Duration(r.pickF(1, 1, 4, 2, 6, 3, 12, 2, 24, 6)) * time.Hour
		dur := time.Duration(r.uniform(3, 40) * float64(time.Minute))
		offset := time.Duration(r.IntN(int(every/(5*time.Minute)))) * 5 * time.Minute
		w.Schedule = &ScheduleSpec{Every: Duration(every), Offset: Duration(offset), Duration: Duration(dur)}
		w.Replicas = 1
		cpuReq := r.pickF(0.5, 2, 1, 3, 2, 2)
		main.Requests.CPU = Quantity(cpuReq)
		main.Limits.CPU = Quantity(cpuReq * r.pickF(1, 1, 2, 1))
		main.CPU = Signal{Pattern: "sparse", Base: Quantity(cpuReq * r.uniform(0.4, 0.9)), Noise: 0.15}
		memReq := r.pickF(512*mib, 2, 1*gib, 3, 2*gib, 1)
		main.Requests.Memory = Quantity(memReq)
		main.Limits.Memory = Quantity(memReq)
		start, end := r.uniform(0.2, 0.35), r.uniform(0.5, 0.8)
		main.Memory = Signal{Pattern: "leak", Base: Quantity(memReq * start), LeakPerHour: (end - start) / start / dur.Hours(), Noise: 0.02}
		exp = &Expectation{CPU: ExpectAny, Memory: ExpectAny, RiskFlags: []string{"short-lived"}}
	}
	if profile != ProfileOOMProne {
		safeMemoryLimit(&main)
	}
	w.Containers = []ContainerSpec{main}
	w.Expect = exp
	if w.Kind != KindCronJob && (ns == "ledger" || ns == "payments") && r.chance(0.2) {
		w.InitContainers = []ContainerSpec{{Name: "db-migrate", Image: main.Image,
			Requests: Resources{CPU: 0.1, Memory: 128 * mib}, Limits: Resources{Memory: 256 * mib}}}
	}
	return w
}

func genDaemonSet(sc *Scenario, r *rnd, name, ns string) WorkloadSpec {
	cpuReq := r.pickF(0.05, 1, 0.1, 2, 0.2, 1)
	memReq := r.pickF(64*mib, 1, 128*mib, 2, 256*mib, 1)
	c := ContainerSpec{
		Name:     name,
		Image:    fmt.Sprintf("%s/platform/%s:%d.%d.%d", sc.ImageRegistry, name, 1+r.IntN(3), r.IntN(20), r.IntN(10)),
		Requests: Resources{CPU: Quantity(cpuReq), Memory: Quantity(memReq)},
		Limits:   Resources{Memory: Quantity(memReq * 2)},
		CPU:      serviceCPU(r, cpuReq*r.uniform(0.1, 0.4)),
		Memory:   serviceMemory(r, memReq*r.uniform(0.3, 0.6)),
	}
	return WorkloadSpec{Name: name, Namespace: ns, Kind: KindDaemonSet, Profile: ProfileDaemon, Replicas: 1,
		Containers: []ContainerSpec{c}, Expect: &Expectation{CPU: ExpectDecrease, Memory: ExpectDecrease}}
}

// serviceCPU returns a CPU signal whose ~P95 is p95 cores.
func serviceCPU(r *rnd, p95 float64) Signal {
	s := Signal{Pattern: r.pick(map[string]float64{"diurnal": 45, "weekly": 25, "flat": 15, "spiky": 15}), Noise: r.uniform(0.05, 0.12), PeakHour: r.uniform(9, 17)}
	switch s.Pattern {
	case "diurnal":
		s.Diurnal = r.uniform(0.3, 0.8)
	case "weekly":
		s.Diurnal = r.uniform(0.3, 0.7)
		s.Weekend = r.uniform(0.3, 0.6)
	case "spiky":
		s.Diurnal = r.uniform(0, 0.3)
		s.SpikesPerDay = r.uniform(2, 12)
		s.SpikeMult = r.uniform(2, 4)
		s.SpikeMinutes = r.uniform(2, 8)
	}
	s.Base = Quantity(p95 / (1 + s.Diurnal) / (1 + 1.5*s.Noise))
	return s
}

// serviceMemory returns a mostly flat memory signal around base bytes.
func serviceMemory(r *rnd, base float64) Signal {
	s := Signal{Pattern: "flat", Base: Quantity(base), Noise: r.uniform(0.01, 0.03)}
	if r.chance(0.2) {
		s.Pattern = "diurnal"
		s.Diurnal = r.uniform(0.05, 0.15)
		s.Base = Quantity(base / (1 + s.Diurnal))
	}
	return s
}

// safeMemoryLimit raises a memory limit that ordinary noise could hit, so only the oom-prone
// profile produces OOM kills.
func safeMemoryLimit(c *ContainerSpec) {
	m := normalize(c.Memory, false)
	peak := float64(m.Base) * (1 + m.Diurnal) * (1 + m.Sawtooth/2) * 1.07 * (1 + 5*m.Noise)
	if m.LeakPerHour > 0 {
		return // batch: bounded by the job duration
	}
	if c.Limits.Memory > 0 && float64(c.Limits.Memory) < peak/0.97 {
		c.Limits.Memory = Quantity(math.Ceil(peak/0.97/(64*mib)) * 64 * mib)
		if c.Requests.Memory > c.Limits.Memory {
			c.Requests.Memory = c.Limits.Memory
		}
	}
}

func setLimits(r *rnd, c *ContainerSpec, allowEqual bool) {
	switch x := r.Float64(); {
	case x < 0.35:
	case x < 0.8 || !allowEqual:
		c.Limits.CPU = c.Requests.CPU * 2
	default:
		c.Limits.CPU = c.Requests.CPU * 4
	}
	switch x := r.Float64(); {
	case x < 0.6:
		c.Limits.Memory = c.Requests.Memory
	case x < 0.9:
		c.Limits.Memory = c.Requests.Memory * 2
	}
}

func appImage(sc *Scenario, r *rnd, ns, name string, jvm bool) string {
	tag := fmt.Sprintf("%d.%d.%d", 1+r.IntN(5), r.IntN(40), r.IntN(20))
	if jvm {
		tag += "-temurin21-jre"
	}
	return fmt.Sprintf("%s/%s/%s:%s", sc.ImageRegistry, ns, name, tag)
}

func applyModifiers(r *rnd, w *WorkloadSpec, f *FleetSpec, days float64) {
	main := &w.Containers[0]
	if w.Kind != KindCronJob && r.chance(f.StepChanges) &&
		(w.Profile == ProfileOverProvisioned || w.Profile == ProfileRightSized) {
		main.CPU.StepAt = Duration(time.Duration(r.uniform(0.15, 0.6) * days * float64(24*time.Hour)))
		if w.Profile == ProfileOverProvisioned {
			main.CPU.StepFactor = r.pickF(0.5, 1, 1.8, 1, 2.2, 1)
			if main.CPU.StepFactor > 1 {
				main.CPU.Base = Quantity(math.Min(float64(main.CPU.Base), float64(main.Requests.CPU)*0.18/(1+main.CPU.Diurnal)))
			}
		} else {
			main.CPU.StepFactor = 0.5
			w.Expect.CPU = ExpectAny
		}
		main.CPU.Pattern = "step"
	}
	if w.Kind != KindCronJob && w.Kind != KindDaemonSet && r.chance(f.Sidecars) {
		w.Containers = append(w.Containers, envoySidecar(r, main))
		w.Expect.RiskFlags = append(w.Expect.RiskFlags, "sidecar")
	}
	if w.Kind != KindCronJob && w.Profile != ProfileOOMProne && r.chance(f.Restarting) {
		main.CrashEvery = Duration(time.Duration(r.uniform(6, 48) * float64(time.Hour)))
		w.Expect.RiskFlags = append(w.Expect.RiskFlags, "restarting")
	}
	if r.chance(f.NewWorkloads) {
		w.Age = Duration(time.Duration(r.uniform(6, 72) * float64(time.Hour)))
		w.Expect.RiskFlags = append(w.Expect.RiskFlags, "low-confidence")
		w.Expect.CPU, w.Expect.Memory = ExpectAny, ExpectAny
	}
	if w.Kind != KindCronJob {
		w.Rollouts = randomRollouts(r, days, f.RolloutEveryDays)
	}
	sort.Strings(w.Expect.RiskFlags)
}

func envoySidecar(r *rnd, main *ContainerSpec) ContainerSpec {
	cpu := Signal{Pattern: main.CPU.Pattern, Base: Quantity(r.uniform(0.008, 0.03)), Diurnal: main.CPU.Diurnal,
		PeakHour: main.CPU.PeakHour, Weekend: main.CPU.Weekend, Noise: 0.1}
	return ContainerSpec{
		Name: "envoy", Image: "docker.io/envoyproxy/envoy:v1.34.1", Sidecar: true,
		Requests: Resources{CPU: 0.1, Memory: 128 * mib},
		Limits:   Resources{CPU: 0.5, Memory: 256 * mib},
		CPU:      cpu,
		Memory:   Signal{Pattern: "flat", Base: Quantity(r.uniform(40, 80) * mib), Noise: 0.01},
	}
}

func randomRollouts(r *rnd, days, every float64) []Duration {
	n := r.poisson(days / every)
	out := make([]Duration, n)
	for i := range out {
		out[i] = Duration(time.Duration(r.uniform(0.02, 0.98) * days * float64(24*time.Hour)))
	}
	return out
}
