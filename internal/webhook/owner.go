// Package webhook is the resource-optimizer mutating admission webhook (WO-07, D-007).
//
// On pod CREATE it resolves the owning workload from ownerReferences, looks up the workload's
// RightsizingRecommendation in an informer cache and, when the recommendation is Applied with
// strategy in-place, sets the recommended container requests/limits on the new pod. It never
// calls the hub and never denies a pod.
package webhook

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// GVKs of the intermediate owners, read as metadata only (PartialObjectMetadata).
var (
	ReplicaSetGVK = appsv1.SchemeGroupVersion.WithKind("ReplicaSet")
	JobGVK        = batchv1.SchemeGroupVersion.WithKind("Job")
)

// Owner is the workload a pod belongs to.
type Owner struct {
	Kind v1alpha1.WorkloadKind
	Name string
}

// ErrUnmanaged means the pod has no owner, or an owner chain that does not end in one of the
// workload kinds resource-optimizer manages (e.g. a bare ReplicaSet or a Job without a CronJob).
type ErrUnmanaged struct{ Reason string }

func (e *ErrUnmanaged) Error() string { return "unmanaged pod: " + e.Reason }

// Unmanaged reasons (also the "reason" label of skipped admissions).
const (
	ReasonNoOwner        = "no-owner"
	ReasonUnmanagedOwner = "unmanaged-owner"
)

// Resolver resolves a pod's owning workload.
type Resolver struct {
	// Cache is the informer-backed reader (ReplicaSets and Jobs as metadata).
	Cache client.Reader
	// Live, if set, is used when an intermediate owner is not in the cache yet (a ReplicaSet
	// created milliseconds before its first pod may not have reached the informer).
	Live client.Reader
}

// Resolve returns the workload owning a pod in namespace ns:
//
//	Pod → ReplicaSet → Deployment
//	Pod → StatefulSet | DaemonSet
//	Pod → Job → CronJob
//
// It returns *ErrUnmanaged for pods outside these chains.
func (r *Resolver) Resolve(ctx context.Context, ns string, pod *corev1.Pod) (Owner, error) {
	ref := metav1.GetControllerOf(pod)
	if ref == nil {
		return Owner{}, &ErrUnmanaged{Reason: ReasonNoOwner}
	}
	gv, _ := schema.ParseGroupVersion(ref.APIVersion)
	switch {
	case gv.Group == appsv1.GroupName && ref.Kind == "StatefulSet":
		return Owner{v1alpha1.KindStatefulSet, ref.Name}, nil
	case gv.Group == appsv1.GroupName && ref.Kind == "DaemonSet":
		return Owner{v1alpha1.KindDaemonSet, ref.Name}, nil
	case gv.Group == appsv1.GroupName && ref.Kind == "ReplicaSet":
		return r.parent(ctx, ns, ref.Name, ReplicaSetGVK, appsv1.GroupName, "Deployment", v1alpha1.KindDeployment)
	case gv.Group == batchv1.GroupName && ref.Kind == "Job":
		return r.parent(ctx, ns, ref.Name, JobGVK, batchv1.GroupName, "CronJob", v1alpha1.KindCronJob)
	}
	return Owner{}, &ErrUnmanaged{Reason: ReasonUnmanagedOwner}
}

// parent reads the intermediate owner (metadata only) and returns its controller if that is
// wantGroup/wantKind.
func (r *Resolver) parent(ctx context.Context, ns, name string, gvk schema.GroupVersionKind,
	wantGroup, wantKind string, kind v1alpha1.WorkloadKind) (Owner, error) {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(gvk)
	key := client.ObjectKey{Namespace: ns, Name: name}
	err := r.Cache.Get(ctx, key, obj)
	if apierrors.IsNotFound(err) && r.Live != nil {
		err = r.Live.Get(ctx, key, obj)
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Owner{}, &ErrUnmanaged{Reason: ReasonUnmanagedOwner}
		}
		return Owner{}, fmt.Errorf("get %s %s/%s: %w", gvk.Kind, ns, name, err)
	}
	ref := metav1.GetControllerOf(obj)
	if ref == nil {
		return Owner{}, &ErrUnmanaged{Reason: ReasonUnmanagedOwner}
	}
	gv, _ := schema.ParseGroupVersion(ref.APIVersion)
	if gv.Group != wantGroup || ref.Kind != wantKind {
		return Owner{}, &ErrUnmanaged{Reason: ReasonUnmanagedOwner}
	}
	return Owner{kind, ref.Name}, nil
}

// IsUnmanaged reports whether err is an *ErrUnmanaged, and returns its reason.
func IsUnmanaged(err error) (string, bool) {
	var u *ErrUnmanaged
	if errors.As(err, &u) {
		return u.Reason, true
	}
	return "", false
}
