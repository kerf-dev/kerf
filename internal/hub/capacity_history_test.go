package hub

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/capacity"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

const historyPath = "/v1/clusters/c1/capacity/history"

func (e *testEnv) saveHour(cluster string, hour time.Time, rows ...capacity.HourRow) {
	e.t.Helper()
	if err := e.srv.capacityDB().SaveHour(e.ctx, cluster, hour, rows); err != nil {
		e.t.Fatal(err)
	}
}

func TestCapacityHistoryRange(t *testing.T) {
	e := newEnv(t)
	day := e.now.Truncate(24 * time.Hour).Add(-48 * time.Hour)
	for _, h := range []int{3, 5} {
		e.saveHour("c1", day.Add(time.Duration(h)*time.Hour),
			capacity.HourRow{Nodes: 2 * h, CPURequested: 1, CPUAllocatable: 4, MemRequested: 1, MemAllocatable: 8},
			capacity.HourRow{NodePool: "general", Nodes: h, CPURequested: 1, CPUAllocatable: 2, MemRequested: 1, MemAllocatable: 4})
	}

	if r := e.do(http.MethodGet, historyPath+"?range=bogus", tokUI, nil); r.code != http.StatusBadRequest {
		t.Errorf("range=bogus: status %d", r.code)
	}

	var def hubapi.CapacityHistory
	r := e.getJSON(historyPath, tokUI, &def)
	if def.Range != "30d" || len(def.Points) != 2 || def.Points[0].Nodes != 6 {
		t.Errorf("default: %+v", def)
	}
	if !strings.Contains(string(r.body), `"range":"30d"`) {
		t.Errorf("body %s", r.body)
	}

	var daily hubapi.CapacityHistory
	e.getJSON(historyPath+"?range=90d", tokUI, &daily)
	if len(daily.Points) != 1 || !daily.Points[0].Hour.Equal(day) || daily.Points[0].Nodes != 8 {
		t.Errorf("90d: %+v", daily.Points)
	}
	if daily.Since == nil || !daily.Since.Equal(day.Add(3*time.Hour)) || len(daily.NodePools) != 1 || daily.NodePools[0] != "general" {
		t.Errorf("90d since %v pools %v", daily.Since, daily.NodePools)
	}

	var pool hubapi.CapacityHistory
	e.getJSON(historyPath+"?nodePool=general", tokUI, &pool)
	if len(pool.Points) != 2 || pool.Points[0].Nodes != 3 {
		t.Errorf("pool: %+v", pool.Points)
	}
}

func TestCapacityHistoryUnknownCluster(t *testing.T) {
	e := newEnv(t)
	e.problem(e.do(http.MethodGet, "/v1/clusters/nope/capacity/history", tokUI, nil), http.StatusNotFound, "cluster_not_found")
}

func TestCapacityHistoryDeletedCluster(t *testing.T) {
	e := newEnv(t)
	e.saveHour("c2", e.now.Truncate(time.Hour), capacity.HourRow{Nodes: 1})
	var ok hubapi.CapacityHistory
	e.getJSON("/v1/clusters/c2/capacity/history", tokUI, &ok)
	if len(ok.Points) != 1 {
		t.Fatalf("before delete: %+v", ok)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE clusters SET deleted_at = now() WHERE name = 'c2'`); err != nil {
		t.Fatal(err)
	}
	e.problem(e.do(http.MethodGet, "/v1/clusters/c2/capacity/history", tokUI, nil), http.StatusNotFound, "cluster_not_found")
}

func TestCapacityHistoryGonePool(t *testing.T) {
	e := newEnv(t)
	e.saveHour("c1", e.now.Truncate(time.Hour),
		capacity.HourRow{Nodes: 2}, capacity.HourRow{NodePool: "general", Nodes: 2})
	r := e.do(http.MethodGet, historyPath+"?nodePool=old", tokUI, nil)
	if r.code != http.StatusOK || !strings.Contains(string(r.body), `"points":[]`) || !strings.Contains(string(r.body), `"events":[]`) {
		t.Fatalf("status %d %s", r.code, r.body)
	}
	var got hubapi.CapacityHistory
	e.getJSON(historyPath+"?nodePool=old", tokUI, &got)
	if len(got.NodePools) != 1 || got.NodePools[0] != "general" {
		t.Errorf("nodePools %v", got.NodePools)
	}
}

func TestCapacityHistoryNoData(t *testing.T) {
	e := newEnv(t)
	r := e.do(http.MethodGet, historyPath, tokUI, nil)
	if r.code != http.StatusOK {
		t.Fatalf("status %d", r.code)
	}
	for _, want := range []string{`"points":[]`, `"events":[]`, `"nodePools":[]`} {
		if !strings.Contains(string(r.body), want) {
			t.Errorf("missing %s in %s", want, r.body)
		}
	}
	if strings.Contains(string(r.body), `"since"`) {
		t.Errorf("since set: %s", r.body)
	}
}

func TestCapacityHistoryPoolEvents(t *testing.T) {
	e := newEnv(t)
	h := e.now.Truncate(time.Hour).Add(-3 * time.Hour)
	e.saveHour("c1", h, capacity.HourRow{Nodes: 1}, capacity.HourRow{NodePool: "general", Nodes: 1})
	e.saveHour("c1", h.Add(time.Hour), capacity.HourRow{Nodes: 2},
		capacity.HourRow{NodePool: "general", Nodes: 1}, capacity.HourRow{NodePool: "spot", Nodes: 1})

	var all hubapi.CapacityHistory
	e.getJSON(historyPath, tokUI, &all)
	if len(all.Events) != 1 || all.Events[0].Type != "pool_added" || all.Events[0].NodePool != "spot" ||
		all.Events[0].Day != h.Add(time.Hour).Format("2006-01-02") {
		t.Fatalf("events %+v", all.Events)
	}
	var other hubapi.CapacityHistory
	e.getJSON(historyPath+"?nodePool=general", tokUI, &other)
	if len(other.Events) != 0 {
		t.Errorf("general events %+v", other.Events)
	}
	var spot hubapi.CapacityHistory
	e.getJSON(historyPath+"?nodePool=spot", tokUI, &spot)
	if len(spot.Events) != 1 {
		t.Errorf("spot events %+v", spot.Events)
	}
}
