package hub

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// TestPruneAbsent (WO-04e): on a cluster with an agent, a workload missing from the agent's full
// status batches after two pulls and without pods is soft-deleted: gone from lists, facets and
// the summary, kept for its detail page and audit, revived when it reappears. Live workloads the
// agent does not report and clusters without an agent keep the metrics-based behaviour.
func TestPruneAbsent(t *testing.T) {
	e := newEnv(t) // c1: checkout, ledger, frontend, report, legacy; c2: api (no agent)
	kGhost := key("c1", "e2e-x", hubapi.KindDeployment, "ghost") // known only from agent reports

	// legacy was deleted: still in the metrics window, but no pods any more.
	e.loader.mu.Lock()
	e.loader.gone = map[string]bool{kLegacy.ID(): true}
	e.loader.mu.Unlock()
	e.advance(time.Minute)
	e.run(e.now, false)
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: []hubapi.Event{event(1, "applied", kLegacy, e.now, "cpu")}}, &res)

	status := func(k hubapi.WorkloadKey) hubapi.WorkloadStatus {
		return hubapi.WorkloadStatus{Workload: k.Ref(), Phase: "Applied", Reason: "Applied",
			Modes: hubapi.EffectiveModes{CPU: hubapi.ModeAutoPatch, Memory: hubapi.ModeAutoPatch}}
	}
	// One agent cycle: pull, the full status batch 2 s later, the next pull 30 s after that.
	// frontend is never reported (it has pods), legacy neither.
	cycle := func(reported ...hubapi.WorkloadKey) {
		t.Helper()
		var list hubapi.RecommendationList
		e.getJSON("/v1/clusters/c1/recommendations", tokC1, &list)
		e.advance(2 * time.Second)
		if len(reported) > 0 {
			items := make([]hubapi.WorkloadStatus, 0, len(reported))
			for _, k := range reported {
				items = append(items, status(k))
			}
			e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: items}, &res)
		}
		e.advance(30 * time.Second)
	}
	listed := func() []string {
		t.Helper()
		var l hubapi.WorkloadList
		e.getJSON("/v1/workloads?cluster=c1&limit=500", tokUI, &l)
		var ids []string
		for _, w := range l.Items {
			ids = append(ids, w.Name)
		}
		slices.Sort(ids)
		return ids
	}
	expect := func(step string, want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := listed(); !slices.Equal(got, want) {
			t.Fatalf("%s: workloads %v, want %v", step, got, want)
		}
		var s hubapi.FleetSummary
		e.getJSON("/v1/summary", tokUI, &s)
		if got := clusterSummary(t, s, "c1").Workloads; got != len(want) {
			t.Errorf("%s: summary %d workloads, want %d", step, got, len(want))
		}
	}
	base := []string{"checkout", "ledger", "frontend", "report"}
	reported := []hubapi.WorkloadKey{kCheckout, kLedger, kReport}

	cycle(append(reported, kGhost)...) // pull A, ghost still reported
	cycle(reported...)                 // pull B: no earlier pull pair yet
	expect("after two pulls", append(base, "legacy", "ghost")...)
	cycle(reported...) // pull C: legacy missed the batches after A and B
	expect("after pull C", append(base, "ghost")...)
	cycle(reported...) // pull D: ghost missed the batches after B and C
	expect("after pull D", base...)

	var s hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &s)
	if c1 := clusterSummary(t, s, "c1"); c1.WorkloadsByMode.CPU.AutoPatch != 3 {
		t.Errorf("auto-patch workloads %+v, want 3 (ghost excluded)", c1.WorkloadsByMode.CPU)
	}
	var f hubapi.WorkloadFacets
	e.getJSON("/v1/workloads/facets", tokUI, &f)
	if slices.ContainsFunc(f.Namespaces, func(c hubapi.FacetCount) bool { return c.Value == "e2e-x" }) {
		t.Errorf("facets still list the deleted namespace: %+v", f.Namespaces)
	}
	// History and audit stay reachable.
	var d hubapi.WorkloadDetail
	if r := e.getJSON(wlPath(kLegacy, ""), tokUI, &d); r.code != 200 {
		t.Errorf("detail of a pruned workload: %d", r.code)
	}
	var audit hubapi.AuditEventList
	e.getJSON("/v1/audit?"+url.Values{"cluster": {"c1"}, "workload": {"legacy"}}.Encode(), tokUI, &audit)
	if len(audit.Items) != 1 {
		t.Errorf("audit of a pruned workload: %d events", len(audit.Items))
	}

	// A regular run still finds legacy in the metrics window but does not revive it (no pods),
	// and stores the savings of the live workloads only.
	e.advance(5 * time.Minute)
	e.run(e.now, false)
	expect("after a run", base...)
	e.getJSON("/v1/summary", tokUI, &s)
	if _, _, total := e.clusterRun("c1"); !approx(total, clusterSummary(t, s, "c1").MonthlySavingsUSD.Total, 0.01) {
		t.Errorf("stored run total %v, summary %v", total, clusterSummary(t, s, "c1").MonthlySavingsUSD.Total)
	}

	// Pulls without status batches (the full batch failed): nothing is pruned.
	cycle()
	cycle()
	cycle()
	expect("pulls without batches", base...)

	// Revived: the agent reports ghost again; legacy has pods again in the next run. That run
	// marks ghost deleted for having no series at all, as before WO-04e.
	cycle(append(reported, kGhost)...)
	expect("ghost reported again", append(base, "ghost")...)
	e.loader.mu.Lock()
	e.loader.gone = nil
	e.loader.mu.Unlock()
	e.run(e.now, false)
	expect("legacy has pods again", append(base, "legacy")...)

	// c2 has no agent: its workloads are untouched.
	var c2 hubapi.WorkloadList
	e.getJSON("/v1/workloads?cluster=c2", tokUI, &c2)
	if c2.Total != 1 {
		t.Errorf("c2 workloads %d, want 1", c2.Total)
	}
}
