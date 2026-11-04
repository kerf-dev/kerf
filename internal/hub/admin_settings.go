package hub

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tomer1983/resource-optimizer/internal/hub/config"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
)

// ---- settings (hub-only, versioned; docs/contracts/admin.md "Settings") ----

func (s *Server) baseRecommender() config.Recommender {
	return config.Recommender{Config: s.RecCfg, Interval: s.Cfg.Recommender.Interval.D(), Retention: s.Cfg.History.Retention.D()}
}

func (s *Server) recommenderDoc(r *http.Request) (hubapi.RecommenderSettingsDoc, error) {
	d, err := s.Store.GetSettings(r.Context(), hubapi.SettingsRecommender)
	if err != nil {
		return hubapi.RecommenderSettingsDoc{}, err
	}
	doc := hubapi.RecommenderSettingsDoc{Version: d.Version, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy, Reason: d.Reason}
	if d.Version == 0 {
		doc.Value = config.RecommenderSettings(s.baseRecommender())
		return doc, nil
	}
	return doc, json.Unmarshal(d.Value, &doc.Value)
}

func (s *Server) pricingDoc(r *http.Request) (hubapi.PricingSettingsDoc, error) {
	d, err := s.Store.GetSettings(r.Context(), hubapi.SettingsPricing)
	if err != nil {
		return hubapi.PricingSettingsDoc{}, err
	}
	doc := hubapi.PricingSettingsDoc{Version: d.Version, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy, Reason: d.Reason,
		Value: hubapi.PricingSettings{Clusters: []hubapi.ClusterPricingAdjustment{}}}
	if d.Version == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(d.Value, &doc.Value); err != nil {
		return doc, err
	}
	if doc.Value.Clusters == nil {
		doc.Value.Clusters = []hubapi.ClusterPricingAdjustment{}
	}
	return doc, nil
}

// getSettings serves GET /v1/admin/settings.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	rec, err := s.recommenderDoc(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	pr, err := s.pricingDoc(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, hubapi.Settings{Recommender: rec, Pricing: pr})
}

// getRecommenderSettings serves GET /v1/admin/settings/recommender.
func (s *Server) getRecommenderSettings(w http.ResponseWriter, r *http.Request) {
	doc, err := s.recommenderDoc(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	setETag(w, doc.Version)
	writeJSON(w, doc)
}

// getPricingSettings serves GET /v1/admin/settings/pricing.
func (s *Server) getPricingSettings(w http.ResponseWriter, r *http.Request) {
	doc, err := s.pricingDoc(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	setETag(w, doc.Version)
	writeJSON(w, doc)
}

// putRecommenderSettings serves PUT /v1/admin/settings/recommender. The change takes effect on
// the next run (the interval at once).
func (s *Server) putRecommenderSettings(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.RecommenderSettingsUpdate
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	applied, errs := config.ApplyRecommender(s.baseRecommender(), in.Value)
	if errs != nil {
		badRequest(w, r, "invalid_settings", strings.Join(errs, "; "))
		return
	}
	value := config.RecommenderSettings(applied) // normalised durations
	before, err := s.recommenderDoc(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.Store.PutSettings(r.Context(), hubapi.SettingsRecommender, ver, value, before.Value, s.change(r, hubapi.ActionSettingsUpdated, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "settings section")
		return
	}
	switch {
	case s.Coord != nil: // the leader runs the schedule; it reloads the interval when notified
		if err := s.Store.Notify(r.Context(), store.NotifySettings); err != nil {
			s.Log.WarnContext(r.Context(), "notify settings change", "err", err)
		}
	case s.Job != nil:
		s.Job.SetInterval(applied.Interval)
	}
	s.Log.InfoContext(r.Context(), "recommender settings updated", "version", v, "by", PrincipalFrom(r.Context()).Actor().Name)
	s.getRecommenderSettings(w, r)
}

// putPricingSettings serves PUT /v1/admin/settings/pricing.
func (s *Server) putPricingSettings(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.PricingSettingsUpdate
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	clusters, err := s.Store.Clusters(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	known := map[string]bool{}
	for _, c := range clusters {
		known[c.Name] = true
	}
	if errs := config.ValidatePricing(in.Value, func(c string) bool { return known[c] }); errs != nil {
		badRequest(w, r, "invalid_settings", strings.Join(errs, "; "))
		return
	}
	if in.Value.Clusters == nil {
		in.Value.Clusters = []hubapi.ClusterPricingAdjustment{}
	}
	before, err := s.pricingDoc(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if _, err := s.Store.PutSettings(r.Context(), hubapi.SettingsPricing, ver, in.Value, before.Value, s.change(r, hubapi.ActionSettingsUpdated, in.Reason)); err != nil {
		s.storeError(w, r, err, "settings section")
		return
	}
	s.getPricingSettings(w, r)
}

// listSettingsVersions serves GET /v1/admin/settings/{section}/versions.
func (s *Server) listSettingsVersions(w http.ResponseWriter, r *http.Request) {
	section := r.PathValue("section")
	if section != hubapi.SettingsRecommender && section != hubapi.SettingsPricing {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Unknown settings section "+section+".")
		return
	}
	limit, before, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, more, err := s.Store.SettingsVersions(r.Context(), section, before, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := hubapi.SettingsVersionList{Items: items}
	if more && len(items) > 0 {
		out.Continue = encodeCursor(cursor{Offset: items[len(items)-1].Version})
	}
	writeJSON(w, out)
}
