//go:build e2e

package e2e

import (
	"fmt"
	"math"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1alpha1 "github.com/kerf-dev/kerf/api/v1alpha1"
)

// realizedSavings is the part of the hub's RealizedSavings the suite reads.
type realizedSavings struct {
	Since           *time.Time `json:"since"`
	ComputedThrough *time.Time `json:"computedThrough"`
	Total           struct {
		CPU, Memory, Total float64
	} `json:"total"`
	ReleasedNow struct {
		CPUCores    float64 `json:"cpuCores"`
		MemoryBytes float64 `json:"memoryBytes"`
	} `json:"releasedNow"`
}

type nodeCostTrend struct {
	Relation string `json:"relation"`
	Points   []struct {
		Time               time.Time `json:"time"`
		NodeCostUSDPerHour *float64  `json:"nodeCostUSDPerHour"`
		CPURequestedRatio  *float64  `json:"cpuRequestedRatio"`
	} `json:"points"`
}

// Realized savings (docs/realized-savings.md): the hub leader materialises, per complete hour,
// the requests released by applied changes x replicas x rates of the time.
//
//   - The leader keeps the cluster's computation current (computedThrough within the last ~70 min).
//   - Changes applied by earlier runs of the suite (stored history, more than two hours old) have
//     a realized saving, also though their namespaces are gone.
//   - The node cost trend has hourly points with node cost and requested/allocatable ratio.
//   - A change applied now is counted once its hour is complete. That takes up to 70 minutes, so
//     it is checked only when E2E_REALIZED_WAIT (e.g. 75m) allows it: then the released CPU in
//     the last computed hour is (request before - request after) x 2 replicas.
func TestRealizedSavings(t *testing.T) {
	t.Parallel()
	cl := env.Spoke1
	ns := newNamespace(t, cl, "realized")
	a := overprovisioned("web")
	a.Replicas = 2
	deploy(t, cl, ns, a)
	armed(t, cl, ns, ns, "web", overCPU, autoCPU(v1alpha1.StrategyInPlace))
	r := waitPhase(t, cl, ns, deployment, "web", v1alpha1.PhaseApplied)
	applied := waitEvent(t, cl, ns, "web", "applied", nil)
	after := r.Spec.Containers[0].Requests[corev1.ResourceCPU]
	released := resource.MustParse(overCPU)
	released.Sub(after)
	t.Logf("applied at %s: cpu %s -> %s (released %s per replica)", applied.Time, overCPU, after.String(), released.String())

	// The leader materialises the cluster's hours.
	var fleet realizedSavings
	eventually(t, 3*time.Minute, 10*time.Second, "realized savings computed for "+cl.Name, func() (bool, string) {
		if _, err := env.Hub.get(ctx(t), "/v1/savings/realized", url.Values{"cluster": {cl.Name}, "step": {"day"}}, &fleet); err != nil {
			return false, err.Error()
		}
		if fleet.ComputedThrough == nil {
			return false, "no computedThrough"
		}
		return time.Since(*fleet.ComputedThrough) < 70*time.Minute, "computedThrough " + fleet.ComputedThrough.String()
	})

	// Earlier applied changes have a realized saving; each answers by id although deleted since.
	var hist struct {
		Items []struct {
			Time     time.Time `json:"time"`
			Workload struct {
				Namespace, Kind, Name string
			} `json:"workload"`
		} `json:"items"`
	}
	_, err := env.Hub.get(ctx(t), "/v1/audit", url.Values{
		"cluster": {cl.Name}, "type": {"applied"}, "order": {"desc"}, "limit": {"50"},
		"from": {time.Now().Add(-14 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		"to":   {time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)},
	}, &hist)
	must(t, err)
	if len(hist.Items) == 0 {
		t.Log("no applied changes older than two hours on this fleet yet: history check skipped")
	} else {
		withSaving := 0
		for _, e := range hist.Items {
			var w realizedSavings
			id := fmt.Sprintf("%s/%s/%s/%s", cl.Name, e.Workload.Namespace, e.Workload.Kind, e.Workload.Name)
			if _, err := env.Hub.get(ctx(t), "/v1/workloads/"+url.PathEscape(id)+"/savings/realized", nil, &w); err != nil {
				t.Errorf("%s: %v", id, err)
				continue
			}
			if w.Since != nil && w.Total.Total != 0 {
				withSaving++
			}
		}
		if withSaving == 0 {
			t.Errorf("none of %d applied changes older than two hours has a realized saving", len(hist.Items))
		}
		if fleet.Since == nil || fleet.Total.Total == 0 {
			t.Errorf("cluster realized savings %+v, want a total", fleet)
		}
	}

	// The node cost trend.
	var nc nodeCostTrend
	_, err = env.Hub.get(ctx(t), "/v1/savings/realized/node-cost", url.Values{
		"cluster": {cl.Name}, "from": {time.Now().Add(-6 * time.Hour).UTC().Format(time.RFC3339)},
	}, &nc)
	must(t, err)
	priced := 0
	for _, p := range nc.Points {
		if p.NodeCostUSDPerHour != nil && *p.NodeCostUSDPerHour > 0 && p.CPURequestedRatio != nil &&
			*p.CPURequestedRatio > 0 && *p.CPURequestedRatio < 1.5 {
			priced++
		}
	}
	if nc.Relation != "correlated" || priced == 0 {
		t.Errorf("node cost trend: relation %q, %d of %d points with cost and ratio", nc.Relation, priced, len(nc.Points))
	}

	// This change, once its first complete hour is computed.
	wait, _ := time.ParseDuration(getenv("E2E_REALIZED_WAIT", "0"))
	if wait <= 0 {
		t.Log("E2E_REALIZED_WAIT not set: the exact check of this change (needs a complete hour) skipped")
		return
	}
	firstFull := applied.Time.Truncate(time.Hour).Add(2 * time.Hour) // the hour after the apply's hour has ended
	id := fmt.Sprintf("%s/%s/%s/%s", cl.Name, ns, deployment, "web")
	var w realizedSavings
	eventually(t, wait, time.Minute, "realized saving of "+id, func() (bool, string) {
		if _, err := env.Hub.get(ctx(t), "/v1/workloads/"+url.PathEscape(id)+"/savings/realized", nil, &w); err != nil {
			return false, err.Error()
		}
		return w.ComputedThrough != nil && !w.ComputedThrough.Before(firstFull), fmt.Sprintf("computedThrough %v", w.ComputedThrough)
	})
	want := released.AsApproximateFloat64() * float64(a.Replicas)
	if w.Since == nil || w.Total.CPU <= 0 || math.Abs(w.ReleasedNow.CPUCores-want) > 0.05*want {
		t.Errorf("realized %+v, want %.3f cores released now", w, want)
	}
}
