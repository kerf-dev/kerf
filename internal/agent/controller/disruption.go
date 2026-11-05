package controller

// Single-replica protection and StatefulSet OnDelete handling (WO-13).

import (
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/apply"
	"github.com/kerf-dev/kerf/internal/agent/guardrails"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
	"github.com/kerf-dev/kerf/internal/agent/policy"
	"github.com/kerf-dev/kerf/internal/agent/workload"
)

// Defaults of the OnDelete roll.
const (
	DefaultMaxPodDeletesPerRun = 1
	DefaultOnDeleteSettle      = 30 * time.Second
)

// slotOnDelete is the dedup slot of events about the OnDelete roll.
const slotOnDelete = "ondelete"

// disruption assesses the workload against the PodDisruptionBudgets of its namespace.
func (w *run) disruption() (*apply.Disruption, error) {
	var pdbs policyv1.PodDisruptionBudgetList
	if err := w.r.Client.List(w.ctx, &pdbs, client.InNamespace(w.k.Namespace)); err != nil {
		return nil, fmt.Errorf("list PodDisruptionBudgets: %w", err)
	}
	d := apply.AssessDisruption(w.obj, pdbs.Items)
	return &d, nil
}

// fallbackUnsafe reports why an in-place change of a single-replica workload must not fall back
// to a rollout ("" if it may).
func (w *run) fallbackUnsafe() (string, error) {
	d, err := w.disruption()
	if err != nil {
		return "", err
	}
	changes := diff(w.rsr.Status.Previous, w.rsr.Spec.Containers, w.ownedTargets())
	return guardrails.SingleReplicaUnsafe(d, v1alpha1.StrategyRollout, workload.Containers(workload.PodSpec(w.obj)), changes), nil
}

// blockFallback refuses an in-place -> rollout fallback that would leave a single-replica
// workload without a Ready pod: the pending resize is reverted, the RSR keeps strategy rollout
// (the sticky fallback, so the guardrail explains the block on every later evaluation) and the
// phase becomes Blocked.
func (w *run) blockFallback(reason, msg, why string) (ctrl.Result, error) {
	r := w.r
	recID := w.rsr.Spec.Source.RecommendationID
	own := w.ownedTargets()
	var prev []v1alpha1.ContainerRecommendation
	for _, p := range w.rsr.Status.Previous {
		prev = append(prev, v1alpha1.ContainerRecommendation{ContainerResources: p})
	}
	if pods, err := workload.Pods(w.ctx, r.Reader, w.obj); err == nil {
		for i := range pods {
			if _, err := apply.Resize(w.ctx, r.Client, &pods[i], prev, own, recID); err != nil {
				w.log.Warn("could not revert pending resize", "pod", pods[i].Name, "err", err)
			}
		}
	}
	status := w.rsr.Status.DeepCopy()
	w.rsr.Spec.Strategy = v1alpha1.StrategyRollout
	if err := r.Client.Update(w.ctx, w.rsr); err != nil {
		return ctrl.Result{}, err
	}
	w.rsr.Status = *status
	w.rsr.Status.Phase = v1alpha1.PhaseBlocked
	w.reason = v1alpha1.ReasonSingleReplicaUnsafe
	w.message = fmt.Sprintf("in-place resize cannot complete (%s: %s); fallback to rollout refused: %s", reason, msg, why)
	w.emit(hubclient.Event{Type: hubclient.EventBlocked, Reason: v1alpha1.ReasonSingleReplicaUnsafe, RecommendationID: recID,
		Resources: keys(own), Message: w.message, Details: map[string]string{"fallbackReason": reason}}, slotDecision, w.rsr.Spec.Containers)
	w.log.Warn("in-place fallback to rollout refused (single replica)", "reason", reason, "why", why)
	return ctrl.Result{}, nil
}

// rollOnDelete replaces the outdated pods of an OnDelete StatefulSet after a rollout (or a
// rollback) changed its template. It returns active=true while pods are still outdated; no new
// decision is taken meanwhile (like an in-place change in flight).
func (w *run) rollOnDelete() (ctrl.Result, bool, error) {
	r := w.r
	if r.Cfg.ReadOnly || w.rsr == nil || !apply.IsOnDelete(w.obj) || w.rsr.Spec.Strategy == v1alpha1.StrategyInPlace {
		return ctrl.Result{}, false, nil
	}
	phase := w.prev.Phase
	if phase != v1alpha1.PhaseApplied && phase != v1alpha1.PhaseRolledBack {
		return ctrl.Result{}, false, nil
	}
	sts := w.obj.(*appsv1.StatefulSet)
	pods, err := workload.Pods(w.ctx, r.Reader, w.obj)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	settle := r.Cfg.OnDeleteSettle
	if settle <= 0 {
		settle = DefaultOnDeleteSettle
	}
	maxDeletes := r.Cfg.MaxPodDeletesPerRun
	if maxDeletes <= 0 {
		maxDeletes = DefaultMaxPodDeletesPerRun
	}
	plan := apply.PlanOnDelete(sts, pods, w.now, settle, maxDeletes)
	if plan.Done() {
		return ctrl.Result{}, false, nil
	}
	defer func() {
		if w.reason != v1alpha1.ReasonPodDisruptionBudget {
			w.message = w.note
		}
	}()
	requeue := ctrl.Result{RequeueAfter: r.Cfg.ApplyingRequeue}
	if requeue.RequeueAfter <= 0 {
		requeue.RequeueAfter = 5 * time.Second
	}
	if phase == v1alpha1.PhaseApplied {
		w.reason = v1alpha1.ReasonRecommendationApplied
	} else {
		w.reason = condReasonFromConditions(w.prev)
	}
	w.note = "OnDelete StatefulSet: " + plan.Progress()

	wait := plan.Waiting
	if wait == "" && phase == v1alpha1.PhaseApplied {
		wait = w.healthGate()
	}
	if wait == "" && w.frozen && phase == v1alpha1.PhaseApplied {
		// A rollback's pod replacement goes on during a freeze; a change's does not (WO-10).
		wait = "inside a freeze window: replacing the remaining pods afterwards"
	}
	if wait == "" && w.rsr.Spec.Strategy == v1alpha1.StrategyDeferred {
		if open, _ := policy.InWindow(w.eff.MaintenanceWindows, w.now); !open {
			wait = "waiting for a maintenance window to replace the remaining pods"
		}
	}
	if wait == "" {
		// The health watch may have rolled the change back a moment ago: confirm uncached.
		var fresh v1alpha1.RightsizingRecommendation
		if err := r.Reader.Get(w.ctx, client.ObjectKeyFromObject(w.rsr), &fresh); err != nil {
			return ctrl.Result{}, true, err
		}
		if fresh.Status.Phase != phase {
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
	}
	if wait != "" {
		w.note += "; " + wait
		return requeue, true, nil
	}

	for _, p := range plan.Evict {
		err := apply.Evict(w.ctx, r.Client, p)
		switch {
		case err == nil:
			w.log.Info("evicted pod (OnDelete roll)", "pod", p.Name, "progress", plan.Progress())
			w.note += "; evicted " + p.Name
		case apierrors.IsNotFound(err) || apierrors.IsConflict(err):
			// Gone or recreated meanwhile: the next pass sees the new state.
		case apply.EvictionRefused(err):
			msg := fmt.Sprintf("eviction of pod %s refused by a PodDisruptionBudget; retrying: %v", p.Name, err)
			w.reason, w.message = v1alpha1.ReasonPodDisruptionBudget, msg
			w.note += "; " + msg
			w.emit(hubclient.Event{Type: hubclient.EventBlocked, Reason: v1alpha1.ReasonPodDisruptionBudget,
				RecommendationID: w.rsr.Status.AppliedRecommendationID, Strategy: string(w.rsr.Spec.Strategy),
				Message: msg, Details: map[string]string{"pod": p.Name}}, slotOnDelete, nil)
			return ctrl.Result{RequeueAfter: max(requeue.RequeueAfter, 15*time.Second)}, true, nil
		default:
			w.emit(hubclient.Event{Type: hubclient.EventFailed, Reason: v1alpha1.ReasonApplyError,
				RecommendationID: w.rsr.Status.AppliedRecommendationID, Strategy: string(w.rsr.Spec.Strategy),
				Message: fmt.Sprintf("evict pod %s: %v", p.Name, err), Details: map[string]string{"error": err.Error(), "pod": p.Name}},
				"failed/evict/"+p.Name, nil)
			return ctrl.Result{}, true, fmt.Errorf("evict pod %s: %w", p.Name, err)
		}
	}
	return requeue, true, nil
}

// healthGate returns why the next pod must wait for the health watch (WO-08), or "".
func (w *run) healthGate() string {
	c := meta.FindStatusCondition(w.prev.Conditions, v1alpha1.ConditionHealthWatch)
	if c == nil {
		return "" // health watch disabled or not started yet; the settle time still applies
	}
	if c.Status == metav1.ConditionTrue || slices.Contains([]string{"Healthy", "WatchStopped"}, c.Reason) {
		return ""
	}
	return "health watch reports " + c.Reason
}

// onDeleteNote is the Applied message after the template patch of an OnDelete StatefulSet.
func (w *run) onDeleteNote() string {
	if !apply.IsOnDelete(w.obj) {
		return ""
	}
	n := w.r.Cfg.MaxPodDeletesPerRun
	if n <= 0 {
		n = DefaultMaxPodDeletesPerRun
	}
	return fmt.Sprintf("OnDelete StatefulSet: the agent replaces the pods, highest ordinal first, at most %d at a time", n)
}
