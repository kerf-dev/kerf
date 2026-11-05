package recommender

import (
	"fmt"
	"sort"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Profile is a named set of recommendation parameters.
type Profile struct {
	Name string `json:"name"`
	// Window is the usage window recommendations are computed over.
	Window time.Duration `json:"window"`
	// CPUPercentile is the CPU usage quantile the CPU request is based on (e.g. 0.95).
	CPUPercentile float64 `json:"cpuPercentile"`
	// MemoryPercentile is the memory usage quantile (e.g. 0.99); the request is based on
	// max(MemoryPercentile, peak).
	MemoryPercentile float64 `json:"memoryPercentile"`
	// MemoryPeakWindow is the window for "peak"; 0 means the whole Window.
	MemoryPeakWindow time.Duration `json:"memoryPeakWindow"`
	// CPUHeadroom is the fraction added to the CPU statistic (0.15 = +15 %).
	CPUHeadroom float64 `json:"cpuHeadroom"`
	// MemoryBuffer is the fraction added to the memory statistic.
	MemoryBuffer float64 `json:"memoryBuffer"`
}

// PeakWindow returns the effective memory peak window.
func (p Profile) PeakWindow() time.Duration {
	if p.MemoryPeakWindow <= 0 || p.MemoryPeakWindow > p.Window {
		return p.Window
	}
	return p.MemoryPeakWindow
}

// DefaultProfiles are the built-in profiles. Every value is configurable (D-010).
func DefaultProfiles() map[string]Profile {
	return map[string]Profile{
		hubapi.ProfileDev: {
			Name: hubapi.ProfileDev, Window: 7 * 24 * time.Hour,
			CPUPercentile: 0.90, MemoryPercentile: 0.95,
			CPUHeadroom: 0.10, MemoryBuffer: 0.10,
		},
		hubapi.ProfileProduction: {
			Name: hubapi.ProfileProduction, Window: 14 * 24 * time.Hour,
			CPUPercentile: 0.95, MemoryPercentile: 0.99,
			CPUHeadroom: 0.15, MemoryBuffer: 0.15,
		},
		hubapi.ProfileHighAvailability: {
			Name: hubapi.ProfileHighAvailability, Window: 28 * 24 * time.Hour,
			CPUPercentile: 0.99, MemoryPercentile: 0.99,
			CPUHeadroom: 0.30, MemoryBuffer: 0.25,
		},
	}
}

// Config holds the recommender parameters that do not depend on the profile.
type Config struct {
	// Step is the subquery resolution for usage statistics.
	Step time.Duration `json:"step"`
	// RateWindow is the range of rate() for CPU counters.
	RateWindow time.Duration `json:"rateWindow"`
	// RiskWindow is the lookback for throttling, OOM kills and restarts.
	RiskWindow time.Duration `json:"riskWindow"`
	// InventoryLookback is how long a workload stays in the inventory after its last pod is gone
	// (covers CronJobs and backfilled synthetic data).
	InventoryLookback time.Duration `json:"inventoryLookback"`

	ThrottleThreshold float64 `json:"throttleThreshold"`
	RestartThreshold  float64 `json:"restartThreshold"`
	// ThrottleIncrease / OOMIncrease: minimum increase over the current value when the container
	// is throttled / OOM-killed (0.2 = +20 %).
	ThrottleIncrease float64 `json:"throttleIncrease"`
	OOMIncrease      float64 `json:"oomIncrease"`

	MinCPUCores    float64 `json:"minCPUCores"`
	MinMemoryBytes float64 `json:"minMemoryBytes"`
	// CPUStepCores and MemoryStepBytes are the rounding steps (results are rounded up).
	CPUStepCores    float64 `json:"cpuStepCores"`
	MemoryStepBytes float64 `json:"memoryStepBytes"`

	// LowConfidence: recommendations below this confidence get the low-confidence flag.
	LowConfidence float64 `json:"lowConfidence"`
	// FullConfidenceSamples is the number of samples at which the sample score saturates.
	FullConfidenceSamples int `json:"fullConfidenceSamples"`
	// ShortLivedDensity: containers present in fewer than this fraction of steps between their
	// first and last sample are short-lived.
	ShortLivedDensity float64 `json:"shortLivedDensity"`
	// SidecarContainers are container names treated as injected sidecars.
	SidecarContainers []string `json:"sidecarContainers"`

	// DefaultSettings fill in ValueSettings the agent did not report.
	DefaultSettings hubapi.ValueSettings `json:"defaultSettings"`
	// DefaultCPUCoreHourUSD / DefaultMemoryGiBHourUSD are used when no pricing series exist.
	DefaultCPUCoreHourUSD   float64 `json:"defaultCPUCoreHourUSD"`
	DefaultMemoryGiBHourUSD float64 `json:"defaultMemoryGiBHourUSD"`

	Profiles map[string]Profile `json:"profiles"`

	// JVM configures JVM-aware memory recommendations (WO-12).
	JVM JVMConfig `json:"jvm"`
}

// MiB and GiB in bytes.
const (
	MiB = 1 << 20
	GiB = 1 << 30
)

// HoursPerMonth is used for monthly savings.
const HoursPerMonth = 730

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	t := true
	return Config{
		Step:                  5 * time.Minute,
		RateWindow:            5 * time.Minute,
		RiskWindow:            7 * 24 * time.Hour,
		InventoryLookback:     25 * time.Hour,
		ThrottleThreshold:     0.10,
		RestartThreshold:      3,
		ThrottleIncrease:      0.20,
		OOMIncrease:           0.20,
		MinCPUCores:           0.010,
		MinMemoryBytes:        32 * MiB,
		CPUStepCores:          0.005,
		MemoryStepBytes:       4 * MiB,
		LowConfidence:         0.5,
		FullConfidenceSamples: 288 * 7,
		ShortLivedDensity:     0.5,
		SidecarContainers:     []string{"istio-proxy", "linkerd-proxy", "envoy", "vault-agent", "cloud-sql-proxy"},
		DefaultSettings: hubapi.ValueSettings{
			Profile: hubapi.ProfileProduction, CPULimits: hubapi.CPULimitsKeep, MemoryLimitEqualsRequest: &t,
		},
		// m6i.xlarge on-demand eu-west-1 ($0.214/h, 4 vCPU, 16 GiB) split 50/50.
		DefaultCPUCoreHourUSD:   0.02675,
		DefaultMemoryGiBHourUSD: 0.0066875,
		Profiles:                DefaultProfiles(),
		JVM:                     DefaultJVMConfig(),
	}
}

// Validate checks the configuration.
func (c Config) Validate() error {
	if c.Step <= 0 || c.RateWindow <= 0 || c.RiskWindow <= 0 || c.InventoryLookback <= 0 {
		return fmt.Errorf("step, rateWindow, riskWindow and inventoryLookback must be positive")
	}
	if c.CPUStepCores <= 0 || c.MemoryStepBytes <= 0 {
		return fmt.Errorf("rounding steps must be positive")
	}
	for name, p := range c.Profiles {
		if p.Window < c.Step {
			return fmt.Errorf("profile %s: window %s shorter than step %s", name, p.Window, c.Step)
		}
		for _, q := range []float64{p.CPUPercentile, p.MemoryPercentile} {
			if q <= 0 || q > 1 {
				return fmt.Errorf("profile %s: percentile %v out of (0,1]", name, q)
			}
		}
		if p.CPUHeadroom < 0 || p.MemoryBuffer < 0 {
			return fmt.Errorf("profile %s: negative margin", name)
		}
	}
	if _, ok := c.Profiles[c.DefaultSettings.Profile]; !ok {
		return fmt.Errorf("default profile %q is not defined", c.DefaultSettings.Profile)
	}
	return nil
}

// EffectiveSettings fills unset fields of s from the defaults and normalises unknown values.
func (c Config) EffectiveSettings(s *hubapi.ValueSettings) hubapi.ValueSettings {
	out := c.DefaultSettings
	if s == nil {
		return out
	}
	if _, ok := c.Profiles[s.Profile]; ok {
		out.Profile = s.Profile
	}
	switch s.CPULimits {
	case hubapi.CPULimitsKeep, hubapi.CPULimitsRemove:
		out.CPULimits, out.CPULimitRatio = s.CPULimits, ""
	case hubapi.CPULimitsRatio:
		if r, err := parseRatio(s.CPULimitRatio); err == nil && r >= 1 {
			out.CPULimits, out.CPULimitRatio = s.CPULimits, s.CPULimitRatio
		}
	}
	if s.MemoryLimitEqualsRequest != nil {
		v := *s.MemoryLimitEqualsRequest
		out.MemoryLimitEqualsRequest = &v
	}
	return out
}

// Quantiles returns the quantiles that must be computed for the given profiles: the explanation
// set (0.5, 0.95, 0.99) plus each profile's percentiles.
func Quantiles(profiles ...Profile) []float64 {
	set := map[float64]bool{0.5: true, 0.95: true, 0.99: true}
	for _, p := range profiles {
		set[p.CPUPercentile] = true
		set[p.MemoryPercentile] = true
	}
	out := make([]float64, 0, len(set))
	for q := range set {
		out = append(out, q)
	}
	sort.Float64s(out)
	return out
}
