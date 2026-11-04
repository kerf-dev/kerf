// Package guardrails decides whether and how a hub recommendation may change a workload.
//
// Evaluate is pure: it gets the recommendation, the effective policy and the live values and
// returns a Decision (apply / skipped / blocked, with reason) and the guardrailed target values.
// Order of checks (the first match decides the workload-level outcome):
//
//  1. no auto-patch resource                    -> skipped ModeOff / ModeAudit
//  2. hub not reachable / recommendation stale  -> blocked HubUnreachable, skipped RecommendationStale
//  3. hpa-coupled without HPA coordination (D-011, WO-11), learning period, confidence, freeze window, maintenance window -> blocked
//  4. per container and resource: clamp decreases to maxChangePercent, floors, never decrease a
//     throttled / oom-killed resource (Throttled / OOMKilled), JVM/Python memory needs
//     RestartContainer for in-place (RestartPolicyRequired); changes below the tolerance are dropped
//  5. nothing left to change                    -> skipped NoSignificantChange (or the refusal)
//  6. cooldown since the last change / rollback -> blocked Cooldown
//  7. single replica without a disruption-free way to apply (WO-13) -> blocked SingleReplicaUnsafe
package guardrails

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/apply"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
	"github.com/tomer1983/resource-optimizer/internal/agent/policy"
	"github.com/tomer1983/resource-optimizer/internal/agent/workload"
)

// DefaultTolerance is the relative change below which a value is not changed.
const DefaultTolerance = 0.05

// Outcome of an evaluation.
type Outcome string

const (
	// Apply: at least one significant change passes every guardrail.
	Apply Outcome = "apply"
	// Skipped: nothing to do and nothing wrong (event type skipped).
	Skipped Outcome = "skipped"
	// Blocked: a guardrail prevents a change the mode allows (event type blocked).
	Blocked Outcome = "blocked"
	// NoRecommendation: the hub has no recommendation for the workload.
	NoRecommendation Outcome = "none"
	// Deferred: strategy deferred, a change passes every guardrail, and no maintenance window is
	// open; it is applied when the next one opens (reason OutsideMaintenanceWindow, WO-10).
	Deferred Outcome = "deferred"
)

// Input is everything Evaluate looks at.
type Input struct {
	Now       time.Time
	Effective policy.Effective
	// Strategy in effect (the effective strategy, or rollout after an in-place fallback).
	Strategy v1alpha1.Strategy
	Rec      *hubclient.WorkloadRecommendation
	// HubFresh is false when the last successful pull is too old (hub unreachable).
	HubFresh bool
	// Containers of the pod template with their live values (template values for rollout,
	// the running pods' values are compared by the in-place applier).
	Containers []workload.Container
	// HPACoupled is set when the agent itself found a CPU/memory-utilization HPA that blocks the
	// change (WO-11: no coordination, or an HPA that cannot be coordinated); HPAMessage says why.
	HPACoupled bool
	HPAMessage string
	// HPAChecked: the caller looked at the live HPA, so the hub's hpa-coupled flag alone does not block.
	HPAChecked    bool
	LastAppliedAt *time.Time
	RolledBackAt  *time.Time
	// RolledBackResources are the resources the last rollback restored ("cpu", "memory");
	// decreases of these are blocked until the cooldown after RolledBackAt ends. Empty means all.
	RolledBackResources []string
	// Tolerance is the relative change below which a value is left alone (default 5%).
	Tolerance float64
	// Disruption describes single-replica protection (WO-13); nil skips the check.
	Disruption *apply.Disruption
}

// Refusal is a per-resource guardrail that removed part of a change.
type Refusal struct {
	Container string
	Resource  string
	Reason    string
	Message   string
}

// Decision is the result of Evaluate.
type Decision struct {
	Outcome Outcome
	Reason  string
	Message string
	// Targets are the guardrailed desired values of the auto-patch resources, one entry per
	// recommended container that exists in the template. Values of resources that do not
	// change are the live values.
	Targets []v1alpha1.ContainerRecommendation
	// Proposed are the guardrailed values for every resource regardless of mode (for display).
	Proposed []v1alpha1.ContainerRecommendation
	// Changes lists the changed containers (only changed values).
	Changes []hubclient.ContainerChange
	// Resources are the resources that change ("cpu", "memory").
	Resources []string
	// Refusals are per-resource guardrails that dropped part of the change.
	Refusals []Refusal
}

var resources = []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory}

// Evaluate applies the guardrails.
func Evaluate(in Input) Decision {
	eff := in.Effective
	if in.Tolerance <= 0 {
		in.Tolerance = DefaultTolerance
	}
	if !eff.AnyAutoPatch() {
		d := Decision{Outcome: Skipped, Reason: v1alpha1.ReasonModeAudit, Message: "no resource is in auto-patch mode"}
		if eff.CPUMode == v1alpha1.ModeOff && eff.MemoryMode == v1alpha1.ModeOff {
			d.Reason, d.Message = v1alpha1.ReasonModeOff, "cpu and memory mode are off"
		}
		if in.Rec != nil {
			d.Proposed, _, _, _ = targets(in, map[string]bool{"cpu": true, "memory": true})
		}
		return d
	}
	if in.Rec == nil {
		if !in.HubFresh {
			return Decision{Outcome: Blocked, Reason: v1alpha1.ReasonHubUnreachable, Message: "no recommendation and the hub is unreachable"}
		}
		return Decision{Outcome: NoRecommendation, Message: "the hub has no recommendation for this workload yet"}
	}
	all := map[string]bool{"cpu": true, "memory": true}
	proposed, _, _, _ := targets(in, all)
	block := func(reason, format string, args ...any) Decision {
		return Decision{Outcome: Blocked, Reason: reason, Message: fmt.Sprintf(format, args...), Proposed: proposed}
	}
	if !in.HubFresh {
		return block(v1alpha1.ReasonHubUnreachable, "hub unreachable: no fresh recommendation, nothing new is applied")
	}
	if !policy.SettingsMatch(eff.Settings(), in.Rec.Settings) {
		return Decision{Outcome: Skipped, Reason: v1alpha1.ReasonRecommendationStale, Proposed: proposed,
			Message: fmt.Sprintf("recommendation settings %s differ from the effective policy %s; waiting for the next hub run",
				fmtSettings(in.Rec.Settings), fmtSettings(eff.Settings()))}
	}
	if in.HPACoupled || (!in.HPAChecked && slices.Contains(in.Rec.RiskFlags, string(v1alpha1.RiskHPACoupled))) {
		msg := in.HPAMessage
		if msg == "" {
			msg = "workload has a CPU/memory-utilization HPA; audit-only without HPA coordination (D-011)"
		}
		return block(v1alpha1.ReasonHPACoupled, "%s", msg)
	}
	if covered, ok := coverage(in.Rec.DataCoverage); !ok || covered < eff.LearningPeriod {
		return block(v1alpha1.ReasonLearningPeriod, "data coverage %s is below the learning period %s", covered, eff.LearningPeriod)
	}
	if in.Rec.Confidence < eff.MinConfidence {
		return block(v1alpha1.ReasonLowConfidence, "confidence %.2f is below the minimum %.2f", in.Rec.Confidence, eff.MinConfidence)
	}
	if open, err := policy.InWindow(eff.FreezeWindows, in.Now); open {
		return block(v1alpha1.ReasonFreezeWindow, "inside a freeze window")
	} else if err != nil {
		return block(v1alpha1.ReasonFreezeWindow, "invalid freeze window (treated as frozen): %v", err)
	}
	auto := map[string]bool{
		"cpu":    eff.CPUMode == v1alpha1.ModeAutoPatch,
		"memory": eff.MemoryMode == v1alpha1.ModeAutoPatch,
	}
	tgt, changes, res, refusals := targets(in, auto)
	d := Decision{Targets: tgt, Proposed: proposed, Changes: changes, Resources: res, Refusals: refusals}
	if len(changes) == 0 {
		if len(refusals) > 0 {
			d.Outcome, d.Reason, d.Message = Blocked, refusals[0].Reason, refusals[0].Message
			return d
		}
		d.Outcome, d.Reason = Skipped, v1alpha1.ReasonNoSignificantChange
		d.Message = fmt.Sprintf("recommended values are within %.0f%% of the live values", in.Tolerance*100)
		return d
	}
	if in.LastAppliedAt != nil && in.Now.Sub(*in.LastAppliedAt) < eff.Cooldown {
		d.Outcome, d.Reason = Blocked, v1alpha1.ReasonCooldown
		d.Message = fmt.Sprintf("last change at %s, cooldown %s", in.LastAppliedAt.UTC().Format(time.RFC3339), eff.Cooldown)
		return d
	}
	if in.RolledBackAt != nil && in.Now.Sub(*in.RolledBackAt) < eff.Cooldown && hasDecrease(changes, in.RolledBackResources) {
		d.Outcome, d.Reason = Blocked, v1alpha1.ReasonCooldown
		d.Message = fmt.Sprintf("rolled back at %s; decreases of %s blocked for %s", in.RolledBackAt.UTC().Format(time.RFC3339), resList(in.RolledBackResources), eff.Cooldown)
		return d
	}
	if why := singleReplicaUnsafe(in, changes); why != "" {
		d.Outcome, d.Reason, d.Message = Blocked, v1alpha1.ReasonSingleReplicaUnsafe, why
		return d
	}
	if in.Strategy == v1alpha1.StrategyDeferred {
		if open, _ := policy.InWindow(eff.MaintenanceWindows, in.Now); !open {
			d.Outcome, d.Reason = Deferred, v1alpha1.ReasonOutsideMaintenanceWindow
			d.Message = "strategy deferred: waiting for the next maintenance window"
			if len(eff.MaintenanceWindows) == 0 {
				d.Message = "strategy deferred but no maintenance window is configured"
			}
			return d
		}
	}
	d.Outcome, d.Reason = Apply, v1alpha1.ReasonRecommendationApplied
	return d
}

func fmtSettings(s hubclient.ValueSettings) string {
	eq := "<nil>"
	if s.MemoryLimitEqualsRequest != nil {
		eq = fmt.Sprint(*s.MemoryLimitEqualsRequest)
	}
	return fmt.Sprintf("{profile=%s cpuLimits=%s ratio=%s memLimit=req:%s}", s.Profile, s.CPULimits, s.CPULimitRatio, eq)
}

func coverage(c hubclient.DataCoverage) (time.Duration, bool) {
	if c.Covered != "" {
		d, err := time.ParseDuration(c.Covered)
		return d, err == nil
	}
	w, err := time.ParseDuration(c.Window)
	if err != nil {
		return 0, false
	}
	return time.Duration(float64(w) * c.Ratio), true
}

func resList(rs []string) string {
	if len(rs) == 0 {
		return "cpu and memory"
	}
	return strings.Join(rs, " and ")
}

// hasDecrease reports whether a change decreases one of the resources (all if empty).
func hasDecrease(changes []hubclient.ContainerChange, resources []string) bool {
	for _, c := range changes {
		if c.From == nil || c.To == nil {
			continue
		}
		if dec(c.From.Requests, c.To.Requests, resources) || dec(c.From.Limits, c.To.Limits, resources) {
			return true
		}
	}
	return false
}

func dec(from, to *hubclient.ResourceValues, resources []string) bool {
	if from == nil || to == nil {
		return false
	}
	less := func(a, b string) bool {
		if a == "" || b == "" {
			return false
		}
		qa, errA := resource.ParseQuantity(a)
		qb, errB := resource.ParseQuantity(b)
		return errA == nil && errB == nil && qa.Cmp(qb) < 0
	}
	in := func(r string) bool { return len(resources) == 0 || slices.Contains(resources, r) }
	return (in("cpu") && less(to.CPU, from.CPU)) || (in("memory") && less(to.Memory, from.Memory))
}

func flagged(rec *hubclient.WorkloadRecommendation, c hubclient.ContainerRecommendation, flag v1alpha1.RiskFlag) bool {
	return slices.Contains(c.RiskFlags, string(flag)) || slices.Contains(rec.RiskFlags, string(flag))
}

// targets computes the guardrailed values for the resources in scope.
func targets(in Input, scope map[string]bool) ([]v1alpha1.ContainerRecommendation, []hubclient.ContainerChange, []string, []Refusal) {
	eff := in.Effective
	var (
		out      []v1alpha1.ContainerRecommendation
		changes  []hubclient.ContainerChange
		refusals []Refusal
		changed  = map[string]bool{}
	)
	for _, rc := range in.Rec.Containers {
		var live workload.Container
		found := false
		for _, c := range in.Containers {
			if c.Name == rc.Name {
				live, found = c, true
				break
			}
		}
		if !found {
			continue
		}
		tgt := v1alpha1.ContainerRecommendation{ContainerResources: v1alpha1.ContainerResources{Name: rc.Name}}
		chg := hubclient.ContainerChange{Name: rc.Name, From: &hubclient.Resources{}, To: &hubclient.Resources{}}
		anyChange := false
		for _, res := range resources {
			r := string(res)
			if !scope[r] {
				continue
			}
			curReq, hasCurReq := live.Resources.Requests[res]
			curLim, hasCurLim := live.Resources.Limits[res]
			newReq, hasNewReq := curReq.DeepCopy(), hasCurReq
			newLim, hasNewLim := curLim.DeepCopy(), hasCurLim
			pressure := (res == corev1.ResourceCPU && flagged(in.Rec, rc, v1alpha1.RiskThrottled)) ||
				(res == corev1.ResourceMemory && flagged(in.Rec, rc, v1alpha1.RiskOOMKilled))
			refuse := func() {
				reason, why := v1alpha1.ReasonThrottled, "throttled"
				if res == corev1.ResourceMemory {
					reason, why = v1alpha1.ReasonOOMKilled, "OOM-killed"
				}
				refusals = append(refusals, Refusal{Container: rc.Name, Resource: r, Reason: reason,
					Message: fmt.Sprintf("container %s is %s; %s decrease refused", rc.Name, why, r)})
			}

			if q, ok := quantity(rc.Recommended.Requests, res); ok {
				newReq, hasNewReq = q, true
				if hasCurReq && q.Cmp(curReq) < 0 {
					if pressure {
						newReq = curReq.DeepCopy()
						refuse()
					} else {
						newReq = clampDecrease(res, curReq, q, eff.MaxChangePercent)
					}
				}
				floor := eff.MinCPU
				if res == corev1.ResourceMemory {
					floor = eff.MinMemory
				}
				if newReq.Cmp(floor) < 0 {
					newReq = floor.DeepCopy()
				}
				if hasCurReq && !significant(curReq, newReq, in.Tolerance) {
					newReq = curReq.DeepCopy()
				}
			}

			removeLimit := slices.Contains(rc.RemoveLimits, r)
			if removeLimit {
				hasNewLim = false
			} else if q, ok := quantity(rc.Recommended.Limits, res); ok {
				newLim, hasNewLim = q, true
				if hasCurLim && q.Cmp(curLim) < 0 {
					if pressure {
						newLim = curLim.DeepCopy()
					} else {
						newLim = clampDecrease(res, curLim, q, eff.MaxChangePercent)
					}
				}
				if hasCurLim && !significant(curLim, newLim, in.Tolerance) {
					newLim = curLim.DeepCopy()
				}
			}
			if hasNewLim && hasNewReq && newLim.Cmp(newReq) < 0 {
				newLim = newReq.DeepCopy()
			}

			reqChanged := hasNewReq && (!hasCurReq || newReq.Cmp(curReq) != 0)
			limChanged := hasNewLim != hasCurLim || (hasNewLim && newLim.Cmp(curLim) != 0)

			// WO-12: memory of a JVM with a fixed -Xmx is audit-only.
			if res == corev1.ResourceMemory && (reqChanged || limChanged) {
				if msg, ok := workload.JVMFixedHeapRefusal(live.Container, eff.JVMDetect, flagged(in.Rec, rc, v1alpha1.RiskJVMFixedHeap)); ok {
					refusals = append(refusals, Refusal{Container: rc.Name, Resource: r, Reason: v1alpha1.ReasonJVMFixedHeap, Message: msg})
					newReq, hasNewReq = curReq.DeepCopy(), hasCurReq
					newLim, hasNewLim = curLim.DeepCopy(), hasCurLim
					reqChanged, limChanged = false, false
				}
			}

			// D-007: memory of JVM/Python containers is bound at start-up; in-place needs RestartContainer.
			if res == corev1.ResourceMemory && (reqChanged || limChanged) && in.Strategy == v1alpha1.StrategyInPlace &&
				eff.JVMDetect && workload.HeapBound(live.Container) &&
				workload.ResizePolicy(live.Container, corev1.ResourceMemory) != corev1.RestartContainer {
				refusals = append(refusals, Refusal{Container: rc.Name, Resource: r, Reason: v1alpha1.ReasonRestartPolicyRequired,
					Message: fmt.Sprintf("container %s looks like a JVM/Python process; in-place memory changes need resizePolicy RestartContainer or strategy rollout", rc.Name)})
				newReq, hasNewReq = curReq.DeepCopy(), hasCurReq
				newLim, hasNewLim = curLim.DeepCopy(), hasCurLim
				reqChanged, limChanged = false, false
			}

			if hasNewReq {
				setQ(&tgt.Requests, res, newReq)
			}
			if hasNewLim {
				setQ(&tgt.Limits, res, newLim)
			} else if hasCurLim {
				tgt.RemoveLimits = append(tgt.RemoveLimits, res)
			}
			if reqChanged {
				chg.From.Requests = setV(chg.From.Requests, res, curReq, hasCurReq)
				chg.To.Requests = setV(chg.To.Requests, res, newReq, true)
			}
			if limChanged {
				chg.From.Limits = setV(chg.From.Limits, res, curLim, hasCurLim)
				chg.To.Limits = setV(chg.To.Limits, res, newLim, hasNewLim)
			}
			if reqChanged || limChanged {
				anyChange = true
				changed[r] = true
			}
		}
		out = append(out, tgt)
		if anyChange {
			changes = append(changes, chg)
		}
	}
	var res []string
	for _, r := range []string{"cpu", "memory"} {
		if changed[r] {
			res = append(res, r)
		}
	}
	return out, changes, res, refusals
}

func quantity(v *hubclient.ResourceValues, res corev1.ResourceName) (resource.Quantity, bool) {
	if v == nil {
		return resource.Quantity{}, false
	}
	s := v.CPU
	if res == corev1.ResourceMemory {
		s = v.Memory
	}
	if s == "" {
		return resource.Quantity{}, false
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return resource.Quantity{}, false
	}
	return q, true
}

// clampDecrease caps a decrease from cur to want at maxPct percent of cur, rounded up to whole
// millicores (cpu) or MiB (memory).
func clampDecrease(res corev1.ResourceName, cur, want resource.Quantity, maxPct int32) resource.Quantity {
	if maxPct >= 100 {
		return want
	}
	if res == corev1.ResourceCPU {
		lowest := int64(math.Ceil(float64(cur.MilliValue()) * float64(100-maxPct) / 100))
		if want.MilliValue() >= lowest {
			return want
		}
		return *resource.NewMilliQuantity(lowest, resource.DecimalSI)
	}
	const mi = 1 << 20
	lowest := int64(math.Ceil(float64(cur.Value()) * float64(100-maxPct) / 100))
	if want.Value() >= lowest {
		return want
	}
	lowest = (lowest + mi - 1) / mi * mi
	if lowest > cur.Value() {
		lowest = cur.Value()
	}
	return *resource.NewQuantity(lowest, resource.BinarySI)
}

func significant(cur, next resource.Quantity, tol float64) bool {
	c, n := float64(cur.MilliValue()), float64(next.MilliValue())
	if c == 0 {
		return n != 0
	}
	return math.Abs(n-c)/c >= tol
}

func setQ(l *corev1.ResourceList, res corev1.ResourceName, q resource.Quantity) {
	if *l == nil {
		*l = corev1.ResourceList{}
	}
	(*l)[res] = q
}

func setV(v *hubclient.ResourceValues, res corev1.ResourceName, q resource.Quantity, ok bool) *hubclient.ResourceValues {
	if v == nil {
		v = &hubclient.ResourceValues{}
	}
	if !ok {
		return v
	}
	if res == corev1.ResourceCPU {
		v.CPU = q.String()
	} else {
		v.Memory = q.String()
	}
	return v
}

// Summary is a one-line description of the changes, for messages.
func Summary(changes []hubclient.ContainerChange) string {
	var parts []string
	for _, c := range changes {
		var p []string
		add := func(kind string, from, to *hubclient.ResourceValues) {
			if to == nil {
				return
			}
			f := from
			if f == nil {
				f = &hubclient.ResourceValues{}
			}
			if to.CPU != "" || f.CPU != "" {
				if to.CPU != f.CPU {
					p = append(p, fmt.Sprintf("cpu %s %s->%s", kind, orNone(f.CPU), orNone(to.CPU)))
				}
			}
			if to.Memory != "" || f.Memory != "" {
				if to.Memory != f.Memory {
					p = append(p, fmt.Sprintf("memory %s %s->%s", kind, orNone(f.Memory), orNone(to.Memory)))
				}
			}
		}
		add("request", c.From.Requests, c.To.Requests)
		add("limit", c.From.Limits, c.To.Limits)
		parts = append(parts, c.Name+": "+strings.Join(p, ", "))
	}
	return strings.Join(parts, "; ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
