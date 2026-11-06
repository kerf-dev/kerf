package report

import (
	"context"
	"fmt"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kerf-dev/kerf/internal/agent/workload"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// kubeReader reads HPAs and JVM settings from the cluster. It only lists: nothing is written.
type kubeReader struct {
	client kubernetes.Interface
	// namespaces limits the lists; empty = all.
	namespaces []string
	cluster    string

	hpas map[hubapi.WorkloadKey]*recommender.HPA
	jvm  map[hubapi.WorkloadKey][]hubapi.JVMContainer
}

// newKubeClient builds a client from a kubeconfig file ("" = the default loading rules: KUBECONFIG,
// ~/.kube/config, in-cluster) and an optional context.
func newKubeClient(kubeconfig, kubeContext string) (kubernetes.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	cfg.Timeout = 30 * time.Second
	return kubernetes.NewForConfig(cfg)
}

// load lists HPAs and the pod templates of Deployments, StatefulSets, DaemonSets and CronJobs.
func (k *kubeReader) load(ctx context.Context) error {
	k.hpas = map[hubapi.WorkloadKey]*recommender.HPA{}
	k.jvm = map[hubapi.WorkloadKey][]hubapi.JVMContainer{}
	namespaces := k.namespaces
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	for _, ns := range namespaces {
		if err := k.loadNamespace(ctx, ns); err != nil {
			return err
		}
	}
	return nil
}

func (k *kubeReader) loadNamespace(ctx context.Context, ns string) error {
	hpas, err := k.client.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list HPAs: %w", err)
	}
	for _, h := range hpas.Items {
		key := hubapi.WorkloadKey{Cluster: k.cluster, Namespace: h.Namespace, Kind: h.Spec.ScaleTargetRef.Kind, Name: h.Spec.ScaleTargetRef.Name}
		k.hpas[key] = hpaFromKube(h)
	}
	add := func(kind, namespace, name string, spec *corev1.PodSpec) {
		key := hubapi.WorkloadKey{Cluster: k.cluster, Namespace: namespace, Kind: kind, Name: name}
		k.jvm[key] = jvmContainers(spec)
	}
	deps, err := k.client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Deployments: %w", err)
	}
	for _, w := range deps.Items {
		add(hubapi.KindDeployment, w.Namespace, w.Name, &w.Spec.Template.Spec)
	}
	sts, err := k.client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list StatefulSets: %w", err)
	}
	for _, w := range sts.Items {
		add(hubapi.KindStatefulSet, w.Namespace, w.Name, &w.Spec.Template.Spec)
	}
	dss, err := k.client.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list DaemonSets: %w", err)
	}
	for _, w := range dss.Items {
		add(hubapi.KindDaemonSet, w.Namespace, w.Name, &w.Spec.Template.Spec)
	}
	cjs, err := k.client.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list CronJobs: %w", err)
	}
	for _, w := range cjs.Items {
		add(hubapi.KindCronJob, w.Namespace, w.Name, &w.Spec.JobTemplate.Spec.Template.Spec)
	}
	return nil
}

// jvmContainers returns the JVM containers of a pod spec the way the agent reports them. An empty
// result still means "reported": the workload was read and has no JVM.
func jvmContainers(spec *corev1.PodSpec) []hubapi.JVMContainer {
	var out []hubapi.JVMContainer
	for _, r := range workload.JVMReport(workload.Containers(spec), true) {
		out = append(out, hubapi.JVMContainer{
			Name: r.Name, DetectedBy: r.DetectedBy, MaxHeap: r.MaxHeap, InitialHeap: r.InitialHeap,
			MaxRAM: r.MaxRAM, MaxRAMPercentage: r.MaxRAMPercentage, Unresolved: r.Unresolved,
		})
	}
	return out
}

// hpaFromKube maps an HPA to the recommender's form, with metric names as kube-state-metrics
// reports them ("<metric_name>/<metric_target_type>").
func hpaFromKube(h autoscalingv2.HorizontalPodAutoscaler) *recommender.HPA {
	out := &recommender.HPA{Name: h.Name, Targets: map[string]float64{}, MaxReplicas: float64(h.Spec.MaxReplicas)}
	if h.Spec.MinReplicas != nil {
		out.MinReplicas = float64(*h.Spec.MinReplicas)
	}
	out.PeakReplicas = float64(max(h.Status.CurrentReplicas, h.Status.DesiredReplicas))
	for _, m := range h.Spec.Metrics {
		var name string
		var t autoscalingv2.MetricTarget
		switch m.Type {
		case autoscalingv2.ResourceMetricSourceType:
			if m.Resource == nil {
				continue
			}
			name, t = string(m.Resource.Name), m.Resource.Target
		case autoscalingv2.ContainerResourceMetricSourceType:
			if m.ContainerResource == nil {
				continue
			}
			// A container metric is not workload-wide utilization: kept apart from cpu/utilization.
			name, t = "container:"+string(m.ContainerResource.Name), m.ContainerResource.Target
		case autoscalingv2.PodsMetricSourceType:
			if m.Pods == nil {
				continue
			}
			name, t = m.Pods.Metric.Name, m.Pods.Target
		case autoscalingv2.ObjectMetricSourceType:
			if m.Object == nil {
				continue
			}
			name, t = m.Object.Metric.Name, m.Object.Target
		case autoscalingv2.ExternalMetricSourceType:
			if m.External == nil {
				continue
			}
			name, t = m.External.Metric.Name, m.External.Target
		default:
			continue
		}
		typ := strings.ToLower(string(t.Type))
		out.Metrics = append(out.Metrics, name+"/"+typ)
		if t.Type == autoscalingv2.UtilizationMetricType && t.AverageUtilization != nil && m.Type == autoscalingv2.ResourceMetricSourceType {
			out.Targets[name] = float64(*t.AverageUtilization)
		}
	}
	return out
}

// loader wraps a recommender loader: HPAs read from the cluster replace what the metrics say.
type kubeLoader struct {
	inner recommender.Loader
	kube  *kubeReader
}

func (l kubeLoader) Load(ctx context.Context, cluster string, end time.Time, window time.Duration) (*recommender.ClusterData, error) {
	d, err := l.inner.Load(ctx, cluster, end, window)
	if err != nil {
		return nil, err
	}
	for key, h := range l.kube.hpas {
		w := d.Workloads[key]
		if w == nil {
			continue
		}
		if w.HPA != nil {
			h.PeakReplicas = max(h.PeakReplicas, w.HPA.PeakReplicas)
		}
		w.HPA = h
	}
	return d, nil
}

// jvmFor is recommender.Engine.JVM: the JVM containers of workloads read from the cluster.
func (k *kubeReader) jvmFor(key hubapi.WorkloadKey) ([]hubapi.JVMContainer, bool) {
	c, ok := k.jvm[key]
	return c, ok
}
