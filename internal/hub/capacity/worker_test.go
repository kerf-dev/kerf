package capacity

import (
	"context"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func tickFixture(t *testing.T) (*Worker, time.Time) {
	t.Helper()
	d := newTestDB(t)
	addCluster(t, d, "x")
	addCluster(t, d, "y")
	now := time.Date(2026, 3, 4, 14, 37, 10, 0, time.UTC)
	rep := hubapi.CapacityReport{NodePools: []hubapi.CapacityNodePool{{Name: "p1"}}}
	ctx := context.Background()
	if err := d.Save(ctx, "x", &rep, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(ctx, "y", &rep, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return &Worker{DB: d, Now: func() time.Time { return now }}, now
}

func countRows(t *testing.T, d *DB, cluster string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM capacity_hourly WHERE cluster = $1`, cluster).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTickFreshAndStale(t *testing.T) {
	w, now := tickFixture(t)
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, w.DB, "x"); n == 0 {
		t.Fatal("no rows for the fresh cluster")
	}
	if n := countRows(t, w.DB, "y"); n != 0 {
		t.Fatalf("stale cluster has %d rows", n)
	}
	var hour time.Time
	if err := w.DB.Pool.QueryRow(context.Background(), `SELECT min(hour) FROM capacity_hourly WHERE cluster = 'x'`).Scan(&hour); err != nil {
		t.Fatal(err)
	}
	if !hour.Equal(now.Truncate(time.Hour)) {
		t.Fatalf("hour = %v", hour)
	}
}

func TestTickTwiceSameHour(t *testing.T) {
	w, _ := tickFixture(t)
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	first := countRows(t, w.DB, "x")
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, w.DB, "x"); got != first {
		t.Fatalf("rows %d, then %d", first, got)
	}
}

func TestNextBoundary(t *testing.T) {
	d := func(h, m, s int) time.Time { return time.Date(2026, 3, 4, h, m, s, 0, time.UTC) }
	if got := nextRun(d(14, 37, 10)); !got.Equal(d(15, 1, 0)) {
		t.Fatalf("got %v", got)
	}
	if got := nextRun(d(14, 0, 30)); !got.Equal(d(14, 1, 0)) {
		t.Fatalf("got %v", got)
	}
}
