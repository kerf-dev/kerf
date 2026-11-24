package capacity

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestPoolEvents(t *testing.T) {
	h0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	h1 := h0.Add(time.Hour)
	h3 := h0.Add(3 * time.Hour)
	h4 := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
	got := PoolEvents(map[time.Time][]string{
		h0: {"a"}, h1: {"a", "b"}, h3: {"a", "b"}, h4: {"b"},
	})
	want := []Event{
		{Day: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Type: "pool_added", Count: 1, NodePool: "b"},
		{Day: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Type: "pool_removed", Count: 1, NodePool: "a"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPoolEventsFirstHour(t *testing.T) {
	h := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if got := PoolEvents(map[time.Time][]string{h: {"a", "b"}}); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func insertAudit(t *testing.T, d *DB, cluster, typ, name string, at time.Time) {
	t.Helper()
	_, err := d.Pool.Exec(context.Background(), `
		INSERT INTO audit_events (id, cluster, namespace, workload_kind, workload_name, type, reason, time)
		VALUES ($1, $2, 'ns', 'Deployment', $3, $4, 'Resized', $5)`,
		uuid.New(), cluster, name, typ, at)
	if err != nil {
		t.Fatal(err)
	}
}

func TestChangeEvents(t *testing.T) {
	d := newTestDB(t)
	addCluster(t, d, "c1")
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		insertAudit(t, d, "c1", "applied", fmt.Sprintf("w%d", i), at)
	}
	insertAudit(t, d, "c1", "applied", "w0", at.Add(time.Minute))
	insertAudit(t, d, "c1", "skipped", "w1", at)
	from, to := at.Add(-24*time.Hour), at.Add(24*time.Hour)

	got, err := d.ChangeEvents(ctx, "c1", from, to, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != "applied" || got[0].Count != 7 || len(got[0].Top) != 5 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Top[0].Name != "w0" || got[0].Top[1].Name != "w1" {
		t.Fatalf("top order: %+v", got[0].Top)
	}
	if !got[0].Day.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("day %v", got[0].Day)
	}

	got, err = d.ChangeEvents(ctx, "c1", from, to, []hubapi.WorkloadRef{{Namespace: "ns", Kind: "Deployment", Name: "w0"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Count != 2 || len(got[0].Top) != 1 {
		t.Fatalf("filtered: %+v", got)
	}

	got, err = d.ChangeEvents(ctx, "c1", from, to, []hubapi.WorkloadRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("empty filter: %+v", got)
	}
}

func TestPoolWorkloads(t *testing.T) {
	w := func(n string) *hubapi.WorkloadRef {
		return &hubapi.WorkloadRef{Namespace: "ns", Kind: "Deployment", Name: n}
	}
	rep := hubapi.CapacityReport{PodGroups: []hubapi.CapacityPodGroup{
		{NodePool: "a", Workload: w("x")},
		{NodePool: "a", Workload: w("x")},
		{NodePool: "a", Workload: w("y")},
		{NodePool: "a"},
		{NodePool: "b", Workload: w("z")},
	}}
	got := PoolWorkloads(rep, "a")
	want := []hubapi.WorkloadRef{*w("x"), *w("y")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
