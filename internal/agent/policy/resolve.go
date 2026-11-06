// Package policy resolves the effective rightsizing policy of a workload (D-005, D-022):
//
//	built-in defaults < hub ro-global-defaults < chart cluster defaults < hub ro-cluster-defaults
//	  < winning RightsizingPolicy (highest priority match) < hub workload override < annotations
//
// Every field is layered separately: a level that leaves a field unset inherits it from the level
// below. Only the winning RightsizingPolicy takes part; fields are never merged across policies.
// The hub's defaults and overrides (CRs labelled managed-by=hub with a hub-policy-type
// annotation) are layers, not competitors: they never win or lose against a policy.
package policy

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// Global defaults (documented on the RightsizingPolicy fields).
const (
	DefaultFallbackAfter    = 10 * time.Minute
	DefaultLearningPeriod   = 168 * time.Hour
	DefaultMinConfidence    = 0.7
	DefaultMaxChangePercent = 50
	DefaultCooldown         = 24 * time.Hour

	DefaultWatchDuration   = 30 * time.Minute
	DefaultRollbackOOMs    = 1
	DefaultRollbackRestart = 3
	DefaultThrottlingRatio = 0.25
	// HPA target bounds, percent.
	DefaultHPAMinTarget int32 = 40
	DefaultHPAMaxTarget int32 = 90
)

var (
	DefaultMinCPU    = resource.MustParse("10m")
	DefaultMinMemory = resource.MustParse("32Mi")
)

// Workload is what policy resolution needs to know about a workload object.
type Workload struct {
	Namespace   string
	Kind        v1alpha1.WorkloadKind
	Name        string
	Labels      map[string]string
	Annotations map[string]string
}

// Effective is the resolved policy of one workload, with every default filled in.
type Effective struct {
	Policy string
	// Override is the hub workload-override policy layered on top of Policy (D-022).
	Override         string
	CPUMode          v1alpha1.Mode
	CPUModeSource    v1alpha1.ModeSource
	MemoryMode       v1alpha1.Mode
	MemoryModeSource v1alpha1.ModeSource
	Strategy         v1alpha1.Strategy
	Profile          v1alpha1.Profile

	CPULimits                v1alpha1.CPULimitPolicy
	CPULimitRatio            string
	MemoryLimitEqualsRequest bool

	FallbackAfter      time.Duration
	MaintenanceWindows []v1alpha1.TimeWindow
	FreezeWindows      []v1alpha1.TimeWindow

	LearningPeriod   time.Duration
	MinConfidence    float64
	MaxChangePercent int32
	MinCPU           resource.Quantity
	MinMemory        resource.Quantity
	Cooldown         time.Duration

	// Rollback (health watch).
	WatchDuration    time.Duration
	RollbackOOMKills int32
	RollbackRestarts int32
	ThrottlingRatio  float64

	JVMDetect bool

	// HPA coordination: change requests of an HPA's utilization resource only together
	// with its target, which must stay within [HPAMinTarget, HPAMaxTarget] percent.
	HPACoordinate bool
	HPAMinTarget  int32
	HPAMaxTarget  int32

	// Warnings lists ignored invalid values (e.g. a bad annotation), for logs and status messages.
	Warnings []string
}

// Mode returns the effective mode of a resource ("cpu" or "memory").
func (e Effective) Mode(res string) v1alpha1.Mode {
	if res == "cpu" {
		return e.CPUMode
	}
	return e.MemoryMode
}

// AnyAutoPatch reports whether CPU or memory is in auto-patch.
func (e Effective) AnyAutoPatch() bool {
	return e.CPUMode == v1alpha1.ModeAutoPatch || e.MemoryMode == v1alpha1.ModeAutoPatch
}

// API returns the RightsizingRecommendation.status.effective form.
func (e Effective) API() *v1alpha1.EffectivePolicy {
	return &v1alpha1.EffectivePolicy{
		Policy:           e.Policy,
		CPUMode:          e.CPUMode,
		CPUModeSource:    e.CPUModeSource,
		MemoryMode:       e.MemoryMode,
		MemoryModeSource: e.MemoryModeSource,
		Strategy:         e.Strategy,
		Profile:          e.Profile,
	}
}

// Settings returns the value settings reported to the hub (ValueSettings).
func (e Effective) Settings() hubclient.ValueSettings {
	eq := e.MemoryLimitEqualsRequest
	s := hubclient.ValueSettings{
		Profile:                  string(e.Profile),
		CPULimits:                string(e.CPULimits),
		MemoryLimitEqualsRequest: &eq,
	}
	if e.CPULimits == v1alpha1.CPULimitRatio {
		s.CPULimitRatio = e.CPULimitRatio
	}
	return s
}

// SettingsMatch reports whether a recommendation computed with got is valid for the effective
// settings want. Fields the hub left empty are taken as global defaults.
func SettingsMatch(want, got hubclient.ValueSettings) bool {
	norm := func(s hubclient.ValueSettings) hubclient.ValueSettings {
		if s.Profile == "" {
			s.Profile = string(v1alpha1.DefaultProfile)
		}
		if s.CPULimits == "" {
			s.CPULimits = string(v1alpha1.CPULimitKeep)
		}
		if s.MemoryLimitEqualsRequest == nil {
			t := true
			s.MemoryLimitEqualsRequest = &t
		}
		if s.CPULimits != string(v1alpha1.CPULimitRatio) {
			s.CPULimitRatio = ""
		}
		return s
	}
	a, b := norm(want), norm(got)
	if a.Profile != b.Profile || a.CPULimits != b.CPULimits || *a.MemoryLimitEqualsRequest != *b.MemoryLimitEqualsRequest {
		return false
	}
	if a.CPULimitRatio == b.CPULimitRatio {
		return true
	}
	ra, errA := strconv.ParseFloat(a.CPULimitRatio, 64)
	rb, errB := strconv.ParseFloat(b.CPULimitRatio, 64)
	return errA == nil && errB == nil && ra == rb
}

// Matches reports whether a policy selector selects the workload.
func Matches(sel v1alpha1.PolicySelector, w Workload) bool {
	if len(sel.Namespaces) > 0 && !globAny(sel.Namespaces, w.Namespace) {
		return false
	}
	if globAny(sel.ExcludeNamespaces, w.Namespace) {
		return false
	}
	for k, v := range sel.MatchLabels {
		if got, ok := w.Labels[k]; !ok || got != v {
			return false
		}
	}
	if len(sel.Kinds) > 0 {
		found := false
		for _, k := range sel.Kinds {
			if k == w.Kind {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(sel.Workloads) > 0 {
		ref := v1alpha1.NamespacedWorkloadRef{Kind: w.Kind, Namespace: w.Namespace, Name: w.Name}
		if !slices.Contains(sel.Workloads, ref) {
			return false
		}
	}
	return true
}

// HubLayer returns the hub policy type of a CR that is a layer rather than a competing policy:
// HubPolicyTypeGlobalDefault, HubPolicyTypeClusterDefault or HubPolicyTypeWorkloadOverride, and
// "" otherwise. Only CRs labelled managed-by=hub count (a Git CR cannot become a layer).
func HubLayer(p *v1alpha1.RightsizingPolicy) string {
	if p.Labels[v1alpha1.LabelManagedBy] != v1alpha1.ManagedByHub {
		return ""
	}
	switch t := p.Annotations[v1alpha1.AnnotationHubPolicyType]; t {
	case v1alpha1.HubPolicyTypeGlobalDefault, v1alpha1.HubPolicyTypeClusterDefault, v1alpha1.HubPolicyTypeWorkloadOverride:
		return t
	}
	return ""
}

func globAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, s); err == nil && ok {
			return true
		}
	}
	return false
}

// Winner returns the matching policy with the highest priority (ties: lowest name), or nil.
func Winner(policies []v1alpha1.RightsizingPolicy, w Workload) *v1alpha1.RightsizingPolicy {
	var matching []*v1alpha1.RightsizingPolicy
	for i := range policies {
		if policies[i].DeletionTimestamp != nil {
			continue
		}
		if Matches(policies[i].Spec.Selector, w) {
			matching = append(matching, &policies[i])
		}
	}
	if len(matching) == 0 {
		return nil
	}
	sort.Slice(matching, func(i, j int) bool {
		if matching[i].Spec.Priority != matching[j].Spec.Priority {
			return matching[i].Spec.Priority > matching[j].Spec.Priority
		}
		return matching[i].Name < matching[j].Name
	})
	return matching[0]
}

// Resolve computes the effective policy. cluster holds the chart's cluster defaults (its selector
// and priority are ignored); nil means none.
func Resolve(w Workload, policies []v1alpha1.RightsizingPolicy, cluster *v1alpha1.RightsizingPolicySpec) Effective {
	var competing []v1alpha1.RightsizingPolicy
	layers := map[string][]v1alpha1.RightsizingPolicy{}
	for i := range policies {
		if t := HubLayer(&policies[i]); t != "" {
			layers[t] = append(layers[t], policies[i])
		} else {
			competing = append(competing, policies[i])
		}
	}
	e := Effective{
		CPUMode:                  v1alpha1.DefaultMode,
		CPUModeSource:            v1alpha1.ModeSourceGlobalDefault,
		MemoryMode:               v1alpha1.DefaultMode,
		MemoryModeSource:         v1alpha1.ModeSourceGlobalDefault,
		Strategy:                 v1alpha1.DefaultStrategy,
		Profile:                  v1alpha1.DefaultProfile,
		CPULimits:                v1alpha1.CPULimitKeep,
		MemoryLimitEqualsRequest: true,
		FallbackAfter:            DefaultFallbackAfter,
		LearningPeriod:           DefaultLearningPeriod,
		MinConfidence:            DefaultMinConfidence,
		MaxChangePercent:         DefaultMaxChangePercent,
		MinCPU:                   DefaultMinCPU.DeepCopy(),
		MinMemory:                DefaultMinMemory.DeepCopy(),
		Cooldown:                 DefaultCooldown,
		WatchDuration:            DefaultWatchDuration,
		RollbackOOMKills:         DefaultRollbackOOMs,
		RollbackRestarts:         DefaultRollbackRestart,
		ThrottlingRatio:          DefaultThrottlingRatio,
		JVMDetect:                true,
		HPAMinTarget:             DefaultHPAMinTarget,
		HPAMaxTarget:             DefaultHPAMaxTarget,
	}
	if p := Winner(layers[v1alpha1.HubPolicyTypeGlobalDefault], w); p != nil {
		e.layer(&p.Spec, v1alpha1.ModeSourceGlobalDefault, "hub global defaults "+p.Name)
	}
	if cluster != nil {
		e.layer(cluster, v1alpha1.ModeSourceClusterDefault, "cluster default")
	}
	if p := Winner(layers[v1alpha1.HubPolicyTypeClusterDefault], w); p != nil {
		e.layer(&p.Spec, v1alpha1.ModeSourceClusterDefault, "hub cluster defaults "+p.Name)
	}
	if p := Winner(competing, w); p != nil {
		e.Policy = p.Name
		e.layer(&p.Spec, v1alpha1.ModeSourcePolicy, "RightsizingPolicy "+p.Name)
	}
	if p := Winner(layers[v1alpha1.HubPolicyTypeWorkloadOverride], w); p != nil {
		e.Override = p.Name
		e.layer(&p.Spec, v1alpha1.ModeSourcePolicy, "workload override "+p.Name)
	}
	e.annotations(w.Annotations)
	return e
}

func (e *Effective) warnf(format string, args ...any) {
	e.Warnings = append(e.Warnings, fmt.Sprintf(format, args...))
}

func validMode(m v1alpha1.Mode) bool {
	return m == v1alpha1.ModeOff || m == v1alpha1.ModeAudit || m == v1alpha1.ModeAutoPatch
}

func validStrategy(s v1alpha1.Strategy) bool {
	return s == v1alpha1.StrategyInPlace || s == v1alpha1.StrategyRollout || s == v1alpha1.StrategyDeferred
}

func validProfile(p v1alpha1.Profile) bool {
	return p == v1alpha1.ProfileDev || p == v1alpha1.ProfileProduction || p == v1alpha1.ProfileHighAvailability
}

func (e *Effective) layer(s *v1alpha1.RightsizingPolicySpec, src v1alpha1.ModeSource, what string) {
	if s.Profile != "" {
		if validProfile(s.Profile) {
			e.Profile = s.Profile
		} else {
			e.warnf("%s: invalid profile %q ignored", what, s.Profile)
		}
	}
	if s.CPU != nil {
		if s.CPU.Mode != "" {
			if validMode(s.CPU.Mode) {
				e.CPUMode, e.CPUModeSource = s.CPU.Mode, src
			} else {
				e.warnf("%s: invalid cpu mode %q ignored", what, s.CPU.Mode)
			}
		}
		if s.CPU.Limits != "" {
			e.CPULimits, e.CPULimitRatio = s.CPU.Limits, s.CPU.LimitRatio
		}
	}
	if s.Memory != nil {
		if s.Memory.Mode != "" {
			if validMode(s.Memory.Mode) {
				e.MemoryMode, e.MemoryModeSource = s.Memory.Mode, src
			} else {
				e.warnf("%s: invalid memory mode %q ignored", what, s.Memory.Mode)
			}
		}
		if s.Memory.LimitEqualsRequest != nil {
			e.MemoryLimitEqualsRequest = *s.Memory.LimitEqualsRequest
		}
	}
	if a := s.Apply; a != nil {
		if a.Strategy != "" {
			if validStrategy(a.Strategy) {
				e.Strategy = a.Strategy
			} else {
				e.warnf("%s: invalid strategy %q ignored", what, a.Strategy)
			}
		}
		if a.FallbackAfter != nil {
			e.FallbackAfter = a.FallbackAfter.Duration
		}
		if a.MaintenanceWindows != nil {
			e.MaintenanceWindows = a.MaintenanceWindows
		}
		if a.FreezeWindows != nil {
			e.FreezeWindows = a.FreezeWindows
		}
	}
	if g := s.Guardrails; g != nil {
		if g.LearningPeriod != nil {
			e.LearningPeriod = g.LearningPeriod.Duration
		}
		if g.MinConfidence != "" {
			if f, err := strconv.ParseFloat(g.MinConfidence, 64); err == nil && f >= 0 && f <= 1 {
				e.MinConfidence = f
			} else {
				e.warnf("%s: invalid minConfidence %q ignored", what, g.MinConfidence)
			}
		}
		if g.MaxChangePercent != nil {
			e.MaxChangePercent = *g.MaxChangePercent
		}
		if g.MinCPU != nil {
			e.MinCPU = g.MinCPU.DeepCopy()
		}
		if g.MinMemory != nil {
			e.MinMemory = g.MinMemory.DeepCopy()
		}
		if g.Cooldown != nil {
			e.Cooldown = g.Cooldown.Duration
		}
		if rb := g.Rollback; rb != nil {
			if rb.WatchDuration != nil {
				e.WatchDuration = rb.WatchDuration.Duration
			}
			if rb.OOMKills != nil {
				e.RollbackOOMKills = *rb.OOMKills
			}
			if rb.Restarts != nil {
				e.RollbackRestarts = *rb.Restarts
			}
			if rb.ThrottlingRatio != "" {
				if f, err := strconv.ParseFloat(rb.ThrottlingRatio, 64); err == nil && f >= 0 && f <= 1 {
					e.ThrottlingRatio = f
				} else {
					e.warnf("%s: invalid rollback.throttlingRatio %q ignored", what, rb.ThrottlingRatio)
				}
			}
		}
	}
	if s.JVM != nil && s.JVM.Detect != nil {
		e.JVMDetect = *s.JVM.Detect
	}
	if h := s.HPA; h != nil {
		if h.Coordinate != nil {
			e.HPACoordinate = *h.Coordinate
		}
		if b := h.TargetBounds; b != nil {
			if b.MinUtilization != nil {
				e.HPAMinTarget = *b.MinUtilization
			}
			if b.MaxUtilization != nil {
				e.HPAMaxTarget = *b.MaxUtilization
			}
			if e.HPAMinTarget > e.HPAMaxTarget {
				e.warnf("%s: hpa.targetBounds min %d > max %d ignored", what, e.HPAMinTarget, e.HPAMaxTarget)
				e.HPAMinTarget, e.HPAMaxTarget = DefaultHPAMinTarget, DefaultHPAMaxTarget
			}
		}
	}
}

func (e *Effective) annotations(ann map[string]string) {
	modeAnn := func(key string) (v1alpha1.Mode, bool) {
		v, ok := ann[key]
		if !ok {
			return "", false
		}
		m := v1alpha1.Mode(v)
		if !validMode(m) {
			e.warnf("annotation %s: invalid mode %q ignored", key, v)
			return "", false
		}
		return m, true
	}
	if m, ok := modeAnn(v1alpha1.AnnotationMode); ok {
		e.CPUMode, e.CPUModeSource = m, v1alpha1.ModeSourceAnnotation
		e.MemoryMode, e.MemoryModeSource = m, v1alpha1.ModeSourceAnnotation
	}
	if m, ok := modeAnn(v1alpha1.AnnotationCPUMode); ok {
		e.CPUMode, e.CPUModeSource = m, v1alpha1.ModeSourceAnnotation
	}
	if m, ok := modeAnn(v1alpha1.AnnotationMemoryMode); ok {
		e.MemoryMode, e.MemoryModeSource = m, v1alpha1.ModeSourceAnnotation
	}
	if v, ok := ann[v1alpha1.AnnotationStrategy]; ok {
		if validStrategy(v1alpha1.Strategy(v)) {
			e.Strategy = v1alpha1.Strategy(v)
		} else {
			e.warnf("annotation %s: invalid strategy %q ignored", v1alpha1.AnnotationStrategy, v)
		}
	}
	if v, ok := ann[v1alpha1.AnnotationProfile]; ok {
		if validProfile(v1alpha1.Profile(v)) {
			e.Profile = v1alpha1.Profile(v)
		} else {
			e.warnf("annotation %s: invalid profile %q ignored", v1alpha1.AnnotationProfile, v)
		}
	}
}
