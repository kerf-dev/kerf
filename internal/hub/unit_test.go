package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// clearHubEnv makes LoadConfig independent of the developer's environment.
func clearHubEnv(t *testing.T) {
	for _, k := range []string{"HUB_DATABASE_URL", "HUB_PROMETHEUS_URL", "HUB_LISTEN", "HUB_MIGRATIONS_DIR", "HUB_DEV_TOKENS_DIR", "HUB_CORS_ORIGINS"} {
		t.Setenv(k, "")
	}
}

func TestLoadConfigYAML(t *testing.T) {
	clearHubEnv(t)
	p := writeFile(t, t.TempDir(), "hub.yaml", `
listen: ":9090"
databaseURL: postgres://u:p@db:5432/ro
prometheusURL: http://vm:8428
prometheusTimeout: 30s
corsOrigins: ["http://localhost:5173"]
agentStaleAfter: 5m
tokenCacheTTL: 1s
devSeed:
  enabled: true
  clusters: [c1]
  tokens: {c1: tok}
recommender:
  interval: 5m
  runOnStart: false
  strategy: percentile
  backfill: {runs: 3, every: 24h}
  step: 1m
  rateWindow: 2m
  minCPU: 20m
  minMemory: 64Mi
  throttleThreshold: 0.2
  fullConfidenceSamples: 100
  sidecarContainers: [envoy]
  defaultProfile: dev
  defaultCPUCoreHourUSD: 0.05
  profiles:
    dev:
      window: 72h
      cpuPercentile: 0.8
      memoryPeakWindow: 24h
history:
  fullResolution: 6h
  retention: 720h
`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9090" || cfg.DatabaseURL != "postgres://u:p@db:5432/ro" || cfg.PrometheusURL != "http://vm:8428" {
		t.Errorf("strings: %+v", cfg)
	}
	if cfg.MigrationsDir != "db/migrations" {
		t.Errorf("default migrationsDir lost: %q", cfg.MigrationsDir)
	}
	for name, got := range map[string]time.Duration{
		"prometheusTimeout": cfg.PrometheusTimeout.D(), "agentStaleAfter": cfg.AgentStaleAfter.D(), "tokenCacheTTL": cfg.TokenCacheTTL.D(),
		"interval": cfg.Recommender.Interval.D(), "backfill.every": cfg.Recommender.Backfill.Every.D(),
		"fullResolution": cfg.History.FullResolution.D(), "retention": cfg.History.Retention.D(),
	} {
		want := map[string]time.Duration{
			"prometheusTimeout": 30 * time.Second, "agentStaleAfter": 5 * time.Minute, "tokenCacheTTL": time.Second,
			"interval": 5 * time.Minute, "backfill.every": 24 * time.Hour, "fullResolution": 6 * time.Hour, "retention": 720 * time.Hour,
		}[name]
		if got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
	if cfg.Recommender.RunOnStart == nil || *cfg.Recommender.RunOnStart || cfg.Recommender.Backfill.Runs != 3 {
		t.Errorf("runOnStart/backfill: %+v", cfg.Recommender)
	}
	if !cfg.DevSeed.Enabled || cfg.DevSeed.Tokens["c1"] != "tok" || !reflect.DeepEqual(cfg.CORSOrigins, []string{"http://localhost:5173"}) {
		t.Errorf("devSeed/cors: %+v %v", cfg.DevSeed, cfg.CORSOrigins)
	}

	rc, err := cfg.RecommenderConfig()
	if err != nil {
		t.Fatal(err)
	}
	def := recommender.DefaultConfig()
	if rc.Step != time.Minute || rc.RateWindow != 2*time.Minute || rc.RiskWindow != def.RiskWindow {
		t.Errorf("durations: step %s rate %s risk %s", rc.Step, rc.RateWindow, rc.RiskWindow)
	}
	if rc.MinCPUCores != 0.02 || rc.MinMemoryBytes != 64*recommender.MiB {
		t.Errorf("min: %v %v", rc.MinCPUCores, rc.MinMemoryBytes)
	}
	if rc.ThrottleThreshold != 0.2 || rc.RestartThreshold != def.RestartThreshold || rc.FullConfidenceSamples != 100 {
		t.Errorf("thresholds: %+v", rc)
	}
	if !reflect.DeepEqual(rc.SidecarContainers, []string{"envoy"}) || rc.DefaultCPUCoreHourUSD != 0.05 || rc.DefaultMemoryGiBHourUSD != def.DefaultMemoryGiBHourUSD {
		t.Errorf("sidecars/pricing: %v %v %v", rc.SidecarContainers, rc.DefaultCPUCoreHourUSD, rc.DefaultMemoryGiBHourUSD)
	}
	if rc.DefaultSettings.Profile != hubapi.ProfileDev {
		t.Errorf("default profile %q", rc.DefaultSettings.Profile)
	}
	dev, defDev := rc.Profiles[hubapi.ProfileDev], def.Profiles[hubapi.ProfileDev]
	if dev.Window != 72*time.Hour || dev.CPUPercentile != 0.8 || dev.MemoryPeakWindow != 24*time.Hour {
		t.Errorf("dev profile overrides not applied: %+v", dev)
	}
	if dev.MemoryPercentile != defDev.MemoryPercentile || dev.CPUHeadroom != defDev.CPUHeadroom || dev.Name != hubapi.ProfileDev {
		t.Errorf("dev profile unset fields changed: %+v", dev)
	}
	if !reflect.DeepEqual(rc.Profiles[hubapi.ProfileProduction], def.Profiles[hubapi.ProfileProduction]) {
		t.Errorf("production profile changed: %+v", rc.Profiles[hubapi.ProfileProduction])
	}
}

func TestLoadConfigDefaultsAndEnv(t *testing.T) {
	clearHubEnv(t)
	if _, err := LoadConfig(""); err == nil || !strings.Contains(err.Error(), "databaseURL") {
		t.Fatalf("want databaseURL error, got %v", err)
	}
	t.Setenv("HUB_DATABASE_URL", "postgres://env/db")
	t.Setenv("HUB_PROMETHEUS_URL", "http://prom-env:9090")
	t.Setenv("HUB_LISTEN", ":1234")
	t.Setenv("HUB_MIGRATIONS_DIR", "/migrations")
	t.Setenv("HUB_DEV_TOKENS_DIR", "/tokens")
	t.Setenv("HUB_CORS_ORIGINS", "http://a,http://b")
	p := writeFile(t, t.TempDir(), "hub.yaml", "databaseURL: postgres://file/db\nprometheusURL: http://file\n")
	for _, path := range []string{"", p} {
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("%q: %v", path, err)
		}
		if cfg.DatabaseURL != "postgres://env/db" || cfg.PrometheusURL != "http://prom-env:9090" || cfg.Listen != ":1234" ||
			cfg.MigrationsDir != "/migrations" || cfg.DevSeed.TokensDir != "/tokens" ||
			!reflect.DeepEqual(cfg.CORSOrigins, []string{"http://a", "http://b"}) {
			t.Errorf("%q: env overrides not applied: %+v", path, cfg)
		}
		if cfg.Recommender.Interval.D() != 24*time.Hour || cfg.Recommender.Strategy != "percentile" || cfg.AgentStaleAfter.D() != 10*time.Minute {
			t.Errorf("%q: defaults lost: %+v", path, cfg.Recommender)
		}
	}
}

func TestLoadConfigErrors(t *testing.T) {
	clearHubEnv(t)
	dir := t.TempDir()
	cases := []struct {
		name, yaml, want string
	}{
		{"unknown top-level key", "databaseURL: x\nbogus: 1\n", "bogus"},
		{"unknown nested key", "databaseURL: x\nrecommender:\n  intervall: 5m\n", "intervall"},
		{"unknown profile field", "databaseURL: x\nrecommender:\n  profiles:\n    dev:\n      windw: 1h\n", "windw"},
		{"duration as number", "databaseURL: x\nprometheusTimeout: 30\n", "duration"},
		{"bad duration", "databaseURL: x\nagentStaleAfter: 5 minutes\n", ""},
		{"zero interval", "databaseURL: x\nrecommender:\n  interval: 0s\n", "interval"},
		{"negative interval", "databaseURL: x\nrecommender:\n  interval: -5m\n", "interval"},
		{"unknown profile", "databaseURL: x\nrecommender:\n  profiles:\n    staging:\n      window: 24h\n", "unknown profile"},
		{"unknown strategy", "databaseURL: x\nrecommender:\n  strategy: magic\n", "unknown strategy"},
		{"bad minCPU", "databaseURL: x\nrecommender:\n  minCPU: lots\n", "minCPU"},
		{"bad minMemory", "databaseURL: x\nrecommender:\n  minMemory: 1Zi\n", "minMemory"},
		{"percentile out of range", "databaseURL: x\nrecommender:\n  profiles:\n    dev:\n      cpuPercentile: 1.5\n", "percentile"},
		{"window shorter than step", "databaseURL: x\nrecommender:\n  step: 1h\n  profiles:\n    dev:\n      window: 30m\n", "window"},
		{"undefined default profile", "databaseURL: x\nrecommender:\n  defaultProfile: staging\n", "default profile"},
		{"negative step", "databaseURL: x\nrecommender:\n  step: -1m\n", "positive"},
		{"not yaml", "databaseURL: [x\n", ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFile(t, dir, "c"+string(rune('a'+i))+".yaml", tc.yaml)
			_, err := LoadConfig(p)
			if err == nil {
				t.Fatalf("no error for %q", tc.yaml)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	if _, err := LoadConfig(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("missing file: no error")
	}
}

func TestRecommenderConfigDirect(t *testing.T) {
	cfg := DefaultConfig()
	rc, err := cfg.RecommenderConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rc, recommender.DefaultConfig()) {
		t.Errorf("no overrides should give recommender.DefaultConfig()")
	}
	cfg.Recommender.Strategy = ""
	if _, err := cfg.RecommenderConfig(); err != nil {
		t.Errorf("empty strategy must be accepted: %v", err)
	}
	ten, one := Duration(10*time.Minute), Duration(time.Minute)
	cfg.Recommender.Profiles = map[string]ProfileConfig{"high-availability": {Window: &ten}}
	rc, err = cfg.RecommenderConfig()
	if err != nil || rc.Profiles["high-availability"].Window != 10*time.Minute {
		t.Errorf("window 10m (>= step 5m): %v %v", rc.Profiles["high-availability"].Window, err)
	}
	cfg.Recommender.Profiles = map[string]ProfileConfig{"high-availability": {Window: &one}}
	if _, err := cfg.RecommenderConfig(); err == nil {
		t.Error("window 1m shorter than step 5m: no error")
	}
	// The profile map must not be shared with recommender.DefaultConfig() callers.
	if recommender.DefaultConfig().Profiles["high-availability"].Window != 28*24*time.Hour {
		t.Error("RecommenderConfig mutated the default profiles")
	}
}

func TestDurationJSON(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"1h30m"`), &d); err != nil || d.D() != 90*time.Minute {
		t.Fatalf("unmarshal: %v %v", d, err)
	}
	b, err := json.Marshal(d)
	if err != nil || string(b) != `"1h30m0s"` {
		t.Errorf("marshal: %s %v", b, err)
	}
	for _, bad := range []string{`90`, `"90"`, `"soon"`, `null1`} {
		if err := json.Unmarshal([]byte(bad), &d); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestChooseStep(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		step    string
		span    time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"explicit", "1h", 7 * 24 * time.Hour, time.Hour, false},
		{"explicit odd", "90s", time.Hour, 90 * time.Second, false},
		{"explicit min", "10s", time.Hour, 10 * time.Second, false},
		{"too small", "5s", time.Hour, 0, true},
		{"not a duration", "5", time.Hour, 0, true},
		{"garbage", "soon", time.Hour, 0, true},
		{"negative", "-1h", time.Hour, 0, true},
		{"too many points", "10s", 7 * 24 * time.Hour, 0, true},
		{"exactly 11000 points", "1m", 11000 * time.Minute, time.Minute, false},
		{"11001 points", "1m", 11001 * time.Minute, 0, true},
		{"default 1h", "", time.Hour, time.Minute, false},
		{"default 1000m", "", 1000 * time.Minute, time.Minute, false},
		{"default 1001m", "", 1001 * time.Minute, 5 * time.Minute, false},
		{"default 24h", "", 24 * time.Hour, 5 * time.Minute, false},
		{"default 7d", "", 7 * 24 * time.Hour, 15 * time.Minute, false},
		{"default 30d", "", 30 * 24 * time.Hour, time.Hour, false},
		{"default 90d", "", 90 * 24 * time.Hour, 3 * time.Hour, false},
		{"default 1y", "", 365 * 24 * time.Hour, 12 * time.Hour, false},
		{"default 2y", "", 730 * 24 * time.Hour, 24 * time.Hour, false},
		{"default 10y", "", 3650 * 24 * time.Hour, 7 * 24 * time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseStep(tc.step, from, from.Add(tc.span))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("step = %s, want %s", got, tc.want)
			}
			if tc.step == "" && tc.span/got > 1000 {
				t.Errorf("default step %s gives %d points", got, tc.span/got)
			}
		})
	}
}

func TestTimeRange(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s := &Server{Now: func() time.Time { return now }}
	cases := []struct {
		name, from, to   string
		wantFrom, wantTo time.Time
		wantErr          bool
	}{
		{"defaults", "", "", now.Add(-7 * 24 * time.Hour), now, false},
		{"from only", "2026-11-01T00:00:00Z", "", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), now, false},
		{"to only", "", "2026-10-10T00:00:00Z", time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), false},
		{"both, offset zone", "2026-11-01T02:00:00+02:00", "2026-11-02T00:00:00Z", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), false},
		{"nanoseconds", "2026-11-01T00:00:00.5Z", "", time.Date(2026, 11, 1, 0, 0, 0, 5e8, time.UTC), now, false},
		{"from equals to", "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z", time.Time{}, time.Time{}, true},
		{"from after to", "2026-11-02T00:00:00Z", "2026-11-01T00:00:00Z", time.Time{}, time.Time{}, true},
		{"from after default to", "2026-12-01T00:00:00Z", "", time.Time{}, time.Time{}, true},
		{"bad from", "yesterday", "", time.Time{}, time.Time{}, true},
		{"bad to", "", "2026-11-01", time.Time{}, time.Time{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to, err := s.timeRange(tc.from, tc.to)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !from.Equal(tc.wantFrom) || !to.Equal(tc.wantTo) {
				t.Errorf("got [%s, %s), want [%s, %s)", from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}

func TestCursor(t *testing.T) {
	ts := time.Date(2026, 11, 4, 12, 0, 0, 123456000, time.UTC)
	for _, c := range []cursor{
		{},
		{Offset: 50, Q: "cluster=c1&sort=name"},
		{Key: "ns\x00Deployment\x00name"},
		{Time: ts, ID: "0193a1b2-0000-7000-8000-000000000001"},
	} {
		s := encodeCursor(c)
		if strings.ContainsAny(s, "+/=") {
			t.Errorf("cursor %q is not raw URL base64", s)
		}
		got, err := decodeCursor(s)
		if err != nil {
			t.Fatalf("decode %q: %v", s, err)
		}
		if got.Offset != c.Offset || got.Key != c.Key || got.ID != c.ID || got.Q != c.Q || !got.Time.Equal(c.Time) {
			t.Errorf("round trip: got %+v, want %+v", got, c)
		}
	}
	if s := encodeCursor(cursor{}); s != base64.RawURLEncoding.EncodeToString([]byte("{}")) {
		t.Errorf("zero cursor = %q (zero time must be omitted)", s)
	}
	for _, bad := range []string{"!!!", "e30=", base64.RawURLEncoding.EncodeToString([]byte("not json")), base64.RawURLEncoding.EncodeToString([]byte(`{"o":"x"}`))} {
		if _, err := decodeCursor(bad); err == nil {
			t.Errorf("decodeCursor(%q): no error", bad)
		}
	}
}

func TestEtagMatches(t *testing.T) {
	const etag = `"abc123"`
	cases := []struct {
		header string
		want   bool
	}{
		{`"abc123"`, true},
		{`W/"abc123"`, true},
		{` "abc123" `, true},
		{`"x", "abc123"`, true},
		{`"x",W/"abc123"`, true},
		{`*`, true},
		{`"x", *`, true},
		{`"abc1234"`, false},
		{`abc123`, false},
		{`"x", "y"`, false},
		{``, false},
	}
	for _, tc := range cases {
		if got := etagMatches(tc.header, etag); got != tc.want {
			t.Errorf("etagMatches(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

const testUUID = "0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

func validStatus() hubapi.WorkloadStatus {
	return hubapi.WorkloadStatus{
		Workload: hubapi.WorkloadRef{Namespace: "payments", Kind: hubapi.KindDeployment, Name: "checkout"},
		Modes:    hubapi.EffectiveModes{CPU: hubapi.ModeAutoPatch, Memory: hubapi.ModeAudit, CPUSource: "policy", MemorySource: "global-default"},
		Strategy: "in-place", Phase: "Applied", RecommendationID: testUUID,
		Settings: &hubapi.ValueSettings{Profile: hubapi.ProfileDev, CPULimits: hubapi.CPULimitsRatio, CPULimitRatio: "2"},
	}
}

func TestValidateStatus(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*hubapi.WorkloadStatus)
		valid bool
	}{
		{"valid", func(*hubapi.WorkloadStatus) {}, true},
		{"minimal", func(s *hubapi.WorkloadStatus) {
			*s = hubapi.WorkloadStatus{Workload: s.Workload, Modes: hubapi.EffectiveModes{CPU: "off", Memory: "off"}, Phase: "Pending"}
		}, true},
		{"every kind StatefulSet", func(s *hubapi.WorkloadStatus) { s.Workload.Kind = hubapi.KindStatefulSet }, true},
		{"every kind CronJob", func(s *hubapi.WorkloadStatus) { s.Workload.Kind = hubapi.KindCronJob }, true},
		{"phase Blocked", func(s *hubapi.WorkloadStatus) { s.Phase = "Blocked" }, true},
		{"empty settings", func(s *hubapi.WorkloadStatus) { s.Settings = &hubapi.ValueSettings{} }, true},
		{"empty namespace", func(s *hubapi.WorkloadStatus) { s.Workload.Namespace = "" }, false},
		{"empty name", func(s *hubapi.WorkloadStatus) { s.Workload.Name = "" }, false},
		{"slash in name", func(s *hubapi.WorkloadStatus) { s.Workload.Name = "a/b" }, false},
		{"slash in namespace", func(s *hubapi.WorkloadStatus) { s.Workload.Namespace = "a/b" }, false},
		{"bad kind", func(s *hubapi.WorkloadStatus) { s.Workload.Kind = "Pod" }, false},
		{"lowercase kind", func(s *hubapi.WorkloadStatus) { s.Workload.Kind = "deployment" }, false},
		{"bad cpu mode", func(s *hubapi.WorkloadStatus) { s.Modes.CPU = "auto" }, false},
		{"missing memory mode", func(s *hubapi.WorkloadStatus) { s.Modes.Memory = "" }, false},
		{"bad cpu source", func(s *hubapi.WorkloadStatus) { s.Modes.CPUSource = "default" }, false},
		{"bad memory source", func(s *hubapi.WorkloadStatus) { s.Modes.MemorySource = "x" }, false},
		{"bad phase", func(s *hubapi.WorkloadStatus) { s.Phase = "Done" }, false},
		{"missing phase", func(s *hubapi.WorkloadStatus) { s.Phase = "" }, false},
		{"bad strategy", func(s *hubapi.WorkloadStatus) { s.Strategy = "restart" }, false},
		{"bad recommendationID", func(s *hubapi.WorkloadStatus) { s.RecommendationID = "rec-1" }, false},
		{"bad profile", func(s *hubapi.WorkloadStatus) { s.Settings.Profile = "staging" }, false},
		{"bad cpuLimits", func(s *hubapi.WorkloadStatus) { s.Settings.CPULimits = "drop" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validStatus()
			tc.mut(&s)
			err := validateStatus(s)
			if (err == nil) != tc.valid {
				t.Errorf("validateStatus: err = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}

func validEvent() hubapi.Event {
	return hubapi.Event{
		ID: testUUID, Type: "applied", Time: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
		Workload:         hubapi.WorkloadRef{Namespace: "payments", Kind: hubapi.KindDeployment, Name: "checkout"},
		RecommendationID: testUUID, Strategy: "rollout", Resources: []string{"cpu", "memory"}, Reason: "Applied", Message: "ok",
	}
}

func TestValidateEvent(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*hubapi.Event)
		valid bool
	}{
		{"valid", func(*hubapi.Event) {}, true},
		{"minimal", func(e *hubapi.Event) {
			*e = hubapi.Event{ID: e.ID, Type: "failed", Time: e.Time, Workload: e.Workload, Reason: "X"}
		}, true},
		{"every type", func(e *hubapi.Event) { e.Type = "rolled_back" }, true},
		{"message 1024", func(e *hubapi.Event) { e.Message = strings.Repeat("x", 1024) }, true},
		{"reason with digits", func(e *hubapi.Event) { e.Reason = "OOMKilled2" }, true},
		{"bad id", func(e *hubapi.Event) { e.ID = "evt-1" }, false},
		{"missing id", func(e *hubapi.Event) { e.ID = "" }, false},
		{"bad type", func(e *hubapi.Event) { e.Type = "rolledBack" }, false},
		{"missing time", func(e *hubapi.Event) { e.Time = time.Time{} }, false},
		{"empty namespace", func(e *hubapi.Event) { e.Workload.Namespace = "" }, false},
		{"bad kind", func(e *hubapi.Event) { e.Workload.Kind = "Job" }, false},
		{"lowercase reason", func(e *hubapi.Event) { e.Reason = "applied" }, false},
		{"reason with space", func(e *hubapi.Event) { e.Reason = "Not Applied" }, false},
		{"missing reason", func(e *hubapi.Event) { e.Reason = "" }, false},
		{"bad strategy", func(e *hubapi.Event) { e.Strategy = "patch" }, false},
		{"bad resource", func(e *hubapi.Event) { e.Resources = []string{"cpu", "gpu"} }, false},
		{"bad recommendationID", func(e *hubapi.Event) { e.RecommendationID = "nope" }, false},
		{"message 1025", func(e *hubapi.Event) { e.Message = strings.Repeat("x", 1025) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := validEvent()
			tc.mut(&e)
			err := validateEvent(e)
			if (err == nil) != tc.valid {
				t.Errorf("validateEvent: err = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}

func TestParseWorkloadID(t *testing.T) {
	valid := []struct {
		id   string
		want hubapi.WorkloadKey
	}{
		{"ro-spoke-1/payments/Deployment/checkout", hubapi.WorkloadKey{Cluster: "ro-spoke-1", Namespace: "payments", Kind: "Deployment", Name: "checkout"}},
		{"c/ns/StatefulSet/db-0", hubapi.WorkloadKey{Cluster: "c", Namespace: "ns", Kind: "StatefulSet", Name: "db-0"}},
		{"c1/kube-system/DaemonSet/aws-node", hubapi.WorkloadKey{Cluster: "c1", Namespace: "kube-system", Kind: "DaemonSet", Name: "aws-node"}},
		{"c1/batch/CronJob/report.v2", hubapi.WorkloadKey{Cluster: "c1", Namespace: "batch", Kind: "CronJob", Name: "report.v2"}},
	}
	for _, tc := range valid {
		got, err := hubapi.ParseWorkloadID(tc.id)
		if err != nil || got != tc.want {
			t.Errorf("ParseWorkloadID(%q) = %+v, %v", tc.id, got, err)
			continue
		}
		if got.ID() != tc.id {
			t.Errorf("ID() = %q, want %q", got.ID(), tc.id)
		}
		esc := got.EscapedID()
		if strings.Contains(esc, "/") {
			t.Errorf("EscapedID %q contains a slash", esc)
		}
		un, err := url.PathUnescape(esc)
		if err != nil {
			t.Fatal(err)
		}
		back, err := hubapi.ParseWorkloadID(un)
		if err != nil || back != tc.want {
			t.Errorf("round trip via %q: %+v %v", esc, back, err)
		}
	}
	if esc := (hubapi.WorkloadKey{Cluster: "c1", Namespace: "a b", Kind: "Deployment", Name: "x?y#z"}).EscapedID(); esc != "c1%2Fa%20b%2FDeployment%2Fx%3Fy%23z" {
		t.Errorf("EscapedID = %q", esc)
	}
	for _, bad := range []string{
		"", "c1", "c1/ns/Deployment", "c1/ns/Deployment/", "c1//Deployment/x", "C1/ns/Deployment/x", "-c1/ns/Deployment/x",
		"c1/ns/Pod/x", "c1/ns/deployment/x", "c1/ns/Deployment/x/y", "/ns/Deployment/x", "c1%2Fns%2FDeployment%2Fx",
		strings.Repeat("a", 64) + "/ns/Deployment/x",
	} {
		if k, err := hubapi.ParseWorkloadID(bad); err == nil {
			t.Errorf("ParseWorkloadID(%q) = %+v, want error", bad, k)
		}
	}
}

func TestRecommendationPoints(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rec := func(cpu, mem string) hubapi.Resources {
		return hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: cpu, Memory: mem}}
	}
	steps := []store.RecommendedPoint{
		{At: t0, Containers: []hubapi.ContainerRecommendation{
			{Name: "app", Recommended: rec("500m", "256Mi")},
			{Name: "sidecar", Recommended: rec("50m", "")},
		}},
		{At: t0.Add(2 * time.Hour), Containers: []hubapi.ContainerRecommendation{
			{Name: "app", Recommended: rec("250m", "128Mi")},
			{Name: "sidecar", Recommended: hubapi.Resources{}},
		}},
	}
	p := func(d time.Duration, v float64) hubapi.Point { return hubapi.Point{float64(t0.Add(d).Unix()), v} }
	cases := []struct {
		name, container, resource string
		from, to                  time.Time
		step                      time.Duration
		want                      []hubapi.Point
	}{
		{"cpu step function", "app", "cpu", t0.Add(-time.Hour), t0.Add(3 * time.Hour), time.Hour,
			[]hubapi.Point{p(0, 0.5), p(time.Hour, 0.5), p(2*time.Hour, 0.25)}}, // to is exclusive
		{"memory", "app", "memory", t0, t0.Add(2 * time.Hour), time.Hour,
			[]hubapi.Point{p(0, 256*recommender.MiB), p(time.Hour, 256*recommender.MiB)}},
		{"between steps", "app", "cpu", t0.Add(30 * time.Minute), t0.Add(150 * time.Minute), time.Hour,
			[]hubapi.Point{p(30*time.Minute, 0.5), p(90*time.Minute, 0.5)}},
		{"unset value skipped", "sidecar", "cpu", t0, t0.Add(3 * time.Hour), time.Hour,
			[]hubapi.Point{p(0, 0.05), p(time.Hour, 0.05)}},
		{"no memory for sidecar", "sidecar", "memory", t0, t0.Add(3 * time.Hour), time.Hour, []hubapi.Point{}},
		{"unknown container", "nope", "cpu", t0, t0.Add(3 * time.Hour), time.Hour, []hubapi.Point{}},
		{"before first recommendation", "app", "cpu", t0.Add(-5 * time.Hour), t0.Add(-time.Hour), time.Hour, []hubapi.Point{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recommendationPoints(steps, tc.container, tc.resource, tc.from, tc.to, tc.step)
			if got == nil {
				t.Fatal("nil slice (must encode as [])")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v\nwant %v", got, tc.want)
			}
		})
	}
	if got := recommendationPoints(nil, "app", "cpu", t0, t0.Add(time.Hour), time.Minute); got == nil || len(got) != 0 {
		t.Errorf("no steps: %v", got)
	}
}

func TestPromDur(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Hour: "1h", 24 * time.Hour: "24h", 15 * time.Minute: "15m", 90 * time.Second: "90s", 500 * time.Millisecond: "1s",
	} {
		if got := promDur(d); got != want {
			t.Errorf("promDur(%s) = %q, want %q", d, got, want)
		}
	}
}

func newUnitServer(origins ...string) *Server {
	cfg := DefaultConfig()
	cfg.CORSOrigins = origins
	return &Server{Cfg: cfg, Metrics: NewMetrics(), Log: discardLog()}
}

func TestCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("next")) })
	cases := []struct {
		name      string
		origins   []string
		method    string
		origin    string
		reqMethod string
		wantCode  int
		wantACAO  string
		wantNext  bool
		preflight bool
	}{
		{"preflight allowed", []string{"http://localhost:5173/"}, http.MethodOptions, "http://localhost:5173", "GET", 204, "http://localhost:5173", false, true},
		{"simple allowed", []string{"http://localhost:5173"}, http.MethodGet, "http://localhost:5173", "", 200, "http://localhost:5173", true, false},
		{"options without request method", []string{"http://localhost:5173"}, http.MethodOptions, "http://localhost:5173", "", 200, "http://localhost:5173", true, false},
		{"preflight disallowed", []string{"http://localhost:5173"}, http.MethodOptions, "http://evil.example", "GET", 200, "", true, false},
		{"simple disallowed", []string{"http://localhost:5173"}, http.MethodGet, "http://evil.example", "", 200, "", true, false},
		{"no origin", []string{"http://localhost:5173"}, http.MethodGet, "", "", 200, "", true, false},
		{"no origins configured", nil, http.MethodOptions, "http://localhost:5173", "GET", 200, "", true, false},
		{"wildcard", []string{"*"}, http.MethodOptions, "http://any.example", "POST", 204, "http://any.example", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newUnitServer(tc.origins...)
			req := httptest.NewRequest(tc.method, "/v1/summary", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.reqMethod != "" {
				req.Header.Set("Access-Control-Request-Method", tc.reqMethod)
			}
			rr := httptest.NewRecorder()
			s.cors(next).ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", rr.Code, tc.wantCode)
			}
			h := rr.Header()
			if got := h.Get("Access-Control-Allow-Origin"); got != tc.wantACAO {
				t.Errorf("Allow-Origin = %q, want %q", got, tc.wantACAO)
			}
			if (rr.Body.String() == "next") != tc.wantNext {
				t.Errorf("next called = %v, want %v", rr.Body.String() == "next", tc.wantNext)
			}
			if tc.wantACAO != "" {
				if h.Get("Vary") != "Origin" || h.Get("Access-Control-Expose-Headers") != "ETag" {
					t.Errorf("Vary/Expose: %v", h)
				}
			} else if len(h.Values("Access-Control-Allow-Methods"))+len(h.Values("Access-Control-Expose-Headers")) > 0 {
				t.Errorf("CORS headers for disallowed origin: %v", h)
			}
			if tc.preflight {
				if !strings.Contains(h.Get("Access-Control-Allow-Methods"), tc.reqMethod) ||
					!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
					!strings.Contains(h.Get("Access-Control-Allow-Headers"), "If-None-Match") ||
					h.Get("Access-Control-Max-Age") == "" {
					t.Errorf("preflight headers: %v", h)
				}
			} else if h.Get("Access-Control-Allow-Methods") != "" {
				t.Errorf("Allow-Methods on non-preflight: %v", h)
			}
		})
	}
}

// TestHandlerWithoutStore covers the paths that never reach the database.
func TestHandlerWithoutStore(t *testing.T) {
	s := newUnitServer("http://localhost:5173")
	h := s.Handler()
	do := func(method, path string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	// Preflight through the full handler: no auth needed.
	rr := do(http.MethodOptions, "/v1/clusters/c1/recommendations", map[string]string{"Origin": "http://localhost:5173", "Access-Control-Request-Method": "GET"})
	if rr.Code != http.StatusNoContent || rr.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("preflight: %d %v", rr.Code, rr.Header())
	}
	if rr := do(http.MethodGet, "/healthz", nil); rr.Code != 200 || rr.Body.String() != "ok\n" {
		t.Errorf("healthz: %d %q", rr.Code, rr.Body.String())
	}
	if rr := do(http.MethodGet, "/readyz", nil); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz before SetReady: %d", rr.Code)
	}
	for _, tc := range []struct {
		name, method, path, auth, code string
		status                         int
	}{
		{"no token", "GET", "/v1/summary", "", "unauthorized", 401},
		{"basic auth", "GET", "/v1/clusters/c1/recommendations", "Basic dXNlcjpwYXNz", "unauthorized", 401},
		{"empty bearer", "GET", "/v1/workloads", "Bearer   ", "unauthorized", 401},
		{"no token on POST", "POST", "/v1/clusters/c1/events", "", "unauthorized", 401},
		{"unknown path", "GET", "/v2/nothing", "", "not_found", 404},
		{"root", "GET", "/", "", "not_found", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hdr := map[string]string{}
			if tc.auth != "" {
				hdr["Authorization"] = tc.auth
			}
			rr := do(tc.method, tc.path, hdr)
			p := checkProblem(t, rr.Code, rr.Header(), rr.Body.Bytes(), tc.status, tc.code)
			if p.Instance != tc.path {
				t.Errorf("instance = %q", p.Instance)
			}
			if tc.status == 401 && !strings.HasPrefix(rr.Header().Get("WWW-Authenticate"), "Bearer") {
				t.Errorf("WWW-Authenticate = %q", rr.Header().Get("WWW-Authenticate"))
			}
		})
	}
	// /metrics works before any refresh.
	if rr := do(http.MethodGet, "/metrics", nil); rr.Code != 200 || !strings.Contains(rr.Body.String(), "ro_hub_http_requests_total") {
		t.Errorf("metrics: %d", rr.Code)
	}
}

// checkProblem asserts an RFC 9457 problem response.
func checkProblem(t *testing.T, gotStatus int, h http.Header, body []byte, status int, code string) hubapi.Problem {
	t.Helper()
	if gotStatus != status {
		t.Errorf("status = %d, want %d (body %s)", gotStatus, status, body)
	}
	if ct := h.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var p hubapi.Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("problem body %q: %v", body, err)
	}
	if p.Code != code || p.Status != status || p.Title != http.StatusText(status) || p.Detail == "" {
		t.Errorf("problem = %+v, want code %q status %d", p, code, status)
	}
	return p
}

func TestDevSeedTokens(t *testing.T) {
	// Mimic a mounted Secret volume:
	//   ..2026_11_04_12_00_00.000000001/{ro-spoke-1,ui,admin}
	//   ..data -> ..2026_11_04_12_00_00.000000001
	//   ro-spoke-1 -> ..data/ro-spoke-1   (and ui, admin)
	dir := t.TempDir()
	ts := "..2026_11_04_12_00_00.000000001"
	if err := os.Mkdir(filepath.Join(dir, ts), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"ro-spoke-1": "tok-spoke\n", "ui": "  tok-ui-file \n", "admin": "tok-admin"}
	for name, content := range files {
		writeFile(t, filepath.Join(dir, ts), name, content)
	}
	if err := os.Symlink(ts, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A regular subdirectory is ignored too.
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := DevSeedTokens(DevSeed{TokensDir: dir, Tokens: map[string]string{"ui": "tok-ui-inline", "synth-a": " tok-synth \n"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ro-spoke-1": "tok-spoke", "ui": "tok-ui-file", "admin": "tok-admin", "synth-a": "tok-synth"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %v, want %v", got, want)
	}

	inline, err := DevSeedTokens(DevSeed{Tokens: map[string]string{"c1": " a\n"}})
	if err != nil || !reflect.DeepEqual(inline, map[string]string{"c1": "a"}) {
		t.Errorf("inline only: %v %v", inline, err)
	}
	if empty, err := DevSeedTokens(DevSeed{}); err != nil || len(empty) != 0 {
		t.Errorf("empty: %v %v", empty, err)
	}
	if _, err := DevSeedTokens(DevSeed{TokensDir: filepath.Join(dir, "missing")}); err == nil {
		t.Error("missing dir: no error")
	}
}

func TestMetricsCountAction(t *testing.T) {
	m := NewMetrics()
	m.countAction("c1", hubapi.Event{Type: "applied", Strategy: "in-place", Resources: []string{"cpu", "memory"}, Reason: "Applied"})
	m.countAction("c1", hubapi.Event{Type: "blocked", Reason: "PDBViolation"})
	s := newUnitServer()
	s.Metrics = m
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	ms := parseMetrics(rr.Body.String())
	for _, want := range []struct {
		labels map[string]string
		value  string
	}{
		{map[string]string{"cluster": "c1", "type": "applied", "strategy": "in-place", "resource": "cpu", "reason": "Applied"}, "1"},
		{map[string]string{"cluster": "c1", "type": "applied", "strategy": "in-place", "resource": "memory", "reason": "Applied"}, "1"},
		{map[string]string{"cluster": "c1", "type": "blocked", "strategy": "", "resource": "", "reason": "PDBViolation"}, "1"},
	} {
		if v, ok := ms.find("ro_actions_total", want.labels); !ok || v != want.value {
			t.Errorf("ro_actions_total%v = %q (found %v)", want.labels, v, ok)
		}
	}
}

// ---- Prometheus exposition parsing ----

type metricLine struct {
	name   string
	labels map[string]string
	value  string
}

type metricSet []metricLine

func parseMetrics(text string) metricSet {
	var out metricSet
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ml := metricLine{labels: map[string]string{}}
		var rest string
		if i := strings.IndexByte(line, '{'); i >= 0 {
			j := strings.LastIndexByte(line, '}')
			ml.name = line[:i]
			for _, kv := range splitLabels(line[i+1 : j]) {
				k, v, _ := strings.Cut(kv, "=")
				ml.labels[k] = strings.Trim(v, `"`)
			}
			rest = strings.TrimSpace(line[j+1:])
		} else {
			ml.name, rest, _ = strings.Cut(line, " ")
		}
		ml.value = strings.Fields(rest)[0]
		out = append(out, ml)
	}
	return out
}

func splitLabels(s string) []string {
	var out []string
	inQ := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			inQ = !inQ
		case ',':
			if !inQ {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// find returns the value of the first series with the name whose labels include want (a label
// with value "" must be absent or empty).
func (ms metricSet) find(name string, want map[string]string) (string, bool) {
	for _, m := range ms {
		if m.name != name {
			continue
		}
		ok := true
		for k, v := range want {
			if m.labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return m.value, true
		}
	}
	return "", false
}

// A request canceled by the client is not an error and gets no 500 body.
func TestInternalClientCanceled(t *testing.T) {
	var buf strings.Builder
	s := &Server{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr := httptest.NewRecorder()
	s.internal(rr, httptest.NewRequest(http.MethodGet, "/v1/summary", nil).WithContext(ctx), fmt.Errorf("query: %w", context.Canceled))
	if rr.Body.Len() != 0 || strings.Contains(buf.String(), "ERROR") {
		t.Errorf("canceled request: body %q log %q", rr.Body.String(), buf.String())
	}
	rr = httptest.NewRecorder()
	s.internal(rr, httptest.NewRequest(http.MethodGet, "/v1/summary", nil), errors.New("boom"))
	if rr.Code != http.StatusInternalServerError || !strings.Contains(buf.String(), "ERROR") {
		t.Errorf("real error: %d %q", rr.Code, buf.String())
	}
}
