package hubclient_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubstub"
)

func TestClientAgainstStub(t *testing.T) {
	stub := hubstub.New()
	stub.AddToken("tok-1", "ro-spoke-1")
	srv := httptest.NewServer(stub.Handler())
	defer srv.Close()

	var recs []hubclient.WorkloadRecommendation
	for i := range 5 {
		recs = append(recs, hubclient.WorkloadRecommendation{
			Workload:   hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: fmt.Sprintf("w%d", i)},
			Containers: []hubclient.ContainerRecommendation{{Name: "app"}},
		})
	}
	stub.SetRecommendations("ro-spoke-1", hubstub.Normalize(recs, time.Now()))
	ctx := context.Background()

	c, err := hubclient.New(srv.URL, "ro-spoke-1", hubclient.StaticToken("tok-1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, etag, err := c.ListRecommendations(ctx, "")
	if err != nil || len(got) != 5 || etag == "" {
		t.Fatalf("list: %d items, etag %q, err %v", len(got), etag, err)
	}
	if _, _, err := c.ListRecommendations(ctx, etag); !errors.Is(err, hubclient.ErrNotModified) {
		t.Fatalf("expected 304, got %v", err)
	}

	// Wrong cluster and wrong token.
	other, _ := hubclient.New(srv.URL, "ro-spoke-2", hubclient.StaticToken("tok-1"), nil)
	var apiErr *hubclient.APIError
	if _, _, err := other.ListRecommendations(ctx, ""); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden || apiErr.Problem.Code != "cluster_forbidden" {
		t.Fatalf("expected 403 cluster_forbidden, got %v", err)
	}
	bad, _ := hubclient.New(srv.URL, "ro-spoke-1", hubclient.StaticToken("nope"), nil)
	if _, _, err := bad.ListRecommendations(ctx, ""); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %v", err)
	}

	// Events are idempotent by id.
	ev := hubclient.Event{ID: "6f1c2a8e-0d7b-4c55-a0b1-8f9e2f0d1c3a", Type: "applied", Time: time.Now(), Reason: "RecommendationApplied",
		Workload: hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: "w0"}}
	if res, err := c.ReportEvents(ctx, []hubclient.Event{ev}); err != nil || res.Accepted != 1 {
		t.Fatalf("events: %+v %v", res, err)
	}
	if res, err := c.ReportEvents(ctx, []hubclient.Event{ev}); err != nil || res.Duplicates != 1 {
		t.Fatalf("duplicate events: %+v %v", res, err)
	}

	// Statuses are batched by 1000.
	var sts []hubclient.WorkloadStatus
	for i := range 1500 {
		sts = append(sts, hubclient.WorkloadStatus{Workload: hubclient.WorkloadRef{Namespace: "default", Kind: "Deployment", Name: fmt.Sprintf("s%d", i)},
			Modes: hubclient.EffectiveModes{CPU: "audit", Memory: "audit"}, Phase: "Pending"})
	}
	if err := c.ReportWorkloadStatus(ctx, "test", sts); err != nil {
		t.Fatal(err)
	}
	if n := len(stub.Statuses("ro-spoke-1")); n != 1500 || stub.Requests("reportWorkloadStatus") != 2 {
		t.Fatalf("statuses %d in %d requests", n, stub.Requests("reportWorkloadStatus"))
	}

	stub.SetDown(true)
	if _, _, err := c.ListRecommendations(ctx, ""); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %v", err)
	}
}

func TestPagination(t *testing.T) {
	stub := hubstub.New()
	srv := httptest.NewServer(stub.Handler())
	defer srv.Close()
	var recs []hubclient.WorkloadRecommendation
	for i := range 7 {
		recs = append(recs, hubclient.WorkloadRecommendation{Workload: hubclient.WorkloadRef{Namespace: "n", Kind: "Deployment", Name: fmt.Sprint(i)}})
	}
	stub.SetRecommendations("c", hubstub.Normalize(recs, time.Now()))
	// The client does not send limit, but must follow continue tokens when the hub pages anyway.
	c, _ := hubclient.New(srv.URL+"/", "c", nil, nil)
	got, _, err := c.ListRecommendations(context.Background(), "")
	if err != nil || len(got) != 7 {
		t.Fatalf("got %d, %v", len(got), err)
	}
}
