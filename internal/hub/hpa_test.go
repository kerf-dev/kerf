package hub

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// TestHPACoordination: the recommendation of an hpa-coupled workload carries hpa (WO-11) on the
// agent and UI endpoints and in the history; other workloads have none.
func TestHPACoordination(t *testing.T) {
	e := newEnv(t)
	e.loader.mu.Lock()
	e.loader.workloads["c2"][0].hpa = &recommender.HPA{Name: "api", Metrics: []string{"cpu/utilization"},
		Targets: map[string]float64{"cpu": 60}, MinReplicas: 2, MaxReplicas: 6, PeakReplicas: 3}
	e.loader.mu.Unlock()
	e.advance(5 * time.Minute)
	e.run(e.now, false)

	var list hubapi.RecommendationList
	r := e.getJSON("/v1/clusters/c2/recommendations", tokC2, &list)
	if len(list.Items) != 1 {
		t.Fatalf("items %+v", list.Items)
	}
	h := list.Items[0].HPA
	// api: 250m request, ~100-120m usage; the target is clamped at 90 %.
	if h == nil || h.Name != "api" || h.Metric != "cpu" || h.CurrentTarget != 60 || h.RecommendedTarget < 40 || h.RecommendedTarget > 90 ||
		h.SkipReason != "" || h.MaxReplicas == nil || *h.MaxReplicas != 6 {
		t.Fatalf("hpa %+v (%s)", h, r.body)
	}
	cur, _ := recommender.ParseCPU(list.Items[0].Containers[0].Current.Requests.CPU)
	next, _ := recommender.ParseCPU(list.Items[0].Containers[0].Recommended.Requests.CPU)
	if p0, p1 := cur*float64(h.CurrentTarget), next*float64(h.RecommendedTarget); p1 < p0*0.96 || p1 > p0*1.04 {
		t.Errorf("request x target %v -> %v", p0, p1)
	}

	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kAPI, ""), tokUI, &d)
	if d.Recommendation == nil || !reflect.DeepEqual(d.Recommendation.HPA, h) {
		t.Errorf("detail hpa %+v", d.Recommendation)
	}
	var hist hubapi.WorkloadHistory
	e.getJSON(wlPath(kAPI, "/history"), tokUI, &hist)
	if len(hist.Recommendations) < 2 || hist.Recommendations[0].HPA == nil || hist.Recommendations[1].HPA != nil {
		t.Errorf("history: newest should have hpa, older ones not: %+v", hist.Recommendations[:min(2, len(hist.Recommendations))])
	}
	var c1 hubapi.RecommendationList
	r = e.getJSON("/v1/clusters/c1/recommendations", tokC1, &c1)
	if strings.Contains(string(r.body), `"hpa"`) {
		t.Errorf("hpa on workloads without an HPA: %s", r.body)
	}

	// An HPA at maxReplicas: hpa with skipReason, no recommended target.
	e.loader.mu.Lock()
	e.loader.workloads["c2"][0].hpa.PeakReplicas = 6
	e.loader.mu.Unlock()
	e.advance(5 * time.Minute)
	e.run(e.now, false)
	var atMax hubapi.RecommendationList
	e.getJSON("/v1/clusters/c2/recommendations", tokC2, &atMax)
	if h := atMax.Items[0].HPA; h == nil || h.SkipReason != hubapi.HPASkipMaxReplicas || h.RecommendedTarget != 0 {
		t.Errorf("at max: %+v", h)
	}
}
