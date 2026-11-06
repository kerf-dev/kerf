// Package charts holds checks on the component Helm charts.
package charts

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChartCopiesInSync fails when a copy inside a chart drifts from its source (run
// `make charts-sync`): Helm can only read files inside the chart directory.
func TestChartCopiesInSync(t *testing.T) {
	for _, tc := range []struct{ src, dst string }{
		{"../../api/crds/*.yaml", "agent/files/crds"},
		{"../../api/crds/*.yaml", "webhook/files/crds"},
		{"../../dashboards/*.json", "hub/files/dashboards"},
	} {
		t.Run(tc.dst, func(t *testing.T) {
			src, _ := filepath.Glob(tc.src)
			dst, _ := filepath.Glob(filepath.Join(tc.dst, "*"+filepath.Ext(tc.src)))
			if len(src) == 0 || len(src) != len(dst) {
				t.Fatalf("%s has %d files, %s has %d", tc.src, len(src), tc.dst, len(dst))
			}
			for _, s := range src {
				a, _ := os.ReadFile(s)
				b, err := os.ReadFile(filepath.Join(tc.dst, filepath.Base(s)))
				if err != nil || !bytes.Equal(a, b) {
					t.Errorf("%s differs from %s (run make charts-sync)", filepath.Join(tc.dst, filepath.Base(s)), s)
				}
			}
		})
	}
}

// TestCommonTemplatesInSync fails when a chart's templates/_common.tpl is not the hub master with
// the chart's own template prefix.
func TestCommonTemplatesInSync(t *testing.T) {
	master, err := os.ReadFile("hub/templates/_common.tpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"web", "pricing", "agent", "webhook"} {
		got, err := os.ReadFile(filepath.Join(c, "templates/_common.tpl"))
		want := strings.ReplaceAll(string(master), `"hub.`, `"`+c+`.`)
		if err != nil || string(got) != want {
			t.Errorf("%s/templates/_common.tpl differs from the hub master (run make charts-sync)", c)
		}
	}
}

// TestNoCRDsDirectory fails when a chart ships crds/: Helm would install those once and never
// upgrade them, and they would clash with the CRD templates.
func TestNoCRDsDirectory(t *testing.T) {
	dirs, _ := filepath.Glob("*/crds")
	if len(dirs) > 0 {
		t.Errorf("charts must not have a crds/ directory (CRDs are templates): %v", dirs)
	}
}
