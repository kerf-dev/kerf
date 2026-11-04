package pricing

import (
	"context"
	"fmt"
	"sort"

	"github.com/tomer1983/resource-optimizer/internal/hub/datasource"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// BindingLister lists the stored metrics bindings by cluster (datasource.PG).
type BindingLister interface {
	Bindings(ctx context.Context) (map[string]hubapi.MetricsBinding, error)
}

// RegistryTargets lists the clusters with a stored metrics binding and resolves their
// datasources (URL, auth, headers) from the hub's datasource registry. Clusters without a binding
// are read from the hub's own VictoriaMetrics as before D-025.
type RegistryTargets struct {
	Registry *datasource.Registry
	Bindings BindingLister
}

// Targets implements TargetLister.
func (r *RegistryTargets) Targets(ctx context.Context) ([]Target, error) {
	bindings, err := r.Bindings.Bindings(ctx)
	if err != nil {
		return nil, err
	}
	clusters := make([]string, 0, len(bindings))
	for c := range bindings {
		clusters = append(clusters, c)
	}
	sort.Strings(clusters)
	out := make([]Target, 0, len(clusters))
	for _, c := range clusters {
		b := bindings[c]
		m, err := datasource.NewMatcher(c, &b)
		if err != nil {
			return nil, fmt.Errorf("cluster %s: %w", c, err)
		}
		cfg, hc, err := r.Registry.Client(ctx, b.DatasourceID)
		if err != nil {
			return nil, fmt.Errorf("cluster %s datasource %s: %w", c, b.DatasourceID, err)
		}
		out = append(out, Target{Cluster: c, BaseURL: cfg.URL, HTTP: hc, Matcher: m})
	}
	return out, nil
}
