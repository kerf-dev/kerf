package controller

// HPA coordination: the reconciler side. The math and the writes are in
// internal/agent/apply/hpa.go.
//
// For a workload whose HPA scales on the utilization of a resource in auto-patch:
//   - without spec.hpa.coordinate in the effective policy, or with an HPA that cannot be
//     coordinated (several or other metrics, ContainerResource, at maxReplicas), the workload is
//     blocked with reason HPACoupled, as before;
//   - otherwise the change is applied with the rollout strategy (never in-place), and when the
//     requests of the HPA's resource change, the HPA target changes with them in the same
//     reconcile so that sum(requests) x target stays constant. A target outside the policy's
//     bounds (default 40-90 %) blocks the change.
// The target is kept live like the template (drift repair), restored by a rollback and released
// with the workload.

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/apply"
	"github.com/kerf-dev/kerf/internal/agent/guardrails"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
	"github.com/kerf-dev/kerf/internal/agent/workload"
)

// hpaState is the HPA side of one reconcile.
type hpaState struct {
	coupling apply.HPACoupling
	// block is the reason the coupled resource must not change (empty: no block).
	block string
	// coordinate: requests of coupling.Resource may change together with the HPA target.
	coordinate bool
	// target is the HPA target to apply with the containers, prev the live one (set by planTarget).
	target *v1alpha1.HPATarget
	prev   int32
}

// planHPA looks at the workload's HPA before the guardrails run.
func (w *run) planHPA() (*hpaState, error) {
	h, err := apply.FindHPA(w.ctx, w.r.Client, w.k.Namespace, w.kind, w.k.Name)
	if err != nil {
		return nil, err
	}
	s := &hpaState{coupling: apply.Coupling(h)}
	c := s.coupling
	if !c.Coupled {
		return s, nil
	}
	auto := autoSet(w.eff)
	// Only the utilization resource matters: other resources change as for any workload. An HPA
	// that cannot be coordinated may couple to either resource.
	if c.Resource != "" && !auto[c.Resource] {
		return s, nil
	}
	switch {
	case !w.eff.HPACoordinate:
		s.block = "workload has a CPU/memory-utilization HPA and HPA coordination is off (RightsizingPolicy spec.hpa.coordinate); audit-only"
	case c.Problem != "":
		s.block = "HPA " + c.HPA.Name + " cannot be coordinated: " + c.Problem
	case w.rec != nil && w.rec.HPA != nil && w.rec.HPA.SkipReason != "":
		s.block = "HPA " + c.HPA.Name + " is not coordinated by the hub: " + w.rec.HPA.SkipReason
	default:
		s.coordinate = true
	}
	return s, nil
}

// guardrailInput sets the HPA fields of the guardrail input.
func (s *hpaState) guardrailInput(in *guardrails.Input) {
	in.HPAChecked = true
	in.HPACoupled = s.block != ""
	in.HPAMessage = s.block
}

// planTarget computes the HPA target for an apply decision. It returns a non-empty message when
// the change must be blocked (the target would leave the policy's bounds).
func (w *run) planTarget(s *hpaState, dec guardrails.Decision) string {
	if s == nil || !s.coordinate {
		return ""
	}
	c := s.coupling
	spec := workload.PodSpec(w.obj)
	before, ok1 := apply.RequestSum(spec, nil, c.Resource)
	after, ok2 := apply.RequestSum(spec, dec.Targets, c.Resource)
	if !ok1 || !ok2 {
		return "HPA " + c.HPA.Name + " cannot be coordinated: " + apply.HPAProblemNoRequest
	}
	if before == after {
		return ""
	}
	t := apply.NewHPATarget(c.Target, before, after)
	lo, hi := min(w.eff.HPAMinTarget, c.Target), max(w.eff.HPAMaxTarget, c.Target)
	if t < lo || t > hi {
		return fmt.Sprintf("HPA %s target would move from %d%% to %d%%, outside %d-%d%%", c.HPA.Name, c.Target, t, lo, hi)
	}
	s.target = &v1alpha1.HPATarget{Name: c.HPA.Name, Resource: c.Resource, AverageUtilization: t}
	s.prev = c.Target
	return ""
}

// applyHPATarget writes the planned target after the pod template was patched.
func (w *run) applyHPATarget(s *hpaState) error {
	if s == nil || s.target == nil {
		return nil
	}
	return apply.ApplyHPATarget(w.ctx, w.r.Client, s.coupling.HPA, s.target.Resource, s.target.AverageUtilization)
}

// hpaDetails describes the HPA change for the applied event.
func (s *hpaState) details() (map[string]string, string) {
	if s == nil || s.target == nil {
		return nil, ""
	}
	t := s.target
	return map[string]string{
		"hpa": t.Name, "hpaResource": string(t.Resource),
		"hpaTargetFrom": strconv.Itoa(int(s.prev)), "hpaTargetTo": strconv.Itoa(int(t.AverageUtilization)),
	}, fmt.Sprintf("; HPA %s %s target %d%% -> %d%%", t.Name, t.Resource, s.prev, t.AverageUtilization)
}

// keepHPA re-asserts the applied HPA target (drift repair, like the template).
func (w *run) keepHPA(own map[corev1.ResourceName]bool) error {
	t := w.rsr.Spec.HPA
	if t == nil || !own[t.Resource] {
		return nil
	}
	h, err := apply.FindHPA(w.ctx, w.r.Client, w.k.Namespace, w.kind, w.k.Name)
	if err != nil || h == nil || h.Name != t.Name {
		return err
	}
	if apply.HPATargetOf(h, t.Resource) == t.AverageUtilization && apply.HPAOwned(h) {
		return nil
	}
	w.log.Info("re-applying HPA target (drifted)", "hpa", h.Name, "target", t.AverageUtilization)
	return apply.ApplyHPATarget(w.ctx, w.r.Client, h, t.Resource, t.AverageUtilization)
}

// releaseHPA gives up ownership of the HPA target when its resource is no longer owned on the
// template (values stay; Argo CD restores Git values on its next sync).
func (w *run) releaseHPA(want map[corev1.ResourceName]bool) error {
	if w.r.Cfg.ReadOnly {
		return nil
	}
	h, err := apply.FindHPA(w.ctx, w.r.Client, w.k.Namespace, w.kind, w.k.Name)
	if err != nil || h == nil || !apply.HPAOwned(h) {
		return err
	}
	if w.rsr != nil && w.rsr.Spec.HPA != nil && want[w.rsr.Spec.HPA.Resource] {
		return nil
	}
	if _, err := apply.Release(w.ctx, w.r.Client, h); err != nil {
		w.emit(hubclient.Event{Type: hubclient.EventFailed, Reason: v1alpha1.ReasonReleaseError,
			Message: "HPA: " + err.Error(), Details: map[string]string{"error": err.Error(), "hpa": h.Name}}, "failed/"+v1alpha1.ReasonReleaseError+"/hpa", nil)
		return fmt.Errorf("release HPA: %w", err)
	}
	w.log.Info("released HPA field ownership", "hpa", h.Name)
	return nil
}
