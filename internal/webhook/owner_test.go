package webhook

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

const ns = "team-a"

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func ctrlRef(apiVersion, kind, name string) metav1.OwnerReference {
	t := true
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: &t}
}

// meta returns an intermediate owner (ReplicaSet/Job) as metadata, optionally controlled by owner.
func meta(gvk schema.GroupVersionKind, name string, owner *metav1.OwnerReference) *metav1.PartialObjectMetadata {
	m := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	m.SetGroupVersionKind(gvk)
	if owner != nil {
		m.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return m
}

func podOwnedBy(refs ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "p-", OwnerReferences: refs}}
}

func ref(r metav1.OwnerReference) *metav1.OwnerReference { return &r }

func TestResolve(t *testing.T) {
	notController := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1"}
	objs := []client.Object{
		meta(ReplicaSetGVK, "web-7d9f", ref(ctrlRef("apps/v1", "Deployment", "web"))),
		meta(ReplicaSetGVK, "bare-rs", nil),
		meta(ReplicaSetGVK, "odd-rs", ref(ctrlRef("example.com/v1", "Deployment", "x"))),
		meta(JobGVK, "batch-29000", ref(ctrlRef("batch/v1", "CronJob", "batch"))),
		meta(JobGVK, "one-off", nil),
		meta(JobGVK, "argo-job", ref(ctrlRef("argoproj.io/v1alpha1", "Workflow", "wf"))),
	}
	live := []client.Object{meta(ReplicaSetGVK, "fresh-rs", ref(ctrlRef("apps/v1", "Deployment", "fresh")))}
	cache := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	apiReader := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(append(objs, live...)...).Build()

	tests := []struct {
		name      string
		pod       *corev1.Pod
		noLive    bool
		want      Owner
		unmanaged string
	}{
		{name: "deployment via replicaset", pod: podOwnedBy(ctrlRef("apps/v1", "ReplicaSet", "web-7d9f")), want: Owner{v1alpha1.KindDeployment, "web"}},
		{name: "statefulset", pod: podOwnedBy(ctrlRef("apps/v1", "StatefulSet", "db")), want: Owner{v1alpha1.KindStatefulSet, "db"}},
		{name: "daemonset", pod: podOwnedBy(ctrlRef("apps/v1", "DaemonSet", "node-agent")), want: Owner{v1alpha1.KindDaemonSet, "node-agent"}},
		{name: "cronjob via job", pod: podOwnedBy(ctrlRef("batch/v1", "Job", "batch-29000")), want: Owner{v1alpha1.KindCronJob, "batch"}},
		{name: "replicaset not in cache yet, found live", pod: podOwnedBy(ctrlRef("apps/v1", "ReplicaSet", "fresh-rs")), want: Owner{v1alpha1.KindDeployment, "fresh"}},
		{name: "replicaset not in cache, no live reader", pod: podOwnedBy(ctrlRef("apps/v1", "ReplicaSet", "fresh-rs")), noLive: true, unmanaged: ReasonUnmanagedOwner},
		{name: "replicaset gone", pod: podOwnedBy(ctrlRef("apps/v1", "ReplicaSet", "gone")), unmanaged: ReasonUnmanagedOwner},
		{name: "bare pod", pod: podOwnedBy(), unmanaged: ReasonNoOwner},
		{name: "owner without controller flag", pod: podOwnedBy(notController), unmanaged: ReasonNoOwner},
		{name: "bare replicaset", pod: podOwnedBy(ctrlRef("apps/v1", "ReplicaSet", "bare-rs")), unmanaged: ReasonUnmanagedOwner},
		{name: "replicaset owned by a foreign Deployment kind", pod: podOwnedBy(ctrlRef("apps/v1", "ReplicaSet", "odd-rs")), unmanaged: ReasonUnmanagedOwner},
		{name: "job without cronjob", pod: podOwnedBy(ctrlRef("batch/v1", "Job", "one-off")), unmanaged: ReasonUnmanagedOwner},
		{name: "job owned by something else", pod: podOwnedBy(ctrlRef("batch/v1", "Job", "argo-job")), unmanaged: ReasonUnmanagedOwner},
		{name: "custom controller", pod: podOwnedBy(ctrlRef("argoproj.io/v1alpha1", "Rollout", "r")), unmanaged: ReasonUnmanagedOwner},
		{name: "node-owned static pod", pod: podOwnedBy(ctrlRef("v1", "Node", "n1")), unmanaged: ReasonUnmanagedOwner},
		{name: "StatefulSet of another group", pod: podOwnedBy(ctrlRef("apps.kruise.io/v1beta1", "StatefulSet", "s")), unmanaged: ReasonUnmanagedOwner},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Resolver{Cache: cache, Live: apiReader}
			if tt.noLive {
				r.Live = nil
			}
			got, err := r.Resolve(context.Background(), ns, tt.pod)
			if tt.unmanaged != "" {
				reason, ok := IsUnmanaged(err)
				if !ok || reason != tt.unmanaged {
					t.Fatalf("want unmanaged %q, got %v, %v", tt.unmanaged, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveLookupError(t *testing.T) {
	boom := errors.New("apiserver down")
	cache := fake.NewClientBuilder().WithScheme(testScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		},
	}).Build()
	r := &Resolver{Cache: cache}
	_, err := r.Resolve(context.Background(), ns, podOwnedBy(ctrlRef("apps/v1", "ReplicaSet", "x")))
	if !errors.Is(err, boom) {
		t.Fatalf("want lookup error, got %v", err)
	}
	if _, ok := IsUnmanaged(err); ok {
		t.Fatal("lookup error must not be reported as unmanaged")
	}
	if (&ErrUnmanaged{Reason: "x"}).Error() != "unmanaged pod: x" {
		t.Fatal("ErrUnmanaged.Error")
	}
}
