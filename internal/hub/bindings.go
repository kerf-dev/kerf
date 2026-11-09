package hub

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// Bindings resolves where a cluster's metrics are: its datasource (querier, API) and the
// matcher selecting its series. A cluster without a stored binding uses the hub's own store with
// label cluster = its name, exactly as it was without a binding.
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
	// Bound is false without a stored binding (behaviour without a binding).
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

// ErrNotPromQL: the datasource is not queried with PromQL (dynatrace), so the query proxy and
// the Prometheus probe do not apply.
var ErrNotPromQL = errors.New("the datasource is not Prometheus-compatible (dynatrace): PromQL queries are not supported")

// Datasource returns the querier and API of a datasource. A dynatrace datasource returns a
// *dynatrace.Source and no API.
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
	if cfg.Type == hubapi.DatasourceDynatrace {
		ag := ""
		if cfg.Auth.Dynatrace != nil {
			ag = cfg.Auth.Dynatrace.ActiveGateURL
		}
		return dynatrace.New(cfg.URL, ag, hc), nil, nil
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
	if cfg.Type == hubapi.DatasourceDynatrace {
		return "", nil, ErrNotPromQL
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
