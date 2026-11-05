// Package config turns the hub's admin settings into what the hub runs with (WO-20): the
// recommender settings section into a recommender configuration, the pricing section into
// adjusted rates, and a cluster's agent configuration into its content-addressed version.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Bounds of the recommender settings.
const (
	MinInterval  = time.Minute
	MaxInterval  = 7 * 24 * time.Hour
	MinRetention = 24 * time.Hour
	MaxWindow    = 90 * 24 * time.Hour
)

// ProfileNames are the built-in profiles, in display order (custom profiles are not in the
// contract: the Profile enum would have to widen).
var ProfileNames = []string{hubapi.ProfileDev, hubapi.ProfileProduction, hubapi.ProfileHighAvailability}

// Recommender is the effective recommender configuration of the hub.
type Recommender struct {
	Config    recommender.Config
	Interval  time.Duration
	Retention time.Duration
}

// RecommenderSettings returns the API form of a recommender configuration (the value of section
// version 0, i.e. the hub configuration file).
func RecommenderSettings(r Recommender) hubapi.RecommenderSettings {
	out := hubapi.RecommenderSettings{Interval: r.Interval.String(), Retention: r.Retention.String()}
	for _, name := range ProfileNames {
		p, ok := r.Config.Profiles[name]
		if !ok {
			continue
		}
		ps := hubapi.RecommenderProfileSettings{
			Name: name, Window: p.Window.String(), CPUPercentile: p.CPUPercentile, MemoryPercentile: p.MemoryPercentile,
			CPUHeadroom: p.CPUHeadroom, MemoryBuffer: p.MemoryBuffer,
		}
		if p.MemoryPeakWindow > 0 {
			ps.MemoryPeakWindow = p.MemoryPeakWindow.String()
		}
		out.Profiles = append(out.Profiles, ps)
	}
	return out
}

// ApplyRecommender validates v and returns base with it applied. Errors list every problem.
func ApplyRecommender(base Recommender, v hubapi.RecommenderSettings) (Recommender, []string) {
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	out := base
	out.Config.Profiles = map[string]recommender.Profile{}
	for k, p := range base.Config.Profiles {
		out.Config.Profiles[k] = p
	}
	dur := func(field, s string, minV, maxV time.Duration) time.Duration {
		d, err := time.ParseDuration(s)
		switch {
		case err != nil:
			bad("%s: invalid duration %q", field, s)
		case d < minV || (maxV > 0 && d > maxV):
			if maxV > 0 {
				bad("%s: %s is out of [%s, %s]", field, s, minV, maxV)
			} else {
				bad("%s: %s is below %s", field, s, minV)
			}
		}
		return d
	}
	out.Interval = dur("interval", v.Interval, MinInterval, MaxInterval)
	out.Retention = dur("retention", v.Retention, MinRetention, 0)
	seen := map[string]bool{}
	for i, p := range v.Profiles {
		f := fmt.Sprintf("profiles[%d]", i)
		if !slices.Contains(ProfileNames, p.Name) {
			bad("%s.name: unknown profile %q (profiles are %v)", f, p.Name, ProfileNames)
			continue
		}
		if seen[p.Name] {
			bad("%s.name: duplicate profile %q", f, p.Name)
			continue
		}
		seen[p.Name] = true
		cur := out.Config.Profiles[p.Name]
		cur.Name = p.Name
		cur.Window = dur(f+".window", p.Window, base.Config.Step, MaxWindow)
		cur.MemoryPeakWindow = 0
		if p.MemoryPeakWindow != "" {
			cur.MemoryPeakWindow = dur(f+".memoryPeakWindow", p.MemoryPeakWindow, base.Config.Step, MaxWindow)
		}
		for _, q := range []struct {
			name string
			v    float64
		}{{"cpuPercentile", p.CPUPercentile}, {"memoryPercentile", p.MemoryPercentile}} {
			if q.v <= 0 || q.v > 1 || math.IsNaN(q.v) {
				bad("%s.%s: %v is out of (0, 1]", f, q.name, q.v)
			}
		}
		for _, m := range []struct {
			name string
			v    float64
		}{{"cpuHeadroom", p.CPUHeadroom}, {"memoryBuffer", p.MemoryBuffer}} {
			if m.v < 0 || m.v > 10 || math.IsNaN(m.v) {
				bad("%s.%s: %v is out of [0, 10]", f, m.name, m.v)
			}
		}
		cur.CPUPercentile, cur.MemoryPercentile = p.CPUPercentile, p.MemoryPercentile
		cur.CPUHeadroom, cur.MemoryBuffer = p.CPUHeadroom, p.MemoryBuffer
		out.Config.Profiles[p.Name] = cur
	}
	for _, name := range ProfileNames {
		if !seen[name] {
			bad("profiles: profile %q is missing (all of %v are required)", name, ProfileNames)
		}
	}
	if len(errs) == 0 {
		if err := out.Config.Validate(); err != nil {
			bad("%v", err)
		}
	}
	if len(errs) > 0 {
		return base, errs
	}
	return out, nil
}

// ValidatePricing checks a pricing section; known reports whether a cluster is registered.
func ValidatePricing(v hubapi.PricingSettings, known func(string) bool) []string {
	var errs []string
	pct := func(f string, p *float64) {
		if p != nil && (*p < 0 || *p >= 100 || math.IsNaN(*p)) {
			errs = append(errs, fmt.Sprintf("%s: %v is out of [0, 100)", f, *p))
		}
	}
	rate := func(f string, p *float64) {
		if p != nil && (*p < 0 || math.IsNaN(*p) || math.IsInf(*p, 0)) {
			errs = append(errs, fmt.Sprintf("%s: must not be negative", f))
		}
	}
	pct("defaultDiscountPercent", v.DefaultDiscountPercent)
	seen := map[string]bool{}
	for i, c := range v.Clusters {
		f := fmt.Sprintf("clusters[%d]", i)
		switch {
		case !hubapi.ClusterNameRE.MatchString(c.Cluster):
			errs = append(errs, fmt.Sprintf("%s.cluster: invalid cluster name %q", f, c.Cluster))
		case seen[c.Cluster]:
			errs = append(errs, fmt.Sprintf("%s.cluster: duplicate cluster %q", f, c.Cluster))
		case known != nil && !known(c.Cluster):
			errs = append(errs, fmt.Sprintf("%s.cluster: unknown cluster %q", f, c.Cluster))
		}
		seen[c.Cluster] = true
		pct(f+".discountPercent", c.DiscountPercent)
		rate(f+".cpuCoreHourUSD", c.CPUCoreHourUSD)
		rate(f+".memoryGiBHourUSD", c.MemoryGiBHourUSD)
	}
	return errs
}

// AdjustPricing applies the pricing section to a cluster's rates: a fixed rate replaces the
// metric or default rate, then the cluster's discount (else the default discount) applies to
// whichever rate is used. Without any adjustment p is returned unchanged.
func AdjustPricing(v hubapi.PricingSettings, version int, cluster string, p recommender.Pricing) recommender.Pricing {
	base := p.Base()
	adj := recommender.PricingAdjustment{BaseCPUCoreHour: base.CPUCoreHour, BaseMemoryGiBHour: base.MemoryGiBHour, SettingsVersion: version}
	out := base
	discount := v.DefaultDiscountPercent
	for _, c := range v.Clusters {
		if c.Cluster != cluster {
			continue
		}
		if c.DiscountPercent != nil {
			discount = c.DiscountPercent
		}
		if c.CPUCoreHourUSD != nil {
			out.CPUCoreHour, adj.FixedCPU = *c.CPUCoreHourUSD, true
		}
		if c.MemoryGiBHourUSD != nil {
			out.MemoryGiBHour, adj.FixedMemory = *c.MemoryGiBHourUSD, true
		}
	}
	if discount != nil && *discount > 0 {
		adj.DiscountPercent = *discount
		f := 1 - *discount/100
		out.CPUCoreHour *= f
		out.MemoryGiBHour *= f
	}
	if adj.DiscountPercent == 0 && !adj.FixedCPU && !adj.FixedMemory {
		return base
	}
	out.Adjustment = &adj
	return out
}

// Version returns the content version of a cluster configuration ("c-" + 16 hex characters of
// a SHA-256 over everything but generatedAt), so the ETag changes only when the content does.
func Version(c hubapi.ClusterConfig) string {
	c.ConfigVersion, c.GeneratedAt = "", time.Time{}
	c.Policies = slices.Clone(c.Policies)
	sort.SliceStable(c.Policies, func(i, j int) bool { return c.Policies[i].Name < c.Policies[j].Name })
	c.Approvals = slices.Clone(c.Approvals)
	sort.SliceStable(c.Approvals, func(i, j int) bool { return c.Approvals[i].ID < c.Approvals[j].ID })
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return "c-" + hex.EncodeToString(h[:8])
}
