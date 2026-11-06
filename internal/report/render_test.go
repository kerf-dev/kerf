package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestRender(t *testing.T) {
	fxs := loadFixtures(t)
	rep, _ := reportFor(t, fxs, hubapi.ProfileProduction)
	render := func(t *testing.T, format string, ro RenderOptions) string {
		t.Helper()
		var b bytes.Buffer
		if err := Render(&b, rep, format, ro); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Run("table", func(t *testing.T) {
		out := render(t, FormatTable, RenderOptions{})
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if last := lines[len(lines)-1]; last != NextStep || !strings.Contains(last, InstallURL) {
			t.Errorf("last line = %q, want the pointer to the full install", last)
		}
		if !strings.Contains(out, "synth-ledger-store-0") || !strings.Contains(out, "SAVING/MO") || !strings.Contains(out, "default rates") {
			t.Errorf("table lacks the header, a workload or the price source:\n%s", out)
		}
		// Sorted by saving: the best saving is the first data row.
		var first string
		for i, l := range lines {
			if strings.HasPrefix(l, "NAMESPACE") {
				first = lines[i+1]
				break
			}
		}
		if !strings.Contains(first, "synth-ledger-store-0") {
			t.Errorf("first row = %q, want the workload with the highest saving", first)
		}
		// Aligned: the saving column starts at the same offset in the header and every row.
		col := -1
		for _, l := range lines {
			if strings.HasPrefix(l, "NAMESPACE") {
				col = strings.Index(l, "SAVING/MO")
			}
		}
		for _, l := range lines {
			if strings.HasPrefix(l, "patterns") && strings.Index(l, "$") != col {
				t.Errorf("row not aligned (saving at %d, want %d): %q", strings.Index(l, "$"), col, l)
			}
		}
	})

	t.Run("limit and min-saving", func(t *testing.T) {
		out := render(t, FormatTable, RenderOptions{Limit: 3})
		if n := strings.Count(out, "\npatterns"); n != 3 {
			t.Errorf("%d rows, want 3", n)
		}
		if !strings.Contains(out, "12 more workloads not shown") {
			t.Errorf("missing the hidden count:\n%s", out)
		}
		out = render(t, FormatTable, RenderOptions{MinSavingUSD: 50})
		if n := strings.Count(out, "\npatterns"); n != 2 {
			t.Errorf("%d rows over $50, want 2", n)
		}
	})

	t.Run("csv", func(t *testing.T) {
		out := render(t, FormatCSV, RenderOptions{})
		recs, err := csv.NewReader(strings.NewReader(out)).ReadAll()
		if err != nil {
			t.Fatalf("not valid CSV: %v", err)
		}
		if len(recs) != len(rep.Rows)+1 || recs[0][0] != "namespace" || recs[1][2] != "synth-ledger-store-0" {
			t.Errorf("%d records, header %v, first %v", len(recs), recs[0], recs[1])
		}
		if strings.Contains(out, InstallURL) {
			t.Error("CSV must contain data only")
		}
	})

	t.Run("json", func(t *testing.T) {
		out := render(t, FormatJSON, RenderOptions{Limit: 2})
		var got struct {
			Cluster   string
			Profile   string
			Workloads []Row
			Totals    Totals
			Next      string
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("not valid JSON: %v\n%s", err, out)
		}
		if got.Cluster != "synth-a" || len(got.Workloads) != 2 || got.Totals.Workloads != len(rep.Rows) || !strings.Contains(got.Next, InstallURL) {
			t.Errorf("json = %+v", got)
		}
	})

	t.Run("markdown", func(t *testing.T) {
		out := render(t, FormatMarkdown, RenderOptions{})
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if last := lines[len(lines)-1]; last != NextStep {
			t.Errorf("last line = %q", last)
		}
		if !strings.Contains(out, "|---|---|---:|") || !strings.Contains(out, "| patterns | StatefulSet/synth-ledger-store-0 |") {
			t.Errorf("markdown table missing:\n%s", out)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		if err := Render(&bytes.Buffer{}, rep, "xml", RenderOptions{}); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		o       Options
		wantErr string
	}{
		{"ok", Options{MetricsURL: "http://prom:9090"}, ""},
		{"url required", Options{}, "--prometheus-url is required"},
		{"url scheme", Options{MetricsURL: "prom:9090"}, "want an http(s) URL"},
		{"profile", Options{MetricsURL: "http://p", Profile: "fast"}, "--profile"},
		{"matcher", Options{MetricsURL: "http://p", ExtraMatchers: []string{"env"}}, "extraMatcher"},
		{"token and user", Options{MetricsURL: "http://p", Auth: Auth{Token: "t", Username: "u"}}, "not both"},
		{"cert without key", Options{MetricsURL: "http://p", Auth: Auth{ClientCertPEM: "x"}}, "go together"},
		{"mtls with token", Options{MetricsURL: "http://p", Auth: Auth{ClientCertPEM: "x", ClientKeyPEM: "y", Token: "t"}}, "mTLS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.o.Validate()
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatal(err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
		})
	}
}

func TestParseClusterLabel(t *testing.T) {
	for in, want := range map[string][2]string{"": {"", ""}, "cluster=prod-eu-1": {"cluster", "prod-eu-1"}, "k8s_cluster=a=b": {"k8s_cluster", "a=b"}} {
		l, v, err := ParseClusterLabel(in)
		if err != nil || l != want[0] || v != want[1] {
			t.Errorf("%q -> %q %q %v", in, l, v, err)
		}
	}
	for _, in := range []string{"cluster", "cluster=", "=x", "9bad=x", "a-b=x"} {
		if _, _, err := ParseClusterLabel(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestParseHeader(t *testing.T) {
	for in, want := range map[string][2]string{"X-Scope-OrgID: t1": {"X-Scope-OrgID", "t1"}, "A=b": {"A", "b"}} {
		n, v, err := ParseHeader(in)
		if err != nil || n != want[0] || v != want[1] {
			t.Errorf("%q -> %q %q %v", in, n, v, err)
		}
	}
	if _, _, err := ParseHeader("novalue"); err == nil {
		t.Error("expected an error")
	}
}
