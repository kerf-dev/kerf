//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// Images already on the k3d nodes (sample workloads, WO-01), so scenarios don't pull.
const (
	stressImage = "docker.io/colinianking/stress-ng:77b17fb95ec2f00e1c2ac2a8a20fe799150d8bd5"
	jvmImage    = "docker.io/library/eclipse-temurin:21.0.13_9-jdk-alpine-3.23"
)

// app describes a test workload: one container "app" running stress-ng.
type app struct {
	Name      string
	Replicas  int32
	CPULoad   int // percent of one core (stress-ng --cpu 1 --cpu-load N)
	Requests  corev1.ResourceList
	Limits    corev1.ResourceList
	Resize    corev1.ResourceResizeRestartPolicy // memory resize policy; cpu is always NotRequired
	Labels    map[string]string
	JVM       bool              // run a small JVM program instead of stress-ng
	FixedHeap bool              // JVM with -Xmx (memory is audit-only, WO-12) instead of MaxRAMPercentage
	Container *corev1.Container // replaces the stress-ng container
	Volumes   []corev1.Volume   // pod volumes for Container
}

func rl(cpu, mem string) corev1.ResourceList {
	l := corev1.ResourceList{}
	if cpu != "" {
		l[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if mem != "" {
		l[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	return l
}

func (a app) deployment(ns string) *appsv1.Deployment {
	labels := map[string]string{"app": a.Name}
	for k, v := range a.Labels {
		labels[k] = v
	}
	if a.Replicas == 0 {
		a.Replicas = 1
	}
	if a.Resize == "" {
		a.Resize = corev1.NotRequired
	}
	c := corev1.Container{
		Name:  "app",
		Image: stressImage,
		Args:  []string{"--cpu", "1", "--cpu-load", fmt.Sprint(a.CPULoad), "--timeout", "0"},
		Resources: corev1.ResourceRequirements{
			Requests: a.Requests,
			Limits:   a.Limits,
		},
		ResizePolicy: []corev1.ContainerResizePolicy{
			{ResourceName: corev1.ResourceCPU, RestartPolicy: corev1.NotRequired},
			{ResourceName: corev1.ResourceMemory, RestartPolicy: a.Resize},
		},
	}
	var volumes []corev1.Volume
	if a.JVM {
		c.Image = jvmImage
		c.Args = nil
		// The heap follows the container memory limit, so a memory resize with RestartContainer
		// takes effect. A fixed -Xmx makes memory audit-only (WO-12, JVMFixedHeap).
		heap := "-XX:MaxRAMPercentage=50"
		if a.FixedHeap {
			heap = "-Xmx64m"
		}
		c.Command = []string{"java", heap, "-XX:+UseSerialGC", "/app/Main.java"}
		c.VolumeMounts = []corev1.VolumeMount{{Name: "src", MountPath: "/app"}}
		volumes = []corev1.Volume{{Name: "src", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: a.Name + "-src"}}}}}
	}
	if a.Container != nil {
		c = *a.Container
		volumes = a.Volumes
	}
	zero := int64(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: ns, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &a.Replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": a.Name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: &zero,
					Containers:                    []corev1.Container{c},
					Volumes:                       volumes,
				},
			},
		},
	}
}

// jvmProgram keeps ~30 MiB of live heap and burns a little CPU.
const jvmProgram = `public class Main {
  public static void main(String[] a) throws Exception {
    byte[][] keep = new byte[30][];
    for (int i = 0; i < keep.length; i++) keep[i] = new byte[1 << 20];
    long x = 0;
    while (true) { for (int i = 0; i < 200000; i++) x += i % 7; Thread.sleep(50); if (x == 42) System.out.println(keep.length); }
  }
}
`

// deploy creates the workload (and its JVM source ConfigMap) and waits until all pods run.
func deploy(t *testing.T, cl cluster, ns string, a app) *appsv1.Deployment {
	t.Helper()
	if a.JVM {
		must(t, cl.C.Create(ctx(t), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: a.Name + "-src", Namespace: ns},
			Data: map[string]string{"Main.java": jvmProgram}}))
	}
	d := a.deployment(ns)
	must(t, cl.C.Create(ctx(t), d))
	waitReady(t, cl, ns, a.Name, 3*time.Minute)
	return d
}

func waitReady(t *testing.T, cl cluster, ns, name string, timeout time.Duration) {
	t.Helper()
	eventually(t, timeout, 3*time.Second, "deployment "+name+" ready", func() (bool, string) {
		var d appsv1.Deployment
		if err := cl.C.Get(ctx(t), client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
			return false, err.Error()
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		s := d.Status
		return s.ObservedGeneration == d.Generation && s.UpdatedReplicas == want && s.ReadyReplicas == want && s.Replicas == want,
			fmt.Sprintf("ready %d/%d updated %d", s.ReadyReplicas, want, s.UpdatedReplicas)
	})
}

// eventually polls cond until it returns true or the timeout passes, logging progress every minute.
func eventually(t *testing.T, timeout, every time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	start := time.Now()
	lastLog := start
	var last string
	for {
		ok, status := cond()
		if ok {
			t.Logf("%s after %s", what, time.Since(start).Round(time.Second))
			return
		}
		last = status
		if time.Since(start) > timeout {
			t.Fatalf("timed out after %s waiting for %s; last status: %s", timeout, what, last)
		}
		if time.Since(lastLog) > time.Minute {
			t.Logf("still waiting for %s (%s): %s", what, time.Since(start).Round(time.Second), last)
			lastLog = time.Now()
		}
		time.Sleep(every)
	}
}

// consistently checks cond for the whole duration and fails on the first violation.
func consistently(t *testing.T, d, every time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if ok, status := cond(); !ok {
			t.Fatalf("%s violated: %s", what, status)
		}
		time.Sleep(every)
	}
}

// policy creates a cluster-scoped RightsizingPolicy named like the namespace (deleted with it).
func policy(t *testing.T, cl cluster, name, ns string, spec v1alpha1.RightsizingPolicySpec) *v1alpha1.RightsizingPolicy {
	t.Helper()
	spec.Selector.Namespaces = []string{ns}
	if spec.Priority == 0 {
		spec.Priority = 1000
	}
	p := &v1alpha1.RightsizingPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{suiteLabel: "true"}}, Spec: spec}
	must(t, cl.C.Create(ctx(t), p))
	if name != ns {
		t.Cleanup(func() { _ = cl.C.Delete(context.Background(), p) })
	}
	return p
}

func ptr[T any](v T) *T { return &v }

// relaxed are the guardrails every scenario uses: a few minutes of data are enough.
func relaxed() *v1alpha1.Guardrails {
	return &v1alpha1.Guardrails{LearningPeriod: &metav1.Duration{}, MinConfidence: "0"}
}

// autoCPU is a policy applying CPU only, with the given strategy.
func autoCPU(strategy v1alpha1.Strategy) v1alpha1.RightsizingPolicySpec {
	return v1alpha1.RightsizingPolicySpec{
		CPU:        &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAutoPatch},
		Memory:     &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAudit},
		Apply:      &v1alpha1.ApplyPolicy{Strategy: strategy},
		Guardrails: relaxed(),
	}
}

// rsr returns the agent's RightsizingRecommendation for a workload (nil if none yet).
func rsr(t *testing.T, cl cluster, ns, kind, name string) *v1alpha1.RightsizingRecommendation {
	t.Helper()
	var list v1alpha1.RightsizingRecommendationList
	if err := cl.C.List(ctx(t), &list, client.InNamespace(ns)); err != nil {
		t.Logf("list rsr: %v", err)
		return nil
	}
	for i := range list.Items {
		r := &list.Items[i]
		if string(r.Spec.TargetRef.Kind) == kind && r.Spec.TargetRef.Name == name {
			return r
		}
	}
	return nil
}

// rsrStatus summarizes phase and condition reasons for logs.
func rsrStatus(r *v1alpha1.RightsizingRecommendation) string {
	if r == nil {
		return "no RightsizingRecommendation yet"
	}
	var cs []string
	for _, c := range r.Status.Conditions {
		cs = append(cs, fmt.Sprintf("%s=%s/%s", c.Type, c.Status, c.Reason))
	}
	return fmt.Sprintf("phase %q strategy %q [%s]", r.Status.Phase, r.Spec.Strategy, strings.Join(cs, " "))
}

func hasReason(r *v1alpha1.RightsizingRecommendation, reason string) bool {
	if r == nil {
		return false
	}
	for _, c := range r.Status.Conditions {
		if c.Reason == reason {
			return true
		}
	}
	return false
}

// waitPhase waits until the workload's RSR reaches the phase.
func waitPhase(t *testing.T, cl cluster, ns, kind, name string, phase v1alpha1.Phase) *v1alpha1.RightsizingRecommendation {
	t.Helper()
	var r *v1alpha1.RightsizingRecommendation
	eventually(t, env.ApplyTimeout, 5*time.Second, fmt.Sprintf("%s %s phase %s", kind, name, phase), func() (bool, string) {
		env.Hub.maybeRecompute(ctx(t), t.Logf)
		r = rsr(t, cl, ns, kind, name)
		return r != nil && r.Status.Phase == phase, rsrStatus(r)
	})
	return r
}

// waitReason waits until a condition of the workload's RSR carries the reason.
func waitReason(t *testing.T, cl cluster, ns, kind, name, reason string, timeout time.Duration) *v1alpha1.RightsizingRecommendation {
	t.Helper()
	var r *v1alpha1.RightsizingRecommendation
	eventually(t, timeout, 5*time.Second, fmt.Sprintf("%s %s reason %s", kind, name, reason), func() (bool, string) {
		r = rsr(t, cl, ns, kind, name)
		return hasReason(r, reason), rsrStatus(r)
	})
	return r
}

// waitHubRecommendation waits until the hub has a recommendation for the workload that
// satisfies ok (any recommendation when ok is nil). The first hub run after a workload starts may
// see only a few samples, so scenarios that assert on values pass a condition and wait for a
// later run instead of judging the first one.
func waitHubRecommendation(t *testing.T, cl cluster, ns, kind, name string, ok func(*workloadRec) (bool, string)) *workloadRec {
	t.Helper()
	var rec *workloadRec
	eventually(t, env.ApplyTimeout, 10*time.Second, fmt.Sprintf("hub recommendation for %s/%s", ns, name), func() (bool, string) {
		env.Hub.maybeRecompute(ctx(t), t.Logf)
		d, err := env.Hub.workload(ctx(t), cl.Name, ns, kind, name)
		if err != nil {
			return false, err.Error()
		}
		if d == nil {
			return false, "hub doesn't know the workload yet"
		}
		if d.Recommendation == nil {
			return false, "no recommendation yet (phase " + d.Phase + ")"
		}
		rec = d.Recommendation
		if ok == nil {
			return true, ""
		}
		good, why := ok(rec)
		return good, fmt.Sprintf("recommendation %s (coverage %s): %s", rec.ID, rec.DataCoverage.Covered, why)
	})
	return rec
}

func pods(t *testing.T, cl cluster, ns, app string) []corev1.Pod {
	t.Helper()
	var list corev1.PodList
	must(t, cl.C.List(ctx(t), &list, client.InNamespace(ns), client.MatchingLabels{"app": app}))
	var out []corev1.Pod
	for _, p := range list.Items {
		if p.DeletionTimestamp == nil {
			out = append(out, p)
		}
	}
	return out
}

// podState is what "nothing changed" compares: identity, resources, restarts.
type podState struct {
	UID      string
	CPUReq   string
	MemReq   string
	Restarts int32
}

func snapshot(t *testing.T, cl cluster, ns, app string) map[string]podState {
	t.Helper()
	out := map[string]podState{}
	for _, p := range pods(t, cl, ns, app) {
		c := p.Spec.Containers[0]
		s := podState{UID: string(p.UID), CPUReq: c.Resources.Requests.Cpu().String(), MemReq: c.Resources.Requests.Memory().String()}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == c.Name {
				s.Restarts = cs.RestartCount
			}
		}
		out[p.Name] = s
	}
	return out
}

func getDeployment(t *testing.T, cl cluster, ns, name string) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	must(t, cl.C.Get(ctx(t), client.ObjectKey{Namespace: ns, Name: name}, &d))
	return &d
}

func templateCPU(d *appsv1.Deployment) *resource.Quantity {
	return d.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu()
}

// q parses a quantity (zero for "").
func q(s string) *resource.Quantity {
	if s == "" {
		return &resource.Quantity{}
	}
	v := resource.MustParse(s)
	return &v
}
