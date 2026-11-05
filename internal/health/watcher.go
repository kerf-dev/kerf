package health

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
	"github.com/kerf-dev/kerf/internal/agent/policy"
	"github.com/kerf-dev/kerf/internal/agent/workload"
)

// HealthWatch condition reasons that are not rollback reasons (conditions only, never events).
const (
	ReasonWatching    = "Watching"
	ReasonHealthy     = "Healthy"
	ReasonWatchEnded  = "WatchStopped"
	defaultCheckEvery = 15 * time.Second
)

var (
	watchesInProgress = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kerf_health_watches_in_progress",
		Help: "Workloads currently watched after an automatic change.",
	})
	rollbacks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kerf_health_rollbacks_total",
		Help: "Rollbacks by reason (OOMKills, Restarts, Throttling) and strategy.",
	}, []string{"reason", "strategy"})
	watchesCompleted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "kerf_health_watches_completed_total",
		Help: "Watches that ended without a rollback.",
	})
	throttleErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kerf_health_throttling_query_errors_total",
		Help: "Failed throttling-ratio lookups by source (promql, cadvisor).",
	}, []string{"source"})
	rollbackErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "kerf_health_rollback_errors_total",
		Help: "Rollbacks that failed (retried with backoff).",
	})
)

func init() {
	metrics.Registry.MustRegister(watchesInProgress, rollbacks, watchesCompleted, throttleErrors, rollbackErrors)
}

// Emitter delivers an audit event: to the hub buffer and as a Kubernetes Event on the RSR.
// Delivery to the hub is asynchronous, so a rollback never waits for it.
type Emitter interface {
	Emit(rsr *v1alpha1.RightsizingRecommendation, workload client.Object, e hubclient.Event)
}

// Config configures the watcher.
type Config struct {
	ClusterName     string
	ClusterDefaults *v1alpha1.RightsizingPolicySpec
	// CheckInterval is how often a watched workload is re-evaluated besides pod events (default 15s).
	CheckInterval time.Duration
	// MinThrottlingWindow is how long after the change throttling is first evaluated (default 2m),
	// so the ratio is not computed from one or two scrapes.
	MinThrottlingWindow time.Duration
	// ReadOnly disables rollbacks (the agent never writes workloads).
	ReadOnly bool
}

// Watcher watches workloads after automatic changes and rolls them back on a breach.
type Watcher struct {
	Client   client.Client
	Reader   client.Reader // uncached reads (pods, ReplicaSets, Jobs)
	Throttle ThrottleSource
	Emitter  Emitter
	Cfg      Config
	Log      *slog.Logger
	Now      func() time.Time

	state *tracker
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Sources combines throttling sources: the first that answers wins.
func Sources(s ...ThrottleSource) ThrottleSource {
	var out fallbackSource
	for _, x := range s {
		if x != nil {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SetupWithManager registers the watcher. The RSR field index IndexTargetRef must be registered.
func (w *Watcher) SetupWithManager(mgr manager.Manager) error {
	if w.state == nil {
		w.state = newTracker()
	}
	if w.Cfg.CheckInterval <= 0 {
		w.Cfg.CheckInterval = defaultCheckEvery
	}
	if w.Cfg.MinThrottlingWindow <= 0 {
		w.Cfg.MinThrottlingWindow = 2 * time.Minute
	}
	podChanged := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, ok1 := e.ObjectOld.(*corev1.Pod)
			n, ok2 := e.ObjectNew.(*corev1.Pod)
			return ok1 && ok2 && podSignature(o) != podSignature(n)
		},
	}
	return ctrl.NewControllerManagedBy(mgr).Named("health").
		For(&v1alpha1.RightsizingRecommendation{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(w.podToRSR), builder.WithPredicates(podChanged)).
		Complete(w)
}

// podSignature changes when a container restarts or terminates.
func podSignature(p *corev1.Pod) string {
	var b strings.Builder
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		fmt.Fprintf(&b, "%s:%d:", cs.Name, cs.RestartCount)
		if t := cs.State.Terminated; t != nil {
			b.WriteString(t.Reason)
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			fmt.Fprintf(&b, "%s@%d", t.Reason, t.FinishedAt.Unix())
		}
		b.WriteByte(';')
	}
	return b.String()
}

// ownerWorkloads derives the candidate workloads of a pod without reading ReplicaSets or Jobs:
// ReplicaSet "<deployment>-<pod-template-hash>", Job "<cronjob>-<scheduled minute>".
func ownerWorkloads(p *corev1.Pod) []struct {
	Kind v1alpha1.WorkloadKind
	Name string
} {
	type wl = struct {
		Kind v1alpha1.WorkloadKind
		Name string
	}
	var out []wl
	for _, ref := range p.OwnerReferences {
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		switch ref.Kind {
		case "ReplicaSet":
			if h := p.Labels["pod-template-hash"]; h != "" && strings.HasSuffix(ref.Name, "-"+h) {
				out = append(out, wl{v1alpha1.KindDeployment, strings.TrimSuffix(ref.Name, "-"+h)})
			}
		case "StatefulSet":
			out = append(out, wl{v1alpha1.KindStatefulSet, ref.Name})
		case "DaemonSet":
			out = append(out, wl{v1alpha1.KindDaemonSet, ref.Name})
		case "Job":
			if i := strings.LastIndex(ref.Name, "-"); i > 0 {
				if _, err := strconv.ParseInt(ref.Name[i+1:], 10, 64); err == nil {
					out = append(out, wl{v1alpha1.KindCronJob, ref.Name[:i]})
				}
			}
		}
	}
	return out
}

func (w *Watcher) podToRSR(ctx context.Context, o client.Object) []reconcile.Request {
	p, ok := o.(*corev1.Pod)
	if !ok {
		return nil
	}
	var out []reconcile.Request
	for _, wl := range ownerWorkloads(p) {
		var list v1alpha1.RightsizingRecommendationList
		if err := w.Client.List(ctx, &list, client.InNamespace(p.Namespace),
			client.MatchingFields{v1alpha1.IndexTargetRef: v1alpha1.TargetRefIndexValue(wl.Kind, wl.Name)}); err != nil {
			continue
		}
		for _, r := range list.Items {
			if r.Status.Phase == v1alpha1.PhaseApplied {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: r.Name}})
			}
		}
	}
	return out
}

// Reconcile evaluates one RightsizingRecommendation.
func (w *Watcher) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rsr v1alpha1.RightsizingRecommendation
	if err := w.Client.Get(ctx, req.NamespacedName, &rsr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	log := w.Log.With("rsr", req.String())
	now := w.now()
	if rsr.Status.Phase == v1alpha1.PhaseRolledBack {
		w.stopWatching(rsr.UID)
		return ctrl.Result{}, w.finishRollback(ctx, &rsr)
	}
	if rsr.Status.Phase != v1alpha1.PhaseApplied || rsr.Status.AppliedAt == nil {
		w.stopWatching(rsr.UID)
		if c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionHealthWatch); c != nil && c.Status == metav1.ConditionTrue {
			return ctrl.Result{}, w.setWatchCondition(ctx, &rsr, metav1.ConditionFalse, ReasonWatchEnded,
				fmt.Sprintf("phase %s: watch stopped", rsr.Status.Phase))
		}
		return ctrl.Result{}, nil
	}
	appliedAt := rsr.Status.AppliedAt.Time

	obj, err := workload.New(rsr.Spec.TargetRef.Kind)
	if err != nil {
		return ctrl.Result{}, nil //nolint:nilerr // not a workload kind we manage
	}
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: rsr.Namespace, Name: rsr.Spec.TargetRef.Name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			w.stopWatching(rsr.UID)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	var policies v1alpha1.RightsizingPolicyList
	if err := w.Client.List(ctx, &policies); err != nil {
		return ctrl.Result{}, err
	}
	eff := policy.Resolve(policy.Workload{Namespace: rsr.Namespace, Kind: rsr.Spec.TargetRef.Kind, Name: obj.GetName(),
		Labels: obj.GetLabels(), Annotations: obj.GetAnnotations()}, policies.Items, w.Cfg.ClusterDefaults)

	end := appliedAt.Add(eff.WatchDuration)
	st := w.state.state(rsr.UID, appliedAt)
	if !now.Before(end) {
		w.stopWatching(rsr.UID)
		c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionHealthWatch)
		if c == nil || c.Status == metav1.ConditionTrue {
			watchesCompleted.Inc()
			log.InfoContext(ctx, "health watch completed without rollback")
			return ctrl.Result{}, w.setWatchCondition(ctx, &rsr, metav1.ConditionFalse, ReasonHealthy,
				fmt.Sprintf("no rollback threshold exceeded within %s after %s", eff.WatchDuration, appliedAt.UTC().Format(time.RFC3339)))
		}
		return ctrl.Result{}, nil
	}
	w.startWatching(st)
	msg := fmt.Sprintf("watching until %s: oomKills>=%d, restarts>=%d, throttlingRatio>=%.2f",
		end.UTC().Format(time.RFC3339), eff.RollbackOOMKills, eff.RollbackRestarts, eff.ThrottlingRatio)
	if err := w.setWatchCondition(ctx, &rsr, metav1.ConditionTrue, ReasonWatching, msg); err != nil {
		return ctrl.Result{}, err
	}

	pods, err := workload.Pods(ctx, w.Reader, obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	sig := st.observe(pods)
	changed := ChangedResources(rsr.Status.Previous, rsr.Spec.Containers)
	b := w.evaluate(ctx, &rsr, eff, sig, changed, pods, st, now)
	if b != nil {
		if w.Cfg.ReadOnly {
			log.WarnContext(ctx, "rollback threshold exceeded but the agent is read-only", "reason", b.reason, "message", b.message)
			return ctrl.Result{RequeueAfter: w.Cfg.CheckInterval}, nil
		}
		w.stopWatching(rsr.UID)
		return ctrl.Result{}, w.rollback(ctx, &rsr, obj, b)
	}
	return ctrl.Result{RequeueAfter: min(w.Cfg.CheckInterval, end.Sub(now)+time.Second)}, nil
}

func (w *Watcher) startWatching(st *watchState) {
	w.state.mu.Lock()
	defer w.state.mu.Unlock()
	if !st.watching {
		st.watching = true
		watchesInProgress.Inc()
	}
}

func (w *Watcher) stopWatching(uid types.UID) {
	w.state.mu.Lock()
	s, ok := w.state.watches[uid]
	if ok && s.watching {
		watchesInProgress.Dec()
	}
	w.state.mu.Unlock()
	w.state.forget(uid)
}

// breach is a rollback decision.
type breach struct {
	reason    string
	resources []string
	message   string
	details   map[string]string
}

func (w *Watcher) evaluate(ctx context.Context, rsr *v1alpha1.RightsizingRecommendation, eff policy.Effective, sig Signals,
	changed []string, pods []corev1.Pod, st *watchState, now time.Time) *breach {
	appliedAt := rsr.Status.AppliedAt.Time
	watch := eff.WatchDuration.String()
	if slices.Contains(changed, "memory") && eff.RollbackOOMKills > 0 && sig.OOMKills >= int(eff.RollbackOOMKills) {
		return &breach{reason: v1alpha1.ReasonOOMKills, resources: []string{"memory"},
			message: fmt.Sprintf("%d OOM kill(s) within %s after the change (threshold %d)", sig.OOMKills, now.Sub(appliedAt).Round(time.Second), eff.RollbackOOMKills),
			details: map[string]string{"oomKills": strconv.Itoa(sig.OOMKills), "watchDuration": watch}}
	}
	if len(changed) > 0 && eff.RollbackRestarts > 0 && sig.Restarts >= int(eff.RollbackRestarts) {
		return &breach{reason: v1alpha1.ReasonRestarts, resources: changed,
			message: fmt.Sprintf("%d container restart(s) within %s after the change (threshold %d)", sig.Restarts, now.Sub(appliedAt).Round(time.Second), eff.RollbackRestarts),
			details: map[string]string{"restarts": strconv.Itoa(sig.Restarts), "watchDuration": watch}}
	}
	if slices.Contains(changed, "cpu") && w.Throttle != nil && now.Sub(appliedAt) >= w.Cfg.MinThrottlingWindow && eff.ThrottlingRatio > 0 {
		ratio, ok, err := w.Throttle.Ratio(ctx, ThrottleQuery{Cluster: w.Cfg.ClusterName, Namespace: rsr.Namespace, Pods: pods,
			Containers: changedContainers(rsr.Status.Previous, rsr.Spec.Containers, "cpu"), Since: appliedAt, State: st})
		if err != nil {
			w.Log.DebugContext(ctx, "throttling ratio unavailable", "rsr", rsr.Namespace+"/"+rsr.Name, "err", err)
		} else if ok && ratio >= eff.ThrottlingRatio {
			r := strconv.FormatFloat(ratio, 'f', 3, 64)
			return &breach{reason: v1alpha1.ReasonThrottling, resources: []string{"cpu"},
				message: fmt.Sprintf("CPU throttling ratio %s since the change (threshold %.2f)", r, eff.ThrottlingRatio),
				details: map[string]string{"throttlingRatio": r, "watchDuration": watch}}
		}
	}
	return nil
}

func (w *Watcher) setWatchCondition(ctx context.Context, rsr *v1alpha1.RightsizingRecommendation, status metav1.ConditionStatus, reason, msg string) error {
	if c := meta.FindStatusCondition(rsr.Status.Conditions, v1alpha1.ConditionHealthWatch); c != nil &&
		c.Status == status && c.Reason == reason && c.Message == msg {
		return nil
	}
	meta.SetStatusCondition(&rsr.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionHealthWatch, Status: status, Reason: reason, Message: msg})
	return w.Client.Status().Update(ctx, rsr)
}

// ResourcesForReason maps a rollback reason (the HealthWatch condition reason after a rollback)
// to the resources whose decreases stay blocked until the cooldown ends. Empty means all.
func ResourcesForReason(reason string) []string {
	switch reason {
	case v1alpha1.ReasonOOMKills:
		return []string{"memory"}
	case v1alpha1.ReasonThrottling:
		return []string{"cpu"}
	}
	return nil
}
