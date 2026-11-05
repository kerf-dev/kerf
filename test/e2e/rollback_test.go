//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kerf-dev/kerf/api/v1alpha1"
)

const pythonImage = "docker.io/library/python:3.13.18-alpine3.23" // already on the nodes (WO-01)

// controllable holds `mib` MiB of touched memory and exits while `crash` is 1, both read every
// second from ConfigMap <name>-ctl, so a test can make a running workload misbehave.
const controllable = `import time
buf = []
def read(k, d):
    try:
        return open('/ctl/' + k).read().strip()
    except OSError:
        return d
while True:
    if read('crash', '0') == '1':
        raise SystemExit(1)
    want = int(read('mib', '0'))
    while len(buf) < want:
        b = bytearray(1 << 20)
        for i in range(0, len(b), 4096):
            b[i] = 1
        buf.append(b)
    time.sleep(1)
`

func controllableApp(name string, requests, limits corev1.ResourceList) app {
	c := &corev1.Container{
		Name:    "app",
		Image:   pythonImage,
		Command: []string{"python3", "-u", "-c", controllable},
		Resources: corev1.ResourceRequirements{
			Requests: requests,
			Limits:   limits,
		},
		ResizePolicy: []corev1.ContainerResizePolicy{
			{ResourceName: corev1.ResourceCPU, RestartPolicy: corev1.NotRequired},
			{ResourceName: corev1.ResourceMemory, RestartPolicy: corev1.NotRequired},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "ctl", MountPath: "/ctl"}},
	}
	return app{Name: name, Container: c, Volumes: []corev1.Volume{{Name: "ctl", VolumeSource: corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name + "-ctl"}}}}}}
}

func setControl(t *testing.T, cl cluster, ns, name string, data map[string]string) {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-ctl", Namespace: ns}}
	err := cl.C.Get(ctx(t), client.ObjectKeyFromObject(cm), cm)
	if err != nil {
		cm.Data = data
		must(t, cl.C.Create(ctx(t), cm))
		return
	}
	cm.Data = data
	must(t, cl.C.Update(ctx(t), cm))
}

// rollbackGuardrails watches every change for 20 minutes with the default thresholds
// (1 OOM kill, 3 restarts).
func rollbackGuardrails() *v1alpha1.Guardrails {
	g := relaxed()
	g.Rollback = &v1alpha1.RollbackPolicy{WatchDuration: &metav1.Duration{Duration: 20 * time.Minute}}
	return g
}

// Scenario 10: rollback (WO-08). A change that makes the workload unhealthy within the watch
// window is rolled back to status.previous, with a rolled_back event.
func TestRollback(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1

	t.Run("OOM after a memory decrease", func(t *testing.T) {
		t.Parallel()
		ns := newNamespace(t, cl, "oom")
		setControl(t, cl, ns, "web", map[string]string{"mib": "20", "crash": "0"})
		deploy(t, cl, ns, controllableApp("web", rl("50m", "256Mi"), rl("", "256Mi")))
		// Rollout: in-place memory changes are refused on nodes with swap (see TestInPlaceJVMMemory).
		armedFor(t, cl, corev1.ResourceMemory, ns, ns, "web", "200Mi", v1alpha1.RightsizingPolicySpec{
			CPU:        &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAudit},
			Memory:     &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeAudit},
			Apply:      &v1alpha1.ApplyPolicy{Strategy: v1alpha1.StrategyRollout},
			Guardrails: rollbackGuardrails(),
		})
		r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
		applied := r.Spec.Containers[0].Requests[corev1.ResourceMemory]
		if applied.Cmp(resource.MustParse("256Mi")) >= 0 {
			t.Fatalf("applied memory %s is not a decrease", applied.String())
		}
		waitReady(t, cl, ns, "web", 3*time.Minute)

		// Grow past the new limit: the container is OOM-killed.
		mib := applied.Value()>>20 + 48
		t.Logf("applied memory %s; making the workload hold %d MiB", applied.String(), mib)
		setControl(t, cl, ns, "web", map[string]string{"mib": fmt.Sprint(mib), "crash": "0"})
		r = waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseRolledBack)
		if r.Status.RolledBackAt == nil {
			t.Error("RolledBack without rolledBackAt")
		}
		eventually(t, 2*time.Minute, 5*time.Second, "template memory restored to 256Mi", func() (bool, string) {
			d := getDeployment(t, cl, ns, "web")
			got := d.Spec.Template.Spec.Containers[0].Resources.Requests.Memory()
			return got.Cmp(resource.MustParse("256Mi")) == 0, "template memory " + got.String()
		})
		ev := waitEvent(t, cl, ns, "web", "rolled_back", func(e auditEvent) bool { return e.Reason == v1alpha1.ReasonOOMKills })
		t.Logf("rolled_back event: %s", ev.Message)
		setControl(t, cl, ns, "web", map[string]string{"mib": "20", "crash": "0"})
		waitReady(t, cl, ns, "web", 3*time.Minute)
	})

	t.Run("restarts after an in-place CPU change", func(t *testing.T) {
		t.Parallel()
		ns := newNamespace(t, cl, "restarts")
		setControl(t, cl, ns, "web", map[string]string{"mib": "10", "crash": "0"})
		deploy(t, cl, ns, controllableApp("web", rl(overCPU, "64Mi"), rl("", "128Mi")))
		spec := autoCPU(v1alpha1.StrategyInPlace)
		spec.Guardrails = rollbackGuardrails()
		armed(t, cl, ns, ns, "web", overCPU, spec)
		r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
		applied := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
		t.Logf("applied cpu %s in place; making the container crash", applied.String())

		setControl(t, cl, ns, "web", map[string]string{"mib": "10", "crash": "1"})
		waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseRolledBack)
		setControl(t, cl, ns, "web", map[string]string{"mib": "10", "crash": "0"})
		ev := waitEvent(t, cl, ns, "web", "rolled_back", func(e auditEvent) bool { return e.Reason == v1alpha1.ReasonRestarts })
		t.Logf("rolled_back event: %s", ev.Message)
		eventually(t, 2*time.Minute, 5*time.Second, "pods resized back to "+overCPU, func() (bool, string) {
			for name, s := range snapshot(t, cl, ns, "web") {
				if q(s.CPUReq).Cmp(resource.MustParse(overCPU)) != 0 {
					return false, fmt.Sprintf("pod %s cpu %s", name, s.CPUReq)
				}
			}
			return true, ""
		})
		if got := templateCPU(getDeployment(t, cl, ns, "web")); got.Cmp(resource.MustParse(overCPU)) != 0 {
			t.Errorf("template cpu %s: an in-place rollback must not touch the template", got.String())
		}
	})
}
