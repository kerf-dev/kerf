package capacity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// DefaultInterval is how often the report is built.
const DefaultInterval = 5 * time.Minute

// DefaultCAStatus is the Cluster Autoscaler status ConfigMap.
var DefaultCAStatus = types.NamespacedName{Namespace: "kube-system", Name: "cluster-autoscaler-status"}

// Collector reads the cluster. Every read is a get or list.
type Collector struct {
	// Pods is the reader for pods (the manager's cache).
	Pods client.Reader
	// Reader reads nodes, PodDisruptionBudgets, Karpenter objects and the status ConfigMap.
	Reader     client.Reader
	PoolLabels []string
	CAStatus   types.NamespacedName
	Now        func() time.Time
	Log        *slog.Logger

	warned sync.Map // what was already logged as unreadable
}

// Collect builds a report. Missing Karpenter CRDs or a missing status ConfigMap are not errors;
// failing to read nodes or pods is.
func (c *Collector) Collect(ctx context.Context) (*hubclient.CapacityReport, error) {
	in := Inputs{Now: time.Now(), PoolLabels: c.PoolLabels}
	if c.Now != nil {
		in.Now = c.Now()
	}
	var nodes corev1.NodeList
	if err := c.Reader.List(ctx, &nodes); err != nil {
		return nil, err
	}
	in.Nodes = nodes.Items
	var pods corev1.PodList
	if err := c.Pods.List(ctx, &pods); err != nil {
		return nil, err
	}
	in.Pods = pods.Items
	var pdbs policyv1.PodDisruptionBudgetList
	if err := c.Reader.List(ctx, &pdbs); err != nil {
		c.warnOnce(ctx, "pdbs", err)
	} else {
		in.PDBs = pdbs.Items
	}

	np := &unstructured.UnstructuredList{}
	np.SetGroupVersionKind(NodePoolListGVK)
	switch err := c.Reader.List(ctx, np); {
	case err == nil:
		in.NodePools = []NodePool{}
		for i := range np.Items {
			in.NodePools = append(in.NodePools, ParseNodePool(&np.Items[i]))
		}
		nc := &unstructured.UnstructuredList{}
		nc.SetGroupVersionKind(NodeClaimListGVK)
		if err := c.Reader.List(ctx, nc); err != nil {
			c.warnOnce(ctx, "nodeclaims", err)
		}
		for i := range nc.Items {
			in.NodeClaims = append(in.NodeClaims, ParseNodeClaim(&nc.Items[i]))
		}
	case absent(err):
	default:
		c.warnOnce(ctx, "nodepools", err)
	}

	if c.CAStatus.Name != "" {
		var cm corev1.ConfigMap
		switch err := c.Reader.Get(ctx, c.CAStatus, &cm); {
		case err == nil:
			if st, ok := ParseCAStatus(cm.Data["status"]); ok {
				st.Object = hubclient.CapacityObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: cm.Namespace, Name: cm.Name}
				in.CA = &st
			}
		case absent(err):
		default:
			c.warnOnce(ctx, "cluster-autoscaler-status", err)
		}
	}
	return Build(in), nil
}

// absent reports errors that mean "not installed": no such CRD or object.
func absent(err error) bool {
	return apierrors.IsNotFound(err) || meta.IsNoMatchError(err)
}

func (c *Collector) warnOnce(ctx context.Context, what string, err error) {
	if _, loaded := c.warned.LoadOrStore(what, true); loaded {
		return
	}
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	log.WarnContext(ctx, "capacity report: cannot read "+what+"; it is left out", "err", err)
}

// Runner builds the report every Interval and hands it to OnReport when it changed (ignoring
// observedAt) or at least every Refresh, so the hub knows it is current.
type Runner struct {
	Collector *Collector
	Interval  time.Duration
	Refresh   time.Duration
	OnReport  func(*hubclient.CapacityReport)
	Log       *slog.Logger

	lastHash [32]byte
	lastSent time.Time
}

// RunOnce builds one report and hands it on if needed.
func (r *Runner) RunOnce(ctx context.Context) error {
	rep, err := r.Collector.Collect(ctx)
	if err != nil {
		return err
	}
	if FitSize(rep, MaxReportBytes) {
		r.log().WarnContext(ctx, "capacity report trimmed to fit the status batch; the estimate is incomplete", "maxBytes", MaxReportBytes, "podGroups", len(rep.PodGroups))
	}
	h := hashReport(rep)
	refresh := r.Refresh
	if refresh <= 0 {
		refresh = 30 * time.Minute
	}
	if h == r.lastHash && time.Since(r.lastSent) < refresh {
		return nil
	}
	r.lastHash, r.lastSent = h, time.Now()
	r.log().InfoContext(ctx, "capacity report built", "nodes", len(rep.Nodes), "nodePools", len(rep.NodePools),
		"podGroups", len(rep.PodGroups), "pdbs", len(rep.PDBs), "blockingPods", len(rep.Pods), "autoscalers", len(rep.Autoscalers), "truncated", rep.Truncated)
	if r.OnReport != nil {
		r.OnReport(rep)
	}
	return nil
}

// Run blocks until ctx is done.
func (r *Runner) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	log := r.log()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "capacity report failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Runner) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

// MaxReportBytes keeps the report well below the hub's 8 MiB request limit, so the status batch
// that carries it is never rejected.
const MaxReportBytes = 4 << 20

// FitSize drops pod groups from the end (then nodes) until the report's JSON is at most limit
// bytes, and marks it truncated. It reports whether it trimmed.
func FitSize(rep *hubclient.CapacityReport, limit int) bool {
	size := func() int { b, _ := json.Marshal(rep); return len(b) }
	n := size()
	if n <= limit {
		return false
	}
	rep.Truncated = true
	for n > limit && len(rep.PodGroups) > 0 {
		keep := len(rep.PodGroups) * limit / n * 9 / 10
		rep.PodGroups = rep.PodGroups[:keep]
		n = size()
	}
	for n > limit && len(rep.Nodes) > 0 {
		rep.Nodes = rep.Nodes[:len(rep.Nodes)*limit/n*9/10]
		n = size()
	}
	return true
}

func hashReport(rep *hubclient.CapacityReport) [32]byte {
	cp := *rep
	cp.ObservedAt = time.Time{}
	b, _ := json.Marshal(cp)
	return sha256.Sum256(b)
}
