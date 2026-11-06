package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/hub/update"
)

const tokPricing = "test-pricing"

func byComponent(m hubapi.Maintenance) map[string]hubapi.ComponentVersion {
	out := map[string]hubapi.ComponentVersion{}
	for _, c := range m.Components {
		out[c.Component+"/"+c.Cluster+"/"+c.Instance] = c
	}
	return out
}

// TestMaintenanceVersions: hub replicas (with the leader), pricing from its User-Agent, agents and
// their webhook pods, compared with the hub's version.
func TestMaintenanceVersions(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	e.srv.Version, e.srv.Identity, e.srv.Started = "0.2.0", "hub-a", e.now.Add(-time.Hour)
	e.srv.Cfg.Maintenance.ChartVersion = "0.2.0"
	if err := e.st.EnsureToken(ctx, tokPricing, hubapi.RolePricing, "", "test"); err != nil {
		t.Fatal(err)
	}

	// This replica records itself; another one is on another version; a third stopped long ago.
	bctx, stopBeat := context.WithCancel(ctx)
	beatDone := make(chan struct{})
	go func() { defer close(beatDone); e.srv.RunReplicaHeartbeat(bctx) }()
	for _, c := range []store.ComponentVersion{
		{Component: store.ComponentHub, Instance: "hub-b", Version: "0.1.0", SeenAt: e.now.Add(-20 * time.Second)},
		{Component: store.ComponentHub, Instance: "hub-old", Version: "0.0.9", SeenAt: e.now.Add(-10 * time.Minute)},
	} {
		if err := e.st.UpsertComponent(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// hub-a holds the leader lock on a session named like its elector's.
	cc := e.st.Pool.Config().ConnConfig.Copy()
	cc.RuntimeParams["application_name"] = ElectorApplicationName("hub-a")
	lock, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close(ctx)
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock($1)`, store.LeaderLockKey); err != nil {
		t.Fatal(err)
	}

	// Pricing reports through its User-Agent on its routes.
	e.do(http.MethodGet, "/v1/internal/metrics-bindings", tokPricing, nil, "User-Agent", "kerf-pricing/0.1.0 (pricing-7c9-x)")
	// Agent c1 reports its version and two webhook pods; c2 never reported.
	batch := hubapi.WorkloadStatusBatch{AgentVersion: "0.1.0", Items: []hubapi.WorkloadStatus{}, Components: []hubapi.AgentComponent{
		{Component: "webhook", Instance: "webhook-b", Version: "0.2.0"}, {Component: "webhook", Instance: "webhook-a", Version: "0.3.0"},
	}}
	if r := e.do(http.MethodPost, "/v1/clusters/c1/workload-status", tokC1, batch); r.code != http.StatusOK {
		t.Fatalf("status batch: %d %s", r.code, r.body)
	}
	bad := hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{}, Components: []hubapi.AgentComponent{{Component: "hub", Instance: "x"}}}
	e.problem(e.do(http.MethodPost, "/v1/clusters/c1/workload-status", tokC1, bad), http.StatusBadRequest, "invalid_item")

	waitFor(t, 5*time.Second, "replica row", func() bool {
		rows, _ := e.st.Components(ctx, time.Time{})
		for _, r := range rows {
			if r.Instance == "hub-a" {
				return true
			}
		}
		return false
	})
	var m hubapi.Maintenance
	e.getJSON("/v1/admin/maintenance", tokUI, &m)
	if m.HubVersion != "0.2.0" || m.ChartVersion != "0.2.0" || m.Update.State != hubapi.UpdateDisabled {
		t.Errorf("maintenance %+v", m)
	}
	got := byComponent(m)
	if len(got) != 7 {
		t.Errorf("components: %d %+v", len(got), m.Components)
	}
	if a := got["hub//hub-a"]; !a.Leader || a.Compat != hubapi.CompatSame || a.StartedAt == nil || !strings.Contains(a.Message, "rolling upgrade") {
		t.Errorf("hub-a %+v", a)
	}
	if b := got["hub//hub-b"]; b.Leader || b.Compat != hubapi.CompatOlder {
		t.Errorf("hub-b %+v", b)
	}
	if _, ok := got["hub//hub-old"]; ok {
		t.Error("a replica not seen for 10 minutes is shown")
	}
	if p := got["pricing//pricing-7c9-x"]; p.Version != "0.1.0" || p.Compat != hubapi.CompatOlder || !strings.Contains(p.Message, "the hub cluster") {
		t.Errorf("pricing %+v", p)
	}
	if a := got["agent/c1/"]; a.Version != "0.1.0" || a.AgentStatus != "connected" || a.Compat != hubapi.CompatOlder || !strings.Contains(a.Message, "upgrade the Kerf chart in c1") {
		t.Errorf("agent c1 %+v", a)
	}
	if a := got["agent/c2/"]; a.Version != "" || a.AgentStatus != "never" || a.Compat != hubapi.CompatUnknown || !strings.Contains(a.Message, "has not reported") {
		t.Errorf("agent c2 %+v", a)
	}
	if w := got["webhook/c1/webhook-b"]; w.Compat != hubapi.CompatSame || w.Message != "" {
		t.Errorf("webhook-b %+v", w)
	}
	if w := got["webhook/c1/webhook-a"]; w.Compat != hubapi.CompatNewer || !strings.Contains(w.Message, "upgrade the hub first") {
		t.Errorf("webhook-a %+v", w)
	}
	// Order: hub replicas, pricing, then per cluster its agent and webhook pods.
	var order []string
	for _, c := range m.Components {
		order = append(order, c.Component+"/"+c.Cluster+"/"+c.Instance)
	}
	want := "hub//hub-a hub//hub-b pricing//pricing-7c9-x agent/c1/ webhook/c1/webhook-a webhook/c1/webhook-b agent/c2/"
	if strings.Join(order, " ") != want {
		t.Errorf("order %v", order)
	}

	// An empty list replaces the cluster's webhook pods; absent leaves them.
	e.do(http.MethodPost, "/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{}})
	e.getJSON("/v1/admin/maintenance", tokUI, &m)
	if _, ok := byComponent(m)["webhook/c1/webhook-a"]; !ok {
		t.Error("a batch without components dropped the webhook pods")
	}
	e.do(http.MethodPost, "/v1/clusters/c1/workload-status", tokC1, `{"items":[],"components":[]}`)
	e.getJSON("/v1/admin/maintenance", tokUI, &m)
	if _, ok := byComponent(m)["webhook/c1/webhook-a"]; ok {
		t.Error("components: [] kept the webhook pods")
	}

	// A stopping replica removes its row; the leader prunes old hub-side rows.
	stopBeat()
	<-beatDone
	if n, err := e.st.PruneComponents(ctx, e.now.Add(-componentRetention)); err != nil || n != 0 {
		t.Errorf("prune: %d %v", n, err)
	}
	if n, err := e.st.PruneComponents(ctx, e.now.Add(-5*time.Minute)); err != nil || n != 1 {
		t.Errorf("prune hub-old: %d %v", n, err)
	}
	rows, _ := e.st.Components(ctx, time.Time{})
	for _, r := range rows {
		if r.Instance == "hub-a" || r.Instance == "hub-old" {
			t.Errorf("row left: %+v", r)
		}
	}
}

// fakeGitHub serves a releases list with an ETag.
type fakeGitHub struct {
	mu       sync.Mutex
	status   int
	body     string
	etag     string
	requests atomic.Int32
	ifNone   []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ifNone = append(f.ifNone, r.Header.Get("If-None-Match"))
	if f.status != 0 {
		http.Error(w, "unavailable", f.status)
		return
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", f.etag)
	_, _ = w.Write([]byte(f.body))
}

func (f *fakeGitHub) set(status int, body, etag string) {
	f.mu.Lock()
	f.status, f.body, f.etag = status, body, etag
	f.mu.Unlock()
}

const releasesJSON = `[
 {"tag_name":"v0.3.0-rc.1","name":"0.3.0-rc.1","html_url":"https://github.com/kerf-dev/kerf/releases/tag/v0.3.0-rc.1","prerelease":true,"published_at":"2026-11-05T10:00:00Z"},
 {"tag_name":"v0.2.0","name":"Kerf 0.2.0","html_url":"https://github.com/kerf-dev/kerf/releases/tag/v0.2.0","published_at":"2026-11-03T10:00:00Z"},
 {"tag_name":"v0.1.0","name":"Kerf 0.1.0","html_url":"https://github.com/kerf-dev/kerf/releases/tag/v0.1.0","published_at":"2026-10-01T10:00:00Z"}]`

func newUpdateEnv(t *testing.T) (*testEnv, *fakeGitHub) {
	e := newEnv(t)
	gh := &fakeGitHub{body: releasesJSON, etag: `W/"r1"`}
	ts := httptest.NewServer(gh)
	t.Cleanup(ts.Close)
	e.srv.Version = "0.1.0"
	e.srv.Cfg.UpdateCheck.URL = ts.URL + "/repos/kerf-dev/kerf/releases"
	e.srv.Cfg.Onboarding.Chart = "oci://123456789012.dkr.ecr.eu-west-1.amazonaws.com/kerf/charts/kerf"
	e.srv.Updates = NewUpdateChecker(e.srv.Cfg, e.st, "0.1.0", discardLog())
	e.srv.Updates.Now = e.srv.Now
	e.srv.Metrics.LeaderOnly().MustRegister(e.srv.Updates)
	return e, gh
}

// TestUpdateCheckNow: "Check now", the cool-down, ETag reuse, failures, the metric and the off switch.
func TestUpdateCheckNow(t *testing.T) {
	e, gh := newUpdateEnv(t)
	var m hubapi.Maintenance
	e.getJSON("/v1/admin/maintenance", tokUI, &m)
	if m.Update.State != hubapi.UpdatePending || !m.Update.Enabled || m.Update.Interval != "24h" || m.Upgrade != nil ||
		!strings.HasSuffix(m.Update.Source, "/repos/kerf-dev/kerf/releases") {
		t.Errorf("before any check %+v", m.Update)
	}
	if _, ok := e.metrics().find("kerf_update_available", nil); ok {
		t.Error("kerf_update_available before any check")
	}

	e.problem(e.do(http.MethodPost, "/v1/admin/maintenance/update-check", tokUI, nil), http.StatusForbidden, "role_forbidden")
	var st hubapi.UpdateStatus
	e.postJSON("/v1/admin/maintenance/update-check", tokAdmin, nil, &st)
	if st.State != hubapi.UpdateAvailable || st.LatestVersion != "0.2.0" || st.ReleaseName != "Kerf 0.2.0" ||
		st.ReleaseURL != "https://github.com/kerf-dev/kerf/releases/tag/v0.2.0" || st.PublishedAt == nil || st.CheckedAt == nil || st.LastSuccessAt == nil {
		t.Errorf("after check %+v", st)
	}
	if v, ok := e.metrics().find("kerf_update_available", map[string]string{"current": "0.1.0", "latest": "0.2.0"}); !ok || v != "1" {
		t.Errorf("kerf_update_available = %q (%v)", v, ok)
	}
	e.getJSON("/v1/admin/maintenance", tokUI, &m)
	if m.Upgrade == nil || m.Upgrade.Version != "0.2.0" ||
		m.Upgrade.HubCommand != "helm --kube-context <hub-cluster> upgrade kerf oci://123456789012.dkr.ecr.eu-west-1.amazonaws.com/kerf/charts/kerf --version 0.2.0 -n kerf -f values-hub.yaml" ||
		!strings.Contains(m.Upgrade.SpokeCommand, "--kube-context <workload-cluster> upgrade kerf") || m.Upgrade.DocsURL != UpgradeDocsURL {
		t.Errorf("upgrade %+v", m.Upgrade)
	}

	// Within 30 s: the stored result, no request.
	st = hubapi.UpdateStatus{}
	e.postJSON("/v1/admin/maintenance/update-check", tokAdmin, nil, &st)
	if n := gh.requests.Load(); n != 1 {
		t.Errorf("%d requests after a check within the cool-down", n)
	}
	// Later: a conditional request; 304 keeps the result.
	e.advance(time.Minute)
	st = hubapi.UpdateStatus{}
	e.postJSON("/v1/admin/maintenance/update-check", tokAdmin, nil, &st)
	if n := gh.requests.Load(); n != 2 || gh.ifNone[1] != `W/"r1"` || st.State != hubapi.UpdateAvailable || st.LatestVersion != "0.2.0" {
		t.Errorf("304: %d requests, If-None-Match %v, %+v", n, gh.ifNone, st)
	}

	// A failure keeps the last known release (and the commands); the metric stays.
	gh.set(http.StatusBadGateway, "", "")
	e.advance(time.Minute)
	st = hubapi.UpdateStatus{}
	e.postJSON("/v1/admin/maintenance/update-check", tokAdmin, nil, &st)
	if st.State != hubapi.UpdateFailed || !strings.Contains(st.Error, "HTTP 502") || st.LatestVersion != "0.2.0" {
		t.Errorf("failed %+v", st)
	}
	m = hubapi.Maintenance{}
	e.getJSON("/v1/admin/maintenance", tokUI, &m)
	if m.Upgrade == nil {
		t.Error("no upgrade commands after a failed check")
	}
	// After a failure the ETag is not trusted: a full request.
	gh.set(0, releasesJSON, `W/"r1"`)
	e.advance(time.Minute)
	st = hubapi.UpdateStatus{}
	e.postJSON("/v1/admin/maintenance/update-check", tokAdmin, nil, &st)
	if last := gh.ifNone[len(gh.ifNone)-1]; last != "" || st.State != hubapi.UpdateAvailable {
		t.Errorf("after failure: If-None-Match %q, %+v", last, st)
	}

	// Only pre-releases published (kerf-dev/kerf before its first stable release): up to date, no
	// latest, no commands, no metric; not a failure.
	gh.set(0, `[{"tag_name":"v0.1.0-rc.2","prerelease":true,"html_url":"https://github.com/kerf-dev/kerf/releases/tag/v0.1.0-rc.2"}]`, `W/"r2"`)
	e.advance(time.Minute)
	st = hubapi.UpdateStatus{}
	e.postJSON("/v1/admin/maintenance/update-check", tokAdmin, nil, &st)
	if st.State != hubapi.UpdateUpToDate || st.LatestVersion != "" || st.Error != "" || st.LastSuccessAt == nil {
		t.Errorf("no stable release %+v", st)
	}
	m = hubapi.Maintenance{}
	e.getJSON("/v1/admin/maintenance", tokUI, &m)
	if m.Upgrade != nil {
		t.Errorf("upgrade commands without a release: %+v", m.Upgrade)
	}
	if _, ok := e.metrics().find("kerf_update_available", nil); ok {
		t.Error("kerf_update_available without a release")
	}

	// Off: 409, state disabled, no metric.
	e.srv.Updates.Enabled = false
	e.problem(e.do(http.MethodPost, "/v1/admin/maintenance/update-check", tokAdmin, nil), http.StatusConflict, hubapi.CodeUpdateCheckDisabled)
	m = hubapi.Maintenance{}
	e.getJSON("/v1/admin/maintenance", tokUI, &m)
	if m.Update.State != hubapi.UpdateDisabled || m.Update.LatestVersion != "" || m.Upgrade != nil {
		t.Errorf("disabled %+v %+v", m.Update, m.Upgrade)
	}
	if _, ok := e.metrics().find("kerf_update_available", nil); ok {
		t.Error("kerf_update_available while disabled")
	}
}

// TestUpdateCheckSchedule: the leader's tick checks soon after taking the lead, then once per
// interval (+ at most 1 h of jitter), retries a failure after 1 h, and re-checks soon after an
// upgrade of the hub.
func TestUpdateCheckSchedule(t *testing.T) {
	e, gh := newUpdateEnv(t)
	u := e.srv.Updates
	tick := func() { u.tick(e.ctx) }
	tick() // due within a minute
	e.advance(61 * time.Second)
	tick()
	if n := gh.requests.Load(); n != 1 {
		t.Fatalf("%d requests after the first minute", n)
	}
	e.advance(23 * time.Hour)
	tick()
	if n := gh.requests.Load(); n != 1 {
		t.Errorf("checked again before the interval: %d", n)
	}
	e.advance(2*time.Hour + time.Minute)
	tick()
	if n := gh.requests.Load(); n != 2 {
		t.Errorf("not checked again after interval + jitter: %d", n)
	}

	gh.set(http.StatusServiceUnavailable, "", "")
	e.advance(26 * time.Hour)
	tick()
	if n := gh.requests.Load(); n != 3 {
		t.Fatalf("%d requests", n)
	}
	e.advance(59 * time.Minute)
	tick()
	if n := gh.requests.Load(); n != 3 {
		t.Errorf("failure retried before 1h: %d", n)
	}
	e.advance(2 * time.Minute)
	tick()
	if n := gh.requests.Load(); n != 4 {
		t.Errorf("failure not retried after 1h: %d", n)
	}

	// The hub was upgraded: check within a minute, without the old ETag.
	gh.set(0, releasesJSON, `W/"r1"`)
	e.advance(time.Minute)
	tick()
	u.Current, u.due = "0.2.0", time.Time{} // a new process
	tick()
	e.advance(61 * time.Second)
	tick()
	if n := gh.requests.Load(); n < 5 {
		t.Errorf("no check after the upgrade: %d", n)
	}
	row, _, _ := e.st.GetUpdateCheck(e.ctx)
	if row.CurrentVersion != "0.2.0" || row.Error != "" || u.Status(row, true).State != hubapi.UpdateUpToDate {
		t.Errorf("after upgrade %+v", row)
	}

	// Off: the tick never checks.
	u.Enabled = false
	before := gh.requests.Load()
	e.advance(100 * time.Hour)
	tick()
	if gh.requests.Load() != before {
		t.Error("checked while disabled")
	}
}

func TestUpdateStatus(t *testing.T) {
	ok := store.UpdateCheck{CheckedAt: time.Now(), LatestVersion: "0.2.0", LastSuccessAt: ptrTime(time.Now())}
	failed := ok
	failed.Error = "dial tcp: no route to host"
	tests := []struct {
		name    string
		enabled bool
		current string
		row     store.UpdateCheck
		had     bool
		want    string
	}{
		{"disabled", false, "0.1.0", ok, true, hubapi.UpdateDisabled},
		{"pending", true, "0.1.0", store.UpdateCheck{}, false, hubapi.UpdatePending},
		{"failed", true, "0.1.0", failed, true, hubapi.UpdateFailed},
		{"dev build", true, "dev-abc123", ok, true, hubapi.UpdateDevBuild},
		{"older", true, "0.1.0", ok, true, hubapi.UpdateAvailable},
		{"same", true, "v0.2.0", ok, true, hubapi.UpdateUpToDate},
		{"pre-release ahead of the latest stable", true, "0.3.0-rc.1", ok, true, hubapi.UpdateUpToDate},
		{"pre-release of the latest", true, "0.2.0-rc.2", ok, true, hubapi.UpdateAvailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &UpdateChecker{Enabled: tt.enabled, Current: tt.current, Interval: 12 * time.Hour, Client: &update.Client{}}
			st := u.Status(tt.row, tt.had)
			if st.State != tt.want || st.Interval != "12h" || st.Source != update.DefaultURL || st.CurrentVersion != tt.current {
				t.Errorf("got %+v, want state %s", st, tt.want)
			}
		})
	}
}

func TestUpgradeCommands(t *testing.T) {
	tests := []struct {
		chart, release, ns string
		want               string
	}{
		{"", "", "", "helm --kube-context <hub-cluster> upgrade kerf oci://<registry>/charts/kerf --version 0.2.0 -n kerf -f values-hub.yaml"},
		{"deploy/charts/kerf", "", "", "helm --kube-context <hub-cluster> upgrade kerf oci://<registry>/charts/kerf --version 0.2.0 -n kerf -f values-hub.yaml"},
		{"https://charts.example.com|kerf", "rightsizing", "platform", "helm --kube-context <hub-cluster> upgrade rightsizing kerf --repo https://charts.example.com --version 0.2.0 -n platform -f values-hub.yaml"},
	}
	for _, tt := range tests {
		s := &Server{Cfg: Config{Onboarding: OnboardingConfig{Chart: tt.chart}, Maintenance: MaintenanceConfig{ReleaseName: tt.release, ReleaseNamespace: tt.ns}}}
		if got := s.upgradeCommands("0.2.0").HubCommand; got != tt.want {
			t.Errorf("chart %q:\n got  %s\n want %s", tt.chart, got, tt.want)
		}
	}
}

func TestUpdateCheckConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "hub.yaml")
		if err := os.WriteFile(p, []byte("databaseURL: postgres://x\n"+s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg, err := LoadConfig(write(""))
	if err != nil || !cfg.UpdateCheck.On() || cfg.UpdateCheck.Interval.D() != 24*time.Hour {
		t.Errorf("defaults: %+v %v", cfg.UpdateCheck, err)
	}
	cfg, err = LoadConfig(write("updateCheck:\n  enabled: false\n  interval: 6h\nmaintenance:\n  chartVersion: 0.2.0\n  releaseName: kerf\n  releaseNamespace: kerf\n"))
	if err != nil || cfg.UpdateCheck.On() || cfg.UpdateCheck.Interval.D() != 6*time.Hour || cfg.Maintenance.ChartVersion != "0.2.0" {
		t.Errorf("set: %+v %+v %v", cfg.UpdateCheck, cfg.Maintenance, err)
	}
	if _, err := LoadConfig(write("updateCheck:\n  interval: 1m\n")); err == nil || !strings.Contains(err.Error(), "at least 10m") {
		t.Errorf("1m interval: %v", err)
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{24 * time.Hour: "24h", 10 * time.Minute: "10m", 90 * time.Minute: "1h30m", 45 * time.Second: "45s"} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%s) = %s, want %s", d, got, want)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
