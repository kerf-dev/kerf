package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
)

// ThrottleQuery identifies the containers whose throttling ratio is wanted.
type ThrottleQuery struct {
	Cluster    string
	Namespace  string
	Pods       []corev1.Pod
	Containers []string
	// Since is the start of the window (the change); the window ends now.
	Since time.Time
	// State keeps per-watch baselines for sources that read raw counters.
	State *watchState
}

// ThrottleSource returns the CFS throttled-periods ratio of the containers over the window.
// ok is false when there is not enough data (no samples, or fewer than minPeriods periods).
type ThrottleSource interface {
	Name() string
	Ratio(ctx context.Context, q ThrottleQuery) (ratio float64, ok bool, err error)
}

// minPeriods is the minimum number of CFS periods (100 ms each) in the window for a ratio to count.
const minPeriods = 300

// PromSource queries a Prometheus-compatible endpoint (the hub's VictoriaMetrics, D-003).
type PromSource struct {
	URL       string
	TokenFile string
	HTTP      *http.Client
}

// Name implements ThrottleSource.
func (p *PromSource) Name() string { return "promql" }

// ThrottlingQuery builds the PromQL for the throttling ratio of the given containers.
func ThrottlingQuery(q ThrottleQuery, window time.Duration) string {
	pods := make([]string, 0, len(q.Pods))
	for _, p := range q.Pods {
		pods = append(pods, regexp.QuoteMeta(p.Name))
	}
	ctrs := make([]string, 0, len(q.Containers))
	for _, c := range q.Containers {
		ctrs = append(ctrs, regexp.QuoteMeta(c))
	}
	sel := fmt.Sprintf(`{cluster=%q,namespace=%q,pod=~%q,container=~%q}`, q.Cluster, q.Namespace, strings.Join(pods, "|"), strings.Join(ctrs, "|"))
	w := fmt.Sprintf("%ds", int(window.Seconds()))
	return fmt.Sprintf(`sum(increase(container_cpu_cfs_throttled_periods_total%s[%s])) / sum(increase(container_cpu_cfs_periods_total%s[%s]))`+
		` and sum(increase(container_cpu_cfs_periods_total%s[%s])) >= %d`, sel, w, sel, w, sel, w, minPeriods)
}

// Ratio implements ThrottleSource.
func (p *PromSource) Ratio(ctx context.Context, q ThrottleQuery) (float64, bool, error) {
	if len(q.Pods) == 0 || len(q.Containers) == 0 {
		return 0, false, nil
	}
	window := time.Since(q.Since).Truncate(time.Second)
	u, err := url.Parse(strings.TrimRight(p.URL, "/") + "/api/v1/query")
	if err != nil {
		return 0, false, err
	}
	u.RawQuery = url.Values{"query": {ThrottlingQuery(q, window)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, false, err
	}
	if p.TokenFile != "" {
		if b, err := os.ReadFile(p.TokenFile); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
		}
	}
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("promql: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	var r struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, false, fmt.Errorf("promql: %w", err)
	}
	if r.Status != "success" {
		return 0, false, fmt.Errorf("promql: %s", r.Error)
	}
	if len(r.Data.Result) == 0 || len(r.Data.Result[0].Value) != 2 {
		return 0, false, nil
	}
	s, _ := r.Data.Result[0].Value[1].(string)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, fmt.Errorf("promql: value %q: %w", s, err)
	}
	return f, true, nil
}

// CadvisorSource reads the kubelets' cAdvisor metrics through the API server node proxy
// (RBAC nodes/proxy get). It is the fallback when the hub's metrics endpoint is unreachable.
type CadvisorSource struct {
	REST rest.Interface // a core/v1 REST client
}

// Name implements ThrottleSource.
func (c *CadvisorSource) Name() string { return "cadvisor" }

// Ratio implements ThrottleSource. Counters of containers that started before the window are
// taken relative to the first observation in this watch.
func (c *CadvisorSource) Ratio(ctx context.Context, q ThrottleQuery) (float64, bool, error) {
	if q.State == nil {
		return 0, false, errors.New("cadvisor: no watch state")
	}
	want := map[string]bool{}
	for _, ctr := range q.Containers {
		want[ctr] = true
	}
	started := map[string]bool{} // pod/container started after the change
	nodes := map[string][]corev1.Pod{}
	for _, p := range q.Pods {
		if p.Spec.NodeName != "" {
			nodes[p.Spec.NodeName] = append(nodes[p.Spec.NodeName], p)
		}
		for _, cs := range p.Status.ContainerStatuses {
			if r := cs.State.Running; r != nil && !r.StartedAt.Time.Before(q.Since) {
				started[p.Name+"/"+cs.Name] = true
			}
		}
	}
	var throttled, periods float64
	for node, pods := range nodes {
		raw, err := c.REST.Get().AbsPath("/api/v1/nodes", node, "proxy", "metrics", "cadvisor").DoRaw(ctx)
		if err != nil {
			return 0, false, fmt.Errorf("cadvisor %s: %w", node, err)
		}
		cur, err := parseCFS(raw, q.Namespace, pods, want)
		if err != nil {
			return 0, false, fmt.Errorf("cadvisor %s: %w", node, err)
		}
		for key, v := range cur {
			base, seen := q.State.throttle[key]
			if !seen {
				if !started[key] {
					base = v
				}
				q.State.throttle[key] = base
			}
			throttled += v.throttled - base.throttled
			periods += v.periods - base.periods
		}
	}
	if periods < minPeriods {
		return 0, false, nil
	}
	return throttled / periods, true, nil
}

// parseCFS returns the CFS counters per pod/container from a cAdvisor text exposition.
func parseCFS(raw []byte, namespace string, pods []corev1.Pod, containers map[string]bool) (map[string]counter, error) {
	podNames := map[string]bool{}
	for _, p := range pods {
		podNames[p.Name] = true
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := parser.TextToMetricFamilies(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	out := map[string]counter{}
	add := func(name string, set func(*counter, float64)) {
		f, ok := fams[name]
		if !ok {
			return
		}
		for _, m := range f.GetMetric() {
			l := labels(m)
			if l["namespace"] != namespace || !podNames[l["pod"]] || !containers[l["container"]] {
				continue
			}
			key := l["pod"] + "/" + l["container"]
			c := out[key]
			set(&c, m.GetCounter().GetValue())
			out[key] = c
		}
	}
	add("container_cpu_cfs_throttled_periods_total", func(c *counter, v float64) { c.throttled += v })
	add("container_cpu_cfs_periods_total", func(c *counter, v float64) { c.periods += v })
	return out, nil
}

func labels(m *dto.Metric) map[string]string {
	out := map[string]string{}
	for _, lp := range m.GetLabel() {
		out[lp.GetName()] = lp.GetValue()
	}
	return out
}

// fallbackSource tries each source in order and returns the first that answers without error.
type fallbackSource []ThrottleSource

func (f fallbackSource) Name() string { return "fallback" }

func (f fallbackSource) Ratio(ctx context.Context, q ThrottleQuery) (float64, bool, error) {
	var errs []error
	for _, s := range f {
		r, ok, err := s.Ratio(ctx, q)
		if err == nil {
			return r, ok, nil
		}
		throttleErrors.WithLabelValues(s.Name()).Inc()
		errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
	}
	return 0, false, errors.Join(errs...)
}
