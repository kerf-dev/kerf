package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/datasource"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// Bindings resolves where a cluster's metrics are (D-025): its datasource (querier, API) and the
// matcher selecting its series. A cluster without a stored binding uses the hub's own store with
// label cluster = its name, exactly as before D-025.
type Bindings struct {
	PG       *datasource.PG
	Registry *datasource.Registry
	// Local is the hub's own store (datasource local) and LocalAPI its probe/label API.
	Local    recommender.Querier
	LocalAPI *datasource.API
	// Timeout of datasource queries when the datasource sets none.
	Timeout time.Duration
}

// Resolved is a cluster's metrics location.
type Resolved struct {
	Binding hubapi.MetricsBinding
	// Bound is false without a stored binding (behaviour before D-025).
	Bound   bool
	Q       recommender.Querier
	API     *datasource.API
	Matcher datasource.Matcher
}

// Resolve returns the metrics location of cluster.
func (b *Bindings) Resolve(ctx context.Context, cluster string) (Resolved, error) {
	bind, ok, err := b.PG.Binding(ctx, cluster)
	if err != nil {
		return Resolved{}, err
	}
	if !ok || datasource.IsDefaultBinding(cluster, bind) {
		return Resolved{Binding: hubapi.DefaultMetricsBinding(cluster), Q: b.Local, API: b.LocalAPI, Matcher: datasource.DefaultMatcher(cluster)}, nil
	}
	return b.ResolveBinding(ctx, cluster, bind)
}

// ResolveBinding returns the metrics location of a binding (also for an unsaved one: the
// datasource test endpoint).
func (b *Bindings) ResolveBinding(ctx context.Context, cluster string, bind hubapi.MetricsBinding) (Resolved, error) {
	if bind.DatasourceID == "" {
		bind.DatasourceID = hubapi.LocalDatasourceID
	}
	m, err := datasource.NewMatcher(cluster, &bind)
	if err != nil {
		return Resolved{}, &datasource.ValidationError{Problems: []string{err.Error()}}
	}
	out := Resolved{Binding: bind, Bound: true, Matcher: m}
	if bind.DatasourceID == hubapi.LocalDatasourceID {
		out.Q, out.API = b.Local, b.LocalAPI
		return out, nil
	}
	q, api, err := b.Datasource(ctx, bind.DatasourceID)
	if err != nil {
		return Resolved{}, err
	}
	out.Q, out.API = q, api
	return out, nil
}

// Datasource returns the querier and API of a datasource.
func (b *Bindings) Datasource(ctx context.Context, id string) (recommender.Querier, *datasource.API, error) {
	if id == hubapi.LocalDatasourceID {
		return b.Local, b.LocalAPI, nil
	}
	cfg, hc, err := b.Registry.Client(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if hc.Timeout == 0 && b.Timeout > 0 {
		c := *hc
		c.Timeout = b.Timeout
		hc = &c
	}
	maxPoints := 2016
	if cfg.MaxPoints > 0 && cfg.MaxPoints < maxPoints {
		maxPoints = cfg.MaxPoints
	}
	return &recommender.PromClient{BaseURL: cfg.URL, HTTP: hc, MaxPointsPerQuery: maxPoints}, datasource.NewAPI(cfg.URL, hc), nil
}

// HTTP returns the base URL and client of a datasource (the pricing query proxy).
func (b *Bindings) HTTP(ctx context.Context, id string, localURL string) (string, *http.Client, error) {
	if id == hubapi.LocalDatasourceID {
		return localURL, b.LocalAPI.HTTP, nil
	}
	cfg, hc, err := b.Registry.Client(ctx, id)
	if err != nil {
		return "", nil, err
	}
	return cfg.URL, hc, nil
}

// ForRecommender is recommender.Source.Binder: the bound querier and matcher, or the local store
// and nil for an unbound cluster (byte-identical queries).
func (b *Bindings) ForRecommender(ctx context.Context, cluster string) (recommender.Querier, *datasource.Matcher, error) {
	r, err := b.Resolve(ctx, cluster)
	if err != nil {
		return nil, nil, err
	}
	if !r.Bound {
		return r.Q, nil, nil
	}
	return r.Q, &r.Matcher, nil
}
