package agent_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestChartCRDsInSync fails when deploy/charts/agent/crds drifts from api/crds (run
// `make agent-sync-crds`).
func TestChartCRDsInSync(t *testing.T) {
	src, _ := filepath.Glob("../../api/crds/*.yaml")
	dst, _ := filepath.Glob("../../deploy/charts/agent/crds/*.yaml")
	if len(src) == 0 || len(src) != len(dst) {
		t.Fatalf("api/crds has %d files, chart has %d", len(src), len(dst))
	}
	for _, s := range src {
		a, _ := os.ReadFile(s)
		b, err := os.ReadFile(filepath.Join("../../deploy/charts/agent/crds", filepath.Base(s)))
		if err != nil || !bytes.Equal(a, b) {
			t.Errorf("%s differs from the chart copy (run make agent-sync-crds)", filepath.Base(s))
		}
	}
}
