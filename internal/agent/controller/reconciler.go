// Package controller is the agent's control loop: it resolves the effective policy of every
// workload, evaluates the hub's recommendation against the guardrails, applies it (in-place or
// rollout), releases field ownership when a workload leaves auto-patch, and keeps the
// RightsizingRecommendation, the hub status and the audit events up to date.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/apply"
	"github.com/tomer1983/resource-optimizer/internal/agent/guardrails"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
	"github.com/tomer1983/resource-optimizer/internal/agent/policy"
	"github.com/tomer1983/resource-optimizer/internal/agent/windows"
	"github.com/tomer1983/resource-optimizer/internal/agent/workload"
	"github.com/tomer1983/resource-optimizer/internal/health"
)

// Condition reasons used only on RightsizingRecommendation conditions for states that are not
// decisions (never sent as events).
const (
	condReasonResolved   = "PolicyResolved"
	condReasonNotBlocked = "NotBlocked"
	condReasonApplying   = "Applying"
	condReasonPending    = "Pending"
)

// Config configures the reconciler.
type Config struct {
	ClusterName string
	// ClusterDefaults is the cluster default level of policy resolution (D-005).
	ClusterDefaults *v1alpha1.RightsizingPolicySpec
	// ReadOnly caps every mode at audit; the agent then never writes workloads or pods.
	ReadOnly bool
	// Tolerance is the relative change below which values are left alone.
	Tolerance float64
	// UnsupportedNodeSelectors mark nodes where in-place resize is not used (static CPU/Memory
	// Manager); nodes with swap are detected from node status.
	UnsupportedNodeSelectors []map[string]string
	// ApplyingRequeue is how often an in-progress in-place resize is checked.
	ApplyingRequeue time.Duration
	// Freeze is the cluster-wide freeze (WO-10): added to every policy's freeze windows.
	Freeze windows.Freeze
	// MaxPodDeletesPerRun caps the pods of an OnDelete StatefulSet evicted at once (WO-13, default 1).
	MaxPodDeletesPerRun int
	// OnDeleteSettle is how long a replaced pod must be Ready before the next one is evicted
	// (WO-13, default 30s; the StatefulSet's minReadySeconds if longer).
	OnDeleteSettle time.Duration
}

// Reconciler reconciles one workload per request. Request.Name is "<Kind>/<name>".
type Reconciler struct {
	Client   client.Client
	Reader   client.Reader // uncached, for pods, ReplicaSets and Jobs
	Cfg      Config
	Store    *Store
	Reporter *Reporter
	Recorder events.EventRecorder
	Log      *slog.Logger
	Now      func() time.Time
	// Admin is the hub-managed configuration (pause flag, approve-once grants; D-022, D-024);
	// nil means none.
	Admin AdminState

	mu sync.Mutex
	// last holds, per workload and dedup slot, the signature of the last event emitted there (D-017).
	last map[string]signature
}

// AdminState is the part of the hub config the reconciler consults (configsync.State).
type AdminState interface {
	Flags() hubclient.ConfigFlags
	Approval(w hubclient.WorkloadRef, recID string, now time.Time) *hubclient.ConfigApproval
	Consume(id string, now time.Time)
}

func requestName(kind v1alpha1.WorkloadKind, name string) string { return string(kind) + "/" + name }

// Reconcile implements reconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	kindS, name, ok := strings.Cut(req.Name, "/")
	if !ok {
		return ctrl.Result{}, nil
	}
	kind := v1alpha1.WorkloadKind(kindS)
	k := wkey{req.Namespace, kindS, name}
	if _, ok := workload.Kinds[kind]; !ok {
		return ctrl.Result{}, nil // not a workload kind we manage
	}
	obj, _ := workload.New(kind)
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			r.Reporter.DeleteStatus(k)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if obj.GetDeletionTimestamp() != nil {
		return ctrl.Result{}, nil
	}
	if !r.Store.Attempted() {
		// Nothing is decided before the first pull; the puller re-triggers every workload after it.
		return ctrl.Result{}, nil
	}
	w := &run{r: r, ctx: ctx, k: k, kind: kind, obj: obj, now: r.now(), log: r.Log.With("workload", k.String())}
	return w.reconcile()
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// run is the state of one reconcile.
type run struct {
	r    *Reconciler
	ctx  context.Context
	k    wkey
	kind v1alpha1.WorkloadKind
	obj  client.Object
	now  time.Time
	log  *slog.Logger

	eff policy.Effective
	// base is the effective policy as resolved, before an approve-once grant raised audit
	// resources to auto-patch for this one change: it is what the RSR status and the hub show.
	base     policy.Effective
	strategy v1alpha1.Strategy
	rec      *hubclient.WorkloadRecommendation
	fresh    bool
	rsr      *v1alpha1.RightsizingRecommendation // nil until created
	prev     v1alpha1.RightsizingRecommendationStatus
	prevSpec v1alpha1.RightsizingRecommendationSpec
	reason   string
	message  string
	hpa      *hpaState // WO-11
	// frozen is true inside a freeze window: nothing is written except rollbacks (WO-10).
	frozen bool
	// holdEffective keeps the stored effective policy while a release waits for a freeze to end,
	// so the release (and its event) still happens afterwards.
	holdEffective bool
	// note is appended to the Applied condition message (OnDelete roll progress, WO-13).
	note string
	// paused: automation is paused in the hub config (D-022); frozen is set too.
	paused     bool
	pauseScope string
	pauseMsg   string
	// approval is the approve-once grant being applied (D-024); nil otherwise.
	approval *hubclient.ConfigApproval
	// approvedApply: this reconcile applied an approval (the Applied condition then says
	// ApprovedOnce, which keeps the field ownership after it, D-027).
	approvedApply bool
}

func (w *run) reconcile() (ctrl.Result, error) {
	r := w.r
	var policies v1alpha1.RightsizingPolicyList
	if err := r.Client.List(w.ctx, &policies); err != nil {
		return ctrl.Result{}, err
	}
	w.eff = policy.Resolve(policy.Workload{
		Namespace: w.k.Namespace, Kind: w.kind, Name: w.k.Name,
		Labels: w.obj.GetLabels(), Annotations: w.obj.GetAnnotations(),
	}, policies.Items, r.Cfg.ClusterDefaults)
	if r.Cfg.ReadOnly {
		for _, m := range []*v1alpha1.Mode{&w.eff.CPUMode, &w.eff.MemoryMode} {
			if *m == v1alpha1.ModeAutoPatch {
				*m = v1alpha1.ModeAudit
				w.eff.Warnings = append(w.eff.Warnings, "agent is read-only: auto-patch capped at audit")
			}
		}
	}
	w.eff.FreezeWindows = r.Cfg.Freeze.Merge(w.eff.FreezeWindows)
	if open, err := windows.Open(w.eff.FreezeWindows, w.now); open || err != nil {
		w.frozen = true // an invalid freeze window counts as frozen, like in the guardrails
	}
	if r.Admin != nil {
		if f := r.Admin.Flags(); f.Paused {
			w.paused, w.frozen = true, true
			w.pauseScope, w.pauseMsg = f.PauseScope, pauseMessage(f)
		}
	}
	w.base = w.eff
	for _, msg := range w.eff.Warnings {
		w.log.Warn("policy", "warning", msg)
	}
	rsr, err := r.findRSR(w.ctx, w.k.Namespace, w.kind, w.k.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if rsr != nil {
		w.rsr = rsr
		w.prev = *rsr.Status.DeepCopy()
		w.prevSpec = *rsr.Spec.DeepCopy()
	}
	w.rec, _ = r.Store.Get(w.k)
	w.fresh = r.Store.Fresh()
	w.approve()

	// Strategy in effect: an in-place fallback (spec.strategy rollout while the configured
	// strategy is in-place) sticks until the configured strategy changes.
	w.strategy = w.eff.Strategy
	if rsr != nil && rsr.Spec.Strategy == v1alpha1.StrategyRollout && w.eff.Strategy == v1alpha1.StrategyInPlace &&
		rsr.Status.Effective != nil && rsr.Status.Effective.Strategy == v1alpha1.StrategyInPlace {
		w.strategy = v1alpha1.StrategyRollout
	}

	if err := w.release(); err != nil {
		return ctrl.Result{}, err
	}
	res, err := w.decide()
	if err != nil {
		return res, err
	}
	if err := w.writeStatus(); err != nil {
		return ctrl.Result{}, err
	}
	w.report()
	return res, nil
}

// ---------------------------------------------------------------------------------------------
// Release (D-006)

func autoSet(e policy.Effective) map[corev1.ResourceName]bool {
	out := map[corev1.ResourceName]bool{}
	if e.CPUMode == v1alpha1.ModeAutoPatch {
		out[corev1.ResourceCPU] = true
	}
	if e.MemoryMode == v1alpha1.ModeAutoPatch {
		out[corev1.ResourceMemory] = true
	}
	return out
}

func usesTemplate(s v1alpha1.Strategy) bool { return s != v1alpha1.StrategyInPlace }

// release gives up ownership of template fields the agent should no longer own and records the
// workload leaving auto-patch.
func (w *run) release() error {
	r := w.r
	auto := autoSet(w.eff)
	want := map[corev1.ResourceName]bool{}
	owned := apply.OwnedResources(w.obj)
	if usesTemplate(w.strategy) {
		want = maps.Clone(auto)
		held, why := w.approvedHold(owned)
		for res := range held {
			want[res] = true
		}
		if why != "" {
			w.log.Info("releasing the field ownership of an approved change", "why", why, "recommendation", w.rsr.Status.AppliedRecommendationID)
		}
	}
	drop := false
	for res := range owned {
		if !want[res] {
			drop = true
		}
	}
	if drop && w.frozen && !r.Cfg.ReadOnly {
		// Releasing hands the fields back to Argo CD, which then changes the workload: wait.
		w.holdEffective = true
		w.reason, w.message = v1alpha1.ReasonFreezeWindow, "inside a freeze window: field ownership is released when it ends"
		if w.paused {
			w.reason, w.message = v1alpha1.ReasonPaused, w.pauseMsg+": field ownership is released when automation resumes"
		}
		w.emit(hubclient.Event{Type: hubclient.EventBlocked, Reason: w.reason, Message: w.message,
			Resources: keys(owned)}, "release", nil)
		return nil
	}
	if drop && !r.Cfg.ReadOnly {
		if _, err := apply.Release(w.ctx, r.Client, w.obj); err != nil {
			w.emit(hubclient.Event{Type: hubclient.EventFailed, Reason: v1alpha1.ReasonReleaseError,
				Message: err.Error(), Details: map[string]string{"error": err.Error()}}, "failed/"+v1alpha1.ReasonReleaseError, nil)
			return fmt.Errorf("release: %w", err)
		}
		w.log.Info("released field ownership", "owned", keys(owned), "keep", keys(want))
		// Re-own what stays in auto-patch, with the live values (no change).
		keep := map[corev1.ResourceName]bool{}
		for res := range owned {
			if want[res] {
				keep[res] = true
			}
		}
		if len(keep) > 0 && w.rsr != nil {
			targets := liveTargets(workload.PodSpec(w.obj))
			if err := apply.ApplyTemplate(w.ctx, r.Client, w.obj, w.kind, targets, keep, w.ownAnnotations(w.rsr.Status.AppliedRecommendationID, w.rsr.Status.Previous)); err != nil {
				return fmt.Errorf("re-apply after release: %w", err)
			}
		}
	}

	if err := w.releaseHPA(want); err != nil {
		return err
	}
	if w.rsr == nil || w.prev.Effective == nil {
		return nil
	}
	prevAuto := map[corev1.ResourceName]bool{}
	if w.prev.Effective.CPUMode == v1alpha1.ModeAutoPatch {
		prevAuto[corev1.ResourceCPU] = true
	}
	if w.prev.Effective.MemoryMode == v1alpha1.ModeAutoPatch {
		prevAuto[corev1.ResourceMemory] = true
	}
	var left []string
	for _, res := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if prevAuto[res] && !auto[res] {
			left = append(left, string(res))
		}
	}
	if len(left) == 0 {
		return nil
	}
	details := map[string]string{}
	for _, res := range left {
		prevMode, newMode := w.prev.Effective.CPUMode, w.eff.CPUMode
		if res == "memory" {
			prevMode, newMode = w.prev.Effective.MemoryMode, w.eff.MemoryMode
		}
		details["previousMode"], details["newMode"] = string(prevMode), string(newMode)
	}
	if len(auto) == 0 {
		w.rsr.Status.Phase = v1alpha1.PhaseReleased
		w.reason, w.message = v1alpha1.ReasonModeChanged, "workload left auto-patch; field ownership released"
	}
	// Persist the new effective modes before emitting: the event is edge-triggered on the stored
	// modes, so a reconcile that fails later must not emit it again (exactly once).
	if err := w.writeStatus(); err != nil {
		return fmt.Errorf("record release: %w", err)
	}
	w.emit(hubclient.Event{Type: hubclient.EventReleased, Reason: v1alpha1.ReasonModeChanged,
		Strategy: string(w.rsr.Spec.Strategy), Resources: left, Details: details,
		RecommendationID: w.rsr.Status.AppliedRecommendationID,
		Message:          fmt.Sprintf("%s left auto-patch; field ownership released", strings.Join(left, " and "))}, "", nil)
	return nil
}

func keys(m map[corev1.ResourceName]bool) []string {
	var out []string
	for k := range m {
		out = append(out, string(k))
	}
	slices.Sort(out)
	return out
}

func liveTargets(spec *corev1.PodSpec) []v1alpha1.ContainerRecommendation {
	var out []v1alpha1.ContainerRecommendation
	for _, c := range workload.Live(spec) {
		out = append(out, v1alpha1.ContainerRecommendation{ContainerResources: c})
	}
	return out
}

func (w *run) ownAnnotations(recID string, previous []v1alpha1.ContainerResources) map[string]string {
	ann := map[string]string{v1alpha1.AnnotationManaged: "true"}
	if recID != "" {
		ann[v1alpha1.AnnotationRecommendationID] = recID
	}
	if len(previous) > 0 {
		ann[v1alpha1.AnnotationPreviousResources] = workload.PreviousJSON(previous)
	}
	return ann
}

// ---------------------------------------------------------------------------------------------
// Decision

// containersForEval returns the containers with the values the workload is running: the pod
// template, overlaid with the in-place values the agent applied.
func (w *run) containersForEval() []workload.Container {
	cs := workload.Containers(workload.PodSpec(w.obj))
	if w.rsr == nil || w.rsr.Spec.Strategy != v1alpha1.StrategyInPlace ||
		(w.prev.Phase != v1alpha1.PhaseApplied && w.prev.Phase != v1alpha1.PhaseApplying) {
		return cs
	}
	for i := range cs {
		for _, t := range w.rsr.Spec.Containers {
			if t.Name != cs[i].Name {
				continue
			}
			res := cs[i].Resources.DeepCopy()
			for rn, q := range t.Requests {
				if res.Requests == nil {
					res.Requests = corev1.ResourceList{}
				}
				res.Requests[rn] = q
			}
			for rn, q := range t.Limits {
				if res.Limits == nil {
					res.Limits = corev1.ResourceList{}
				}
				res.Limits[rn] = q
			}
			cs[i].Resources = *res
		}
	}
	return cs
}

func (w *run) decide() (ctrl.Result, error) {
	r := w.r
	auto := autoSet(w.eff)
	requeue := ctrl.Result{RequeueAfter: r.Cfg.ApplyingRequeue}

	// An in-place change in flight keeps going (offline too: it was decided while online).
	if w.rsr != nil && w.prev.Phase == v1alpha1.PhaseApplying && len(auto) > 0 {
		if w.strategy == v1alpha1.StrategyInPlace && w.rsr.Spec.Strategy == v1alpha1.StrategyInPlace {
			return w.progressInPlace()
		}
	}
	// An OnDelete StatefulSet roll in flight keeps going (WO-13).
	if res, active, err := w.rollOnDelete(); err != nil || active {
		return res, err
	}

	var lastApplied, rolledBack *time.Time
	if w.rsr != nil {
		if t := w.rsr.Status.AppliedAt; t != nil {
			lastApplied = &t.Time
		}
		if t := w.rsr.Status.RolledBackAt; t != nil {
			rolledBack = &t.Time
		}
	}
	hpa, err := w.planHPA()
	if err != nil {
		return ctrl.Result{}, err
	}
	disruption, err := w.disruption()
	if err != nil {
		return ctrl.Result{}, err
	}
	w.hpa = hpa
	if w.hpa.coordinate && w.strategy == v1alpha1.StrategyInPlace {
		w.strategy = v1alpha1.StrategyRollout // in-place is not used for HPA workloads (WO-11)
	}
	in := guardrails.Input{
		Now: w.now, Effective: w.eff, Strategy: w.strategy, Rec: w.rec, HubFresh: w.fresh,
		Containers:    w.containersForEval(),
		LastAppliedAt: lastApplied, RolledBackAt: rolledBack, Tolerance: r.Cfg.Tolerance,
		RolledBackResources: w.rolledBackResources(), Disruption: disruption,
		Paused: w.paused, PauseMessage: w.pauseMsg,
	}
	w.hpa.guardrailInput(&in)
	dec := guardrails.Evaluate(in)
	if dec.Outcome == guardrails.Apply {
		if msg := w.planTarget(w.hpa, dec); msg != "" {
			dec = guardrails.Decision{Outcome: guardrails.Blocked, Reason: v1alpha1.ReasonHPACoupled, Message: msg, Proposed: dec.Proposed}
		}
	}

	// Values of the current recommendation are live: keep them live (drift repair, silently).
	if w.rsr != nil && w.rec != nil && len(auto) > 0 && w.prev.Phase == v1alpha1.PhaseApplied &&
		w.rsr.Status.AppliedRecommendationID == w.rec.ID {
		w.reason, w.message = v1alpha1.ReasonRecommendationApplied, "recommended values are live"
		return w.keepLive()
	}
	// Rolled back by the health watch (WO-08): never re-apply the same recommendation.
	if w.rsr != nil && w.rec != nil && w.prev.Phase == v1alpha1.PhaseRolledBack && w.rsr.Spec.Source.RecommendationID == w.rec.ID {
		w.reason, w.message = condReasonFromConditions(w.prev), "rolled back; waiting for a new recommendation"
		return ctrl.Result{}, nil
	}

	switch dec.Outcome {
	case guardrails.Deferred:
		return w.deferChange(dec)

	case guardrails.NoRecommendation:
		w.reason, w.message = condReasonPending, dec.Message
		return ctrl.Result{}, nil

	case guardrails.Skipped, guardrails.Blocked:
		w.reason, w.message = dec.Reason, dec.Message
		evType := hubclient.EventSkipped
		if dec.Outcome == guardrails.Blocked {
			evType = hubclient.EventBlocked
		}
		if w.rec == nil {
			// Without a recommendation only "hub unreachable" is worth an event (once); there is
			// nothing to record in a CR.
			if dec.Outcome == guardrails.Blocked {
				w.emit(hubclient.Event{Type: evType, Reason: dec.Reason, Message: dec.Message}, slotDecision, nil)
			}
			return ctrl.Result{}, nil
		}
		live := w.rsr != nil && w.rsr.Status.Phase == v1alpha1.PhaseApplied
		if !live {
			targets := dec.Targets
			if len(auto) == 0 || len(targets) == 0 {
				targets = dec.Proposed
			}
			if err := w.upsertSpec(targets, w.specStrategy()); err != nil {
				return ctrl.Result{}, err
			}
			switch {
			case w.rsr.Status.Phase == v1alpha1.PhaseRolledBack:
				// The previous values are live again; RolledBack describes that until a new change
				// is applied (WO-08).
			case dec.Outcome == guardrails.Blocked:
				w.rsr.Status.Phase = v1alpha1.PhaseBlocked
			case w.rsr.Status.Phase == v1alpha1.PhaseReleased:
				// keep
			default:
				w.rsr.Status.Phase = v1alpha1.PhasePending
			}
		}
		w.emit(hubclient.Event{Type: evType, Reason: dec.Reason, Message: dec.Message,
			RecommendationID: w.rec.ID, Resources: refusedResources(dec)}, slotDecision, recValues(w.rec))
		return ctrl.Result{}, nil
	}

	// Apply.
	for _, ref := range dec.Refusals {
		w.emit(hubclient.Event{Type: hubclient.EventBlocked, Reason: ref.Reason, Message: ref.Message,
			RecommendationID: w.rec.ID, Resources: []string{ref.Resource}}, "refusal/"+ref.Resource, recValues(w.rec))
	}
	if w.strategy == v1alpha1.StrategyInPlace {
		res, err := w.startInPlace(dec)
		if err != nil || res.RequeueAfter > 0 {
			return res, err
		}
		return requeue, nil
	}
	if err := w.applyRollout(dec.Targets, w.liveValues(), w.strategy, ""); err != nil || !apply.IsOnDelete(w.obj) {
		return ctrl.Result{}, err
	}
	return requeue, nil
}

// rolledBackResources are the resources the last rollback restored, from the HealthWatch
// condition reason (OOMKills -> memory, Throttling -> cpu, otherwise all).
func (w *run) rolledBackResources() []string {
	if w.rsr == nil || w.rsr.Status.RolledBackAt == nil {
		return nil
	}
	if c := meta.FindStatusCondition(w.rsr.Status.Conditions, v1alpha1.ConditionHealthWatch); c != nil && c.Status == metav1.ConditionFalse {
		return health.ResourcesForReason(c.Reason)
	}
	return nil
}

func refusedResources(dec guardrails.Decision) []string {
	var out []string
	for _, ref := range dec.Refusals {
		if ref.Reason == dec.Reason && !slices.Contains(out, ref.Resource) {
			out = append(out, ref.Resource)
		}
	}
	return out
}

func condReasonFromConditions(st v1alpha1.RightsizingRecommendationStatus) string {
	if c := meta.FindStatusCondition(st.Conditions, v1alpha1.ConditionApplied); c != nil {
		return c.Reason
	}
	return ""
}

func (w *run) specStrategy() v1alpha1.Strategy {
	if w.rsr != nil && w.rsr.Status.Phase == v1alpha1.PhaseApplying {
		return w.rsr.Spec.Strategy
	}
	return w.strategy
}

func (w *run) liveValues() []v1alpha1.ContainerResources {
	return workload.Live(workload.PodSpec(w.obj))
}

// keepLive re-asserts the applied values without an event: Argo CD or a user may have reverted a
// template field, or pods were recreated without the webhook.
func (w *run) keepLive() (ctrl.Result, error) {
	r := w.r
	own := w.ownedTargets()
	if len(own) == 0 || r.Cfg.ReadOnly || w.frozen {
		return ctrl.Result{}, nil
	}
	// The health watch (WO-08) may have rolled this change back a moment ago and the cached RSR
	// not show it yet: confirm the phase uncached before re-asserting anything.
	var fresh v1alpha1.RightsizingRecommendation
	if err := r.Reader.Get(w.ctx, client.ObjectKeyFromObject(w.rsr), &fresh); err != nil {
		return ctrl.Result{}, err
	}
	if fresh.Status.Phase != v1alpha1.PhaseApplied || fresh.Status.AppliedRecommendationID != w.rsr.Status.AppliedRecommendationID {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	targets := w.rsr.Spec.Containers
	if w.rsr.Spec.Strategy != v1alpha1.StrategyInPlace {
		if err := w.keepHPA(own); err != nil {
			return ctrl.Result{}, err
		}
		if templateMatches(workload.PodSpec(w.obj), targets, own) && hasAll(apply.OwnedResources(w.obj), own) {
			return ctrl.Result{}, nil
		}
		w.log.Info("re-applying live values (template drifted)")
		err := apply.ApplyTemplate(w.ctx, r.Client, w.obj, w.kind, targets, own,
			w.ownAnnotations(w.rsr.Status.AppliedRecommendationID, w.rsr.Status.Previous))
		return ctrl.Result{}, err
	}
	pods, err := workload.Pods(w.ctx, r.Reader, w.obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	pending := false
	for i := range pods {
		st := apply.ObserveResize(&pods[i], targets, own)
		if st.State != apply.ResizeNeeded {
			continue
		}
		if ok, _ := w.nodesSupportInPlace(pods[i:i+1], own[corev1.ResourceMemory]); !ok {
			continue
		}
		if _, err := apply.Resize(w.ctx, r.Client, &pods[i], targets, own, w.rsr.Spec.Source.RecommendationID); err != nil {
			w.log.Warn("resize of a new pod failed", "pod", pods[i].Name, "err", err)
			continue
		}
		w.log.Info("resized pod to the applied values", "pod", pods[i].Name)
		pending = true
	}
	if pending {
		return ctrl.Result{RequeueAfter: r.Cfg.ApplyingRequeue}, nil
	}
	return ctrl.Result{}, nil
}

func hasAll(have, want map[corev1.ResourceName]bool) bool {
	for k := range want {
		if !have[k] {
			return false
		}
	}
	return true
}

func templateMatches(spec *corev1.PodSpec, targets []v1alpha1.ContainerRecommendation, own map[corev1.ResourceName]bool) bool {
	for _, t := range targets {
		c, ok := workload.FindContainer(spec, t.Name)
		if !ok {
			continue
		}
		for r := range own {
			if q, ok := t.Requests[r]; ok {
				if cur, has := c.Resources.Requests[r]; !has || cur.Cmp(q) != 0 {
					return false
				}
			}
			if q, ok := t.Limits[r]; ok {
				if cur, has := c.Resources.Limits[r]; !has || cur.Cmp(q) != 0 {
					return false
				}
			}
			if slices.Contains(t.RemoveLimits, r) {
				if _, has := c.Resources.Limits[r]; has {
					return false
				}
			}
		}
	}
	return true
}

// ownedTargets are the auto-patch resources that the RSR's spec carries values for.
func (w *run) ownedTargets() map[corev1.ResourceName]bool {
	auto := autoSet(w.eff)
	out := map[corev1.ResourceName]bool{}
	for _, t := range w.rsr.Spec.Containers {
		for res := range auto {
			if _, ok := t.Requests[res]; ok {
				out[res] = true
			}
			if _, ok := t.Limits[res]; ok {
				out[res] = true
			}
			if slices.Contains(t.RemoveLimits, res) {
				out[res] = true
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// Deferred (WO-10)

// deferChange queues a change of a deferred workload: the RSR carries the guardrailed values,
// phase Deferred and the next maintenance window; the reconcile is requeued for when it opens,
// and the change is then applied as a rollout (re-evaluated against the guardrails first).
func (w *run) deferChange(dec guardrails.Decision) (ctrl.Result, error) {
	if err := w.upsertSpec(dec.Targets, v1alpha1.StrategyDeferred); err != nil {
		return ctrl.Result{}, err
	}
	w.rsr.Status.Phase = v1alpha1.PhaseDeferred
	w.reason, w.message = dec.Reason, dec.Message
	res := ctrl.Result{}
	if next, ok := windows.Next(w.eff.MaintenanceWindows, w.now); ok {
		t := metav1.NewTime(next)
		w.rsr.Status.NextWindow = &t
		w.message = fmt.Sprintf("strategy deferred: %s applied in the next maintenance window at %s",
			guardrails.Summary(dec.Changes), next.UTC().Format(time.RFC3339))
		res.RequeueAfter = next.Sub(w.now) + time.Second
	} else {
		w.rsr.Status.NextWindow = nil
	}
	w.emit(hubclient.Event{Type: hubclient.EventBlocked, Reason: dec.Reason, Message: w.message,
		RecommendationID: w.rec.ID, Strategy: string(v1alpha1.StrategyDeferred)}, slotDecision, recValues(w.rec))
	return res, nil
}

// ---------------------------------------------------------------------------------------------
// Rollout

func (w *run) applyRollout(targets []v1alpha1.ContainerRecommendation, previous []v1alpha1.ContainerResources,
	strategy v1alpha1.Strategy, fallbackFrom string) error {
	r := w.r
	own := autoSet(w.eff)
	recID := w.rec.ID
	if fallbackFrom != "" {
		recID = w.rsr.Spec.Source.RecommendationID
	}
	if fallbackFrom == "" {
		if err := w.upsertSpec(targets, strategy); err != nil {
			return err
		}
		w.rsr.Status.Previous = previous
		w.rsr.Status.PreviousHPATarget = nil
		if w.hpa != nil && w.hpa.target != nil {
			prev := w.hpa.prev
			w.rsr.Status.PreviousHPATarget = &prev
		}
	} else {
		status := w.rsr.Status.DeepCopy()
		w.rsr.Spec.Strategy = v1alpha1.StrategyRollout
		if err := r.Client.Update(w.ctx, w.rsr); err != nil {
			return err
		}
		w.rsr.Status = *status
	}
	w.rsr.Status.Phase = v1alpha1.PhaseApplying
	if err := w.writeStatus(); err != nil {
		return err
	}
	if err := apply.ApplyTemplate(w.ctx, r.Client, w.obj, w.kind, targets, own, w.ownAnnotations(recID, previous)); err != nil {
		w.reason, w.message = v1alpha1.ReasonApplyError, err.Error()
		w.emit(hubclient.Event{Type: hubclient.EventFailed, Reason: v1alpha1.ReasonApplyError, RecommendationID: recID,
			Strategy: string(strategy), Message: err.Error(), Details: map[string]string{"error": err.Error()}}, "failed/"+v1alpha1.ReasonApplyError+"/"+recID, nil)
		return fmt.Errorf("apply %s: %w", w.k, err)
	}
	// The HPA target follows in the same reconcile (WO-11). If it fails, the change is recorded as
	// applied anyway and keepLive re-asserts the target on the next reconcile.
	if err := w.applyHPATarget(w.hpa); err != nil {
		w.emit(hubclient.Event{Type: hubclient.EventFailed, Reason: v1alpha1.ReasonApplyError, RecommendationID: recID,
			Strategy: string(strategy), Message: "HPA target: " + err.Error(), Details: map[string]string{"error": err.Error()}},
			"failed/"+v1alpha1.ReasonApplyError+"/hpa/"+recID, nil)
		w.log.Warn("HPA target not applied; retried by drift repair", "err", err)
	}
	changes := diff(previous, targets, own)
	hpaDetails, hpaMsg := w.hpa.details()
	now := metav1.NewTime(w.now)
	w.rsr.Status.Phase = v1alpha1.PhaseApplied
	w.rsr.Status.AppliedAt = &now
	w.rsr.Status.AppliedRecommendationID = recID
	w.reason = v1alpha1.ReasonRecommendationApplied
	w.message = "pod template patched, rollout started: " + guardrails.Summary(changes) + hpaMsg
	if note := w.onDeleteNote(); note != "" {
		w.message = "pod template patched: " + guardrails.Summary(changes) + hpaMsg + "; " + note
		w.note = note
	}
	w.emit(hubclient.Event{Type: hubclient.EventApplied, Reason: v1alpha1.ReasonRecommendationApplied,
		RecommendationID: recID, Strategy: string(strategy), Resources: resourcesOf(changes),
		Containers: changes, Message: w.message, Details: hpaDetails}, "", nil)
	w.log.Info("applied (rollout)", "recommendation", recID, "changes", guardrails.Summary(changes))
	return nil
}

// ---------------------------------------------------------------------------------------------
// In-place (D-007)

func (w *run) nodesSupportInPlace(pods []corev1.Pod, memoryChange bool) (bool, string) {
	seen := map[string]bool{}
	for _, p := range pods {
		if p.Spec.NodeName == "" || seen[p.Spec.NodeName] {
			continue
		}
		seen[p.Spec.NodeName] = true
		var node corev1.Node
		if err := w.r.Client.Get(w.ctx, client.ObjectKey{Name: p.Spec.NodeName}, &node); err != nil {
			continue
		}
		if ok, why := apply.NodeSupportsInPlace(&node, w.r.Cfg.UnsupportedNodeSelectors, memoryChange); !ok {
			return false, why
		}
	}
	return true, ""
}

func (w *run) startInPlace(dec guardrails.Decision) (ctrl.Result, error) {
	r := w.r
	pods, err := workload.Pods(w.ctx, r.Reader, w.obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ok, why := w.nodesSupportInPlace(pods, slices.Contains(dec.Resources, "memory")); !ok {
		if err := w.upsertSpec(dec.Targets, v1alpha1.StrategyInPlace); err != nil {
			return ctrl.Result{}, err
		}
		if w.rsr.Status.Phase != v1alpha1.PhaseApplied {
			w.rsr.Status.Phase = v1alpha1.PhaseBlocked
		}
		w.reason, w.message = v1alpha1.ReasonUnsupportedNode, why
		w.emit(hubclient.Event{Type: hubclient.EventBlocked, Reason: v1alpha1.ReasonUnsupportedNode,
			RecommendationID: w.rec.ID, Message: why}, slotDecision, recValues(w.rec))
		return ctrl.Result{}, nil
	}
	previous := w.liveValues()
	if len(pods) > 0 {
		previous = workload.Live(&pods[0].Spec)
	}
	if err := w.upsertSpec(dec.Targets, v1alpha1.StrategyInPlace); err != nil {
		return ctrl.Result{}, err
	}
	w.rsr.Status.Previous = previous
	w.rsr.Status.Phase = v1alpha1.PhaseApplying
	w.reason, w.message = condReasonApplying, "resizing running pods: "+guardrails.Summary(dec.Changes)
	if err := w.writeStatus(); err != nil {
		return ctrl.Result{}, err
	}
	w.log.Info("applying (in-place)", "recommendation", w.rec.ID, "pods", len(pods), "changes", guardrails.Summary(dec.Changes))
	return w.progressInPlace()
}

func (w *run) progressInPlace() (ctrl.Result, error) {
	r := w.r
	targets := w.rsr.Spec.Containers
	own := w.ownedTargets()
	recID := w.rsr.Spec.Source.RecommendationID
	pods, err := workload.Pods(w.ctx, r.Reader, w.obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	done := true
	frozenWait := false
	var deferred []string
	for i := range pods {
		st := apply.ObserveResize(&pods[i], targets, own)
		if w.frozen && st.State != apply.ResizeDone && st.State != apply.ResizeInProgress {
			// Inside a freeze only watch what was already submitted; no new resize, no fallback.
			done = false
			frozenWait = true
			continue
		}
		switch st.State {
		case apply.ResizeDone:
			continue
		case apply.ResizeNeeded:
			if _, err := apply.Resize(w.ctx, r.Client, &pods[i], targets, own, recID); err != nil {
				if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
					return w.fallback(v1alpha1.ReasonResizeInfeasible, fmt.Sprintf("pod %s: resize rejected: %v", pods[i].Name, err), "Rejected")
				}
				if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
					done = false
					continue
				}
				w.emit(hubclient.Event{Type: hubclient.EventFailed, Reason: v1alpha1.ReasonResizeError, RecommendationID: recID,
					Strategy: string(v1alpha1.StrategyInPlace), Message: err.Error(), Details: map[string]string{"error": err.Error()}}, "failed/"+v1alpha1.ReasonResizeError+"/"+recID, nil)
				return ctrl.Result{}, fmt.Errorf("resize pod %s: %w", pods[i].Name, err)
			}
			done = false
		case apply.ResizeInfeasible:
			return w.fallback(v1alpha1.ReasonResizeInfeasible, fmt.Sprintf("pod %s: %s", st.Pod, st.Message), "Infeasible")
		case apply.ResizeDeferred:
			if !st.Since.IsZero() && w.now.Sub(st.Since) > w.eff.FallbackAfter {
				return w.fallback(v1alpha1.ReasonResizeDeferredTimeout,
					fmt.Sprintf("pod %s: resize deferred for %s (fallbackAfter %s): %s", st.Pod, w.now.Sub(st.Since).Round(time.Second), w.eff.FallbackAfter, st.Message), "Deferred")
			}
			deferred = append(deferred, st.Pod)
			done = false
		default:
			done = false
		}
	}
	if !done {
		w.rsr.Status.Phase = v1alpha1.PhaseApplying
		w.reason = condReasonApplying
		if len(deferred) > 0 {
			w.message = "resize deferred on " + strings.Join(deferred, ", ")
		}
		if frozenWait {
			w.message = "inside a freeze window: remaining pods are resized when it ends"
			if w.paused {
				w.message = w.pauseMsg + ": remaining pods are resized when automation resumes"
			}
		}
		if err := w.writeStatus(); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.Cfg.ApplyingRequeue}, nil
	}
	changes := diff(w.rsr.Status.Previous, targets, own)
	now := metav1.NewTime(w.now)
	w.rsr.Status.Phase = v1alpha1.PhaseApplied
	w.rsr.Status.AppliedAt = &now
	w.rsr.Status.AppliedRecommendationID = recID
	w.reason = v1alpha1.ReasonRecommendationApplied
	w.message = fmt.Sprintf("%d pod(s) resized in place: %s", len(pods), guardrails.Summary(changes))
	w.emit(hubclient.Event{Type: hubclient.EventApplied, Reason: v1alpha1.ReasonRecommendationApplied,
		RecommendationID: recID, Strategy: string(v1alpha1.StrategyInPlace), Resources: resourcesOf(changes),
		Containers: changes, Message: w.message}, "", nil)
	w.log.Info("applied (in-place)", "recommendation", recID, "pods", len(pods))
	return ctrl.Result{}, nil
}

// fallback switches an in-place change to rollout (D-007) and applies it to the template.
func (w *run) fallback(reason, msg, resizeStatus string) (ctrl.Result, error) {
	recID := w.rsr.Spec.Source.RecommendationID
	if why, err := w.fallbackUnsafe(); err != nil {
		return ctrl.Result{}, err
	} else if why != "" {
		return w.blockFallback(reason, msg, why)
	}
	w.emit(hubclient.Event{Type: hubclient.EventFallback, Reason: reason, RecommendationID: recID,
		Strategy: string(v1alpha1.StrategyRollout), Resources: keys(w.ownedTargets()), Message: msg,
		Details: map[string]string{"resizeStatus": resizeStatus}}, "", nil)
	w.log.Warn("in-place resize fell back to rollout", "reason", reason, "message", msg)
	w.strategy = v1alpha1.StrategyRollout
	if w.rec == nil || w.rec.ID != recID {
		// Rollout the values that were being applied.
		w.rec = &hubclient.WorkloadRecommendation{ID: recID}
	}
	return ctrl.Result{}, w.applyRollout(w.rsr.Spec.Containers, w.rsr.Status.Previous, v1alpha1.StrategyRollout, reason)
}

// ---------------------------------------------------------------------------------------------
// Helpers

func diff(previous []v1alpha1.ContainerResources, targets []v1alpha1.ContainerRecommendation, own map[corev1.ResourceName]bool) []hubclient.ContainerChange {
	var out []hubclient.ContainerChange
	for _, t := range targets {
		var prev v1alpha1.ContainerResources
		for _, p := range previous {
			if p.Name == t.Name {
				prev = p
			}
		}
		chg := hubclient.ContainerChange{Name: t.Name, From: &hubclient.Resources{}, To: &hubclient.Resources{}}
		changed := false
		for _, res := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if !own[res] {
				continue
			}
			cmp := func(from, to corev1.ResourceList, remove bool, fromV, toV **hubclient.ResourceValues) {
				f, fok := from[res]
				tq, tok := to[res]
				if remove {
					tok = false
				} else if !tok {
					return // unchanged
				}
				if fok == tok && (!fok || f.Cmp(tq) == 0) {
					return
				}
				changed = true
				*fromV = putV(*fromV, res, f, fok)
				*toV = putV(*toV, res, tq, tok)
			}
			cmp(prev.Requests, t.Requests, false, &chg.From.Requests, &chg.To.Requests)
			cmp(prev.Limits, t.Limits, slices.Contains(t.RemoveLimits, res), &chg.From.Limits, &chg.To.Limits)
		}
		if changed {
			out = append(out, chg)
		}
	}
	return out
}

func putV(v *hubclient.ResourceValues, res corev1.ResourceName, q resource.Quantity, ok bool) *hubclient.ResourceValues {
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

func resourcesOf(changes []hubclient.ContainerChange) []string {
	set := map[string]bool{}
	for _, c := range changes {
		for _, v := range []*hubclient.ResourceValues{c.From.Requests, c.To.Requests, c.From.Limits, c.To.Limits} {
			if v == nil {
				continue
			}
			if v.CPU != "" {
				set["cpu"] = true
			}
			if v.Memory != "" {
				set["memory"] = true
			}
		}
	}
	var out []string
	for _, r := range []string{"cpu", "memory"} {
		if set[r] {
			out = append(out, r)
		}
	}
	return out
}

// findRSR looks a RightsizingRecommendation up by its target (never by name).
func (r *Reconciler) findRSR(ctx context.Context, ns string, kind v1alpha1.WorkloadKind, name string) (*v1alpha1.RightsizingRecommendation, error) {
	var list v1alpha1.RightsizingRecommendationList
	if err := r.Client.List(ctx, &list, client.InNamespace(ns),
		client.MatchingFields{v1alpha1.IndexTargetRef: v1alpha1.TargetRefIndexValue(kind, name)}); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	slices.SortFunc(list.Items, func(a, b v1alpha1.RightsizingRecommendation) int {
		return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
	})
	return &list.Items[0], nil
}

// upsertSpec creates the RSR or updates its spec.
func (w *run) upsertSpec(targets []v1alpha1.ContainerRecommendation, strategy v1alpha1.Strategy) error {
	r := w.r
	if len(targets) == 0 {
		for _, c := range workload.Containers(workload.PodSpec(w.obj)) {
			targets = append(targets, v1alpha1.ContainerRecommendation{ContainerResources: v1alpha1.ContainerResources{Name: c.Name}})
			break
		}
	}
	gvk := workload.Kinds[w.kind]
	spec := v1alpha1.RightsizingRecommendationSpec{
		TargetRef:  v1alpha1.TargetRef{APIVersion: gvk.GroupVersion().String(), Kind: w.kind, Name: w.k.Name},
		Containers: targets,
		Strategy:   strategy,
		Source: v1alpha1.RecommendationSource{
			RecommendationID: w.rec.ID,
			GeneratedAt:      metav1.NewTime(w.rec.GeneratedAt),
			Confidence:       fmt.Sprintf("%.3g", w.rec.Confidence),
			RiskFlags:        riskFlags(w.rec.RiskFlags),
		},
	}
	if w.hpa != nil && w.hpa.target != nil {
		t := *w.hpa.target
		spec.HPA = &t
	}
	if w.rsr != nil {
		if specEqual(w.rsr.Spec, spec) {
			return nil
		}
		status := w.rsr.Status.DeepCopy()
		w.rsr.Spec = spec
		if err := r.Client.Update(w.ctx, w.rsr); err != nil {
			return err
		}
		w.rsr.Status = *status
		return nil
	}
	name := v1alpha1.RecommendationName(w.kind, w.k.Name, false)
	var existing v1alpha1.RightsizingRecommendation
	err := r.Client.Get(w.ctx, client.ObjectKey{Namespace: w.k.Namespace, Name: name}, &existing)
	if err == nil && (existing.Spec.TargetRef.Kind != w.kind || existing.Spec.TargetRef.Name != w.k.Name) {
		name = v1alpha1.RecommendationName(w.kind, w.k.Name, true)
	} else if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	rsr := &v1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: w.k.Namespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "resource-optimizer-agent"}},
		Spec: spec,
	}
	if err := controllerutil.SetOwnerReference(w.obj, rsr, r.Client.Scheme()); err != nil {
		return err
	}
	if err := r.Client.Create(w.ctx, rsr); err != nil {
		return err
	}
	w.rsr = rsr
	w.prev = v1alpha1.RightsizingRecommendationStatus{}
	return nil
}

func specEqual(a, b v1alpha1.RightsizingRecommendationSpec) bool {
	return jsonEqual(a, b)
}

func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

func riskFlags(in []string) []v1alpha1.RiskFlag {
	var out []v1alpha1.RiskFlag
	for _, f := range in {
		if !slices.Contains(out, v1alpha1.RiskFlag(f)) {
			out = append(out, v1alpha1.RiskFlag(f))
		}
	}
	return out
}

// writeStatus writes the RSR status (effective policy, phase, conditions) if it changed.
func (w *run) writeStatus() error {
	if w.rsr == nil {
		return nil
	}
	st := &w.rsr.Status
	if st.Phase == "" {
		st.Phase = v1alpha1.PhasePending
	}
	st.ObservedGeneration = w.rsr.Generation
	if !w.holdEffective || st.Effective == nil {
		st.Effective = w.base.API()
	}
	if st.Phase != v1alpha1.PhaseDeferred {
		st.NextWindow = nil
	}
	b := w.base
	msg := fmt.Sprintf("policy %q; cpu %s (%s), memory %s (%s); strategy %s; profile %s",
		b.Policy, b.CPUMode, b.CPUModeSource, b.MemoryMode, b.MemoryModeSource, b.Strategy, b.Profile)
	if b.Override != "" {
		msg += fmt.Sprintf("; override %q", b.Override)
	}
	if w.approval != nil {
		msg += fmt.Sprintf("; approved once by %s (approval %s)", w.approval.ApprovedBy, w.approval.ID)
	}
	if len(b.Warnings) > 0 {
		msg += "; " + strings.Join(b.Warnings, "; ")
	}
	setCond(st, v1alpha1.ConditionPolicyResolved, metav1.ConditionTrue, condReasonResolved, msg)

	reason, message := w.reason, w.message
	if reason == "" {
		reason = string(st.Phase)
	}
	blocked := st.Phase == v1alpha1.PhaseBlocked || isBlockedReason(reason)
	if st.Phase == v1alpha1.PhaseApplied {
		applied := "recommended values are live"
		if st.AppliedRecommendationID != "" {
			applied = "recommendation " + st.AppliedRecommendationID + " is live"
		}
		if w.note != "" {
			applied += "; " + w.note
		}
		appliedReason := v1alpha1.ReasonRecommendationApplied
		if w.approvedApply || (approvedLive(w.prev) && st.AppliedRecommendationID == w.prev.AppliedRecommendationID) {
			// Remembered across restarts: the approved values keep their field ownership (D-027).
			appliedReason = v1alpha1.ReasonApprovedOnce
			applied += " (approved once)"
		}
		setCond(st, v1alpha1.ConditionApplied, metav1.ConditionTrue, appliedReason, applied)
	} else {
		// The reason is the latest decision (e.g. ModeAudit for a Released workload): the phase
		// already says Released, and the reason lets a restarted agent recognise events it emitted.
		setCond(st, v1alpha1.ConditionApplied, metav1.ConditionFalse, reason, message)
	}
	switch {
	case blocked:
		setCond(st, v1alpha1.ConditionBlocked, metav1.ConditionTrue, reason, message)
	case st.Phase == v1alpha1.PhaseApplied && isSkipReason(reason):
		// Values are live and a newer recommendation was skipped: keep the reason here (the
		// Applied condition stays True), so a restarted agent knows it reported it (D-017).
		setCond(st, v1alpha1.ConditionBlocked, metav1.ConditionFalse, reason, message)
	default:
		setCond(st, v1alpha1.ConditionBlocked, metav1.ConditionFalse, condReasonNotBlocked, "")
	}

	if reflect.DeepEqual(normStatus(w.prev), normStatus(*st)) {
		return nil
	}
	if err := w.r.Client.Status().Update(w.ctx, w.rsr); err != nil {
		return err
	}
	w.prev = *w.rsr.Status.DeepCopy()
	return nil
}

func normStatus(s v1alpha1.RightsizingRecommendationStatus) string {
	s = *s.DeepCopy()
	for i := range s.Conditions {
		s.Conditions[i].LastTransitionTime = metav1.Time{}
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func isBlockedReason(r string) bool {
	switch r {
	case v1alpha1.ReasonLearningPeriod, v1alpha1.ReasonLowConfidence, v1alpha1.ReasonCooldown, v1alpha1.ReasonHPACoupled,
		v1alpha1.ReasonThrottled, v1alpha1.ReasonOOMKilled, v1alpha1.ReasonFreezeWindow, v1alpha1.ReasonOutsideMaintenanceWindow,
		v1alpha1.ReasonUnsupportedNode, v1alpha1.ReasonRestartPolicyRequired, v1alpha1.ReasonHubUnreachable,
		v1alpha1.ReasonSingleReplicaUnsafe, v1alpha1.ReasonPodDisruptionBudget, v1alpha1.ReasonPaused:
		return true
	}
	return false
}

func setCond(st *v1alpha1.RightsizingRecommendationStatus, typ string, status metav1.ConditionStatus, reason, msg string) {
	if len(msg) > 32768 {
		msg = msg[:32768]
	}
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: typ, Status: status, Reason: reason, Message: msg})
}

// slotDecision is the dedup slot of workload-level skipped/blocked events.
const slotDecision = "decision"

// signature identifies a de-duplicated event: type, reason and resources must match exactly, the
// values (the hub's recommended values) within the agent's tolerance.
type signature struct {
	head   string
	values []v1alpha1.ContainerRecommendation
}

// recValues are the hub's recommended values of a recommendation, independent of the live
// values (which change with the phase), for de-duplication.
func recValues(rec *hubclient.WorkloadRecommendation) []v1alpha1.ContainerRecommendation {
	if rec == nil {
		return nil
	}
	var out []v1alpha1.ContainerRecommendation
	for _, c := range rec.Containers {
		cr := v1alpha1.ContainerRecommendation{ContainerResources: v1alpha1.ContainerResources{Name: c.Name}}
		conv := func(v *hubclient.ResourceValues) corev1.ResourceList {
			if v == nil {
				return nil
			}
			l := corev1.ResourceList{}
			for res, s := range map[corev1.ResourceName]string{corev1.ResourceCPU: v.CPU, corev1.ResourceMemory: v.Memory} {
				if q, err := resource.ParseQuantity(s); err == nil && s != "" {
					l[res] = q
				}
			}
			return l
		}
		cr.Requests, cr.Limits = conv(c.Recommended.Requests), conv(c.Recommended.Limits)
		for _, r := range c.RemoveLimits {
			cr.RemoveLimits = append(cr.RemoveLimits, corev1.ResourceName(r))
		}
		out = append(out, cr)
	}
	return out
}

// valuesClose reports whether two value sets have the same containers, resources and limit
// removals, with every quantity within tol (relative) of the other.
func valuesClose(a, b []v1alpha1.ContainerRecommendation, tol float64) bool {
	if len(a) != len(b) {
		return false
	}
	near := func(x, y corev1.ResourceList) bool {
		if len(x) != len(y) {
			return false
		}
		for k, qx := range x {
			qy, ok := y[k]
			if !ok {
				return false
			}
			fx, fy := float64(qx.MilliValue()), float64(qy.MilliValue())
			if fx == fy {
				continue
			}
			if m := max(fx, fy); m == 0 || (fx-fy)/m > tol || (fy-fx)/m > tol {
				return false
			}
		}
		return true
	}
	for i := range a {
		if a[i].Name != b[i].Name || !near(a[i].Requests, b[i].Requests) || !near(a[i].Limits, b[i].Limits) ||
			!slices.Equal(a[i].RemoveLimits, b[i].RemoveLimits) {
			return false
		}
	}
	return true
}

func (r *Reconciler) tolerance() float64 {
	if r.Cfg.Tolerance > 0 {
		return r.Cfg.Tolerance
	}
	return guardrails.DefaultTolerance
}

// emit sends an event to the hub buffer and records it as a Kubernetes Event on the RSR.
//
// With a non-empty slot the event is de-duplicated (D-017): it is emitted only when its type,
// reason or resources differ from the last event emitted in that slot for the workload, or the
// hub's recommended values moved by more than the tolerance. A new recommendation id alone does
// not re-emit. An applied, rolled_back or released event resets the workload's slots.
func (w *run) emit(e hubclient.Event, slot string, values []v1alpha1.ContainerRecommendation) {
	r := w.r
	e.Workload = hubclient.WorkloadRef{Namespace: w.k.Namespace, Kind: w.k.Kind, Name: w.k.Name}
	if e.Reason == v1alpha1.ReasonPaused && w.pauseScope != "" {
		e.Details = withDetail(e.Details, hubclient.DetailPauseScope, w.pauseScope)
	}
	if a := w.approval; a != nil && e.Type == hubclient.EventApplied && e.Reason == v1alpha1.ReasonRecommendationApplied {
		// Approve once (D-024): the hub marks the approval consumed when it stores this event.
		e.Reason = v1alpha1.ReasonApprovedOnce
		e.Details = withDetail(withDetail(e.Details, hubclient.DetailApprovalID, a.ID), hubclient.DetailApprovedBy, a.ApprovedBy)
		e.Message = "approved once by " + a.ApprovedBy + ": " + e.Message
		r.Admin.Consume(a.ID, w.now)
		w.approval = nil
		w.approvedApply = true
		w.log.Info("approval applied", "approval", a.ID, "recommendation", e.RecommendationID, "approvedBy", a.ApprovedBy)
	}
	if slot != "" {
		sig := signature{head: strings.Join([]string{e.Type, e.Reason, strings.Join(e.Resources, ","), w.policyDigest()}, "|"), values: values}
		key := w.k.String() + "|" + slot
		r.mu.Lock()
		if r.last == nil {
			r.last = map[string]signature{}
		}
		last, known := r.last[key]
		same := known && last.head == sig.head && valuesClose(last.values, sig.values, r.tolerance())
		if !same {
			r.last[key] = sig
		}
		r.mu.Unlock()
		if same || (!known && slot == slotDecision && w.alreadyRecorded(e)) {
			return
		}
	}
	switch e.Type {
	case hubclient.EventApplied, hubclient.EventRolledBack, hubclient.EventReleased:
		r.resetSlots(w.k)
	}
	e.Time = w.now.UTC()
	r.Reporter.Event(e)
	if r.Recorder != nil && w.rsr != nil {
		r.record(w.rsr, w.obj, e)
	}
}

// policyDigest identifies the effective policy: editing it (e.g. turning HPA coordination on)
// re-reports the current skip/block once, with the new reasoning (D-017).
func (w *run) policyDigest() string {
	e := w.base
	e.Warnings = nil
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func (r *Reconciler) resetSlots(k wkey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := k.String() + "|"
	for key := range r.last {
		if strings.HasPrefix(key, prefix) {
			delete(r.last, key)
		}
	}
}

// record writes a Kubernetes Event on the RSR (docs/contracts/events.md).
func (r *Reconciler) record(rsr *v1alpha1.RightsizingRecommendation, obj client.Object, e hubclient.Event) {
	typ := corev1.EventTypeNormal
	if e.Type == hubclient.EventFailed || e.Type == hubclient.EventRolledBack || e.Type == hubclient.EventFallback {
		typ = corev1.EventTypeWarning
	}
	note := e.Message
	if note == "" {
		note = e.Type
	}
	if len(note) > 1000 {
		note = note[:1000]
	}
	r.Recorder.Eventf(rsr, obj, typ, e.Reason, e.Type, "%s", note)
}

// Emit delivers an event raised outside a reconcile (the health watch, WO-08): to the hub
// buffer (asynchronously) and as a Kubernetes Event on the RSR.
func (r *Reconciler) Emit(rsr *v1alpha1.RightsizingRecommendation, obj client.Object, e hubclient.Event) {
	if kind, ok := workload.KindOf(obj); ok {
		e.Workload = hubclient.WorkloadRef{Namespace: obj.GetNamespace(), Kind: string(kind), Name: obj.GetName()}
		if e.Type == hubclient.EventRolledBack {
			r.resetSlots(wkey{obj.GetNamespace(), string(kind), obj.GetName()})
		}
	}
	if e.Time.IsZero() {
		e.Time = r.now().UTC()
	}
	r.Reporter.Event(e)
	if r.Recorder != nil && rsr != nil {
		r.record(rsr, obj, e)
	}
}

// alreadyRecorded makes the decision slot survive agent restarts: the previous status already
// carries this reason, and (unless values are live) the spec already held values within the
// tolerance of the ones just written.
func (w *run) alreadyRecorded(e hubclient.Event) bool {
	if w.rsr == nil {
		return false
	}
	if w.prev.Phase != v1alpha1.PhaseApplied && !valuesClose(w.prevSpec.Containers, w.rsr.Spec.Containers, w.r.tolerance()) {
		return false
	}
	switch e.Type {
	case hubclient.EventBlocked:
		c := meta.FindStatusCondition(w.prev.Conditions, v1alpha1.ConditionBlocked)
		return c != nil && c.Status == metav1.ConditionTrue && c.Reason == e.Reason
	case hubclient.EventSkipped:
		if w.prev.Phase == v1alpha1.PhaseApplied {
			c := meta.FindStatusCondition(w.prev.Conditions, v1alpha1.ConditionBlocked)
			return c != nil && c.Status == metav1.ConditionFalse && c.Reason == e.Reason
		}
		c := meta.FindStatusCondition(w.prev.Conditions, v1alpha1.ConditionApplied)
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == e.Reason
	}
	return false
}

func isSkipReason(r string) bool {
	switch r {
	case v1alpha1.ReasonModeOff, v1alpha1.ReasonModeAudit, v1alpha1.ReasonNoSignificantChange, v1alpha1.ReasonRecommendationStale:
		return true
	}
	return false
}

// report records the hub WorkloadStatus.
func (w *run) report() {
	b := w.base
	settings := b.Settings()
	st := hubclient.WorkloadStatus{
		Workload: hubclient.WorkloadRef{Namespace: w.k.Namespace, Kind: w.k.Kind, Name: w.k.Name},
		Policy:   b.Policy,
		Override: b.Override,
		Modes: hubclient.EffectiveModes{CPU: string(b.CPUMode), Memory: string(b.MemoryMode),
			CPUSource: string(b.CPUModeSource), MemorySource: string(b.MemoryModeSource)},
		Strategy: string(w.strategy),
		Settings: &settings,
		Phase:    string(v1alpha1.PhasePending),
		Reason:   w.reason,
		Message:  w.message,
	}
	if st.Reason == condReasonPending {
		st.Reason = ""
	}
	if len(st.Message) > 1024 {
		st.Message = st.Message[:1021] + "..."
	}
	live := w.containersForEval()
	for _, c := range live {
		cr := workload.ToAPI(c.Name, c.Resources)
		st.Live = append(st.Live, toHubResources(cr))
	}
	st.JVM = workload.JVMReport(live, w.eff.JVMDetect)
	if w.rsr != nil {
		st.Phase = string(w.rsr.Status.Phase)
		if st.Phase == "" {
			st.Phase = string(v1alpha1.PhasePending)
		}
		st.RecommendationID = w.rsr.Spec.Source.RecommendationID
		if w.rsr.Status.Phase == v1alpha1.PhaseApplied && w.rsr.Status.AppliedRecommendationID != "" {
			st.RecommendationID = w.rsr.Status.AppliedRecommendationID
		}
		if t := w.rsr.Status.AppliedAt; t != nil {
			at := t.UTC()
			st.AppliedAt = &at
		}
		if t := w.rsr.Status.NextWindow; t != nil {
			nw := t.UTC()
			st.NextWindow = &nw
		}
	} else if w.rec != nil {
		st.RecommendationID = w.rec.ID
	}
	w.r.Reporter.SetStatus(w.k, st)
}

func toHubResources(c v1alpha1.ContainerResources) hubclient.ContainerResources {
	conv := func(l corev1.ResourceList) *hubclient.ResourceValues {
		if len(l) == 0 {
			return nil
		}
		v := &hubclient.ResourceValues{}
		if q, ok := l[corev1.ResourceCPU]; ok {
			v.CPU = q.String()
		}
		if q, ok := l[corev1.ResourceMemory]; ok {
			v.Memory = q.String()
		}
		return v
	}
	return hubclient.ContainerResources{Name: c.Name, Requests: conv(c.Requests), Limits: conv(c.Limits)}
}

// withDetail returns a copy of details with key set (event details may be shared).
func withDetail(details map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(details)+1)
	for k, v := range details {
		out[k] = v
	}
	out[key] = value
	return out
}

// pauseMessage describes a hub pause for events and status messages.
func pauseMessage(f hubclient.ConfigFlags) string {
	scope := f.PauseScope
	if scope == "" {
		scope = hubclient.PauseScopeCluster
	}
	msg := "automation is paused in the hub (" + scope + ")"
	if f.PauseReason != "" {
		msg += ": " + f.PauseReason
	}
	return msg
}

// ---------------------------------------------------------------------------------------------
// Approve once (D-024)

// approve applies an approve-once grant for the workload's current recommendation: its audit
// resources count as auto-patch for this one change, with every guardrail. The grant is
// consumed by the applied event (emit); until then it also carries an in-place change already in
// flight. A recommendation that is already live is not applied again.
func (w *run) approve() {
	r := w.r
	if r.Admin == nil || r.Cfg.ReadOnly {
		return
	}
	ref := hubclient.WorkloadRef{Namespace: w.k.Namespace, Kind: w.k.Kind, Name: w.k.Name}
	var a *hubclient.ConfigApproval
	if w.rec != nil && (w.rsr == nil || w.prev.Phase != v1alpha1.PhaseApplied || w.rsr.Status.AppliedRecommendationID != w.rec.ID) {
		a = r.Admin.Approval(ref, w.rec.ID, w.now)
	}
	if a == nil && w.rsr != nil && w.prev.Phase == v1alpha1.PhaseApplying {
		a = r.Admin.Approval(ref, w.rsr.Spec.Source.RecommendationID, w.now)
	}
	if a == nil {
		return
	}
	raised := false
	for _, m := range []*v1alpha1.Mode{&w.eff.CPUMode, &w.eff.MemoryMode} {
		if *m == v1alpha1.ModeAudit {
			*m, raised = v1alpha1.ModeAutoPatch, true
		}
	}
	if raised {
		w.approval = a
	}
}

// approvedLive reports whether the live values come from an approve-once apply (D-024).
func approvedLive(st v1alpha1.RightsizingRecommendationStatus) bool {
	if st.Phase != v1alpha1.PhaseApplied {
		return false
	}
	c := meta.FindStatusCondition(st.Conditions, v1alpha1.ConditionApplied)
	return c != nil && c.Status == metav1.ConditionTrue && c.Reason == v1alpha1.ReasonApprovedOnce
}

// approvedHold returns the owned template resources the agent keeps after an approve-once apply
// although they are in audit (D-027): Argo CD's ignoreDifferences on the resource-optimizer field
// manager then keeps self-heal from reverting the approved values. The hold ends for a resource
// set to off, and for all of them when the health watch rolled the change back or a newer
// recommendation recommends different values (a new recommendation id with the same values
// within the tolerance does not end it). why says why a hold ended in this reconcile.
func (w *run) approvedHold(owned map[corev1.ResourceName]bool) (map[corev1.ResourceName]bool, string) {
	if w.rsr == nil || !approvedLive(w.prev) || len(owned) == 0 {
		return nil, ""
	}
	if w.rec != nil && w.rec.ID != w.rsr.Status.AppliedRecommendationID &&
		!sameAdvice(recValues(w.rec), w.rsr.Spec.Containers, owned, w.r.tolerance()) {
		return nil, "newer recommendation " + w.rec.ID
	}
	held := map[corev1.ResourceName]bool{}
	var off []string
	for res := range owned {
		switch w.eff.Mode(string(res)) {
		case v1alpha1.ModeAudit:
			held[res] = true
		case v1alpha1.ModeOff:
			off = append(off, string(res))
		}
	}
	if len(off) > 0 {
		slices.Sort(off)
		return held, strings.Join(off, " and ") + " set to off"
	}
	return held, ""
}

// sameAdvice reports whether rec recommends, for the resources res, the values of targets
// within tol (relative): every target request has a recommended request close to it, and every
// target limit the hub also recommends is close to it.
func sameAdvice(rec, targets []v1alpha1.ContainerRecommendation, res map[corev1.ResourceName]bool, tol float64) bool {
	near := func(a, b resource.Quantity) bool {
		fa, fb := float64(a.MilliValue()), float64(b.MilliValue())
		m := max(fa, fb)
		return fa == fb || (m > 0 && (fa-fb)/m <= tol && (fb-fa)/m <= tol)
	}
	for _, t := range targets {
		i := slices.IndexFunc(rec, func(c v1alpha1.ContainerRecommendation) bool { return c.Name == t.Name })
		if i < 0 {
			return false
		}
		c := rec[i]
		for r := range res {
			if q, ok := t.Requests[r]; ok {
				if got, ok := c.Requests[r]; !ok || !near(q, got) {
					return false
				}
			}
			// Limits only where the hub recommends one (it may leave them to the settings).
			if q, ok := t.Limits[r]; ok {
				if got, ok := c.Limits[r]; ok && !near(q, got) {
					return false
				}
			}
		}
	}
	return true
}
