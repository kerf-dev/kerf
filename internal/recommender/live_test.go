package recommender

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestLive runs the engine against a real Prometheus/VictoriaMetrics when RO_TEST_PROM_URL and
// RO_TEST_CLUSTER are set, e.g. RO_TEST_PROM_URL=http://localhost:18428 RO_TEST_CLUSTER=ro-spoke-1.
func TestLive(t *testing.T) {
	url, cluster := os.Getenv("RO_TEST_PROM_URL"), os.Getenv("RO_TEST_CLUSTER")
	if url == "" || cluster == "" {
		t.Skip("RO_TEST_PROM_URL / RO_TEST_CLUSTER not set")
	}
	cfg := DefaultConfig()
	e := &Engine{Loader: &Source{Q: NewPromClient(url, 2*time.Minute), Cfg: cfg}, Cfg: cfg, Strategy: PercentileStrategy{}}
	start := time.Now()
	res, err := e.RunCluster(context.Background(), cluster, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cluster %s: %d workloads, %d recommendations, pricing %+v in %s", cluster, len(res.Inventory), len(res.Results), res.Pricing, time.Since(start))
	if os.Getenv("RO_TEST_VERBOSE") != "" {
		for _, r := range res.Results {
			b, _ := json.MarshalIndent(r.Recommendation, "", "  ")
			t.Logf("%s", b)
		}
	}
}
