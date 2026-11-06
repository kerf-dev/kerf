package report

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
	"github.com/kerf-dev/kerf/tools/synth/synth"
)

var update = flag.Bool("update", false, "rewrite testdata/golden-*.json")

// hubRecommendations runs the recommender the way the hub does (the default configuration, the
// percentile strategy, no binding) over the fixtures.
func hubRecommendations(t *testing.T, fxs []*synth.Fixture, profile string) *recommender.ClusterResult {
	t.Helper()
	cfg := recommender.DefaultConfig()
	cfg.DefaultSettings.Profile = profile
	cluster := fxs[0].Cluster
	q := recommender.Queries{Cluster: cluster, RateWindow: cfg.RateWindow}
	fq := newFixtureQuerier(t, q, cfg, cfg.Profiles[profile].Window, fxs)
	eng := &recommender.Engine{
		Loader: &recommender.Source{Q: fq, Cfg: cfg}, Cfg: cfg, Strategy: recommender.PercentileStrategy{},
		ProfilesInUse: func(string) []string { return []string{profile} },
	}
	res, err := eng.RunCluster(context.Background(), cluster, fixtureEnd(fxs), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func reportFor(t *testing.T, fxs []*synth.Fixture, profile string) (*Report, *fixtureQuerier) {
	t.Helper()
	cfg := recommender.DefaultConfig()
	cluster := fxs[0].Cluster
	// The report reads through its own matcher: label cluster = the cluster name, like the hub's default binding.
	m := recommender.Queries{Cluster: cluster, RateWindow: cfg.RateWindow}
	fq := newFixtureQuerier(t, m, cfg, cfg.Profiles[profile].Window, fxs)
	rep, err := Run(context.Background(), Options{
		MetricsURL: "http://metrics.invalid", ClusterLabel: "cluster", ClusterValue: cluster, Profile: profile, Now: fixtureEnd(fxs),
	}, Deps{Querier: fq})
	if err != nil {
		t.Fatal(err)
	}
	return rep, fq
}

// TestReportMatchesHub is the golden test of the acceptance criteria: for the same history and
// profile, the report equals the recommendations of the hub's recommender.
func TestReportMatchesHub(t *testing.T) {
	fxs := loadFixtures(t)
	for _, profile := range []string{hubapi.ProfileDev, hubapi.ProfileProduction, hubapi.ProfileHighAvailability} {
		t.Run(profile, func(t *testing.T) {
			want := hubRecommendations(t, fxs, profile)
			got, _ := reportFor(t, fxs, profile)
			if len(want.Results) == 0 {
				t.Fatal("the hub recommender produced nothing from the fixtures")
			}
			if len(got.Rows) != len(want.Results) {
				t.Fatalf("report has %d workloads, hub %d", len(got.Rows), len(want.Results))
			}
			byName := map[hubapi.WorkloadRef]recommender.Result{}
			for _, r := range want.Results {
				byName[r.Recommendation.Workload] = r
			}
			total := 0.0
			for i, row := range got.Rows {
				ref := hubapi.WorkloadRef{Namespace: row.Namespace, Kind: row.Kind, Name: row.Workload}
				w, ok := byName[ref]
				if !ok {
					t.Fatalf("%v is not in the hub's result", ref)
				}
				rec := w.Recommendation
				if !reflect.DeepEqual(row.Containers, rec.Containers) {
					t.Errorf("%v containers differ:\n report %+v\n hub    %+v", ref, row.Containers, rec.Containers)
				}
				if row.MonthlySavingsUSD != rec.MonthlySavingsUSD || row.Confidence != rec.Confidence ||
					!reflect.DeepEqual(row.RiskFlags, rec.RiskFlags) || !reflect.DeepEqual(row.HPA, rec.HPA) || row.Replicas != rec.AverageReplicas {
					t.Errorf("%v: saving/confidence/flags/hpa differ: report %+v, hub %+v", ref, row, rec)
				}
				total += rec.MonthlySavingsUSD.Total
				if i > 0 && got.Rows[i-1].MonthlySavingsUSD.Total < row.MonthlySavingsUSD.Total {
					t.Errorf("rows are not sorted by monthly saving at %d", i)
				}
			}
			if d := got.Totals.MonthlySavingsUSD - total; d > 1e-6 || d < -1e-6 {
				t.Errorf("total saving %v, want %v", got.Totals.MonthlySavingsUSD, total)
			}
			if got.Pricing.Source != PriceSourceDefault {
				t.Errorf("pricing source = %q, want %q (the fixtures have no price series)", got.Pricing.Source, PriceSourceDefault)
			}
		})
	}
}

// goldenRow is what the golden file records per workload.
type goldenRow struct {
	Workload   string                  `json:"workload"`
	Saving     float64                 `json:"monthlySavingUSD"`
	Confidence float64                 `json:"confidence"`
	Flags      []string                `json:"riskFlags"`
	Containers []goldenContainer       `json:"containers"`
	HPA        *hubapi.HPACoordination `json:"hpa,omitempty"`
}

type goldenContainer struct {
	Name        string           `json:"name"`
	Current     hubapi.Resources `json:"current"`
	Recommended hubapi.Resources `json:"recommended"`
	Flags       []string         `json:"riskFlags,omitempty"`
}

// TestGoldenProduction pins the production profile's output on testdata/series, so a change of the
// recommender that moves a recommendation is a visible diff (go test ./internal/report -update).
func TestGoldenProduction(t *testing.T) {
	fxs := loadFixtures(t)
	got, _ := reportFor(t, fxs, hubapi.ProfileProduction)
	var rows []goldenRow
	for _, r := range got.Rows {
		g := goldenRow{Workload: r.Namespace + "/" + r.Kind + "/" + r.Workload, Saving: r.MonthlySavingsUSD.Total, Confidence: r.Confidence, Flags: r.RiskFlags, HPA: r.HPA}
		for _, c := range r.Containers {
			g.Containers = append(g.Containers, goldenContainer{Name: c.Name, Current: c.Current, Recommended: c.Recommended, Flags: c.RiskFlags})
		}
		rows = append(rows, g)
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const path = "testdata/golden-production.json"
	if *update {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (create it with: go test ./internal/report -update)", err)
	}
	if !bytes.Equal(want, b) {
		t.Errorf("output differs from %s; run go test ./internal/report -update and review the diff", path)
	}
}

// TestFixtureExpectations checks the report against what the fixtures say a correct recommender
// does (expect.riskFlags, expect.cpu), so the golden file is not only self-referential.
func TestFixtureExpectations(t *testing.T) {
	fxs := loadFixtures(t)
	got, _ := reportFor(t, fxs, hubapi.ProfileProduction)
	rows := map[string]Row{}
	for _, r := range got.Rows {
		rows[r.Workload] = r
	}
	for _, fx := range fxs {
		if fx.Expect == nil {
			continue
		}
		r, ok := rows[fx.Workload]
		if !ok {
			if len(fx.Expect.RiskFlags) > 0 {
				t.Errorf("%s has no row but expects %v", fx.Workload, fx.Expect.RiskFlags)
			}
			continue
		}
		for _, f := range fx.Expect.RiskFlags {
			found := false
			for _, g := range r.RiskFlags {
				found = found || g == f
			}
			if !found {
				t.Errorf("%s: missing risk flag %q, has %v", fx.Workload, f, r.RiskFlags)
			}
		}
		switch fx.Expect.CPU {
		case "decrease":
			if r.CPURecommendedCores >= r.CPURequestCores {
				t.Errorf("%s: cpu %v -> %v, want a decrease", fx.Workload, r.CPURequestCores, r.CPURecommendedCores)
			}
		case "increase":
			if r.CPURecommendedCores <= r.CPURequestCores {
				t.Errorf("%s: cpu %v -> %v, want an increase", fx.Workload, r.CPURequestCores, r.CPURecommendedCores)
			}
		}
	}
}

func TestEndTime(t *testing.T) {
	fxs := loadFixtures(t)
	got, _ := reportFor(t, fxs, hubapi.ProfileProduction)
	if !got.GeneratedAt.Equal(fixtureEnd(fxs)) || got.Window != (14*24*time.Hour).String() || !strings.HasPrefix(got.Profile, "prod") {
		t.Errorf("generatedAt %v window %s profile %s", got.GeneratedAt, got.Window, got.Profile)
	}
}
