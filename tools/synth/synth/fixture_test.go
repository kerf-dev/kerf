package synth

import (
	"math"
	"path/filepath"
	"testing"
)

// TestFixturesUpToDate regenerates testdata/series and compares it with the committed files.
// Run `make synth-fixtures` after changing the generator or fleet-small.yaml.
func TestFixturesUpToDate(t *testing.T) {
	sc, err := LoadScenario(filepath.Join("..", "..", "..", FixtureScenario))
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(sc, Options{End: FixtureEnd, Days: FixtureDays, Clusters: []string{FixtureCluster}})
	if err != nil {
		t.Fatal(err)
	}
	cm := g.Clusters[0]
	files, _ := filepath.Glob(filepath.Join("..", "..", "..", "testdata", "series", "*.json"))
	if len(files) != len(cm.Spec.Workloads) {
		t.Fatalf("%d fixture files, %d explicit workloads in %s", len(files), len(cm.Spec.Workloads), FixtureScenario)
	}
	for i := range cm.Spec.Workloads {
		w := cm.Workloads[i]
		t.Run(w.Name, func(t *testing.T) {
			want, err := g.BuildFixture(g.Simulate(cm, w), FixtureBucket)
			if err != nil {
				t.Fatal(err)
			}
			got, err := LoadFixture(filepath.Join("..", "..", "..", "testdata", "series", w.Name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if got.Points != want.Points || got.Start != want.Start || len(got.Containers) != len(want.Containers) {
				t.Fatalf("shape differs: points %d/%d start %d/%d", got.Points, want.Points, got.Start, want.Start)
			}
			for ci, c := range want.Containers {
				gc := got.Containers[ci]
				if gc.Restarts != c.Restarts || gc.OOMKills != c.OOMKills {
					t.Errorf("%s restarts/ooms %d/%d, want %d/%d", c.Name, gc.Restarts, gc.OOMKills, c.Restarts, c.OOMKills)
				}
				if c.CPU == nil {
					continue
				}
				for name, pair := range map[string][2]Values{
					"cpu.max": {gc.CPU.Max, c.CPU.Max}, "memory.max": {gc.Memory.Max, c.Memory.Max}, "throttle": {gc.ThrottleRatio, c.ThrottleRatio},
				} {
					if !closeValues(pair[0], pair[1]) {
						t.Errorf("%s %s differs from the generator; run make synth-fixtures", c.Name, name)
					}
				}
			}
		})
	}
}

func closeValues(a, b Values) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.IsNaN(a[i]) != math.IsNaN(b[i]) {
			return false
		}
		if !math.IsNaN(a[i]) && math.Abs(a[i]-b[i]) > 1e-5*math.Max(1, math.Abs(b[i])) {
			return false
		}
	}
	return true
}
