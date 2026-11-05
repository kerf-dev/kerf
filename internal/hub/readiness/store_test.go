package readiness_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/readiness"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

func testDB(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("KERF_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("KERF_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "kerf_rd_test_" + hex.EncodeToString(b)
	adminConn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminConn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminConn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		adminConn.Close(context.Background())
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Migrate(ctx, os.DirFS("../../../db/migrations")); err != nil {
		t.Fatal(err)
	}
	return st
}

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func pass(at time.Time, ids ...string) []hubapi.ReadinessCheck {
	var out []hubapi.ReadinessCheck
	for _, id := range ids {
		out = append(out, readiness.NewCheck(id, rd.StatusPass, "ok", "", "", "", at, nil))
	}
	return out
}

func blockingIDs(source rd.Source) []string {
	var out []string
	for _, c := range rd.Catalog {
		if c.Source == source && c.Severity == rd.SeverityBlocking {
			out = append(out, c.ID)
		}
	}
	return out
}

func TestStore(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO clusters (name) VALUES ('ro-spoke-2'), ('ro-spoke-1')`); err != nil {
		t.Fatal(err)
	}
	g := readiness.NewGauges()
	s := &readiness.Store{DB: db.Pool, Gauges: g}

	// nothing stored yet
	r, err := s.Get(ctx, "ro-spoke-2")
	if err != nil || r.State != "Registered" || r.NextState != "AgentConnected" || len(r.Checks) != 0 {
		t.Fatalf("empty %v %+v", err, r)
	}

	// hub checks: agent.connected + metrics + recommendations
	hub := pass(t0, append(append(readiness.MetricsCheckIDs(), rd.CheckAgentConnected), rd.CheckRecommendations)...)
	a, err := s.Apply(ctx, "ro-spoke-2", readiness.Update{Checks: hub, ReplaceIDs: readiness.MetricsCheckIDs(), At: t0})
	if err != nil {
		t.Fatal(err)
	}
	if a.State != rd.StateRegistered || a.Transitioned() {
		t.Errorf("without agent checks: %+v", a.Evaluation)
	}

	// agent checks arrive, with a pending re-check before them
	if err := s.RequestRecheck(ctx, "ro-spoke-2", t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if rt, _ := s.RecheckRequestedAt(ctx, "ro-spoke-2"); rt == nil || !rt.Equal(t0.Add(30*time.Second)) {
		t.Errorf("recheck %v", rt)
	}
	caps := []hubapi.AgentCapability{{Name: "resize", Granted: true}}
	agent := pass(t0.Add(time.Minute), blockingIDs(rd.SourceAgent)...)
	agent = append(agent, readiness.NewCheck(rd.CheckArgoCD, rd.StatusWarn, "argocd-cm lacks managedFieldsManagers", "snippet", hubapi.RemediationYAML, "https://docs", t0.Add(time.Minute), map[string]string{"namespace": "argocd"}))
	a, err = s.Apply(ctx, "ro-spoke-2", readiness.Update{Checks: agent, ReplaceIDs: readiness.AgentCheckIDs(), Capabilities: caps, At: t0.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if a.State != rd.StateAutomationReady || a.Prev != rd.StateRegistered || !a.Transitioned() {
		t.Fatalf("all pass: %+v", a)
	}
	if rt, _ := s.RecheckRequestedAt(ctx, "ro-spoke-2"); rt != nil {
		t.Errorf("recheck not cleared: %v", rt)
	}

	// rbac.resize lost
	lost := make([]hubapi.ReadinessCheck, len(agent))
	copy(lost, agent)
	for i := range lost {
		lost[i].CheckedAt = t0.Add(10 * time.Minute)
		if lost[i].ID == rd.CheckRBACResize {
			lost[i].Status, lost[i].Message = "fail", "SelfSubjectAccessReview denied patch pods/resize"
		}
	}
	// and the agent stops reporting the Argo CD check (replaced set)
	lost = lost[:len(lost)-1]
	a, err = s.Apply(ctx, "ro-spoke-2", readiness.Update{Checks: lost, ReplaceIDs: readiness.AgentCheckIDs(), Capabilities: []hubapi.AgentCapability{{Name: "resize", Missing: []string{`{"apiGroups":[""],"resources":["pods/resize"],"verbs":["patch"]}`}}}, At: t0.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if a.State != rd.StateReadyAudit || a.Prev != rd.StateAutomationReady || strings.Join(a.BlockedBy, ",") != rd.CheckRBACResize {
		t.Fatalf("resize lost: %+v", a)
	}

	r, err = s.Get(ctx, "ro-spoke-2")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "ReadyAudit" || r.NextState != "AutomationReady" || strings.Join(r.BlockedBy, ",") != rd.CheckRBACResize ||
		r.Summary.Blocking != 1 || r.StateSince == nil || !r.StateSince.Equal(t0.Add(10*time.Minute)) || len(r.Capabilities) != 1 || r.Capabilities[0].Granted {
		t.Errorf("get %+v", r)
	}
	for _, c := range r.Checks {
		if c.ID == rd.CheckArgoCD {
			t.Error("replaced agent check kept")
		}
		if c.ID == rd.CheckRBACResize && (c.Gates != "AutomationReady" || c.Message == "") {
			t.Errorf("resize check %+v", c)
		}
	}

	// history: newest first, transitions and status changes
	h, err := s.History(ctx, "ro-spoke-2", t0.Add(-time.Hour), time.Time{}, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Items) != 3 || h.Continue == "" {
		t.Fatalf("history page %+v", h)
	}
	var kinds []string
	for _, e := range h.Items {
		kinds = append(kinds, e.Kind+":"+e.CheckID+e.ToState+e.ToStatus)
	}
	joined := strings.Join(kinds, " ")
	if !strings.Contains(joined, "state:ReadyAudit") || !strings.Contains(joined, "check:rbac.resizefail") {
		t.Errorf("history %v", kinds)
	}
	next, err := s.History(ctx, "ro-spoke-2", t0.Add(-time.Hour), time.Time{}, 1000, h.Continue)
	if err != nil || len(next.Items) == 0 {
		t.Fatalf("next page %v %d", err, len(next.Items))
	}

	// config audit rows
	rows, err := db.Pool.Query(ctx, `SELECT message, actor, before, after FROM audit_events WHERE type = 'config_changed' AND reason = 'ReadinessChanged' AND target_type = 'readiness' ORDER BY time`)
	if err != nil {
		t.Fatal(err)
	}
	var audits []string
	for rows.Next() {
		var msg string
		var actor, before, after []byte
		if err := rows.Scan(&msg, &actor, &before, &after); err != nil {
			t.Fatal(err)
		}
		var ac hubapi.Actor
		_ = json.Unmarshal(actor, &ac)
		if ac.Type != "system" {
			t.Errorf("actor %s", actor)
		}
		audits = append(audits, msg+" "+string(after))
	}
	rows.Close()
	if len(audits) != 2 || !strings.HasPrefix(audits[1], "AutomationReady → ReadyAudit: rbac.resize fail") || !strings.Contains(audits[1], `"blockedBy": ["rbac.resize"]`) {
		t.Errorf("audits %v", audits)
	}

	// summaries and states
	sums, err := s.Summaries(ctx)
	if err != nil || sums["ro-spoke-2"].State != "ReadyAudit" || sums["ro-spoke-2"].Blocking != 1 {
		t.Errorf("summaries %v %+v", err, sums)
	}
	states, _ := s.States(ctx)
	if states["ro-spoke-2"] != rd.StateReadyAudit {
		t.Errorf("states %v", states)
	}
	if err := s.Refresh(ctx, []string{"ro-spoke-1", "ro-spoke-2"}); err != nil {
		t.Fatal(err)
	}
	if rt, err := s.RecheckRequestedAt(ctx, "unknown-cluster"); err != nil || rt != nil {
		t.Errorf("unknown cluster recheck %v %v", rt, err)
	}
	if _, err := s.History(ctx, "ro-spoke-2", time.Time{}, time.Time{}, 0, "x"); err == nil {
		t.Error("bad continue token accepted")
	}
}
