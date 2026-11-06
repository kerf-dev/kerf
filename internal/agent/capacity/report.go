// Package capacity builds the agent's read-only capacity report: nodes, node pools (Karpenter
// NodePools, Cluster Autoscaler node groups, node pool labels), pod requests grouped by owner and
// shape, and what may block consolidation (PodDisruptionBudgets at zero, do-not-disrupt and
// safe-to-evict annotations, local storage). The hub turns it into the node estimate
// (docs/node-savings.md). Nothing here writes to the cluster.
package capacity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// Well-known labels and annotations.
const (
	LabelKarpenterNodePool = "karpenter.sh/nodepool"
	LabelInstanceType      = "node.kubernetes.io/instance-type"
	LabelInstanceTypeBeta  = "beta.kubernetes.io/instance-type"
	LabelCapacityType      = "karpenter.sh/capacity-type"
	LabelEKSCapacityType   = "eks.amazonaws.com/capacityType"
	LabelZone              = "topology.kubernetes.io/zone"

	AnnotationDoNotDisrupt        = "karpenter.sh/do-not-disrupt"
	AnnotationSafeToEvict         = "cluster-autoscaler.kubernetes.io/safe-to-evict"
	AnnotationSafeToEvictLocalVol = "cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"
	AnnotationScaleDownDisabled   = "cluster-autoscaler.kubernetes.io/scale-down-disabled"
	AnnotationMirrorPod           = "kubernetes.io/config.mirror"
)

// DefaultPoolLabels are the node labels that name a node's pool when it is not a Karpenter node,
// in order: EKS managed node groups, GKE, AKS, kOps, the Cluster Autoscaler KWOK provider.
var DefaultPoolLabels = []string{
	"eks.amazonaws.com/nodegroup",
	"cloud.google.com/gke-nodepool",
	"kubernetes.azure.com/agentpool",
	"agentpool",
	"kops.k8s.io/instancegroup",
	"kwok-nodegroup",
}

// Limits of the report lists; Truncated is set when one is hit.
const (
	MaxNodes     = 10000
	MaxPodGroups = 50000
	MaxPDBs      = 500
	MaxPods      = 500
)

// Inputs are the objects one report is built from.
type Inputs struct {
	Now        time.Time
	Nodes      []corev1.Node
	Pods       []corev1.Pod
	PDBs       []policyv1.PodDisruptionBudget
	NodePools  []NodePool  // Karpenter NodePools; nil = Karpenter not installed (or not readable)
	NodeClaims []NodeClaim // Karpenter NodeClaims
	CA         *CAStatus   // Cluster Autoscaler status; nil = not found
	// PoolLabels are tried in order for nodes without karpenter.sh/nodepool (DefaultPoolLabels
	// when empty).
	PoolLabels []string
}

// Build builds the report. It never fails: what is missing is left out.
func Build(in Inputs) *hubclient.CapacityReport {
	rep := &hubclient.CapacityReport{
		ObservedAt: in.Now.UTC().Truncate(time.Second),
		NodePools:  []hubclient.CapacityNodePool{},
		Nodes:      []hubclient.CapacityNode{},
		PodGroups:  []hubclient.CapacityPodGroup{},
	}
	poolLabels := in.PoolLabels
	if len(poolLabels) == 0 {
		poolLabels = DefaultPoolLabels
	}

	// Autoscalers.
	if in.NodePools != nil {
		rep.Autoscalers = append(rep.Autoscalers, hubclient.CapacityAutoscaler{Kind: hubclient.AutoscalerKarpenter})
	}
	if in.CA != nil {
		a := hubclient.CapacityAutoscaler{Kind: hubclient.AutoscalerCA, Object: &in.CA.Object, Status: in.CA.Health, ScaleDown: in.CA.ScaleDown}
		if !in.CA.LastProbeTime.IsZero() {
			t := in.CA.LastProbeTime.UTC()
			a.LastProbeTime = &t
		}
		rep.Autoscalers = append(rep.Autoscalers, a)
	}

	// Node pools: Karpenter NodePools, then Cluster Autoscaler node groups.
	pools := map[string]*hubclient.CapacityNodePool{}
	addPool := func(p hubclient.CapacityNodePool) {
		if _, ok := pools[p.Name]; ok {
			return
		}
		pools[p.Name] = &p
	}
	for _, np := range in.NodePools {
		addPool(np.Report())
	}
	var caGroups []string
	if in.CA != nil {
		for _, g := range in.CA.NodeGroups {
			caGroups = append(caGroups, g.Name)
			addPool(hubclient.CapacityNodePool{Name: g.Name, Autoscaler: hubclient.AutoscalerCA, MinNodes: g.MinSize, MaxNodes: g.MaxSize})
		}
	}

	// Nodes.
	claimPool := map[string]string{}
	for _, c := range in.NodeClaims {
		if c.NodeName != "" && c.NodePool != "" {
			claimPool[c.NodeName] = c.NodePool
		}
	}
	nodePool := map[string]string{}
	nodes := slices.Clone(in.Nodes)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	for _, n := range nodes {
		pool, kind := poolOf(n, claimPool, poolLabels, caGroups)
		nodePool[n.Name] = pool
		if pool != "" {
			addPool(hubclient.CapacityNodePool{Name: pool, Autoscaler: kind})
		}
		if len(rep.Nodes) >= MaxNodes {
			rep.Truncated = true
			continue
		}
		rep.Nodes = append(rep.Nodes, hubclient.CapacityNode{
			Name:              n.Name,
			NodePool:          pool,
			InstanceType:      firstLabel(n.Labels, LabelInstanceType, LabelInstanceTypeBeta),
			CapacityType:      capacityType(n.Labels),
			Zone:              n.Labels[LabelZone],
			Allocatable:       values(n.Status.Allocatable),
			Unschedulable:     n.Spec.Unschedulable,
			DoNotDisrupt:      n.Annotations[AnnotationDoNotDisrupt] == "true",
			ScaleDownDisabled: n.Annotations[AnnotationScaleDownDisabled] == "true",
		})
	}
	names := make([]string, 0, len(pools))
	for n := range pools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rep.NodePools = append(rep.NodePools, *pools[n])
	}

	// Pods: groups for the estimate, blocking pods for the blockers.
	type group struct {
		g   hubclient.CapacityPodGroup
		key string
	}
	groups := map[string]*group{}
	var order []string
	var blocking []hubclient.CapacityPod
	running := make([]corev1.Pod, 0, len(in.Pods))
	for _, p := range in.Pods {
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		running = append(running, p)
	}
	sort.Slice(running, func(i, j int) bool {
		if running[i].Namespace != running[j].Namespace {
			return running[i].Namespace < running[j].Namespace
		}
		return running[i].Name < running[j].Name
	})
	for i := range running {
		p := &running[i]
		owner, wl := OwnerOf(p)
		ds := isNodeLocal(p)
		pool := nodePool[p.Spec.NodeName]
		shape := podShape(&p.Spec)
		key := fmt.Sprintf("%v|%s|%t|%s", owner, pool, ds, shape.key)
		if g, ok := groups[key]; ok {
			g.g.Count++
		} else {
			groups[key] = &group{key: key, g: hubclient.CapacityPodGroup{
				Owner: owner, Workload: wl, NodePool: pool, DaemonSet: ds, Count: 1,
				Containers: shape.containers, InitRequests: shape.init, Overhead: shape.overhead,
			}}
			order = append(order, key)
		}
		if reasons := BlockingReasons(p); len(reasons) > 0 {
			blocking = append(blocking, hubclient.CapacityPod{
				Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName, NodePool: pool, Workload: wl, Reasons: reasons,
			})
		}
	}
	for _, k := range order {
		if len(rep.PodGroups) >= MaxPodGroups {
			rep.Truncated = true
			break
		}
		rep.PodGroups = append(rep.PodGroups, groups[k].g)
	}
	if len(blocking) > MaxPods {
		blocking, rep.Truncated = blocking[:MaxPods], true
	}
	rep.Pods = blocking

	// PodDisruptionBudgets that allow no disruption of at least one pod.
	pdbs := slices.Clone(in.PDBs)
	sort.Slice(pdbs, func(i, j int) bool {
		if pdbs[i].Namespace != pdbs[j].Namespace {
			return pdbs[i].Namespace < pdbs[j].Namespace
		}
		return pdbs[i].Name < pdbs[j].Name
	})
	for _, b := range pdbs {
		if b.Status.DisruptionsAllowed > 0 || b.Status.ExpectedPods == 0 {
			continue
		}
		if len(rep.PDBs) >= MaxPDBs {
			rep.Truncated = true
			break
		}
		rep.PDBs = append(rep.PDBs, hubclient.CapacityPDB{
			Namespace: b.Namespace, Name: b.Name,
			DisruptionsAllowed: int(b.Status.DisruptionsAllowed),
			CurrentHealthy:     int(b.Status.CurrentHealthy),
			DesiredHealthy:     int(b.Status.DesiredHealthy),
			ExpectedPods:       int(b.Status.ExpectedPods),
			Workload:           pdbWorkload(&b, running),
		})
	}
	return rep
}

// poolOf returns a node's pool and its autoscaler.
func poolOf(n corev1.Node, claimPool map[string]string, poolLabels, caGroups []string) (string, string) {
	if p := n.Labels[LabelKarpenterNodePool]; p != "" {
		return p, hubclient.AutoscalerKarpenter
	}
	if p := claimPool[n.Name]; p != "" {
		return p, hubclient.AutoscalerKarpenter
	}
	for _, l := range poolLabels {
		v := n.Labels[l]
		if v == "" {
			continue
		}
		if g := matchCAGroup(v, caGroups); g != "" {
			return g, hubclient.AutoscalerCA
		}
		return v, hubclient.AutoscalerNone
	}
	return "", ""
}

// matchCAGroup finds the Cluster Autoscaler node group of a node pool label value: the same
// name, or the EKS Auto Scaling group name eks-<nodegroup>-<id>.
func matchCAGroup(v string, groups []string) string {
	for _, g := range groups {
		if g == v {
			return g
		}
	}
	for _, g := range groups {
		if strings.HasPrefix(g, "eks-"+v+"-") {
			return g
		}
	}
	return ""
}

func firstLabel(l map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := l[k]; v != "" {
			return v
		}
	}
	return ""
}

func capacityType(l map[string]string) string {
	if v := l[LabelCapacityType]; v != "" {
		return v
	}
	switch strings.ToUpper(l[LabelEKSCapacityType]) {
	case "SPOT":
		return "spot"
	case "ON_DEMAND":
		return "on-demand"
	}
	return ""
}

// values renders CPU in millicores and memory in bytes (both match the Quantity pattern).
func values(l corev1.ResourceList) hubclient.ResourceValues {
	var v hubclient.ResourceValues
	if q, ok := l[corev1.ResourceCPU]; ok {
		v.CPU = fmt.Sprintf("%dm", q.MilliValue())
	}
	if q, ok := l[corev1.ResourceMemory]; ok {
		v.Memory = fmt.Sprintf("%d", q.Value())
	}
	return v
}

func valuesPtr(l corev1.ResourceList) *hubclient.ResourceValues {
	v := values(l)
	if v.IsZero() {
		return nil
	}
	return &v
}

type shape struct {
	containers []hubclient.ContainerResources
	init       *hubclient.ResourceValues
	overhead   *hubclient.ResourceValues
	key        string
}

// podShape returns the requests of a pod: regular containers and sidecars (restartable init
// containers), the init floor (the largest non-sidecar init container plus the sidecars started
// before it) and the pod overhead.
func podShape(spec *corev1.PodSpec) shape {
	var s shape
	sidecars := corev1.ResourceList{}
	floor := corev1.ResourceList{}
	for _, c := range spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			s.containers = append(s.containers, hubclient.ContainerResources{Name: c.Name, Requests: valuesPtr(c.Resources.Requests)})
			addTo(sidecars, c.Resources.Requests)
			continue
		}
		need := sidecars.DeepCopy()
		addTo(need, c.Resources.Requests)
		for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if q, ok := need[r]; ok {
				if cur, ok := floor[r]; !ok || q.Cmp(cur) > 0 {
					floor[r] = q
				}
			}
		}
	}
	for _, c := range spec.Containers {
		s.containers = append(s.containers, hubclient.ContainerResources{Name: c.Name, Requests: valuesPtr(c.Resources.Requests)})
	}
	if s.containers == nil {
		s.containers = []hubclient.ContainerResources{}
	}
	s.init = valuesPtr(floor)
	s.overhead = valuesPtr(spec.Overhead)
	b, _ := json.Marshal([]any{s.containers, s.init, s.overhead})
	s.key = string(b)
	return s
}

func addTo(dst, src corev1.ResourceList) {
	for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		q, ok := src[r]
		if !ok {
			continue
		}
		cur := dst[r]
		cur.Add(q)
		dst[r] = cur
	}
}

// isNodeLocal reports daemonset and static (mirror) pods: per-node overhead that moves with the
// node, not with the bin packing.
func isNodeLocal(p *corev1.Pod) bool {
	if _, ok := p.Annotations[AnnotationMirrorPod]; ok {
		return true
	}
	if c := metav1.GetControllerOf(p); c != nil && c.Kind == "DaemonSet" {
		return true
	}
	return false
}

var cronJobSuffix = regexp.MustCompile(`^(.+)-[0-9]{8,}$`)

// OwnerOf returns the top-level owner of a pod and the Kerf workload when the owner is one
// (Deployment through its ReplicaSet's pod-template-hash, StatefulSet, DaemonSet, CronJob
// through its Job's name). Bare pods have no owner.
func OwnerOf(p *corev1.Pod) (*hubclient.CapacityObjectRef, *hubclient.WorkloadRef) {
	c := metav1.GetControllerOf(p)
	if c == nil {
		return nil, nil
	}
	ref := func(apiVersion, kind, name string) *hubclient.CapacityObjectRef {
		return &hubclient.CapacityObjectRef{APIVersion: apiVersion, Kind: kind, Namespace: p.Namespace, Name: name}
	}
	wl := func(kind, name string) *hubclient.WorkloadRef {
		return &hubclient.WorkloadRef{Namespace: p.Namespace, Kind: kind, Name: name}
	}
	switch c.Kind {
	case "ReplicaSet":
		if h := p.Labels["pod-template-hash"]; h != "" && strings.HasSuffix(c.Name, "-"+h) {
			d := strings.TrimSuffix(c.Name, "-"+h)
			return ref("apps/v1", "Deployment", d), wl("Deployment", d)
		}
		return ref(c.APIVersion, c.Kind, c.Name), nil
	case "StatefulSet", "DaemonSet":
		return ref(c.APIVersion, c.Kind, c.Name), wl(c.Kind, c.Name)
	case "Job":
		if m := cronJobSuffix.FindStringSubmatch(c.Name); m != nil {
			return ref("batch/v1", "CronJob", m[1]), wl("CronJob", m[1])
		}
		return ref(c.APIVersion, c.Kind, c.Name), nil
	}
	return ref(c.APIVersion, c.Kind, c.Name), nil
}

// BlockingReasons returns why a pod blocks the removal of its node: karpenter.sh/do-not-disrupt,
// cluster-autoscaler.kubernetes.io/safe-to-evict=false, or local storage (emptyDir on disk or
// hostPath, which the Cluster Autoscaler does not evict unless the pod is marked safe to evict).
// Daemonset and static pods never block.
func BlockingReasons(p *corev1.Pod) []string {
	if isNodeLocal(p) {
		return nil
	}
	var out []string
	if p.Annotations[AnnotationDoNotDisrupt] == "true" {
		out = append(out, hubclient.PodReasonDoNotDisrupt)
	}
	safe := p.Annotations[AnnotationSafeToEvict]
	if safe == "false" {
		out = append(out, hubclient.PodReasonSafeToEvictFalse)
	}
	if safe != "true" && hasLocalStorage(p) {
		out = append(out, hubclient.PodReasonLocalStorage)
	}
	return out
}

func hasLocalStorage(p *corev1.Pod) bool {
	safe := map[string]bool{}
	for _, v := range strings.Split(p.Annotations[AnnotationSafeToEvictLocalVol], ",") {
		if v = strings.TrimSpace(v); v != "" {
			safe[v] = true
		}
	}
	for _, v := range p.Spec.Volumes {
		local := v.HostPath != nil || (v.EmptyDir != nil && v.EmptyDir.Medium != corev1.StorageMediumMemory)
		if local && !safe[v.Name] {
			return true
		}
	}
	return false
}

// pdbWorkload returns the workload of the first pod the budget selects.
func pdbWorkload(b *policyv1.PodDisruptionBudget, pods []corev1.Pod) *hubclient.WorkloadRef {
	if b.Spec.Selector == nil {
		return nil
	}
	sel, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
	if err != nil || sel.Empty() {
		return nil
	}
	for i := range pods {
		p := &pods[i]
		if p.Namespace == b.Namespace && sel.Matches(labels.Set(p.Labels)) {
			if _, wl := OwnerOf(p); wl != nil {
				return wl
			}
		}
	}
	return nil
}
