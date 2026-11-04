package configsync

import (
	"context"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func TestStateApprovals(t *testing.T) {
	w := hubclient.WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "web"}
	s := NewState()
	if s.Approval(w, "r1", t0) != nil || s.Flags().Paused || s.ConfigVersion() != "" {
		t.Fatal("empty state")
	}
	cfg := &hubclient.ClusterConfig{ConfigVersion: "c-1", Approvals: []hubclient.ConfigApproval{
		{ID: "a1", Workload: w, RecommendationID: "r1", ExpiresAt: t0.Add(time.Hour), ApprovedBy: "bob"},
		{ID: "a2", Workload: hubclient.WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "api"}, RecommendationID: "r2", ExpiresAt: t0.Add(-time.Minute)},
	}}
	s.set(cfg, `"c-1"`)
	if a := s.Approval(w, "r1", t0); a == nil || a.ID != "a1" {
		t.Fatalf("approval %+v", a)
	}
	tests := []struct {
		name string
		w    hubclient.WorkloadRef
		rec  string
		at   time.Time
	}{
		{"other recommendation", w, "r0", t0},
		{"expired", hubclient.WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "api"}, "r2", t0},
		{"after expiry", w, "r1", t0.Add(time.Hour)},
		{"other workload", hubclient.WorkloadRef{Namespace: "shop", Kind: "StatefulSet", Name: "web"}, "r1", t0},
	}
	for _, tt := range tests {
		if a := s.Approval(tt.w, tt.rec, tt.at); a != nil {
			t.Errorf("%s: %+v", tt.name, a)
		}
	}
	s.Consume("a1", t0)
	if s.Approval(w, "r1", t0) != nil {
		t.Fatal("consumed approval returned")
	}
	// Still sent by the hub: stays consumed. No longer sent: forgotten.
	s.set(cfg, `"c-1"`)
	if s.Approval(w, "r1", t0) != nil {
		t.Fatal("consumed approval revived by the same config")
	}
	s.set(&hubclient.ClusterConfig{ConfigVersion: "c-2"}, `"c-2"`)
	if len(s.consumed) != 0 {
		t.Fatalf("consumed not pruned: %v", s.consumed)
	}
}

type fakeHub struct {
	cfg  *hubclient.ClusterConfig
	err  error
	etag []string
}

func (f *fakeHub) GetConfig(_ context.Context, etag string) (*hubclient.ClusterConfig, string, error) {
	f.etag = append(f.etag, etag)
	if f.err != nil {
		return nil, "", f.err
	}
	if f.cfg == nil || etag == `"`+f.cfg.ConfigVersion+`"` {
		return nil, etag, hubclient.ErrNotModified
	}
	return f.cfg, `"` + f.cfg.ConfigVersion + `"`, nil
}

// TestSyncPersists: the pause flag and approvals survive a restart without the hub; the hub being
// unreachable or not serving config keeps the last config.
func TestSyncPersists(t *testing.T) {
	needEnv(t)
	const cm = "sync-test-state"
	t.Cleanup(func() {
		_ = k8s.Delete(testCtx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: cm}})
	})
	w := hubclient.WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "web"}
	hub := &fakeHub{cfg: &hubclient.ClusterConfig{ConfigVersion: "c-7",
		Flags:     hubclient.ConfigFlags{Paused: true, PauseScope: hubclient.PauseScopeFleet, PauseReason: "incident"},
		Approvals: []hubclient.ConfigApproval{{ID: "a1", Workload: w, RecommendationID: "r1", ExpiresAt: time.Now().Add(time.Hour)}}}}
	var gotVersion string
	var gotInv []hubclient.PolicyInventoryItem
	newSyncer := func(h Hub) *Syncer {
		return &Syncer{Hub: h, Client: k8s, Reader: k8s, State: NewState(), Namespace: "default", ConfigMap: cm, Log: testLog,
			Inventory: func(v string, inv []hubclient.PolicyInventoryItem) { gotVersion, gotInv = v, inv }}
	}
	s := newSyncer(hub)
	if err := s.Load(testCtx); err != nil { // no ConfigMap yet
		t.Fatal(err)
	}
	s.Sync(testCtx)
	if !s.State.Flags().Paused || gotVersion != "c-7" || gotInv == nil {
		t.Fatalf("after sync: %+v %q %v", s.State.Flags(), gotVersion, gotInv)
	}
	s.State.Consume("a1", time.Now())
	s.Sync(testCtx) // 304; persists the consumption
	if hub.etag[1] != `"c-7"` {
		t.Fatalf("ETag not sent: %v", hub.etag)
	}
	var stored corev1.ConfigMap
	if err := k8s.Get(testCtx, client.ObjectKey{Namespace: "default", Name: cm}, &stored); err != nil || stored.Data[StateKey] == "" {
		t.Fatalf("state ConfigMap: %v %+v", err, stored.Data)
	}

	// Restart with the hub down: everything restored, nothing lost on errors.
	down := &fakeHub{err: &hubclient.APIError{StatusCode: http.StatusServiceUnavailable}}
	s2 := newSyncer(down)
	if err := s2.Load(testCtx); err != nil {
		t.Fatal(err)
	}
	if f := s2.State.Flags(); !f.Paused || f.PauseScope != hubclient.PauseScopeFleet || s2.State.ConfigVersion() != "c-7" {
		t.Fatalf("restored flags %+v %q", f, s2.State.ConfigVersion())
	}
	if a := s2.State.Approval(w, "r1", time.Now()); a != nil {
		t.Fatal("consumed approval revived after restart")
	}
	s2.Sync(testCtx)
	if down.etag[0] != `"c-7"` || !s2.State.Flags().Paused {
		t.Fatalf("hub down: etag %v flags %+v", down.etag, s2.State.Flags())
	}
	// A hub without the endpoint (404) also keeps the last config.
	down.err = &hubclient.APIError{StatusCode: http.StatusNotFound}
	s2.Sync(testCtx)
	if !s2.State.Flags().Paused || !s2.unsupported {
		t.Fatal("404 dropped the config")
	}
	// Resume.
	s2.Hub = &fakeHub{cfg: &hubclient.ClusterConfig{ConfigVersion: "c-8"}}
	s2.Sync(testCtx)
	if s2.State.Flags().Paused || gotVersion != "c-8" {
		t.Fatalf("resume: %+v %q", s2.State.Flags(), gotVersion)
	}
}

func TestRecheck(t *testing.T) {
	needEnv(t)
	at := t0
	var calls []time.Time
	hub := &fakeHub{cfg: &hubclient.ClusterConfig{ConfigVersion: "c-1", Flags: hubclient.ConfigFlags{Recheck: true, RecheckRequestedAt: &at}}}
	s := &Syncer{Hub: hub, Client: k8s, Reader: k8s, State: NewState(), Log: testLog, Recheck: func(t time.Time) { calls = append(calls, t) }}
	s.Sync(testCtx)
	s.Sync(testCtx)
	if len(calls) != 1 || !calls[0].Equal(at) {
		t.Fatalf("recheck calls %v", calls)
	}
}
