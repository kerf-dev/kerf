package controller

import (
	"context"
	"testing"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

type batchHub struct {
	batches []hubclient.WorkloadStatusBatch
}

func (h *batchHub) ListRecommendations(context.Context, string) ([]hubclient.WorkloadRecommendation, string, error) {
	return nil, "", nil
}
func (h *batchHub) ReportWorkloadStatus(_ context.Context, b hubclient.WorkloadStatusBatch) error {
	h.batches = append(h.batches, b)
	return nil
}
func (h *batchHub) ReportEvents(context.Context, []hubclient.Event) (hubclient.BatchResult, error) {
	return hubclient.BatchResult{}, nil
}

// TestReporterSelfChecks: a self-check run goes with the next batch (even without changed
// statuses), then only with full batches.
func TestReporterSelfChecks(t *testing.T) {
	h := &batchHub{}
	rep := NewReporter(h, "test", 10, nil)
	ctx := context.Background()
	if err := rep.Flush(ctx, false); err != nil || len(h.batches) != 0 {
		t.Fatalf("empty flush sent %d batches", len(h.batches))
	}
	rep.SetSelfChecks([]hubclient.AgentCapability{{Name: "resize", Granted: true}}, []hubclient.ReadinessCheck{{ID: "rbac.resize", Status: "pass"}})
	if err := rep.Flush(ctx, false); err != nil {
		t.Fatal(err)
	}
	if len(h.batches) != 1 || len(h.batches[0].Checks) != 1 || len(h.batches[0].Capabilities) != 1 || len(h.batches[0].Items) != 0 {
		t.Fatalf("batches %+v", h.batches)
	}
	rep.SetStatus(wkey{"ns", "Deployment", "a"}, hubclient.WorkloadStatus{Modes: hubclient.EffectiveModes{CPU: "off", Memory: "off"}})
	rep.SetStatus(wkey{"ns2", "Deployment", "b"}, hubclient.WorkloadStatus{Modes: hubclient.EffectiveModes{CPU: "audit", Memory: "off"}})
	if err := rep.Flush(ctx, false); err != nil {
		t.Fatal(err)
	}
	if b := h.batches[1]; b.Checks != nil || b.Capabilities != nil || len(b.Items) != 2 {
		t.Errorf("incremental batch resent the checks: %+v", b)
	}
	if err := rep.Flush(ctx, true); err != nil {
		t.Fatal(err)
	}
	if b := h.batches[2]; len(b.Checks) != 1 {
		t.Errorf("full batch without the checks: %+v", b)
	}
	n, ex := rep.Coverage()
	if n != 2 || len(ex) != 1 || ex[0] != "ns" {
		t.Errorf("coverage %d %v", n, ex)
	}
}

// TestReporterComponents: the components found by a self-check run go with the checks; an
// empty list is sent (no webhook), nil (unknown) is not.
func TestReporterComponents(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		in   []hubclient.AgentComponent
		want []hubclient.AgentComponent
	}{
		{"pods", []hubclient.AgentComponent{{Component: "webhook", Instance: "w-1", Version: "0.2.0"}}, []hubclient.AgentComponent{{Component: "webhook", Instance: "w-1", Version: "0.2.0"}}},
		{"none", []hubclient.AgentComponent{}, []hubclient.AgentComponent{}},
		{"unknown", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &batchHub{}
			rep := NewReporter(h, "test", 10, nil)
			rep.SetSelfCheckResult(nil, []hubclient.ReadinessCheck{{ID: "webhook", Status: "pass"}}, tt.in)
			if err := rep.Flush(ctx, false); err != nil || len(h.batches) != 1 {
				t.Fatalf("flush: %v, %d batches", err, len(h.batches))
			}
			got := h.batches[0].Components
			if (got == nil) != (tt.want == nil) || len(got) != len(tt.want) || (len(got) > 0 && got[0] != tt.want[0]) {
				t.Errorf("components %#v, want %#v", got, tt.want)
			}
		})
	}
}
