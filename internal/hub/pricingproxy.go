package hub

import (
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// listMetricsBindings serves GET /v1/internal/metrics-bindings (pricing service, D-026): the
// metrics binding of every registered cluster, never datasource secrets.
func (s *Server) listMetricsBindings(w http.ResponseWriter, r *http.Request) {
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
		out.Items = append(out.Items, hubapi.ClusterMetricsBinding{Cluster: c, Binding: b, Bound: true})
	}
	writeJSON(w, out)
}

// proxyParams are the Prometheus query API parameters the proxy forwards.
var proxyParams = []string{"query", "time", "start", "end", "step", "timeout"}

// proxyQuery serves /v1/internal/datasources/{datasourceId}/api/v1/query[_range] (pricing
// service, D-026): it runs the query against the datasource with the datasource's auth and
// returns the datasource's answer unchanged. Credentials never leave the hub.
func (s *Server) proxyQuery(w http.ResponseWriter, r *http.Request) {
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
