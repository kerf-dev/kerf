package readiness

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	rd "github.com/tomer1983/resource-optimizer/api/v1alpha1/readiness"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// Gauges exports ro_cluster_readiness{cluster,check,severity} (1 = pass, 0 otherwise, one series
// per catalog check) and ro_cluster_readiness_state{cluster,state} (1 for the current state).
// docs/contracts/metrics.md.
type Gauges struct {
	checks *prometheus.GaugeVec
	state  *prometheus.GaugeVec
	mu     sync.Mutex
}

// NewGauges creates the gauges; register them with Register.
func NewGauges() *Gauges {
	return &Gauges{
		checks: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ro_cluster_readiness",
			Help: "1 when the latest result of a cluster readiness check is pass, 0 when it is warn, fail or unknown (or has no result).",
		}, []string{"cluster", "check", "severity"}),
		state: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ro_cluster_readiness_state",
			Help: "1 for the cluster's current readiness state, 0 for the others.",
		}, []string{"cluster", "state"}),
	}
}

// Register registers the gauges.
func (g *Gauges) Register(reg prometheus.Registerer) error {
	if err := reg.Register(g.checks); err != nil {
		return err
	}
	return reg.Register(g.state)
}

// Set replaces the series of one cluster.
func (g *Gauges) Set(cluster string, state rd.State, checks []hubapi.ReadinessCheck) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pass := map[string]bool{}
	for _, c := range checks {
		pass[c.ID] = c.Status == string(rd.StatusPass)
	}
	for _, c := range rd.Catalog {
		v := 0.0
		if pass[c.ID] {
			v = 1
		}
		g.checks.WithLabelValues(cluster, c.ID, string(c.Severity)).Set(v)
	}
	for _, st := range rd.States {
		v := 0.0
		if st == state {
			v = 1
		}
		g.state.WithLabelValues(cluster, string(st)).Set(v)
	}
}

// Delete removes the series of a cluster (deregistered).
func (g *Gauges) Delete(cluster string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checks.DeletePartialMatch(prometheus.Labels{"cluster": cluster})
	g.state.DeletePartialMatch(prometheus.Labels{"cluster": cluster})
}
