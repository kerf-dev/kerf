package webhook

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// IndexTargetRef extracts the IndexTargetRef value of a RightsizingRecommendation.
func IndexTargetRef(o client.Object) []string {
	r, ok := o.(*v1alpha1.RightsizingRecommendation)
	if !ok {
		return nil
	}
	return []string{v1alpha1.TargetRefIndexValue(r.Spec.TargetRef.Kind, r.Spec.TargetRef.Name)}
}

// Setup registers the cache index and informers, the webhook handler at Path and the readiness
// check on mgr. The returned handler is ready once the manager has started and the caches synced.
func Setup(ctx context.Context, mgr manager.Manager, metrics *Metrics, log *slog.Logger) (*Handler, error) {
	c := mgr.GetCache()
	if err := c.IndexField(ctx, &v1alpha1.RightsizingRecommendation{}, v1alpha1.IndexTargetRef, IndexTargetRef); err != nil {
		return nil, err
	}
	// Start the metadata informers up front, so the first admissions do not wait for them.
	for _, gvk := range []schema.GroupVersionKind{ReplicaSetGVK, JobGVK} {
		obj := &metav1.PartialObjectMetadata{}
		obj.SetGroupVersionKind(gvk)
		if _, err := c.GetInformer(ctx, obj); err != nil {
			return nil, err
		}
	}

	var synced atomic.Bool
	h := &Handler{
		Cache:    mgr.GetClient(),
		Resolver: &Resolver{Cache: mgr.GetClient(), Live: mgr.GetAPIReader()},
		Decoder:  admission.NewDecoder(mgr.GetScheme()),
		Metrics:  metrics,
		Log:      log,
		Synced:   synced.Load,
	}
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if !c.WaitForCacheSync(ctx) {
			return errors.New("informer caches did not sync")
		}
		synced.Store(true)
		metrics.CacheReady.Set(1)
		log.InfoContext(ctx, "informer caches synced")
		<-ctx.Done()
		return nil
	})); err != nil {
		return nil, err
	}

	srv := mgr.GetWebhookServer()
	srv.Register(Path, &webhook.Admission{Handler: h, RecoverPanic: ptr(true)})
	started := srv.StartedChecker()
	if err := mgr.AddReadyzCheck("webhook", func(req *http.Request) error {
		if !synced.Load() {
			return errors.New("informer caches not synced")
		}
		return started(req)
	}); err != nil {
		return nil, err
	}
	return h, nil
}

func ptr[T any](v T) *T { return &v }
