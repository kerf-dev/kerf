package health

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestThrottlingQuerySelector(t *testing.T) {
	q := ThrottleQuery{Cluster: "ro-spoke-1", Namespace: "shop", Pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api-1"}}}, Containers: []string{"app"}}
	def := ThrottlingQuery(q, 5*time.Minute)
	if !strings.Contains(def, `{cluster="ro-spoke-1",namespace="shop",pod=~"api-1",container=~"app"}`) {
		t.Errorf("default selector changed: %s", def)
	}
	if got := ThrottlingQuerySelector(q, 5*time.Minute, nil); got != def {
		t.Error("nil selector differs from the default")
	}
	got := ThrottlingQuerySelector(q, 5*time.Minute, &MetricsSelector{Label: "k8s_cluster", Value: "prod", Extra: []string{`env="prod"`}})
	if !strings.Contains(got, `{k8s_cluster="prod",env="prod",namespace="shop",`) || strings.Contains(got, `cluster="ro-spoke-1"`) {
		t.Errorf("bound selector: %s", got)
	}
	got = ThrottlingQuerySelector(q, 5*time.Minute, &MetricsSelector{Label: "k8s_cluster"})
	if !strings.Contains(got, `{k8s_cluster="ro-spoke-1",namespace=`) {
		t.Errorf("default value: %s", got)
	}
	got = ThrottlingQuerySelector(q, 5*time.Minute, &MetricsSelector{})
	if !strings.Contains(got, `{namespace="shop",`) {
		t.Errorf("no cluster label: %s", got)
	}
}
