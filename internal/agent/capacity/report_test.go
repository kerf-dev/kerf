package capacity

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

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

func ctrlRef(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: ptr.To(true)}}
}

func pod(ns, name, node string, mod func(*corev1.Pod)) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{}, Annotations: map[string]string{}},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{
			Name: "app", Resources: corev1.ResourceRequirements{Requests: rl("500m", "1Gi")},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if mod != nil {
		mod(&p)
	}
	return p
}

func TestOwnerOf(t *testing.T) {
	tests := []struct {
		name      string
		mod       func(*corev1.Pod)
		wantOwner *hubclient.CapacityObjectRef
		wantWL    *hubclient.WorkloadRef
	}{
		{"bare pod", nil, nil, nil},
		{"deployment through its replicaset", func(p *corev1.Pod) {
			p.OwnerReferences = ctrlRef("ReplicaSet", "web-7d9f8b")
			p.Labels["pod-template-hash"] = "7d9f8b"
		}, &hubclient.CapacityObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "ns", Name: "web"},
			&hubclient.WorkloadRef{Namespace: "ns", Kind: "Deployment", Name: "web"}},
		{"replicaset without the hash label", func(p *corev1.Pod) { p.OwnerReferences = ctrlRef("ReplicaSet", "rs") },
			&hubclient.CapacityObjectRef{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: "ns", Name: "rs"}, nil},
		{"statefulset", func(p *corev1.Pod) { p.OwnerReferences = ctrlRef("StatefulSet", "db") },
			&hubclient.CapacityObjectRef{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "ns", Name: "db"},
			&hubclient.WorkloadRef{Namespace: "ns", Kind: "StatefulSet", Name: "db"}},
		{"cronjob through its job", func(p *corev1.Pod) { p.OwnerReferences = ctrlRef("Job", "report-29123456") },
			&hubclient.CapacityObjectRef{APIVersion: "batch/v1", Kind: "CronJob", Namespace: "ns", Name: "report"},
			&hubclient.WorkloadRef{Namespace: "ns", Kind: "CronJob", Name: "report"}},
		{"plain job", func(p *corev1.Pod) { p.OwnerReferences = ctrlRef("Job", "migrate") },
			&hubclient.CapacityObjectRef{APIVersion: "apps/v1", Kind: "Job", Namespace: "ns", Name: "migrate"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := pod("ns", "p", "n1", tt.mod)
			o, w := OwnerOf(&p)
			if d := diff(tt.wantOwner, o); d != "" {
				t.Errorf("owner (-want +got):\n%s", d)
			}
			if d := diff(tt.wantWL, w); d != "" {
				t.Errorf("workload (-want +got):\n%s", d)
			}
		})
	}
}

func TestBlockingReasons(t *testing.T) {
	emptyDir := func(medium corev1.StorageMedium) corev1.Volume {
		return corev1.Volume{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium}}}
	}
	tests := []struct {
		name string
		mod  func(*corev1.Pod)
		want []string
	}{
		{"nothing", nil, nil},
		{"do-not-disrupt", func(p *corev1.Pod) { p.Annotations[AnnotationDoNotDisrupt] = "true" }, []string{"do-not-disrupt"}},
		{"do-not-disrupt false", func(p *corev1.Pod) { p.Annotations[AnnotationDoNotDisrupt] = "false" }, nil},
		{"safe-to-evict false", func(p *corev1.Pod) { p.Annotations[AnnotationSafeToEvict] = "false" }, []string{"safe-to-evict-false"}},
		{"emptyDir on disk", func(p *corev1.Pod) { p.Spec.Volumes = []corev1.Volume{emptyDir("")} }, []string{"local-storage"}},
		{"emptyDir in memory", func(p *corev1.Pod) { p.Spec.Volumes = []corev1.Volume{emptyDir(corev1.StorageMediumMemory)} }, nil},
		{"hostPath", func(p *corev1.Pod) {
			p.Spec.Volumes = []corev1.Volume{{Name: "h", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/x"}}}}
		}, []string{"local-storage"}},
		{"local storage marked safe to evict", func(p *corev1.Pod) {
			p.Spec.Volumes = []corev1.Volume{emptyDir("")}
			p.Annotations[AnnotationSafeToEvict] = "true"
		}, nil},
		{"local volume listed as safe", func(p *corev1.Pod) {
			p.Spec.Volumes = []corev1.Volume{emptyDir("")}
			p.Annotations[AnnotationSafeToEvictLocalVol] = "other, scratch"
		}, nil},
		{"daemonset pods never block", func(p *corev1.Pod) {
			p.OwnerReferences = ctrlRef("DaemonSet", "agent")
			p.Annotations[AnnotationDoNotDisrupt] = "true"
			p.Spec.Volumes = []corev1.Volume{emptyDir("")}
		}, nil},
		{"several", func(p *corev1.Pod) {
			p.Annotations[AnnotationDoNotDisrupt] = "true"
			p.Annotations[AnnotationSafeToEvict] = "false"
			p.Spec.Volumes = []corev1.Volume{emptyDir("")}
		}, []string{"do-not-disrupt", "safe-to-evict-false", "local-storage"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := pod("ns", "p", "n1", tt.mod)
			if d := diff(tt.want, BlockingReasons(&p)); d != "" {
				t.Errorf("(-want +got):\n%s", d)
			}
		})
	}
}

func TestPodShape(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	tests := []struct {
		name           string
		spec           corev1.PodSpec
		wantContainers []hubclient.ContainerResources
		wantInit       *hubclient.ResourceValues
		wantOverhead   *hubclient.ResourceValues
	}{
		{"containers only", corev1.PodSpec{Containers: []corev1.Container{
			{Name: "a", Resources: corev1.ResourceRequirements{Requests: rl("250m", "512Mi")}},
			{Name: "b"},
		}}, []hubclient.ContainerResources{
			{Name: "a", Requests: &hubclient.ResourceValues{CPU: "250m", Memory: "536870912"}},
			{Name: "b"},
		}, nil, nil},
		{"init floor includes the sidecars started before it", corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "proxy", RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: rl("100m", "")}},
				{Name: "migrate", Resources: corev1.ResourceRequirements{Requests: rl("2", "1Gi")}},
			},
			Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: rl("1", "")}}},
			Overhead:   rl("50m", ""),
		}, []hubclient.ContainerResources{
			{Name: "proxy", Requests: &hubclient.ResourceValues{CPU: "100m"}},
			{Name: "app", Requests: &hubclient.ResourceValues{CPU: "1000m"}},
		}, &hubclient.ResourceValues{CPU: "2100m", Memory: "1073741824"}, &hubclient.ResourceValues{CPU: "50m"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := podShape(&tt.spec)
			if d := diff(tt.wantContainers, s.containers); d != "" {
				t.Errorf("containers (-want +got):\n%s", d)
			}
			if d := diff(tt.wantInit, s.init); d != "" {
				t.Errorf("init (-want +got):\n%s", d)
			}
			if d := diff(tt.wantOverhead, s.overhead); d != "" {
				t.Errorf("overhead (-want +got):\n%s", d)
			}
		})
	}
}

const nodePoolYAML = `
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: general
spec:
  disruption:
    consolidationPolicy: WhenEmptyOrUnderutilized
    consolidateAfter: 1m
    budgets:
      - nodes: "10%"
      - nodes: "0"
        schedule: "0 9 * * mon-fri"
        duration: 8h
        reasons: [Underutilized]
  template:
    spec:
      requirements:
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m5.large, m5.xlarge]
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: kubernetes.io/arch
          operator: In
          values: [amd64]
`

func TestParseNodePool(t *testing.T) {
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(nodePoolYAML), &u.Object); err != nil {
		t.Fatal(err)
	}
	got := ParseNodePool(u)
	want := NodePool{
		Name: "general", ConsolidationPolicy: "WhenEmptyOrUnderutilized", ConsolidateAfter: "1m",
		Budgets: []hubclient.CapacityDisruptionBudget{
			{Nodes: "10%"},
			{Nodes: "0", Schedule: "0 9 * * mon-fri", Duration: "8h", Reasons: []string{"Underutilized"}},
		},
		InstanceTypes: []string{"m5.large", "m5.xlarge"},
		CapacityTypes: []string{"on-demand"},
	}
	if d := diff(want, got); d != "" {
		t.Errorf("(-want +got):\n%s", d)
	}
}

func TestParseCAStatus(t *testing.T) {
	yamlStatus := `time: 2026-11-06 10:00:00.123 +0000 UTC
autoscalerStatus: Running
clusterWide:
  health:
    status: Healthy
    nodeCounts:
      registered: {total: 5, ready: 5}
    lastProbeTime: "2026-11-06T10:00:00Z"
  scaleUp: {status: NoActivity}
  scaleDown: {status: CandidatesPresent, candidates: 1}
nodeGroups:
- name: ng-small
  health: {status: Healthy, cloudProviderTarget: 3, minSize: 1, maxSize: 6}
  scaleDown: {status: CandidatesPresent, candidates: 1}
- name: ng-large
  health: {status: Healthy, cloudProviderTarget: 2, minSize: 2, maxSize: 4}
  scaleDown: {status: NoCandidates}
`
	textStatus := `Cluster-autoscaler status at 2026-11-06 10:00:00.123 +0000 UTC:
Cluster-wide:
  Health:      Healthy (ready=5 unready=0 notStarted=0 longNotStarted=0 registered=5 longUnregistered=0)
               LastProbeTime:      2026-11-06 10:00:00 +0000 UTC m=+120.5
               LastTransitionTime: 2026-11-06 09:00:00 +0000 UTC m=+10.1
  ScaleUp:     NoActivity (ready=5 registered=5)
  ScaleDown:   CandidatesPresent (candidates=1)

NodeGroups:
  Name:        ng-small
  Health:      Healthy (ready=3 unready=0 notStarted=0 longNotStarted=0 registered=3 longUnregistered=0 cloudProviderTarget=3 (minSize=1, maxSize=6))
  ScaleUp:     NoActivity (ready=3 cloudProviderTarget=3)
  ScaleDown:   CandidatesPresent (candidates=1)

  Name:        ng-large
  Health:      Healthy (ready=2 unready=0 notStarted=0 longNotStarted=0 registered=2 longUnregistered=0 cloudProviderTarget=2 (minSize=2, maxSize=4))
  ScaleDown:   NoCandidates (candidates=0)
`
	want := CAStatus{
		Health: "Healthy", ScaleDown: "CandidatesPresent", LastProbeTime: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		NodeGroups: []CANodeGroup{
			{Name: "ng-small", Health: "Healthy", MinSize: ptr.To(1), MaxSize: ptr.To(6), Target: ptr.To(3), ScaleDown: "CandidatesPresent"},
			{Name: "ng-large", Health: "Healthy", MinSize: ptr.To(2), MaxSize: ptr.To(4), Target: ptr.To(2), ScaleDown: "NoCandidates"},
		},
	}
	tests := []struct {
		name, in string
		ok       bool
	}{{"yaml", yamlStatus, true}, {"text", textStatus, true}, {"garbage", "hello", false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseCAStatus(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			got.LastProbeTime = got.LastProbeTime.UTC()
			if d := diff(want, got); d != "" {
				t.Errorf("(-want +got):\n%s", d)
			}
		})
	}
}

func node(name string, lbl map[string]string, ann map[string]string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbl, Annotations: ann},
		Status:     corev1.NodeStatus{Allocatable: rl("3920m", "15Gi")},
	}
}

func TestBuild(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	deploy := func(name, hash string) func(*corev1.Pod) {
		return func(p *corev1.Pod) {
			p.OwnerReferences = ctrlRef("ReplicaSet", name+"-"+hash)
			p.Labels["pod-template-hash"] = hash
			p.Labels["app"] = name
		}
	}
	in := Inputs{
		Now: now,
		Nodes: []corev1.Node{
			node("k2", map[string]string{LabelKarpenterNodePool: "general", LabelInstanceType: "m5.xlarge", LabelCapacityType: "spot"},
				map[string]string{AnnotationDoNotDisrupt: "true"}),
			node("k1", map[string]string{LabelKarpenterNodePool: "general", LabelInstanceType: "m5.xlarge", LabelZone: "eu-west-1a"}, nil),
			node("e1", map[string]string{"eks.amazonaws.com/nodegroup": "system", LabelInstanceType: "t3.large", LabelEKSCapacityType: "ON_DEMAND"},
				map[string]string{AnnotationScaleDownDisabled: "true"}),
			node("x1", map[string]string{"team": "a"}, nil),
		},
		Pods: []corev1.Pod{
			pod("shop", "web-1", "k1", deploy("web", "abc")),
			pod("shop", "web-2", "k2", deploy("web", "abc")),
			pod("shop", "web-3", "k2", deploy("web", "abc")),
			pod("shop", "cache-1", "k1", func(p *corev1.Pod) { deploy("cache", "def")(p); p.Annotations[AnnotationDoNotDisrupt] = "true" }),
			pod("kube-system", "fluent-1", "k1", func(p *corev1.Pod) { p.OwnerReferences = ctrlRef("DaemonSet", "fluent") }),
			pod("shop", "pending", "", nil),
			pod("shop", "done", "k1", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }),
		},
		PDBs: []policyv1.PodDisruptionBudget{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
				Spec:   policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}},
				Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0, CurrentHealthy: 3, DesiredHealthy: 3, ExpectedPods: 3}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "ok"}, Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 1, ExpectedPods: 3}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "empty"}, Status: policyv1.PodDisruptionBudgetStatus{ExpectedPods: 0}},
		},
		NodePools: []NodePool{{Name: "general", ConsolidationPolicy: "WhenEmpty"}, {Name: "idle"}},
		CA: &CAStatus{Object: hubclient.CapacityObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "kube-system", Name: "cluster-autoscaler-status"},
			Health: "Healthy", NodeGroups: []CANodeGroup{{Name: "eks-system-1a2b", MinSize: ptr.To(2)}}},
	}
	got := Build(in)

	if !got.ObservedAt.Equal(now) || got.Truncated {
		t.Errorf("observedAt %v truncated %v", got.ObservedAt, got.Truncated)
	}
	if len(got.Autoscalers) != 2 || got.Autoscalers[0].Kind != "karpenter" || got.Autoscalers[1].Kind != "cluster-autoscaler" || got.Autoscalers[1].Status != "Healthy" {
		t.Errorf("autoscalers: %+v", got.Autoscalers)
	}
	var poolNames []string
	for _, p := range got.NodePools {
		poolNames = append(poolNames, p.Name+"/"+p.Autoscaler)
	}
	if d := diff([]string{"eks-system-1a2b/cluster-autoscaler", "general/karpenter", "idle/karpenter"}, poolNames); d != "" {
		t.Errorf("pools (-want +got):\n%s", d)
	}
	wantNodes := []hubclient.CapacityNode{
		{Name: "e1", NodePool: "eks-system-1a2b", InstanceType: "t3.large", CapacityType: "on-demand", Allocatable: hubclient.ResourceValues{CPU: "3920m", Memory: "16106127360"}, ScaleDownDisabled: true},
		{Name: "k1", NodePool: "general", InstanceType: "m5.xlarge", Zone: "eu-west-1a", Allocatable: hubclient.ResourceValues{CPU: "3920m", Memory: "16106127360"}},
		{Name: "k2", NodePool: "general", InstanceType: "m5.xlarge", CapacityType: "spot", Allocatable: hubclient.ResourceValues{CPU: "3920m", Memory: "16106127360"}, DoNotDisrupt: true},
		{Name: "x1", Allocatable: hubclient.ResourceValues{CPU: "3920m", Memory: "16106127360"}},
	}
	if d := diff(wantNodes, got.Nodes); d != "" {
		t.Errorf("nodes (-want +got):\n%s", d)
	}
	type g struct {
		Owner, Pool string
		DS          bool
		Count       int
	}
	var groups []g
	for _, pg := range got.PodGroups {
		groups = append(groups, g{pg.Owner.Kind + "/" + pg.Owner.Name, pg.NodePool, pg.DaemonSet, pg.Count})
	}
	wantGroups := []g{{"DaemonSet/fluent", "general", true, 1}, {"Deployment/cache", "general", false, 1}, {"Deployment/web", "general", false, 3}}
	if d := diff(wantGroups, groups); d != "" {
		t.Errorf("groups (-want +got):\n%s", d)
	}
	wantPods := []hubclient.CapacityPod{{Namespace: "shop", Name: "cache-1", Node: "k1", NodePool: "general",
		Workload: &hubclient.WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "cache"}, Reasons: []string{"do-not-disrupt"}}}
	if d := diff(wantPods, got.Pods); d != "" {
		t.Errorf("pods (-want +got):\n%s", d)
	}
	wantPDBs := []hubclient.CapacityPDB{{Namespace: "shop", Name: "web", CurrentHealthy: 3, DesiredHealthy: 3, ExpectedPods: 3,
		Workload: &hubclient.WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "web"}}}
	if d := diff(wantPDBs, got.PDBs); d != "" {
		t.Errorf("pdbs (-want +got):\n%s", d)
	}
}

func TestBuildWithoutAutoscalers(t *testing.T) {
	got := Build(Inputs{Now: time.Now()})
	if got.Autoscalers != nil || len(got.NodePools) != 0 || got.Nodes == nil || got.PodGroups == nil {
		t.Errorf("got %+v", got)
	}
}

// diff returns "" when want and got are deeply equal, else both as JSON.
func diff(want, got any) string {
	if reflect.DeepEqual(want, got) {
		return ""
	}
	w, _ := json.Marshal(want)
	g, _ := json.Marshal(got)
	return "want " + string(w) + "\n got " + string(g)
}

func TestFitSize(t *testing.T) {
	rep := &hubclient.CapacityReport{Nodes: []hubclient.CapacityNode{{Name: "n1"}}}
	for i := range 2000 {
		rep.PodGroups = append(rep.PodGroups, hubclient.CapacityPodGroup{NodePool: "general", Count: i + 1,
			Containers: []hubclient.ContainerResources{{Name: "app", Requests: &hubclient.ResourceValues{CPU: "100m", Memory: "134217728"}}}})
	}
	if FitSize(rep, 1<<20) || rep.Truncated {
		t.Fatal("trimmed a small report")
	}
	if !FitSize(rep, 20000) || !rep.Truncated || len(rep.PodGroups) == 0 || len(rep.PodGroups) >= 2000 {
		t.Fatalf("not trimmed: %d groups, truncated %v", len(rep.PodGroups), rep.Truncated)
	}
	if b, _ := json.Marshal(rep); len(b) > 20000 {
		t.Errorf("still %d bytes", len(b))
	}
}
