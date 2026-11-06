package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSummary(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	events := []event{
		{Time: at(0), Action: "start"},
		{Time: at(20), Action: "run", Test: "TestA"},
		{Time: at(20), Action: "run", Test: "TestB"},
		{Time: at(30), Action: "output", Test: "TestA", Output: "    k8s_test.go:170: hub recommendation for e2e-a/web after 1m3s\n"},
		{Time: at(31), Action: "output", Test: "TestA", Output: "    k8s_test.go:190: audit leaves the workload alone held for 45s\n"},
		{Time: at(32), Action: "output", Test: "TestA", Output: "    some other log line\n"},
		{Time: at(33), Action: "run", Test: "TestA/sub"},
		{Time: at(34), Action: "output", Test: "TestA/sub", Output: "    admin_client_test.go:534: spoke-2 lock taken after 2m0s\n"},
		{Time: at(35), Action: "pass", Test: "TestA/sub", Elapsed: 2},
		{Time: at(40), Action: "output", Test: "TestB", Output: "    k8s_test.go:200: slept 1m0s: two agent pulls\n"},
		{Time: at(140), Action: "pass", Test: "TestA", Elapsed: 120},
		{Time: at(150), Action: "fail", Test: "TestB", Elapsed: 130},
		{Time: at(151), Action: "fail", Elapsed: 151},
	}
	s := newSummary()
	for _, e := range events {
		s.add(e)
	}
	var b bytes.Buffer
	s.print(&b, 1, t0.Add(-10*time.Second))
	out := b.String()
	for _, want := range []string{
		"| TestB ", "| FAIL ", "130.0", "| TestA ", "| PASS ", "120.0", "| TestA/sub",
		"setup (make e2e start -> first test started): 30s; wall (make e2e start -> last result): 2m41s",
		"-- TestB (130s): waits --", "slept 1m0s: two agent pulls",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "| TestB ") > strings.Index(out, "| TestA ") {
		t.Errorf("not sorted by duration:\n%s", out)
	}
	// Only the slowest top-level test's waits are listed.
	if strings.Contains(out, "hub recommendation for e2e-a/web after 1m3s") {
		t.Errorf("waits of TestA listed with -slowest 1:\n%s", out)
	}
	s.print(&b, 5, time.Time{})
	for _, want := range []string{"hub recommendation for e2e-a/web after 1m3s", "audit leaves the workload alone held for 45s", "sub: spoke-2 lock taken after 2m0s"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("waits lack %q", want)
		}
	}
	if strings.Contains(b.String(), "some other log line") {
		t.Error("non-wait line listed as a wait")
	}
}
