package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// Path is the HTTP path the webhook is served on.
const Path = "/mutate-v1-pod"

// Results (the "result" label of ro_webhook_admissions_total).
const (
	ResultMutated = "mutated"
	ResultSkipped = "skipped"
	ResultError   = "error"
)

// Further skip and error reasons (see also owner.go and mutate.go).
const (
	ReasonNotPodCreate       = "not-pod-create"
	ReasonOptOut             = "opt-out"
	ReasonCacheNotSynced     = "cache-not-synced"
	ReasonNoRecommendation   = "no-recommendation"
	ReasonPodLevelResources  = "pod-level-resources"
	ReasonNoChange           = "no-change"
	ReasonDecode             = "decode"
	ReasonOwnerLookup        = "owner-lookup"
	ReasonRecommendationRead = "recommendation-lookup"
	ReasonPatch              = "patch"
)

// Handler is the pod mutating admission handler. It never denies: every error path allows the
// pod unchanged (the webhook configuration also uses failurePolicy Ignore).
type Handler struct {
	// Cache is the informer-backed reader for RightsizingRecommendations (with the IndexTargetRef
	// field index), ReplicaSets and Jobs (metadata only).
	Cache    client.Reader
	Resolver *Resolver
	Decoder  admission.Decoder
	Metrics  *Metrics
	Log      *slog.Logger
	// Synced reports whether the informer caches have synced. Until then nothing is injected.
	Synced func() bool
}

// outcome is what one admission did.
type outcome struct {
	result, reason string
	resp           admission.Response
}

func allow(result, reason string) outcome {
	return outcome{result: result, reason: reason, resp: admission.Allowed(reason)}
}

// Handle implements admission.Handler.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	start := time.Now()
	o := h.handle(ctx, req)
	h.Metrics.Observe(o.result, o.reason, time.Since(start))
	return o.resp
}

func (h *Handler) handle(ctx context.Context, req admission.Request) outcome {
	if req.Operation != admissionv1.Create || req.Kind.Kind != "Pod" || req.SubResource != "" {
		return allow(ResultSkipped, ReasonNotPodCreate)
	}
	pod := &corev1.Pod{}
	if err := h.Decoder.Decode(req, pod); err != nil {
		h.Log.WarnContext(ctx, "decode pod", "uid", req.UID, "err", err)
		return allow(ResultError, ReasonDecode)
	}
	ns := req.Namespace
	if ns == "" {
		ns = pod.Namespace
	}
	log := h.Log.With("namespace", ns, "pod", podName(pod))
	if pod.Labels[v1alpha1.LabelWebhookSkip] == "true" {
		return allow(ResultSkipped, ReasonOptOut)
	}
	if h.Synced != nil && !h.Synced() {
		return allow(ResultSkipped, ReasonCacheNotSynced)
	}
	owner, err := h.Resolver.Resolve(ctx, ns, pod)
	if err != nil {
		if reason, ok := IsUnmanaged(err); ok {
			return allow(ResultSkipped, reason)
		}
		log.WarnContext(ctx, "resolve owner", "err", err)
		return allow(ResultError, ReasonOwnerLookup)
	}
	log = log.With("kind", owner.Kind, "workload", owner.Name)

	rec, reason, err := h.recommendation(ctx, ns, owner)
	if err != nil {
		log.WarnContext(ctx, "read recommendation", "err", err)
		return allow(ResultError, ReasonRecommendationRead)
	}
	if rec == nil {
		return allow(ResultSkipped, reason)
	}
	if pod.Spec.Resources != nil {
		// Pod-level resources bound the sum of the containers; changing containers could make the
		// pod invalid. Leave such pods to the agent.
		return allow(ResultSkipped, ReasonPodLevelResources)
	}

	raw := req.Object.Raw
	m := Mutate(pod, rec.Spec.Containers)
	for _, s := range m.Skipped {
		log.InfoContext(ctx, "value not injected", "detail", s, "recommendation", rec.Name)
	}
	if !m.Changed {
		return allow(ResultSkipped, ReasonNoChange)
	}
	injected, err := json.Marshal(m.Injected)
	if err != nil {
		return allow(ResultError, ReasonPatch)
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[v1alpha1.AnnotationInjectedResources] = string(injected)
	if id := recommendationID(rec); id != "" {
		pod.Annotations[v1alpha1.AnnotationRecommendationID] = id
	}
	mutated, err := json.Marshal(pod)
	if err != nil {
		log.WarnContext(ctx, "marshal pod", "err", err)
		return allow(ResultError, ReasonPatch)
	}
	resp := admission.PatchResponseFromRaw(raw, mutated)
	if resp.Result != nil && resp.Result.Code == http.StatusInternalServerError {
		log.WarnContext(ctx, "build patch", "err", resp.Result.Message)
		return allow(ResultError, ReasonPatch)
	}
	log.InfoContext(ctx, "injected recommended resources", "recommendation", rec.Name,
		"recommendationID", recommendationID(rec), "injected", string(injected))
	return outcome{result: ResultMutated, reason: "", resp: resp}
}

// recommendation returns the eligible RightsizingRecommendation of a workload, or nil and the skip
// reason. Recommendations are looked up by spec.targetRef (never by name, WO-00).
func (h *Handler) recommendation(ctx context.Context, ns string, owner Owner) (*v1alpha1.RightsizingRecommendation, string, error) {
	var list v1alpha1.RightsizingRecommendationList
	if err := h.Cache.List(ctx, &list, client.InNamespace(ns),
		client.MatchingFields{v1alpha1.IndexTargetRef: v1alpha1.TargetRefIndexValue(owner.Kind, owner.Name)}); err != nil {
		return nil, "", err
	}
	if len(list.Items) == 0 {
		return nil, ReasonNoRecommendation, nil
	}
	// One per workload by contract; be deterministic if there are more.
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	reason := ""
	for i := range list.Items {
		r := &list.Items[i]
		why := Eligible(r)
		if why == "" {
			return r, "", nil
		}
		if reason == "" {
			reason = why
		}
	}
	return nil, reason, nil
}

func recommendationID(r *v1alpha1.RightsizingRecommendation) string {
	if r.Status.AppliedRecommendationID != "" {
		return r.Status.AppliedRecommendationID
	}
	return r.Spec.Source.RecommendationID
}

func podName(p *corev1.Pod) string {
	if p.Name != "" {
		return p.Name
	}
	return p.GenerateName
}
