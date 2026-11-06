package hubclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// TestReportCapacity: a batch with only a capacity report is sent, and the report goes with the
// first request of a split batch only.
func TestReportCapacity(t *testing.T) {
	var mu sync.Mutex
	var bodies []hubclient.WorkloadStatusBatch
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b hubclient.WorkloadStatusBatch
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"accepted":0}`))
	}))
	defer srv.Close()
	c, err := hubclient.New(srv.URL, "c1", hubclient.StaticToken("t"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rep := &hubclient.CapacityReport{ObservedAt: time.Now().UTC().Truncate(time.Second), NodePools: []hubclient.CapacityNodePool{},
		Nodes: []hubclient.CapacityNode{{Name: "n1"}}, PodGroups: []hubclient.CapacityPodGroup{}}
	if err := c.ReportWorkloadStatus(context.Background(), hubclient.WorkloadStatusBatch{Capacity: rep}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0].Capacity == nil || bodies[0].Capacity.Nodes[0].Name != "n1" {
		t.Fatalf("capacity-only batch: %+v", bodies)
	}
	var sts []hubclient.WorkloadStatus
	for i := range 1500 {
		sts = append(sts, hubclient.WorkloadStatus{Workload: hubclient.WorkloadRef{Namespace: "d", Kind: "Deployment", Name: fmt.Sprint(i)}})
	}
	bodies = nil
	if err := c.ReportWorkloadStatus(context.Background(), hubclient.WorkloadStatusBatch{Items: sts, Capacity: rep}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0].Capacity == nil || bodies[1].Capacity != nil {
		t.Fatalf("split batch: %d requests, capacity in first %v, second %v", len(bodies), bodies[0].Capacity != nil, len(bodies) > 1 && bodies[1].Capacity != nil)
	}
}
