package hub

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/datasource/dynatrace"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// listMetricsBindings serves GET /v1/internal/metrics-bindings (pricing service): the
// metrics binding of every registered cluster, never datasource secrets.
func (s *Server) listMetricsBindings(w http.ResponseWriter, r *http.Request) {
	s.recordPricing(r)
	if !s.onb(w, r) {
		return
	}
	names, err := s.clusterNames(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	stored, err := s.Onb.Bind.PG.Bindings(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sort.Strings(names)
	types := map[string]string{}
	if ds, err := s.Onb.DS.List(r.Context()); err == nil {
		for _, d := range ds {
			types[d.ID] = d.Type
		}
	}
	out := hubapi.MetricsBindingList{Items: make([]hubapi.ClusterMetricsBinding, 0, len(names))}
	for _, c := range names {
		b, ok := stored[c]
		if !ok || datasource.IsDefaultBinding(c, b) {
			out.Items = append(out.Items, hubapi.ClusterMetricsBinding{Cluster: c, Binding: hubapi.DefaultMetricsBinding(c)})
			continue
		}
		if b.ClusterLabel != "" && b.ClusterLabelValue == "" {
			b.ClusterLabelValue = c
		}
		out.Items = append(out.Items, hubapi.ClusterMetricsBinding{Cluster: c, Binding: b, Bound: true, DatasourceType: types[b.DatasourceID]})
	}
	writeJSON(w, out)
}

// proxyParams are the Prometheus query API parameters the proxy forwards.
var proxyParams = []string{"query", "time", "start", "end", "step", "timeout"}

// proxyQuery serves /v1/internal/datasources/{datasourceId}/api/v1/query[_range] (pricing
// service): it runs the query against the datasource with the datasource's auth and
// returns the datasource's answer unchanged. Credentials never leave the hub.
func (s *Server) proxyQuery(w http.ResponseWriter, r *http.Request) {
	s.recordPricing(r)
	if !s.onb(w, r) {
		return
	}
	id := r.PathValue("datasourceId")
	if _, err := s.Onb.DS.Get(r.Context(), id); err != nil {
		s.dsError(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		badRequest(w, r, "invalid_query", "Invalid form: "+err.Error())
		return
	}
	if strings.TrimSpace(r.Form.Get("query")) == "" {
		badRequest(w, r, "invalid_query", "query is required.")
		return
	}
	form := url.Values{}
	for _, p := range proxyParams {
		if v := r.Form.Get(p); v != "" {
			form.Set(p, v)
		}
	}
	base, hc, err := s.Onb.Bind.HTTP(r.Context(), id, s.Cfg.PrometheusURL)
	if errors.Is(err, ErrNotPromQL) {
		// the Prometheus error format, so a PromQL client reports it as a query error
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "errorType": "bad_data",
			"error": "datasource " + id + " is a dynatrace datasource: use GET /v1/internal/clusters/{cluster}/nodes"})
		return
	}
	if err != nil {
		s.dsError(w, r, err)
		return
	}
	path := "/api/v1/query"
	if strings.HasSuffix(r.URL.Path, "/query_range") {
		path = "/api/v1/query_range"
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(base, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := hc.Do(req)
	if err != nil {
		writeProblem(w, r, http.StatusBadGateway, "datasource_unreachable", "Datasource "+id+": "+err.Error())
		return
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, 256<<20))
}

// clusterNodeInventory serves GET /v1/internal/clusters/{cluster}/nodes (pricing service): the
// nodes of a cluster bound to a dynatrace datasource, with their pricing labels and
// allocatable capacity. Other clusters are read through the query proxy (409 not_supported).
func (s *Server) clusterNodeInventory(w http.ResponseWriter, r *http.Request) {
	s.recordPricing(r)
	if !s.onb(w, r) {
		return
	}
	cluster := r.PathValue("cluster")
	if _, err := s.Store.GetCluster(r.Context(), cluster); err != nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Unknown cluster.")
		return
	}
	res, err := s.Onb.Bind.Resolve(r.Context(), cluster)
	if err != nil {
		s.dsError(w, r, err)
		return
	}
	src, ok := res.Q.(*dynatrace.Source)
	if !ok {
		writeProblem(w, r, http.StatusConflict, hubapi.CodeNotSupported, "The cluster's datasource is Prometheus-compatible: read its nodes through the query proxy.")
		return
	}
	cl := res.Matcher.Value
	if cl == "" {
		cl = cluster
	}
	nodes, err := src.Nodes(r.Context(), cl, s.now())
	if err != nil {
		writeProblem(w, r, http.StatusBadGateway, "datasource_unreachable", "Dynatrace: "+err.Error())
		return
	}
	writeJSON(w, hubapi.NodeInventory{Items: nodes})
}
