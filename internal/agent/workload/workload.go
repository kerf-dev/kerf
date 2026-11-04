// Package workload gives uniform access to the four workload kinds resource-optimizer manages
// (Deployment, StatefulSet, DaemonSet, CronJob) and to their running pods.
package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// Kinds lists the supported kinds with their API versions.
var Kinds = map[v1alpha1.WorkloadKind]schema.GroupVersionKind{
	v1alpha1.KindDeployment:  appsv1.SchemeGroupVersion.WithKind("Deployment"),
	v1alpha1.KindStatefulSet: appsv1.SchemeGroupVersion.WithKind("StatefulSet"),
	v1alpha1.KindDaemonSet:   appsv1.SchemeGroupVersion.WithKind("DaemonSet"),
	v1alpha1.KindCronJob:     batchv1.SchemeGroupVersion.WithKind("CronJob"),
}

// New returns an empty object of the kind.
func New(kind v1alpha1.WorkloadKind) (client.Object, error) {
	switch kind {
	case v1alpha1.KindDeployment:
		return &appsv1.Deployment{}, nil
	case v1alpha1.KindStatefulSet:
		return &appsv1.StatefulSet{}, nil
	case v1alpha1.KindDaemonSet:
		return &appsv1.DaemonSet{}, nil
	case v1alpha1.KindCronJob:
		return &batchv1.CronJob{}, nil
	}
	return nil, fmt.Errorf("unsupported kind %q", kind)
}

// KindOf returns the kind of a workload object.
func KindOf(obj client.Object) (v1alpha1.WorkloadKind, bool) {
	switch obj.(type) {
	case *appsv1.Deployment:
		return v1alpha1.KindDeployment, true
	case *appsv1.StatefulSet:
		return v1alpha1.KindStatefulSet, true
	case *appsv1.DaemonSet:
		return v1alpha1.KindDaemonSet, true
	case *batchv1.CronJob:
		return v1alpha1.KindCronJob, true
	}
	return "", false
}

// PodSpec returns the pod template spec of a workload.
func PodSpec(obj client.Object) *corev1.PodSpec {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return &o.Spec.Template.Spec
	case *appsv1.StatefulSet:
		return &o.Spec.Template.Spec
	case *appsv1.DaemonSet:
		return &o.Spec.Template.Spec
	case *batchv1.CronJob:
		return &o.Spec.JobTemplate.Spec.Template.Spec
	}
	return nil
}

// Selector returns the pod selector of a workload; nil for CronJob.
func Selector(obj client.Object) *metav1.LabelSelector {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return o.Spec.Selector
	case *appsv1.StatefulSet:
		return o.Spec.Selector
	case *appsv1.DaemonSet:
		return o.Spec.Selector
	}
	return nil
}

// TemplatePath is the path to the pod spec inside the object, for server-side apply.
func TemplatePath(kind v1alpha1.WorkloadKind) []string {
	if kind == v1alpha1.KindCronJob {
		return []string{"spec", "jobTemplate", "spec", "template", "spec"}
	}
	return []string{"spec", "template", "spec"}
}

// Container is a container of a pod spec with its kind.
type Container struct {
	corev1.Container
	// Init is true for init containers (including native sidecars).
	Init bool
	// Sidecar is true for native sidecars (init containers with restartPolicy Always).
	Sidecar bool
}

// Containers returns the regular and init containers of a pod spec.
func Containers(spec *corev1.PodSpec) []Container {
	var out []Container
	for _, c := range spec.Containers {
		out = append(out, Container{Container: c})
	}
	for _, c := range spec.InitContainers {
		side := c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
		out = append(out, Container{Container: c, Init: true, Sidecar: side})
	}
	return out
}

// FindContainer returns the named container of a pod spec.
func FindContainer(spec *corev1.PodSpec, name string) (Container, bool) {
	for _, c := range Containers(spec) {
		if c.Name == name {
			return c, true
		}
	}
	return Container{}, false
}

// ToAPI converts core resources to the CRD form (cpu and memory only).
func ToAPI(name string, r corev1.ResourceRequirements) v1alpha1.ContainerResources {
	out := v1alpha1.ContainerResources{Name: name}
	pick := func(l corev1.ResourceList) corev1.ResourceList {
		var res corev1.ResourceList
		for _, k := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if q, ok := l[k]; ok {
				if res == nil {
					res = corev1.ResourceList{}
				}
				res[k] = q.DeepCopy()
			}
		}
		return res
	}
	out.Requests = pick(r.Requests)
	out.Limits = pick(r.Limits)
	return out
}

// Live returns the cpu/memory resources of every container in a pod spec.
func Live(spec *corev1.PodSpec) []v1alpha1.ContainerResources {
	var out []v1alpha1.ContainerResources
	for _, c := range Containers(spec) {
		out = append(out, ToAPI(c.Name, c.Resources))
	}
	return out
}

// PreviousJSON encodes previous values for the previous-resources annotation.
func PreviousJSON(prev []v1alpha1.ContainerResources) string {
	b, _ := json.Marshal(prev)
	return string(b)
}

// Pods returns the pods that belong to the workload (owner chain: Deployment -> ReplicaSet -> Pod,
// StatefulSet/DaemonSet -> Pod, CronJob -> Job -> Pod). Terminating and finished pods are skipped.
func Pods(ctx context.Context, r client.Reader, obj client.Object) ([]corev1.Pod, error) {
	ns := obj.GetNamespace()
	opts := []client.ListOption{client.InNamespace(ns)}
	if sel := Selector(obj); sel != nil {
		s, err := metav1.LabelSelectorAsSelector(sel)
		if err != nil {
			return nil, err
		}
		opts = append(opts, client.MatchingLabelsSelector{Selector: s})
	} else {
		opts = append(opts, client.MatchingLabelsSelector{Selector: labels.Everything()})
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, opts...); err != nil {
		return nil, err
	}

	owners := map[string]bool{string(obj.GetUID()): true}
	kind, _ := KindOf(obj)
	switch kind {
	case v1alpha1.KindDeployment:
		var rss appsv1.ReplicaSetList
		if err := r.List(ctx, &rss, opts...); err != nil {
			return nil, err
		}
		owners = map[string]bool{}
		for _, rs := range rss.Items {
			if ownedBy(rs.OwnerReferences, obj.GetUID()) {
				owners[string(rs.UID)] = true
			}
		}
	case v1alpha1.KindCronJob:
		var jobs batchv1.JobList
		if err := r.List(ctx, &jobs, client.InNamespace(ns)); err != nil {
			return nil, err
		}
		owners = map[string]bool{}
		for _, j := range jobs.Items {
			if ownedBy(j.OwnerReferences, obj.GetUID()) {
				owners[string(j.UID)] = true
			}
		}
	}

	var out []corev1.Pod
	for _, p := range pods.Items {
		if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, ref := range p.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && owners[string(ref.UID)] {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

func ownedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller && ref.UID == uid {
			return true
		}
	}
	return false
}

var heapRuntime = regexp.MustCompile(`(?i)(^|[/:_.-])(java|jdk|jre|openjdk|temurin|corretto|zulu|graalvm|jvm|python[0-9.]*|pypy[0-9.]*)($|[/:_.@-])`)

// HeapBound reports whether a container looks like a JVM or Python process, whose memory is
// bound at start-up (heap sizing), so a memory change only takes effect after a restart (D-007).
func HeapBound(c corev1.Container) bool {
	if heapRuntime.MatchString(c.Image) {
		return true
	}
	for _, s := range append(append([]string{}, c.Command...), c.Args...) {
		base := s[strings.LastIndex(s, "/")+1:]
		if heapRuntime.MatchString(base) || strings.HasPrefix(base, "-Xmx") || strings.HasPrefix(base, "-XX:MaxRAM") {
			return true
		}
	}
	for _, e := range c.Env {
		switch e.Name {
		case "JAVA_TOOL_OPTIONS", "JAVA_OPTS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "JAVA_HOME", "PYTHONPATH", "PYTHONUNBUFFERED":
			return true
		}
	}
	return false
}

// ResizePolicy returns the restart policy of a container for a resource (default NotRequired).
func ResizePolicy(c corev1.Container, res corev1.ResourceName) corev1.ResourceResizeRestartPolicy {
	for _, p := range c.ResizePolicy {
		if p.ResourceName == res {
			return p.RestartPolicy
		}
	}
	return corev1.NotRequired
}
