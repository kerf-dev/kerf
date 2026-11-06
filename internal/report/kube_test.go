package report

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

func kubeObjects() *fake.Clientset {
	pod := func(c corev1.Container) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
	}
	meta := func(ns, name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: ns, Name: name} }
	return fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: meta("shop", "orders"), Spec: appsv1.DeploymentSpec{Template: pod(corev1.Container{
			Name: "app", Image: "registry/orders:1.0",
			Env: []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-Xmx512m -Xms256m"}},
		})}},
		&appsv1.Deployment{ObjectMeta: meta("shop", "web"), Spec: appsv1.DeploymentSpec{Template: pod(corev1.Container{Name: "web", Image: "nginx:1.27"})}},
		&appsv1.StatefulSet{ObjectMeta: meta("data", "db"), Spec: appsv1.StatefulSetSpec{Template: pod(corev1.Container{Name: "pg", Image: "postgres:17"})}},
		&batchv1.CronJob{ObjectMeta: meta("batch", "nightly"), Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{
			Spec: batchv1.JobSpec{Template: pod(corev1.Container{Name: "job", Image: "eclipse-temurin:21-jre"})}}}},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("shop", "web"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
			MinReplicas:    ptr.To[int32](2), MaxReplicas: 10,
			Metrics: []autoscalingv2.MetricSpec{
				{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr.To[int32](70)}}},
			}},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 4, DesiredReplicas: 5}},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("shop", "orders"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "orders"}, MaxReplicas: 3,
			Metrics: []autoscalingv2.MetricSpec{
				{Type: autoscalingv2.PodsMetricSourceType, Pods: &autoscalingv2.PodsMetricSource{Metric: autoscalingv2.MetricIdentifier{Name: "rps"},
					Target: autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType}}},
			}}},
	)
}

func TestKubeReader(t *testing.T) {
	kr := &kubeReader{client: kubeObjects(), cluster: "c1"}
	if err := kr.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	key := func(ns, kind, name string) hubapi.WorkloadKey {
		return hubapi.WorkloadKey{Cluster: "c1", Namespace: ns, Kind: kind, Name: name}
	}

	t.Run("JVM settings", func(t *testing.T) {
		j, reported := kr.jvmFor(key("shop", hubapi.KindDeployment, "orders"))
		if !reported || len(j) != 1 || j[0].Name != "app" || j[0].MaxHeap != "512Mi" || j[0].InitialHeap != "256Mi" {
			t.Errorf("orders = %+v reported=%v", j, reported)
		}
		if j, reported := kr.jvmFor(key("shop", hubapi.KindDeployment, "web")); !reported || len(j) != 0 {
			t.Errorf("web has no JVM but is read: %+v reported=%v", j, reported)
		}
		if j, _ := kr.jvmFor(key("batch", hubapi.KindCronJob, "nightly")); len(j) != 1 {
			t.Errorf("a JVM image in a CronJob is detected: %+v", j)
		}
		if _, reported := kr.jvmFor(key("shop", hubapi.KindDeployment, "unknown")); reported {
			t.Error("a workload the cluster does not have must fall back to image detection")
		}
	})

	t.Run("HPAs", func(t *testing.T) {
		h := kr.hpas[key("shop", hubapi.KindDeployment, "web")]
		if h == nil || !h.Coupled() || h.Targets["cpu"] != 70 || h.MinReplicas != 2 || h.MaxReplicas != 10 || h.PeakReplicas != 5 {
			t.Fatalf("web HPA = %+v", h)
		}
		o := kr.hpas[key("shop", hubapi.KindDeployment, "orders")]
		if o == nil || o.Coupled() || len(o.Metrics) != 1 || o.Metrics[0] != "rps/averagevalue" {
			t.Fatalf("orders HPA = %+v", o)
		}
	})

	t.Run("loader fills HPAs the metrics did not have", func(t *testing.T) {
		inner := fixedLoader{d: &recommender.ClusterData{Cluster: "c1", Workloads: map[hubapi.WorkloadKey]*recommender.RawWorkload{
			key("shop", hubapi.KindDeployment, "web"): {HPA: &recommender.HPA{Name: "web", PeakReplicas: 9}},
			key("data", hubapi.KindStatefulSet, "db"): {},
		}}}
		d, err := kubeLoader{inner: inner, kube: kr}.Load(context.Background(), "c1", fixtureNow, 0)
		if err != nil {
			t.Fatal(err)
		}
		web := d.Workloads[key("shop", hubapi.KindDeployment, "web")].HPA
		if web == nil || web.Targets["cpu"] != 70 || web.PeakReplicas != 9 {
			t.Errorf("web HPA = %+v: the cluster's spec wins and the larger peak from the history stays", web)
		}
		if d.Workloads[key("data", hubapi.KindStatefulSet, "db")].HPA != nil {
			t.Error("db has no HPA")
		}
	})

	t.Run("only lists", func(t *testing.T) {
		for _, a := range kr.client.(*fake.Clientset).Actions() {
			if a.GetVerb() != "list" {
				t.Errorf("the cluster read must be read-only, got %s %s", a.GetVerb(), a.GetResource().Resource)
			}
		}
	})
}

func TestKubeReaderNamespaces(t *testing.T) {
	cs := kubeObjects()
	kr := &kubeReader{client: cs, cluster: "c1", namespaces: []string{"data"}}
	if err := kr.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	var namespaces []string
	for _, a := range cs.Actions() {
		if l, ok := a.(k8stesting.ListAction); ok {
			namespaces = append(namespaces, l.GetNamespace())
		}
	}
	for _, ns := range namespaces {
		if ns != "data" {
			t.Errorf("listed namespace %q, want only data", ns)
		}
	}
}

// TestReportWithKube: an hpa-coupled flag from the cluster and JVM detection reach the report even
// when kube-state-metrics has no HPA series.
func TestReportWithKube(t *testing.T) {
	fxs := loadFixtures(t)
	cfg := recommender.DefaultConfig()
	q := recommender.Queries{Cluster: fxs[0].Cluster, RateWindow: cfg.RateWindow}
	fq := newFixtureQuerier(t, q, cfg, cfg.Profiles["production"].Window, fxs)
	// Take the HPA series away from the metrics.
	for _, query := range []string{q.HPAInfo(cfg.InventoryLookback), q.HPAMetrics(cfg.InventoryLookback)} {
		delete(fq.instant, query)
	}
	kube := fake.NewSimpleClientset(&autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "patterns", Name: "diurnal"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "synth-diurnal-0"}, MinReplicas: ptr.To[int32](2), MaxReplicas: 8,
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{
				Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr.To[int32](70)}}}},
		},
	})
	rep, err := Run(context.Background(), Options{
		MetricsURL: "http://x.invalid", ClusterLabel: "cluster", ClusterValue: fxs[0].Cluster, Now: fixtureEnd(fxs),
	}, Deps{Querier: fq, Kube: kube})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Rows {
		if r.Workload != "synth-diurnal-0" {
			continue
		}
		if !contains(r.RiskFlags, hubapi.FlagHPACoupled) || r.HPA == nil {
			t.Errorf("synth-diurnal-0 flags %v hpa %+v; want hpa-coupled from the cluster", r.RiskFlags, r.HPA)
		}
		return
	}
	t.Fatal("synth-diurnal-0 is not in the report")
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
