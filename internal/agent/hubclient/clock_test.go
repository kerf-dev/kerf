package hubclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHubClock(t *testing.T) {
	hubTime := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", hubTime.Format(http.TimeFormat))
		_, _ = w.Write([]byte(`{"accepted":0}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "c1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d, l := c.HubClock(); !d.IsZero() || !l.IsZero() {
		t.Error("clock before a response")
	}
	if err := c.ReportWorkloadStatus(context.Background(), WorkloadStatusBatch{Checks: []ReadinessCheck{{ID: "agent.version", Status: "pass"}}}); err != nil {
		t.Fatal(err)
	}
	d, l := c.HubClock()
	if !d.Equal(hubTime) || time.Since(l) > time.Minute {
		t.Errorf("clock %v %v", d, l)
	}
}
