package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

func base() Recommender {
	return Recommender{Config: recommender.DefaultConfig(), Interval: 24 * time.Hour, Retention: 90 * 24 * time.Hour}
}

func f(v float64) *float64 { return &v }

func TestRecommenderRoundTrip(t *testing.T) {
	b := base()
	s := RecommenderSettings(b)
	if s.Interval != "24h0m0s" || s.Retention != "2160h0m0s" || len(s.Profiles) != 3 || s.Profiles[0].Name != hubapi.ProfileDev {
		t.Fatalf("RecommenderSettings = %+v", s)
	}
	got, errs := ApplyRecommender(b, s)
	if errs != nil {
		t.Fatal(errs)
	}
	if got.Interval != b.Interval || got.Config.Profiles[hubapi.ProfileProduction] != b.Config.Profiles[hubapi.ProfileProduction] {
		t.Errorf("round trip changed the config: %+v", got)
	}
	s.Interval = "5m"
	s.Profiles[1].Window = "336h"
	s.Profiles[1].CPUPercentile = 0.9
	s.Profiles[1].MemoryPeakWindow = "24h"
	got, errs = ApplyRecommender(b, s)
	if errs != nil {
		t.Fatal(errs)
	}
	p := got.Config.Profiles[hubapi.ProfileProduction]
	if got.Interval != 5*time.Minute || p.CPUPercentile != 0.9 || p.MemoryPeakWindow != 24*time.Hour {
		t.Errorf("applied = %+v %+v", got.Interval, p)
	}
	if b.Config.Profiles[hubapi.ProfileProduction].CPUPercentile != 0.95 {
		t.Error("base was modified")
	}
	if RecommenderSettings(got).Profiles[1].MemoryPeakWindow != "24h0m0s" {
		t.Error("peak window not rendered")
	}
}

func TestApplyRecommenderErrors(t *testing.T) {
	good := RecommenderSettings(base())
	for name, tc := range map[string]struct {
		mut  func(*hubapi.RecommenderSettings)
		want string
	}{
		"interval syntax":   {func(s *hubapi.RecommenderSettings) { s.Interval = "soon" }, "interval: invalid duration"},
		"interval small":    {func(s *hubapi.RecommenderSettings) { s.Interval = "10s" }, "interval: 10s is out of"},
		"retention small":   {func(s *hubapi.RecommenderSettings) { s.Retention = "1h" }, "retention: 1h is below"},
		"unknown profile":   {func(s *hubapi.RecommenderSettings) { s.Profiles[0].Name = "custom" }, "unknown profile"},
		"duplicate profile": {func(s *hubapi.RecommenderSettings) { s.Profiles[0].Name = hubapi.ProfileProduction }, "duplicate profile"},
		"missing profile":   {func(s *hubapi.RecommenderSettings) { s.Profiles = s.Profiles[:2] }, "is missing"},
		"percentile":        {func(s *hubapi.RecommenderSettings) { s.Profiles[0].CPUPercentile = 1.5 }, "cpuPercentile: 1.5 is out of"},
		"headroom":          {func(s *hubapi.RecommenderSettings) { s.Profiles[0].MemoryBuffer = -1 }, "memoryBuffer: -1 is out of"},
		"window":            {func(s *hubapi.RecommenderSettings) { s.Profiles[0].Window = "3000h" }, "window: 3000h is out of"},
	} {
		t.Run(name, func(t *testing.T) {
			s := good
			s.Profiles = append([]hubapi.RecommenderProfileSettings(nil), good.Profiles...)
			tc.mut(&s)
			got, errs := ApplyRecommender(base(), s)
			if !strings.Contains(strings.Join(errs, "; "), tc.want) {
				t.Errorf("errors %v, want %q", errs, tc.want)
			}
			if got.Interval != base().Interval {
				t.Error("base not returned on error")
			}
		})
	}
	// Config.Validate catches what the field checks do not (window shorter than the step).
	b := base()
	b.Config.Step = 48 * time.Hour
	s := RecommenderSettings(base())
	s.Profiles[0].Window = "24h"
	if _, errs := ApplyRecommender(b, s); len(errs) == 0 {
		t.Error("window below step accepted")
	}
}

func TestValidatePricing(t *testing.T) {
	known := func(c string) bool { return c == "a" || c == "b" }
	ok := hubapi.PricingSettings{DefaultDiscountPercent: f(10), Clusters: []hubapi.ClusterPricingAdjustment{
		{Cluster: "a", DiscountPercent: f(28)}, {Cluster: "b", CPUCoreHourUSD: f(0.031), MemoryGiBHourUSD: f(0.004)},
	}}
	if errs := ValidatePricing(ok, known); errs != nil {
		t.Fatal(errs)
	}
	bad := hubapi.PricingSettings{DefaultDiscountPercent: f(100), Clusters: []hubapi.ClusterPricingAdjustment{
		{Cluster: "a", DiscountPercent: f(-1)}, {Cluster: "a"}, {Cluster: "Bad_Name"}, {Cluster: "zzz", CPUCoreHourUSD: f(-2)},
	}}
	got := strings.Join(ValidatePricing(bad, known), "; ")
	for _, want := range []string{"defaultDiscountPercent: 100", "clusters[0].discountPercent: -1", "duplicate cluster", "invalid cluster name", "unknown cluster \"zzz\"", "cpuCoreHourUSD: must not be negative"} {
		if !strings.Contains(got, want) {
			t.Errorf("errors %q lack %q", got, want)
		}
	}
}

func TestAdjustPricing(t *testing.T) {
	metrics := recommender.Pricing{CPUCoreHour: 0.04, MemoryGiBHour: 0.005, Source: recommender.PricingSourceMetrics, Nodes: 3}
	v := hubapi.PricingSettings{DefaultDiscountPercent: f(10), Clusters: []hubapi.ClusterPricingAdjustment{
		{Cluster: "a", DiscountPercent: f(25)},
		{Cluster: "b", CPUCoreHourUSD: f(0.03)},
		{Cluster: "c", DiscountPercent: f(0)},
	}}
	near := func(a, b float64) bool { d := a - b; return d < 1e-12 && d > -1e-12 }
	a := AdjustPricing(v, 3, "a", metrics)
	if !near(a.CPUCoreHour, 0.03) || !near(a.MemoryGiBHour, 0.00375) || a.Adjustment == nil || a.Adjustment.DiscountPercent != 25 ||
		a.Adjustment.SettingsVersion != 3 || a.Source != recommender.PricingSourceMetrics || a.Nodes != 3 {
		t.Errorf("a = %+v %+v", a, a.Adjustment)
	}
	if !reflect.DeepEqual(a.Base(), metrics) {
		t.Errorf("Base() = %+v", a.Base())
	}
	// The rate sources survive an adjustment and a re-adjustment.
	withSources := metrics
	withSources.Sources = []hubapi.PricingRateSource{{Source: hubapi.RateSourceCUR, Nodes: 3}}
	if again := AdjustPricing(v, 3, "a", AdjustPricing(v, 3, "a", withSources)); !reflect.DeepEqual(again.Sources, withSources.Sources) {
		t.Errorf("sources after re-adjustment: %+v", again.Sources)
	}
	// Adjusting adjusted rates starts from the base again.
	if again := AdjustPricing(v, 3, "a", a); !near(again.CPUCoreHour, 0.03) {
		t.Errorf("double adjustment: %+v", again)
	}
	b := AdjustPricing(v, 3, "b", metrics)
	if !near(b.CPUCoreHour, 0.027) || !near(b.MemoryGiBHour, 0.0045) || !b.Adjustment.FixedCPU || b.Adjustment.FixedMemory {
		t.Errorf("b = %+v %+v", b, b.Adjustment)
	}
	if c := AdjustPricing(v, 3, "c", metrics); c.Adjustment != nil || !reflect.DeepEqual(c, metrics) {
		t.Errorf("zero discount must not adjust: %+v", c)
	}
	if d := AdjustPricing(hubapi.PricingSettings{}, 0, "d", metrics); !reflect.DeepEqual(d, metrics) {
		t.Errorf("no settings: %+v", d)
	}
	mem := AdjustPricing(hubapi.PricingSettings{Clusters: []hubapi.ClusterPricingAdjustment{{Cluster: "m", MemoryGiBHourUSD: f(0.001)}}}, 1, "m", metrics)
	if mem.MemoryGiBHour != 0.001 || mem.CPUCoreHour != 0.04 || !mem.Adjustment.FixedMemory {
		t.Errorf("fixed memory: %+v", mem)
	}
}

func TestVersion(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	c := hubapi.ClusterConfig{
		Cluster: "c1", PolicySource: hubapi.PolicySourceBoth, GeneratedAt: now,
		Policies: []hubapi.ConfigPolicy{
			{ID: "2", Name: "b", Type: v1alpha1.HubPolicyTypePolicy, Version: 1, Priority: 5},
			{ID: "1", Name: "a", Type: v1alpha1.HubPolicyTypePolicy, Version: 1, Priority: 5},
		},
		Approvals: []hubapi.ConfigApproval{{ID: "y"}, {ID: "x"}},
	}
	v := Version(c)
	if !strings.HasPrefix(v, "c-") || len(v) != 18 {
		t.Fatalf("Version = %q", v)
	}
	same := c
	same.GeneratedAt = now.Add(time.Hour)
	same.ConfigVersion = "c-old"
	same.Policies = []hubapi.ConfigPolicy{c.Policies[1], c.Policies[0]}
	same.Approvals = []hubapi.ConfigApproval{c.Approvals[1], c.Approvals[0]}
	if Version(same) != v {
		t.Error("version depends on order or generatedAt")
	}
	if c.Policies[0].Name != "b" {
		t.Error("Version reordered its input")
	}
	changed := c
	changed.Flags.Paused = true
	if Version(changed) == v {
		t.Error("pause did not change the version")
	}
	changed = c
	changed.Policies = []hubapi.ConfigPolicy{c.Policies[0], {ID: "1", Name: "a", Type: v1alpha1.HubPolicyTypePolicy, Version: 2, Priority: 5}}
	if Version(changed) == v {
		t.Error("policy version did not change the version")
	}
}
