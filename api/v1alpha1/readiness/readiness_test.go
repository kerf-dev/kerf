package readiness

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var idRE = regexp.MustCompile(`^[a-z]+\.[a-z0-9-]+$`)

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	cats := map[Category]bool{}
	for _, c := range Categories {
		cats[c] = true
	}
	for _, c := range Catalog {
		if !idRE.MatchString(c.ID) {
			t.Errorf("%s: id must match %s", c.ID, idRE)
		}
		if seen[c.ID] {
			t.Errorf("%s: duplicate id", c.ID)
		}
		seen[c.ID] = true
		if !strings.HasPrefix(c.ID, string(c.Category)+".") {
			t.Errorf("%s: id must start with its category %q", c.ID, c.Category)
		}
		if !cats[c.Category] {
			t.Errorf("%s: unknown category %q", c.ID, c.Category)
		}
		if (c.Severity == SeverityBlocking) != (c.Gates != "") {
			t.Errorf("%s: blocking checks, and only those, gate a state", c.ID)
		}
		if c.Gates != "" && rank(c.Gates) <= 0 {
			t.Errorf("%s: gates unknown or initial state %q", c.ID, c.Gates)
		}
		if (c.Category == CategoryRBAC) != (c.Capability != "") {
			t.Errorf("%s: rbac checks, and only those, name a capability", c.ID)
		}
		if c.Title == "" {
			t.Errorf("%s: no title", c.ID)
		}
	}
	for _, capab := range Capabilities {
		if _, ok := Lookup("rbac." + string(capab)); !ok {
			t.Errorf("capability %s has no rbac check", capab)
		}
	}
}

// TestCatalogDocumented keeps the catalog and docs/contracts/readiness.md in sync.
func TestCatalogDocumented(t *testing.T) {
	b, err := os.ReadFile("../../../docs/contracts/readiness.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	documented := regexp.MustCompile("(?m)^\\| `([a-z]+\\.[a-z0-9-]+)` \\|").FindAllStringSubmatch(doc, -1)
	inDoc := map[string]bool{}
	for _, m := range documented {
		inDoc[m[1]] = true
	}
	for _, c := range Catalog {
		if !inDoc[c.ID] {
			t.Errorf("check %s is not a row of docs/contracts/readiness.md", c.ID)
		}
		delete(inDoc, c.ID)
	}
	for id := range inDoc {
		t.Errorf("docs/contracts/readiness.md documents %s, which is not in the catalog", id)
	}
}

func TestComputeState(t *testing.T) {
	passAll := func(gates ...State) []Result {
		var out []Result
		for _, c := range Catalog {
			for _, g := range gates {
				if c.Gates == g {
					out = append(out, Result{ID: c.ID, Status: StatusPass})
				}
			}
		}
		return out
	}
	tests := []struct {
		name    string
		results []Result
		want    State
	}{
		{"nothing", nil, StateRegistered},
		{"agent connected", passAll(StateAgentConnected), StateAgentConnected},
		{"metrics ok", passAll(StateAgentConnected, StateMetricsOK), StateMetricsOK},
		{"metrics ok but no agent", passAll(StateMetricsOK), StateRegistered},
		{"ready audit", passAll(StateAgentConnected, StateMetricsOK, StateReadyAudit), StateReadyAudit},
		{"automation ready", passAll(States[1:]...), StateAutomationReady},
		{"resize rbac lost", append(passAll(States[1:]...), Result{ID: CheckRBACResize, Status: StatusFail}), StateReadyAudit},
		{"ksm unknown", append(passAll(States[1:]...), Result{ID: CheckKSMOwners, Status: StatusUnknown}), StateAgentConnected},
		{"warning does not gate", append(passAll(States[1:]...), Result{ID: CheckWebhook, Status: StatusFail}), StateAutomationReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeState(tt.results); got != tt.want {
				t.Errorf("ComputeState = %s, want %s", got, tt.want)
			}
		})
	}
	if !StateAutomationReady.AtLeast(StateReadyAudit) || StateMetricsOK.AtLeast(StateReadyAudit) || State("x").AtLeast(StateRegistered) {
		t.Error("AtLeast")
	}
}
