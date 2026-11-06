package controller

import (
	"context"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// TestReporterCapacity: a capacity report goes with the next batch (even without changed
// statuses) exactly once.
func TestReporterCapacity(t *testing.T) {
	h := &batchHub{}
	rep := NewReporter(h, "test", 10, nil)
	ctx := context.Background()
	c := &hubclient.CapacityReport{ObservedAt: time.Now(), NodePools: []hubclient.CapacityNodePool{}, Nodes: []hubclient.CapacityNode{{Name: "n1"}}, PodGroups: []hubclient.CapacityPodGroup{}}
	rep.SetCapacity(c)
	if err := rep.Flush(ctx, false); err != nil {
		t.Fatal(err)
	}
	if len(h.batches) != 1 || h.batches[0].Capacity != c {
		t.Fatalf("batches %+v", h.batches)
	}
	if err := rep.Flush(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, b := range h.batches[1:] {
		if b.Capacity != nil {
			t.Errorf("capacity sent twice: %+v", b)
		}
	}
}
