package readiness

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func chk(id, status string) hubapi.ReadinessCheck {
	return NewCheck(id, rd.Status(status), "m-"+id, "fix", hubapi.RemediationText, "", now, nil)
}

// allPass returns a passing result for every blocking check gating up to st.
func allPass(st rd.State) []hubapi.ReadinessCheck {
	var out []hubapi.ReadinessCheck
	for _, c := range rd.Catalog {
		if c.Severity == rd.SeverityBlocking && st.AtLeast(c.Gates) {
			out = append(out, chk(c.ID, "pass"))
		}
	}
	return out
}

func without(cs []hubapi.ReadinessCheck, id, status string) []hubapi.ReadinessCheck {
	var out []hubapi.ReadinessCheck
	for _, c := range cs {
		if c.ID == id {
			if status == "" {
				continue
			}
			c.Status = status
		}
		out = append(out, c)
	}
	return out
}

func TestEvaluate(t *testing.T) {
	full := append(allPass(rd.StateAutomationReady), chk(rd.CheckHistory, "warn"), chk(rd.CheckHPASeries, "unknown"), chk(rd.CheckCertManager, "fail"))
	for _, tc := range []struct {
		name      string
		checks    []hubapi.ReadinessCheck
		state     rd.State
		next      rd.State
		blockedBy string
		counts    hubapi.ReadinessCounts
	}{
		{"nothing", nil, rd.StateRegistered, rd.StateAgentConnected, "agent.connected,agent.version,agent.crds,rbac.read", hubapi.ReadinessCounts{}},
		{"all", full, rd.StateAutomationReady, "", "", hubapi.ReadinessCounts{Warnings: 1, Unknown: 1}},
		{"resize lost", without(full, rd.CheckRBACResize, "fail"), rd.StateReadyAudit, rd.StateAutomationReady, "rbac.resize", hubapi.ReadinessCounts{Blocking: 1, Warnings: 1, Unknown: 1}},
		{"ksm down", without(full, rd.CheckKSMOwners, "fail"), rd.StateAgentConnected, rd.StateMetricsOK, "metrics.ksm-owners", hubapi.ReadinessCounts{Blocking: 1, Warnings: 1, Unknown: 1}},
		{"no recommendations yet", without(allPass(rd.StateAutomationReady), rd.CheckRecommendations, ""), rd.StateMetricsOK, rd.StateReadyAudit, "metrics.recommendations", hubapi.ReadinessCounts{}},
		{"unknown blocking", without(full, rd.CheckFreshness, "unknown"), rd.StateAgentConnected, rd.StateMetricsOK, "metrics.freshness", hubapi.ReadinessCounts{Warnings: 1, Unknown: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Evaluate(tc.checks)
			if e.State != tc.state || e.NextState != tc.next || strings.Join(e.BlockedBy, ",") != tc.blockedBy || e.Summary != tc.counts {
				t.Errorf("got %+v", e)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	in := []hubapi.ReadinessCheck{
		{ID: rd.CheckRBACResize, Status: "fail", Severity: "info", Category: "x"}, // agent lies about severity
		{ID: "made.up", Status: "pass"},
		{ID: rd.CheckClockSkew, Status: "broken"},
		{ID: rd.CheckAgentVersion, Status: "pass"},
		{ID: rd.CheckRBACResize, Status: "pass"}, // last wins
	}
	out := Normalize(in)
	if len(out) != 2 || out[0].ID != rd.CheckAgentVersion || out[1].ID != rd.CheckRBACResize {
		t.Fatalf("normalized %+v", out)
	}
	if r := out[1]; r.Status != "pass" || r.Severity != "blocking" || r.Category != "rbac" || r.Gates != "AutomationReady" || r.Source != "agent" {
		t.Errorf("catalog fields %+v", r)
	}
}

func TestDiffAndMessage(t *testing.T) {
	prev := allPass(rd.StateAutomationReady)
	next := without(prev, rd.CheckRBACResize, "fail")
	ch := Diff(rd.StateAutomationReady, prev, next, rd.StateReadyAudit, now)
	if len(ch) != 2 || ch[0].Kind != "check" || ch[0].CheckID != rd.CheckRBACResize || ch[0].FromStatus != "pass" || ch[0].ToStatus != "fail" {
		t.Fatalf("diff %+v", ch)
	}
	if ch[1].Kind != "state" || ch[1].FromState != "AutomationReady" || ch[1].ToState != "ReadyAudit" || ch[1].Message != "AutomationReady → ReadyAudit: rbac.resize fail" {
		t.Errorf("state change %+v", ch[1])
	}
	if m := TransitionMessage(rd.StateReadyAudit, rd.StateAutomationReady, prev); m != "ReadyAudit → AutomationReady" {
		t.Errorf("up message %q", m)
	}
	if m := TransitionMessage("", rd.StateRegistered, nil); m != "→ Registered" {
		t.Errorf("first message %q", m)
	}
	if ch := Diff(rd.StateRegistered, nil, []hubapi.ReadinessCheck{chk(rd.CheckAgentConnected, "pass")}, rd.StateRegistered, now); len(ch) != 1 || ch[0].FromStatus != "" {
		t.Errorf("new check %+v", ch)
	}
}

func TestGating(t *testing.T) {
	states := map[string]rd.State{"a": rd.StateAutomationReady, "b": rd.StateReadyAudit}
	nr := NotAutomationReady(states, []string{"a", "b", "c"})
	if len(nr) != 2 || nr["b"] != rd.StateReadyAudit || nr["c"] != rd.StateRegistered {
		t.Errorf("not ready %v", nr)
	}
	if d := GateDetail(nr); !strings.Contains(d, "b (ReadyAudit), c (Registered)") || !strings.Contains(d, "overrideReadiness") {
		t.Errorf("detail %q", d)
	}
	if len(NotAutomationReady(states, []string{"a"})) != 0 {
		t.Error("ready cluster gated")
	}
}

func TestHubChecks(t *testing.T) {
	last := now.Add(-90 * time.Second)
	if c := AgentConnectedCheck(&last, time.Minute, now, ""); c.Status != "pass" || c.Details["lastPull"] == "" {
		t.Errorf("connected %+v", c)
	}
	old := now.Add(-4 * time.Minute)
	if c := AgentConnectedCheck(&old, time.Minute, now, ""); c.Status != "fail" || c.Remediation == "" {
		t.Errorf("stale %+v", c)
	}
	if c := AgentConnectedCheck(nil, 0, now, ""); c.Status != "fail" || !strings.Contains(c.Message, "never") {
		t.Errorf("never %+v", c)
	}
	if c := RecommendationsCheck(10, 8, now, ""); c.Status != "pass" {
		t.Errorf("recs %+v", c)
	}
	if c := RecommendationsCheck(10, 0, now, ""); c.Status != "fail" {
		t.Errorf("no recs %+v", c)
	}
	if c := RecommendationsCheck(0, 0, now, ""); c.Status != "fail" || !strings.Contains(c.Message, "no live workloads") {
		t.Errorf("no workloads %+v", c)
	}
}

func TestGauges(t *testing.T) {
	g := NewGauges()
	reg := prometheus.NewRegistry()
	if err := g.Register(reg); err != nil {
		t.Fatal(err)
	}
	g.Set("ro-spoke-2", rd.StateReadyAudit, without(allPass(rd.StateAutomationReady), rd.CheckRBACResize, "fail"))
	if v := testutil.ToFloat64(g.checks.WithLabelValues("ro-spoke-2", rd.CheckRBACResize, "blocking")); v != 0 {
		t.Errorf("resize gauge %v", v)
	}
	if v := testutil.ToFloat64(g.checks.WithLabelValues("ro-spoke-2", rd.CheckRBACRead, "blocking")); v != 1 {
		t.Errorf("read gauge %v", v)
	}
	if v := testutil.ToFloat64(g.state.WithLabelValues("ro-spoke-2", "ReadyAudit")); v != 1 {
		t.Errorf("state gauge %v", v)
	}
	if n := testutil.CollectAndCount(g.checks); n != len(rd.Catalog) {
		t.Errorf("%d check series, want one per catalog check (%d)", n, len(rd.Catalog))
	}
	if n := testutil.CollectAndCount(g.state); n != len(rd.States) {
		t.Errorf("%d state series", n)
	}
	g.Delete("ro-spoke-2")
	if n := testutil.CollectAndCount(g.checks); n != 0 {
		t.Errorf("%d series after delete", n)
	}
}

func TestRunner(t *testing.T) {
	var mu sync.Mutex
	runs := map[string]int{}
	done := make(chan struct{}, 10)
	r := &Runner{
		Clusters: func(context.Context) ([]string, error) { return []string{"a", "b"}, nil },
		Check: func(_ context.Context, c string) error {
			mu.Lock()
			runs[c]++
			mu.Unlock()
			done <- struct{}{}
			return nil
		},
		Interval: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	<-done
	<-done
	if !r.Trigger("b") {
		t.Fatal("trigger dropped")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("trigger not run")
	}
	mu.Lock()
	defer mu.Unlock()
	if runs["a"] != 1 || runs["b"] != 2 {
		t.Errorf("runs %v", runs)
	}
}
