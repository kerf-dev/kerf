package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

// TestJVMFixedHeap: the agent reports the JVM heap settings to the hub and never changes
// the memory of a JVM with a fixed -Xmx, while CPU is applied.
func TestJVMFixedHeap(t *testing.T) {
	needEnv(t)
	const name = "jvm-fixed"
	u := deploymentApply(name, map[string]string{v1alpha1.AnnotationMode: "auto-patch", v1alpha1.AnnotationStrategy: "rollout"}, "500m", "256Mi")
	c := u.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	c["image"] = "eclipse-temurin:21-jre"
	c["command"] = []any{"java", "-Xmx128m", "-jar", "/app.jar"}
	c["env"] = []any{map[string]any{"name": "JAVA_TOOL_OPTIONS", "value": "-XX:+UseSerialGC"}}
	if err := argoApply(t, u, false); err != nil {
		t.Fatal(err)
	}
	setRec(recFor(name, "250m", "200Mi"))
	eventually(t, 20*time.Second, "applied", wantPhase(name, v1alpha1.PhaseApplied))
	d := getDeploy(t, name)
	if cpuReq(d) != "250m" || memReq(d) != "256Mi" {
		t.Fatalf("cpu %s memory %s, want cpu applied (250m) and memory kept (256Mi)", cpuReq(d), memReq(d))
	}
	eventually(t, 5*time.Second, "hub status with jvm", func() error {
		st, ok := stub.Statuses(testCluster)["default/Deployment/"+name]
		if !ok {
			return fmt.Errorf("no status")
		}
		if len(st.JVM) != 1 || st.JVM[0].Name != "app" || st.JVM[0].MaxHeap != "128Mi" || len(st.JVM[0].DetectedBy) != 3 {
			return fmt.Errorf("jvm %+v", st.JVM)
		}
		return nil
	})
}
