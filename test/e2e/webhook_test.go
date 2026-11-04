//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// The webhook (WO-07) is the Helm release `webhook`, Deployment resource-optimizer/webhook.
var (
	webhookNamespace  = getenv("E2E_WEBHOOK_NAMESPACE", "resource-optimizer")
	webhookDeployment = getenv("E2E_WEBHOOK_DEPLOYMENT", "webhook")
)

const (
	injectedAnnotation = "resource-optimizer.oz.io/injected-resources"
	// scaledFromAnnotation records the webhook's replicas while scenario 8 has it scaled to 0, so
	// `make e2e-unblock` can restore it after a killed run.
	scaledFromAnnotation = "e2e.resource-optimizer.oz.io/scaled-from"
)

func scale(t *testing.T, cl cluster, ns, name string, replicas int32) {
	t.Helper()
	eventually(t, time.Minute, 2*time.Second, fmt.Sprintf("scale %s/%s to %d", ns, name, replicas), func() (bool, string) {
		var d appsv1.Deployment
		if err := cl.C.Get(ctx(t), client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
			return false, err.Error()
		}
		d.Spec.Replicas = &replicas
		if err := cl.C.Update(ctx(t), &d); err != nil {
			return false, err.Error()
		}
		return true, ""
	})
}

// webhookDown scales the webhook to 0 and returns the function that restores it (also run at
// cleanup). Waits until no webhook pod is left.
func webhookDown(t *testing.T, cl cluster) func() {
	t.Helper()
	var d appsv1.Deployment
	key := client.ObjectKey{Namespace: webhookNamespace, Name: webhookDeployment}
	must(t, cl.C.Get(ctx(t), key, &d))
	orig := int32(1)
	if d.Spec.Replicas != nil {
		orig = *d.Spec.Replicas
	}
	if v, ok := d.Annotations[scaledFromAnnotation]; ok {
		n, _ := strconv.Atoi(v)
		orig = int32(n)
	}
	if orig == 0 {
		t.Fatal("the webhook is already scaled to 0")
	}
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	d.Annotations[scaledFromAnnotation] = strconv.Itoa(int(orig))
	zero := int32(0)
	d.Spec.Replicas = &zero
	must(t, cl.C.Update(ctx(t), &d))
	t.Logf("webhook on %s scaled 0 (was %d)", cl.Name, orig)
	eventually(t, 2*time.Minute, 2*time.Second, "webhook pods gone", func() (bool, string) {
		var list corev1.PodList
		if err := cl.C.List(ctx(t), &list, client.InNamespace(webhookNamespace),
			client.MatchingLabels{"app.kubernetes.io/name": "resource-optimizer-webhook"}); err != nil {
			return false, err.Error()
		}
		return len(list.Items) == 0, fmt.Sprintf("%d webhook pods", len(list.Items))
	})
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for i := 0; i < 10; i++ {
			var cur appsv1.Deployment
			err := cl.C.Get(c, key, &cur)
			if err == nil {
				cur.Spec.Replicas = &orig
				delete(cur.Annotations, scaledFromAnnotation)
				err = cl.C.Update(c, &cur)
			}
			if err == nil {
				t.Logf("webhook on %s scaled back to %d", cl.Name, orig)
				if err := releaseFields(c, cl, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}); err != nil {
					t.Errorf("%v (make e2e-unblock drops it)", err)
				}
				return
			}
			time.Sleep(time.Second)
		}
		t.Errorf("SCALE the webhook on %s BACK BY HAND to %d (make e2e-unblock)", cl.Name, orig)
	}
	t.Cleanup(restore)
	return restore
}

// Scenario 8: webhook. New pods of an Applied in-place workload get the applied values at
// admission; with the webhook down, pods are still created (failurePolicy Ignore), with the
// template values, and the agent's drift repair resizes them afterwards.
func TestWebhook(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "webhook")
	deploy(t, cl, ns, overprovisioned("web"))
	armed(t, cl, ns, ns, "web", overCPU, autoCPU(v1alpha1.StrategyInPlace))
	r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	want := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
	// The webhook only trusts a status written for the current spec.
	eventually(t, time.Minute, 2*time.Second, "RSR status observed its spec", func() (bool, string) {
		r = rsr(t, cl, ns, deployment, "web")
		return r.Status.ObservedGeneration == r.Generation, fmt.Sprintf("observedGeneration %d, generation %d", r.Status.ObservedGeneration, r.Generation)
	})

	t.Run("scale-out gets the applied values", func(t *testing.T) {
		before := snapshot(t, cl, ns, "web")
		scale(t, cl, ns, "web", 2)
		waitReady(t, cl, ns, "web", 2*time.Minute)
		for _, p := range pods(t, cl, ns, "web") {
			if _, old := before[p.Name]; old {
				continue
			}
			if got := p.Spec.Containers[0].Resources.Requests.Cpu(); got.Cmp(want) != 0 {
				t.Errorf("new pod %s cpu %s, want the applied %s", p.Name, got.String(), want.String())
			}
			if _, ok := p.Annotations[injectedAnnotation]; !ok {
				t.Errorf("new pod %s has no %s annotation (not mutated at admission)", p.Name, injectedAnnotation)
			}
			for _, cs := range p.Status.ContainerStatuses {
				if cs.RestartCount != 0 {
					t.Errorf("new pod %s restarted", p.Name)
				}
			}
		}
		if got := templateCPU(getDeployment(t, cl, ns, "web")); got.Cmp(resource.MustParse(overCPU)) != 0 {
			t.Errorf("template cpu %s: the webhook must not touch the Deployment", got.String())
		}
	})

	t.Run("webhook down does not block pod creation", func(t *testing.T) {
		restore := webhookDown(t, cl)
		before := snapshot(t, cl, ns, "web")
		start := time.Now()
		scale(t, cl, ns, "web", 3)
		waitReady(t, cl, ns, "web", 2*time.Minute)
		if d := time.Since(start); d > time.Minute {
			t.Errorf("scale-out took %s with the webhook down; admission must not wait for it", d.Round(time.Second))
		}
		var fresh []corev1.Pod
		for _, p := range pods(t, cl, ns, "web") {
			if _, old := before[p.Name]; !old {
				fresh = append(fresh, p)
			}
		}
		if len(fresh) != 1 {
			t.Fatalf("%d new pods, want 1", len(fresh))
		}
		if _, ok := fresh[0].Annotations[injectedAnnotation]; ok {
			t.Errorf("pod %s was mutated although the webhook is down", fresh[0].Name)
		}
		restore()
		// The agent's drift repair brings the unmutated pod to the applied value.
		eventually(t, 3*time.Minute, 5*time.Second, "drift repair resizes "+fresh[0].Name, func() (bool, string) {
			var p corev1.Pod
			if err := cl.C.Get(ctx(t), client.ObjectKeyFromObject(&fresh[0]), &p); err != nil {
				return false, err.Error()
			}
			got := p.Spec.Containers[0].Resources.Requests.Cpu()
			return got.Cmp(want) == 0, "cpu " + got.String()
		})
	})
}
