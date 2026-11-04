package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/workload"
)

// IndexRSRByTarget registers the IndexTargetRef field index (shared convention, WO-00).
func IndexRSRByTarget(ctx context.Context, mgr manager.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &v1alpha1.RightsizingRecommendation{}, v1alpha1.IndexTargetRef,
		func(o client.Object) []string {
			r := o.(*v1alpha1.RightsizingRecommendation)
			return []string{v1alpha1.TargetRefIndexValue(r.Spec.TargetRef.Kind, r.Spec.TargetRef.Name)}
		})
}

func req(ns string, kind v1alpha1.WorkloadKind, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: requestName(kind, name)}}
}

// SetupWithManager wires the reconciler and returns the function the puller uses to trigger
// reconciles of every workload.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr manager.Manager) (func(), error) {
	if err := IndexRSRByTarget(ctx, mgr); err != nil {
		return nil, err
	}
	trigger := make(chan event.GenericEvent, 4096)

	workloadPred := predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}, predicate.LabelChangedPredicate{})
	mapKind := func(kind v1alpha1.WorkloadKind) handler.EventHandler {
		return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			return []reconcile.Request{req(o.GetNamespace(), kind, o.GetName())}
		})
	}
	b := ctrl.NewControllerManagedBy(mgr).Named("workload").
		Watches(&appsv1.Deployment{}, mapKind(v1alpha1.KindDeployment), builder.WithPredicates(workloadPred)).
		Watches(&appsv1.StatefulSet{}, mapKind(v1alpha1.KindStatefulSet), builder.WithPredicates(workloadPred)).
		Watches(&appsv1.DaemonSet{}, mapKind(v1alpha1.KindDaemonSet), builder.WithPredicates(workloadPred)).
		Watches(&batchv1.CronJob{}, mapKind(v1alpha1.KindCronJob), builder.WithPredicates(workloadPred)).
		Watches(&v1alpha1.RightsizingPolicy{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
			return r.allRequests(ctx)
		}), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.RightsizingRecommendation{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			rsr := o.(*v1alpha1.RightsizingRecommendation)
			return []reconcile.Request{req(rsr.Namespace, rsr.Spec.TargetRef.Kind, rsr.Spec.TargetRef.Name)}
		})).
		Watches(&autoscalingv2.HorizontalPodAutoscaler{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			h := o.(*autoscalingv2.HorizontalPodAutoscaler)
			kind := v1alpha1.WorkloadKind(h.Spec.ScaleTargetRef.Kind)
			if _, ok := workload.Kinds[kind]; !ok {
				return nil
			}
			return []reconcile.Request{req(h.Namespace, kind, h.Spec.ScaleTargetRef.Name)}
		})).
		WatchesRawSource(source.Channel(trigger, &handler.EnqueueRequestForObject{}))
	if err := b.Complete(r); err != nil {
		return nil, err
	}
	return func() {
		for _, rq := range r.allRequests(ctx) {
			obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: rq.Namespace, Name: rq.Name}}
			select {
			case trigger <- event.GenericEvent{Object: obj}:
			case <-ctx.Done():
				return
			}
		}
	}, nil
}

// allRequests lists every workload from the cache.
func (r *Reconciler) allRequests(ctx context.Context) []reconcile.Request {
	var out []reconcile.Request
	lists := []struct {
		kind v1alpha1.WorkloadKind
		list client.ObjectList
	}{
		{v1alpha1.KindDeployment, &appsv1.DeploymentList{}},
		{v1alpha1.KindStatefulSet, &appsv1.StatefulSetList{}},
		{v1alpha1.KindDaemonSet, &appsv1.DaemonSetList{}},
		{v1alpha1.KindCronJob, &batchv1.CronJobList{}},
	}
	for _, l := range lists {
		if err := r.Client.List(ctx, l.list); err != nil {
			r.Log.ErrorContext(ctx, "list workloads", "kind", l.kind, "err", err)
			continue
		}
		switch list := l.list.(type) {
		case *appsv1.DeploymentList:
			for _, o := range list.Items {
				out = append(out, req(o.Namespace, l.kind, o.Name))
			}
		case *appsv1.StatefulSetList:
			for _, o := range list.Items {
				out = append(out, req(o.Namespace, l.kind, o.Name))
			}
		case *appsv1.DaemonSetList:
			for _, o := range list.Items {
				out = append(out, req(o.Namespace, l.kind, o.Name))
			}
		case *batchv1.CronJobList:
			for _, o := range list.Items {
				out = append(out, req(o.Namespace, l.kind, o.Name))
			}
		}
	}
	return out
}
