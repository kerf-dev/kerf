package capacity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// newTestDB creates a fresh migrated database (dropped at the end) when KERF_TEST_DATABASE_URL is
// set to a superuser URL; otherwise the test is skipped.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	base := os.Getenv("KERF_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("KERF_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "kerf_capacity_test_" + hex.EncodeToString(b)
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
		admin.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Migrate(ctx, os.DirFS("../../../db/migrations")); err != nil {
		t.Fatal(err)
	}
	return &DB{Pool: st.Pool}
}

// addCluster inserts a clusters row so history rows can reference it.
func addCluster(t *testing.T, d *DB, name string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(), `INSERT INTO clusters (name) VALUES ($1)`, name); err != nil {
		t.Fatal(err)
	}
}

func totalRow(nodes int) HourRow {
	return HourRow{Nodes: nodes, CPURequested: float64(nodes), CPUAllocatable: float64(nodes) * 2}
}

func TestSaveHourIdempotent(t *testing.T) {
	d := newTestDB(t)
	addCluster(t, d, "c1")
	ctx := context.Background()
	h := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	if err := d.SaveHour(ctx, "c1", h, []HourRow{totalRow(2), {NodePool: "a", Nodes: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveHour(ctx, "c1", h, []HourRow{totalRow(5), {NodePool: "a", Nodes: 5}}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM capacity_hourly`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	pts, err := d.History(ctx, "c1", "a", h.Add(-time.Hour), h.Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].Nodes != 5 || !pts[0].Time.Equal(h.Truncate(time.Hour)) {
		t.Fatalf("points = %+v", pts)
	}
}

func seedDays(t *testing.T, d *DB) (day1, day2 time.Time) {
	t.Helper()
	day1 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day2 = day1.Add(24 * time.Hour)
	for i, n := range []int{2, 3, 4} {
		if err := d.SaveHour(context.Background(), "c1", day1.Add(time.Duration(i)*time.Hour), []HourRow{totalRow(n), {NodePool: "a", Nodes: n}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SaveHour(context.Background(), "c1", day2.Add(time.Hour), []HourRow{totalRow(10), {NodePool: "b", Nodes: 10}}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestHistoryDaily(t *testing.T) {
	d := newTestDB(t)
	addCluster(t, d, "c1")
	day1, day2 := seedDays(t, d)
	ctx := context.Background()
	from, to := day1.Add(-time.Hour), day2.Add(24*time.Hour)

	daily, err := d.History(ctx, "c1", "", from, to, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 2 || !daily[0].Time.Equal(day1) || daily[0].Nodes != 3 || !daily[1].Time.Equal(day2) || daily[1].Nodes != 10 {
		t.Fatalf("daily = %+v", daily)
	}
	hourly, err := d.History(ctx, "c1", "", from, to, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hourly) != 4 {
		t.Fatalf("hourly = %+v", hourly)
	}
}

func TestHistoryPoolFilter(t *testing.T) {
	d := newTestDB(t)
	addCluster(t, d, "c1")
	day1, day2 := seedDays(t, d)
	ctx := context.Background()
	from, to := day1.Add(-time.Hour), day2.Add(24*time.Hour)

	a, err := d.History(ctx, "c1", "a", from, to, false)
	if err != nil || len(a) != 3 {
		t.Fatalf("a = %+v, %v", a, err)
	}
	none, err := d.History(ctx, "c1", "nope", from, to, false)
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("unknown pool = %#v, %v", none, err)
	}
	pools, err := d.Pools(ctx, "c1", from, to)
	if err != nil || len(pools) != 2 || pools[0] != "a" || pools[1] != "b" {
		t.Fatalf("pools = %v, %v", pools, err)
	}
	ph, err := d.PoolHours(ctx, "c1", from, to)
	if err != nil || len(ph) != 4 || len(ph[day2.Add(time.Hour)]) != 1 || ph[day2.Add(time.Hour)][0] != "b" {
		t.Fatalf("poolHours = %v, %v", ph, err)
	}
	since, ok, err := d.Since(ctx, "c1")
	if err != nil || !ok || !since.Equal(day1) {
		t.Fatalf("since = %v %v %v", since, ok, err)
	}
	if _, ok, _ := d.Since(ctx, "other"); ok {
		t.Fatal("since for unknown cluster should be absent")
	}
	tr, err := d.AnyTruncated(ctx, "c1", from, to)
	if err != nil || tr {
		t.Fatalf("truncated = %v, %v", tr, err)
	}
	if err := d.SaveHour(ctx, "c1", day1, []HourRow{{Nodes: 1, Truncated: true}}); err != nil {
		t.Fatal(err)
	}
	if tr, err = d.AnyTruncated(ctx, "c1", from, to); err != nil || !tr {
		t.Fatalf("truncated = %v, %v", tr, err)
	}
}

func TestPruneHistory(t *testing.T) {
	d := newTestDB(t)
	addCluster(t, d, "c1")
	ctx := context.Background()
	now := time.Now().UTC()
	for _, ago := range []time.Duration{401 * 24 * time.Hour, 24 * time.Hour} {
		if err := d.SaveHour(ctx, "c1", now.Add(-ago), []HourRow{totalRow(1)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := d.PruneHistory(ctx, "c1", now.Add(-400*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned = %d, %v", n, err)
	}
}

func TestCascadeOnClusterDelete(t *testing.T) {
	d := newTestDB(t)
	addCluster(t, d, "c1")
	ctx := context.Background()
	h := time.Now().UTC().Truncate(time.Hour)
	if err := d.SaveHour(ctx, "c1", h, []HourRow{totalRow(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `DELETE FROM clusters WHERE name = 'c1'`); err != nil {
		t.Fatal(err)
	}
	pts, err := d.History(ctx, "c1", "", h.Add(-time.Hour), h.Add(time.Hour), false)
	if err != nil || len(pts) != 0 {
		t.Fatalf("points = %+v, %v", pts, err)
	}
}

func TestSnapshots(t *testing.T) {
	d := newTestDB(t)
	addCluster(t, d, "c1")
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	rep := hubapi.CapacityReport{}
	if err := d.Save(ctx, "c1", &rep, at); err != nil {
		t.Fatal(err)
	}
	got, bad, err := d.Snapshots(ctx)
	if err != nil || len(bad) != 0 || len(got) != 1 || got[0].Cluster != "c1" || !got[0].ReportedAt.Equal(at) {
		t.Fatalf("snapshots = %+v, %v", got, err)
	}
}
