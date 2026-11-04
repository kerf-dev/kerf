package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// TestReportedJVM (WO-12): JVM containers reported with the status are stored next to the settings
// and returned per workload; a status without JVM containers maps to an empty slice.
func TestReportedJVM(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if err := st.EnsureCluster(ctx, "c1", ""); err != nil {
		t.Fatal(err)
	}
	a, b, c := wkey("c1", "jvm-xmx"), wkey("c1", "nginx"), wkey("c1", "jvm-noset")
	jvm := []hubapi.JVMContainer{{Name: "app", DetectedBy: []string{"image", "command"}, MaxHeap: "128Mi"}}
	items := []hubapi.WorkloadStatus{
		{Workload: a.Ref(), Modes: hubapi.EffectiveModes{CPU: "audit", Memory: "audit"}, Phase: "Pending", Settings: &hubapi.ValueSettings{Profile: "dev"}, JVM: jvm},
		{Workload: b.Ref(), Modes: hubapi.EffectiveModes{CPU: "audit", Memory: "audit"}, Phase: "Pending", Settings: &hubapi.ValueSettings{Profile: "dev"}},
		{Workload: c.Ref(), Modes: hubapi.EffectiveModes{CPU: "audit", Memory: "audit"}, Phase: "Pending", JVM: jvm},
	}
	if err := st.UpsertStatuses(ctx, "c1", items, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReportedJVM(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[hubapi.WorkloadKey][]hubapi.JVMContainer{a: jvm, b: {}, c: jvm}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReportedJVM = %+v, want %+v", got, want)
	}
	// Settings are unaffected by the extra key; a status with only JVM data has no settings.
	rs, err := st.ReportedSettings(ctx, "c1")
	if err != nil || len(rs) != 2 || rs[a] == nil || rs[a].Profile != "dev" || rs[c] != nil {
		t.Errorf("reported settings %v %v", rs, err)
	}
	views, err := st.LoadWorkloads(ctx, WorkloadFilter{Cluster: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Key == a && (v.Status == nil || !reflect.DeepEqual(v.Status.JVM, jvm) || v.Status.Settings == nil) {
			t.Errorf("status %+v", v.Status)
		}
		if v.Key == b && (v.Status == nil || v.Status.JVM != nil) {
			t.Errorf("status %+v", v.Status)
		}
	}
	// A new report without JVM containers clears them.
	items[0].JVM = nil
	if err := st.UpsertStatuses(ctx, "c1", items[:1], now); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ReportedJVM(ctx, "c1"); len(got[a]) != 0 {
		t.Errorf("after clear: %+v", got[a])
	}
}

func TestStatusSettingsColumn(t *testing.T) {
	if got := string(statusSettings(hubapi.WorkloadStatus{})); got != "{}" {
		t.Errorf("empty = %s, want {}", got)
	}
	if statusJVM([]byte("not json")) != nil || statusJVM([]byte(`{"profile":"dev"}`)) != nil {
		t.Error("statusJVM must be nil without jvm data")
	}
}

// TestAllRiskFlagsStorable: every RiskFlag of the API contract passes the recommendations
// risk_flags check constraint (a new flag needs a migration, e.g. 0003 for jvm-fixed-heap).
func TestAllRiskFlagsStorable(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.EnsureCluster(ctx, "c1", ""); err != nil {
		t.Fatal(err)
	}
	k := wkey("c1", "flags")
	ids, err := st.UpsertInventory(ctx, "c1", inventory(k), now, true)
	if err != nil {
		t.Fatal(err)
	}
	r := result(k, now, "1", 1)
	r.Recommendation.RiskFlags = append([]string{}, hubapi.AllRiskFlags...)
	r.Recommendation.Containers[0].RiskFlags = []string{hubapi.FlagJVMFixedHeap}
	insert(t, st, "c1", ids, r)
	views, err := st.LoadWorkloads(ctx, WorkloadFilter{Cluster: "c1"})
	if err != nil || len(views) != 1 || views[0].Rec == nil || !reflect.DeepEqual(views[0].Rec.RiskFlags, hubapi.AllRiskFlags) {
		t.Fatalf("views %+v %v", views, err)
	}
}
