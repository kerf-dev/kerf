package readiness

import (
	"fmt"
	"sort"
	"strings"
	"time"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Evaluation is the readiness of a cluster computed from its check results.
type Evaluation struct {
	State rd.State
	// NextState is the state after State; "" at AutomationReady.
	NextState rd.State
	// BlockedBy are the blocking checks gating NextState that do not pass (missing ones included).
	BlockedBy []string
	Summary   hubapi.ReadinessCounts
}

// Normalize fills each result's category, severity, source and gates from the catalog, drops
// unknown ids and unknown statuses, keeps the last result per id, and sorts by catalog order.
func Normalize(checks []hubapi.ReadinessCheck) []hubapi.ReadinessCheck {
	byID := map[string]hubapi.ReadinessCheck{}
	for _, c := range checks {
		cat, ok := rd.Lookup(c.ID)
		if !ok || !validStatus(c.Status) {
			continue
		}
		c.Category, c.Severity, c.Source, c.Gates = string(cat.Category), string(cat.Severity), string(cat.Source), string(cat.Gates)
		byID[c.ID] = c
	}
	out := make([]hubapi.ReadinessCheck, 0, len(byID))
	for _, cat := range rd.Catalog {
		if c, ok := byID[cat.ID]; ok {
			out = append(out, c)
		}
	}
	return out
}

func validStatus(s string) bool {
	for _, v := range rd.Statuses {
		if string(v) == s {
			return true
		}
	}
	return false
}

// Evaluate computes the state, next state, blockers and counts.
func Evaluate(checks []hubapi.ReadinessCheck) Evaluation {
	results := make([]rd.Result, 0, len(checks))
	status := map[string]string{}
	var e Evaluation
	for _, c := range checks {
		results = append(results, rd.Result{ID: c.ID, Status: rd.Status(c.Status)})
		status[c.ID] = c.Status
		cat, ok := rd.Lookup(c.ID)
		if !ok {
			continue
		}
		switch {
		case c.Status == string(rd.StatusUnknown):
			e.Summary.Unknown++
		case c.Status == string(rd.StatusPass):
		case cat.Severity == rd.SeverityBlocking:
			e.Summary.Blocking++
		case cat.Severity == rd.SeverityWarning:
			e.Summary.Warnings++
		}
	}
	e.State = rd.ComputeState(results)
	for i, st := range rd.States {
		if st == e.State && i+1 < len(rd.States) {
			e.NextState = rd.States[i+1]
		}
	}
	if e.NextState != "" {
		for _, c := range rd.Catalog {
			if c.Severity == rd.SeverityBlocking && c.Gates == e.NextState && status[c.ID] != string(rd.StatusPass) {
				e.BlockedBy = append(e.BlockedBy, c.ID)
			}
		}
	}
	return e
}

// Change is one history entry produced by applying new results.
type Change = hubapi.ReadinessHistoryEntry

// Diff returns the history entries between the previous results and state and the new ones:
// one per check whose status changed (a new check counts as a change from no status) and one for
// a state transition.
func Diff(prevState rd.State, prev, next []hubapi.ReadinessCheck, nextState rd.State, at time.Time) []Change {
	old := map[string]string{}
	for _, c := range prev {
		old[c.ID] = c.Status
	}
	var out []Change
	for _, c := range next {
		if old[c.ID] == c.Status {
			continue
		}
		out = append(out, Change{Time: at, Kind: "check", CheckID: c.ID, FromStatus: old[c.ID], ToStatus: c.Status, Message: c.Message})
	}
	if prevState != nextState {
		out = append(out, Change{Time: at, Kind: "state", FromState: string(prevState), ToState: string(nextState), Message: TransitionMessage(prevState, nextState, next)})
	}
	return out
}

// TransitionMessage explains a state change, e.g. "AutomationReady → ReadyAudit: rbac.resize fail".
func TransitionMessage(from, to rd.State, checks []hubapi.ReadinessCheck) string {
	msg := fmt.Sprintf("%s → %s", from, to)
	if from == "" {
		msg = fmt.Sprintf("→ %s", to)
	}
	if to.AtLeast(from) && from != "" {
		return msg
	}
	// lost a state: name the blocking checks that fail
	var failing []string
	for _, c := range checks {
		if c.Severity == string(rd.SeverityBlocking) && c.Status != string(rd.StatusPass) {
			failing = append(failing, c.ID+" "+c.Status)
		}
	}
	sort.Strings(failing)
	if len(failing) > 0 {
		msg += ": " + strings.Join(failing, ", ")
	}
	return msg
}

// Summary is the list form of an evaluation.
func Summary(e Evaluation, checkedAt *time.Time) hubapi.ReadinessSummary {
	return hubapi.ReadinessSummary{State: string(e.State), Blocking: e.Summary.Blocking, Warnings: e.Summary.Warnings, CheckedAt: checkedAt}
}
