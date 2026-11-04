package hub

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/api/v1alpha1/validate"
	"github.com/tomer1983/resource-optimizer/internal/hub/admin"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
)

// ---- hub-managed policies (D-022; docs/contracts/admin.md "Policies") ----

var policyTypes = []string{v1alpha1.HubPolicyTypeGlobalDefault, v1alpha1.HubPolicyTypeClusterDefault, v1alpha1.HubPolicyTypePolicy, v1alpha1.HubPolicyTypeWorkloadOverride}

func invalidPolicy(w http.ResponseWriter, r *http.Request, detail string) {
	badRequest(w, r, hubapi.CodeInvalidPolicy, detail)
}

// normalisePolicy turns a PolicyInput into the stored policy: reserved names and priorities by
// type, then the shared validator (api/v1alpha1/validate). It answers 400 itself.
func (s *Server) normalisePolicy(w http.ResponseWriter, r *http.Request, in hubapi.PolicyInput) (hubapi.AdminPolicy, bool) {
	p := hubapi.AdminPolicy{Type: in.Type, Name: in.Name, Description: in.Description, Clusters: slices.Clone(in.Clusters), Spec: in.Spec}
	if p.Clusters == nil {
		p.Clusters = []string{}
	}
	switch in.Type {
	case v1alpha1.HubPolicyTypeGlobalDefault:
		if len(p.Clusters) > 0 {
			invalidPolicy(w, r, "clusters: global defaults apply to every cluster")
			return p, false
		}
		p.Name, p.Priority = v1alpha1.GlobalDefaultsPolicyName, v1alpha1.PriorityGlobalDefault
	case v1alpha1.HubPolicyTypeClusterDefault:
		if len(p.Clusters) != 1 {
			invalidPolicy(w, r, "clusters: a cluster default names exactly one cluster")
			return p, false
		}
		p.Name, p.Priority = v1alpha1.ClusterDefaultsPolicyName, v1alpha1.PriorityClusterDefault
	case v1alpha1.HubPolicyTypePolicy:
		p.Priority = in.Spec.Priority
	case v1alpha1.HubPolicyTypeWorkloadOverride:
		invalidPolicy(w, r, "workload overrides are set with PUT /v1/workloads/{id}/override")
		return p, false
	default:
		invalidPolicy(w, r, fmt.Sprintf("type: must be one of %v", policyTypes[:3]))
		return p, false
	}
	p.Spec.Priority = p.Priority
	if errs := validate.HubPolicy(p.Type, p.Name, &p.Spec); len(errs) > 0 {
		invalidPolicy(w, r, errs.ToAggregate().Error())
		return p, false
	}
	if !s.knownClusters(w, r, p.Clusters) {
		return p, false
	}
	return p, true
}

// knownClusters answers 400 unless every cluster is registered and not deleted.
func (s *Server) knownClusters(w http.ResponseWriter, r *http.Request, names []string) bool {
	if len(names) == 0 {
		return true
	}
	clusters, err := s.Store.Clusters(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	for _, n := range names {
		if !slices.ContainsFunc(clusters, func(c store.Cluster) bool { return c.Name == n }) {
			badRequest(w, r, "unknown_cluster", "Unknown cluster "+n+".")
			return false
		}
	}
	return true
}

// listPolicies serves GET /v1/admin/policies.
func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.PolicyFilter{Cluster: q.Get("cluster"), Type: q.Get("type"), IncludeDeleted: q.Get("includeDeleted") == "true"}
	if f.Type != "" && !slices.Contains(policyTypes, f.Type) {
		badRequest(w, r, "invalid_type", "Unknown policy type "+f.Type+".")
		return
	}
	items, err := s.Store.ListPolicies(r.Context(), f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, hubapi.AdminPolicyList{Items: items})
}

// createPolicy serves POST /v1/admin/policies.
func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) {
	var in hubapi.PolicyInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	p, ok := s.normalisePolicy(w, r, in)
	if !ok {
		return
	}
	extra, ok := s.readinessGate(w, r, p.Clusters, setsAutoPatch(p.Spec), in.OverrideReadiness)
	if !ok {
		return
	}
	c := s.change(r, hubapi.ActionPolicyCreated, in.Reason)
	c.AfterExtra = extra
	out, err := s.Store.CreatePolicy(r.Context(), p, c)
	if err != nil {
		s.storeError(w, r, err, "policy")
		return
	}
	setETag(w, out.Version)
	writeJSONStatus(w, http.StatusCreated, out)
}

// getPolicy serves GET /v1/admin/policies/{policyId}.
func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetPolicy(r.Context(), r.PathValue("policyId"))
	if err != nil {
		s.storeError(w, r, err, "policy")
		return
	}
	setETag(w, p.Version)
	writeJSON(w, p)
}

// updatePolicy serves PUT /v1/admin/policies/{policyId}.
func (s *Server) updatePolicy(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.PolicyInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	cur, err := s.Store.GetPolicy(r.Context(), r.PathValue("policyId"))
	if err != nil {
		s.storeError(w, r, err, "policy")
		return
	}
	if in.Type != cur.Type {
		badRequest(w, r, hubapi.CodeTypeImmutable, "The type of a policy cannot change (it is "+cur.Type+").")
		return
	}
	var next hubapi.AdminPolicy
	if cur.Type == v1alpha1.HubPolicyTypeWorkloadOverride {
		k, _ := hubapi.ParseWorkloadID(cur.WorkloadID)
		var ok bool
		if next, ok = s.normaliseOverride(w, r, k, hubapi.WorkloadOverrideInput{Description: in.Description, Spec: in.Spec}); !ok {
			return
		}
	} else if next, ok = s.normalisePolicy(w, r, in); !ok {
		return
	}
	if cur.DeletedAt != nil {
		writeProblem(w, r, http.StatusConflict, "policy_deleted", "The policy is deleted; revert it to a version first.")
		return
	}
	gateClusters := next.Clusters
	if cur.Type == v1alpha1.HubPolicyTypeWorkloadOverride {
		k, _ := hubapi.ParseWorkloadID(cur.WorkloadID)
		gateClusters = []string{k.Cluster}
	}
	extra, ok := s.readinessGate(w, r, gateClusters, newAutoPatch(cur, next), in.OverrideReadiness)
	if !ok {
		return
	}
	ch := s.change(r, hubapi.ActionPolicyUpdated, in.Reason)
	ch.AfterExtra = extra
	out, err := s.Store.UpdatePolicy(r.Context(), cur.ID, ver, func(c hubapi.AdminPolicy, _ func(int) (hubapi.PolicyVersion, error)) (hubapi.AdminPolicy, string, int, error) {
		if c.DeletedAt != nil {
			return c, "", 0, store.ErrNotFound
		}
		c.Name, c.Description, c.Clusters, c.Priority, c.Spec = next.Name, next.Description, next.Clusters, next.Priority, next.Spec
		return c, hubapi.PolicyOpUpdate, 0, nil
	}, ch)
	if err != nil {
		s.storeError(w, r, err, "policy")
		return
	}
	setETag(w, out.Version)
	writeJSON(w, out)
}

// deletePolicy serves DELETE /v1/admin/policies/{policyId} (soft delete).
func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	out, err := s.Store.UpdatePolicy(r.Context(), r.PathValue("policyId"), ver, deleteMutation, s.change(r, hubapi.ActionPolicyDeleted, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "policy")
		return
	}
	setETag(w, out.Version)
	writeJSON(w, out)
}

func deleteMutation(c hubapi.AdminPolicy, _ func(int) (hubapi.PolicyVersion, error)) (hubapi.AdminPolicy, string, int, error) {
	if c.DeletedAt != nil {
		return c, "", 0, store.ErrNotFound
	}
	return c, hubapi.PolicyOpDelete, 0, nil
}

// revertPolicy serves POST /v1/admin/policies/{policyId}/revert: a new version with the content
// of an older one, undeleted.
func (s *Server) revertPolicy(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.PolicyRevert
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	var badVersion bool
	out, err := s.Store.UpdatePolicy(r.Context(), r.PathValue("policyId"), ver, func(c hubapi.AdminPolicy, version func(int) (hubapi.PolicyVersion, error)) (hubapi.AdminPolicy, string, int, error) {
		if in.Version < 1 || in.Version >= c.Version {
			badVersion = true
			return c, "", 0, errors.New("bad version")
		}
		v, err := version(in.Version)
		if errors.Is(err, store.ErrNotFound) {
			badVersion = true
		}
		if err != nil {
			return c, "", 0, err
		}
		c.Name, c.Description, c.Clusters, c.Priority, c.Spec = v.Name, v.Description, v.Clusters, v.Priority, v.Spec
		c.DeletedAt = nil
		return c, hubapi.PolicyOpRevert, in.Version, nil
	}, s.change(r, hubapi.ActionPolicyReverted, in.Reason))
	if badVersion {
		badRequest(w, r, "invalid_version", fmt.Sprintf("version %d is not an earlier version of the policy.", in.Version))
		return
	}
	if err != nil {
		s.storeError(w, r, err, "policy")
		return
	}
	setETag(w, out.Version)
	writeJSON(w, out)
}

// listPolicyVersions serves GET /v1/admin/policies/{policyId}/versions.
func (s *Server) listPolicyVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("policyId")
	if _, err := s.Store.GetPolicy(r.Context(), id); err != nil {
		s.storeError(w, r, err, "policy")
		return
	}
	limit, before, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, more, err := s.Store.PolicyVersions(r.Context(), id, before, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := hubapi.PolicyVersionList{Items: items}
	if more && len(items) > 0 {
		out.Continue = encodeCursor(cursor{Offset: items[len(items)-1].Version})
	}
	writeJSON(w, out)
}

// getPolicyInventory serves GET /v1/admin/policy-inventory.
func (s *Server) getPolicyInventory(w http.ResponseWriter, r *http.Request) {
	only := r.URL.Query().Get("cluster")
	rows, err := s.Store.ClusterRows(r.Context(), false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := hubapi.PolicyInventoryList{Items: []hubapi.ClusterPolicyInventory{}}
	for _, c := range rows {
		if only != "" && c.Name != only {
			continue
		}
		out.Items = append(out.Items, clusterInventory(c))
	}
	writeJSON(w, out)
}

func clusterInventory(c store.ClusterRow) hubapi.ClusterPolicyInventory {
	inv := hubapi.ClusterPolicyInventory{Cluster: c.Name, ReportedAt: c.PolicyInventoryAt, ConfigVersion: c.AppliedConfigVersion,
		Items: []hubapi.PolicyInventoryItem{}}
	if c.PolicyInventory != nil {
		inv.Items = *c.PolicyInventory
	}
	return inv
}

// ---- effective policy and preview ----

// policyContext is what policy resolution needs for a set of clusters.
type policyContext struct {
	hub      []hubapi.AdminPolicy
	clusters map[string]store.ClusterRow
}

func (s *Server) loadPolicyContext(r *http.Request) (*policyContext, error) {
	hub, err := s.Store.ListPolicies(r.Context(), store.PolicyFilter{})
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.ClusterRows(r.Context(), false)
	if err != nil {
		return nil, err
	}
	pc := &policyContext{hub: hub, clusters: map[string]store.ClusterRow{}}
	for _, c := range rows {
		pc.clusters[c.Name] = c
	}
	return pc, nil
}

func hubCandidate(p hubapi.AdminPolicy) admin.Candidate {
	return admin.Candidate{Name: p.Name, Origin: hubapi.PolicySourceHub, Type: p.Type, PolicyID: p.ID, Clusters: p.Clusters,
		WorkloadID: p.WorkloadID, Spec: p.Spec}
}

// candidates returns what applies on a cluster: the hub policies (none with policySource git)
// and the Git CRs the agent reported. hub may replace the stored hub policies (preview).
func (pc *policyContext) candidates(cluster string, hub []admin.Candidate) []admin.Candidate {
	c := pc.clusters[cluster]
	var out []admin.Candidate
	if c.PolicySource != hubapi.PolicySourceGit {
		out = append(out, hub...)
	}
	if c.PolicyInventory != nil {
		for _, it := range *c.PolicyInventory {
			if it.Source == hubapi.PolicySourceGit && it.Spec != nil {
				out = append(out, admin.Candidate{Name: it.Name, Origin: hubapi.PolicySourceGit, Clusters: []string{cluster}, Spec: *it.Spec})
			}
		}
	}
	return out
}

func (pc *policyContext) hubCandidates() []admin.Candidate {
	out := make([]admin.Candidate, 0, len(pc.hub))
	for _, p := range pc.hub {
		out = append(out, hubCandidate(p))
	}
	return out
}

// getEffectivePolicy serves GET /v1/workloads/{id}/effective-policy.
func (s *Server) getEffectivePolicy(w http.ResponseWriter, r *http.Request) {
	v := s.workloadFromPath(w, r)
	if v == nil {
		return
	}
	pc, err := s.loadPolicyContext(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ep := admin.Resolve(admin.Workload{Key: v.Key, Status: v.Status}, pc.candidates(v.Key.Cluster, pc.hubCandidates()))
	if c, ok := pc.clusters[v.Key.Cluster]; ok {
		if want, err := s.clusterConfig(r, c); err == nil && c.AppliedConfigVersion != want.ConfigVersion {
			ep.Warnings = append(ep.Warnings, "agent has not applied config "+want.ConfigVersion+" yet")
		}
	}
	writeJSON(w, ep)
}

// previewPolicyChange serves POST /v1/admin/preview (read-only).
func (s *Server) previewPolicyChange(w http.ResponseWriter, r *http.Request) {
	var in hubapi.PreviewRequest
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Changes) == 0 || len(in.Changes) > 500 {
		badRequest(w, r, "invalid_changes", "changes: 1 to 500 changes are required.")
		return
	}
	pc, err := s.loadPolicyContext(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	before := pc.hubCandidates()
	after, err := admin.ApplyChanges(before, in.Changes)
	var inv *admin.InvalidError
	switch {
	case errors.As(err, &inv):
		badRequest(w, r, inv.Code, inv.Error())
		return
	case errors.Is(err, admin.ErrUnknownPolicy):
		writeProblem(w, r, http.StatusNotFound, "not_found", err.Error())
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	views, err := s.Store.LoadWorkloads(r.Context(), store.WorkloadFilter{})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byCluster := map[string][]admin.PreviewWorkload{}
	var names []string
	for _, v := range views {
		if _, ok := byCluster[v.Key.Cluster]; !ok {
			names = append(names, v.Key.Cluster)
		}
		pw := admin.PreviewWorkload{Workload: admin.Workload{Key: v.Key, Status: v.Status}}
		if v.Rec != nil {
			sv := v.Rec.MonthlySavingsUSD
			pw.Savings = &sv
		}
		byCluster[v.Key.Cluster] = append(byCluster[v.Key.Cluster], pw)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = admin.DefaultPreviewLimit
	}
	limit = min(limit, admin.MaxPreviewLimit)
	out := hubapi.ImpactPreview{Items: []hubapi.ImpactItem{}}
	for _, c := range names {
		p := admin.Preview(byCluster[c], pc.candidates(c, before), pc.candidates(c, after), admin.MaxPreviewLimit)
		out.WorkloadsEvaluated += p.WorkloadsEvaluated
		out.WorkloadsChanged += p.WorkloadsChanged
		out.SavingsIntoAutoPatchUSD = out.SavingsIntoAutoPatchUSD.Add(p.SavingsIntoAutoPatchUSD)
		out.SavingsOutOfAutoPatchUSD = out.SavingsOutOfAutoPatchUSD.Add(p.SavingsOutOfAutoPatchUSD)
		out.Items = append(out.Items, p.Items...)
		out.Truncated = out.Truncated || p.Truncated
		for _, wn := range p.Warnings {
			if !slices.Contains(out.Warnings, wn) && len(out.Warnings) < 20 {
				out.Warnings = append(out.Warnings, wn)
			}
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := savingsTotal(out.Items[i].MonthlySavingsUSD), savingsTotal(out.Items[j].MonthlySavingsUSD)
		if a != b {
			return a > b
		}
		return out.Items[i].WorkloadID < out.Items[j].WorkloadID
	})
	if len(out.Items) > limit {
		out.Items, out.Truncated = out.Items[:limit], true
	}
	out.SavingsIntoAutoPatchUSD = roundSavings(out.SavingsIntoAutoPatchUSD)
	out.SavingsOutOfAutoPatchUSD = roundSavings(out.SavingsOutOfAutoPatchUSD)
	writeJSON(w, out)
}

func savingsTotal(s *hubapi.Savings) float64 {
	if s == nil {
		return 0
	}
	return s.Total
}

// ---- workload override and approve once ----

// normaliseOverride builds the workload's override policy from the input (fixed name, priority
// and selector). It answers 400 itself.
func (s *Server) normaliseOverride(w http.ResponseWriter, r *http.Request, k hubapi.WorkloadKey, in hubapi.WorkloadOverrideInput) (hubapi.AdminPolicy, bool) {
	sel := in.Spec.Selector
	if in.Spec.Priority != 0 || len(sel.Namespaces)+len(sel.ExcludeNamespaces)+len(sel.MatchLabels)+len(sel.Kinds)+len(sel.Workloads) > 0 {
		badRequest(w, r, hubapi.CodeInvalidOverride, "An override sets neither selector nor priority: the hub sets them.")
		return hubapi.AdminPolicy{}, false
	}
	kind := v1alpha1.WorkloadKind(k.Kind)
	p := hubapi.AdminPolicy{
		Type: v1alpha1.HubPolicyTypeWorkloadOverride, Name: v1alpha1.OverridePolicyName(k.Namespace, kind, k.Name),
		Description: in.Description, Clusters: []string{k.Cluster}, WorkloadID: k.ID(), Priority: v1alpha1.PriorityWorkloadOverride,
		Spec: in.Spec,
	}
	p.Spec.Priority = p.Priority
	p.Spec.Selector = validate.OverrideSelector(k.Namespace, kind, k.Name)
	if errs := validate.HubPolicy(p.Type, p.Name, &p.Spec); len(errs) > 0 {
		badRequest(w, r, hubapi.CodeInvalidOverride, errs.ToAggregate().Error())
		return p, false
	}
	return p, true
}

// newAutoPatch reports whether an update enables auto-patch: next sets it and cur did not, or
// next reaches clusters cur did not (readiness gating, D-025).
func newAutoPatch(cur, next hubapi.AdminPolicy) bool {
	if !setsAutoPatch(next.Spec) {
		return false
	}
	if !setsAutoPatch(cur.Spec) || cur.DeletedAt != nil {
		return true
	}
	if len(next.Clusters) == 0 {
		return len(cur.Clusters) != 0
	}
	if len(cur.Clusters) == 0 {
		return false
	}
	for _, c := range next.Clusters {
		if !slices.Contains(cur.Clusters, c) {
			return true
		}
	}
	return false
}

func setsAutoPatch(spec v1alpha1.RightsizingPolicySpec) bool {
	return (spec.CPU != nil && spec.CPU.Mode == v1alpha1.ModeAutoPatch) || (spec.Memory != nil && spec.Memory.Mode == v1alpha1.ModeAutoPatch)
}

// prodGuard answers 403 admin_required when a non-admin sets auto-patch on a prod cluster or
// sends overrideReadiness (an admin-only flag, D-025).
func (s *Server) prodGuard(w http.ResponseWriter, r *http.Request, cluster string, autoPatch, overrideReadiness bool) bool {
	p := PrincipalFrom(r.Context())
	if p.IsAdmin() {
		return true
	}
	if overrideReadiness {
		writeProblem(w, r, http.StatusForbidden, hubapi.CodeAdminRequired, "Only an admin may send overrideReadiness.")
		return false
	}
	if !autoPatch {
		return true
	}
	c, err := s.Store.GetCluster(r.Context(), cluster)
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if c.Environment == hubapi.EnvironmentProd {
		writeProblem(w, r, http.StatusForbidden, hubapi.CodeAdminRequired, "Enabling auto-patch on the prod cluster "+cluster+" needs an admin.")
		return false
	}
	return true
}

// getWorkloadOverride serves GET /v1/workloads/{id}/override.
func (s *Server) getWorkloadOverride(w http.ResponseWriter, r *http.Request) {
	v := s.workloadFromPath(w, r)
	if v == nil {
		return
	}
	p, err := s.Store.GetOverride(r.Context(), v.Key)
	if err != nil {
		s.storeError(w, r, err, "override")
		return
	}
	setETag(w, p.Version)
	writeJSON(w, p)
}

// putWorkloadOverride serves PUT /v1/workloads/{id}/override.
func (s *Server) putWorkloadOverride(w http.ResponseWriter, r *http.Request) {
	v := s.workloadFromPath(w, r)
	if v == nil {
		return
	}
	ifv, ok := optionalIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.WorkloadOverrideInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	next, ok := s.normaliseOverride(w, r, v.Key, in)
	if !ok || !s.prodGuard(w, r, v.Key.Cluster, setsAutoPatch(next.Spec), in.OverrideReadiness) {
		return
	}
	cur, err := s.Store.GetOverride(r.Context(), v.Key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.storeError(w, r, err, "override")
		return
	}
	gated := setsAutoPatch(next.Spec) && (errors.Is(err, store.ErrNotFound) || cur.DeletedAt != nil || !setsAutoPatch(cur.Spec))
	extra, ok := s.readinessGate(w, r, []string{v.Key.Cluster}, gated, in.OverrideReadiness)
	if !ok {
		return
	}
	change := s.change(r, hubapi.ActionOverrideSet, in.Reason)
	change.AfterExtra = extra
	var out hubapi.AdminPolicy
	switch {
	case errors.Is(err, store.ErrNotFound):
		out, err = s.Store.CreatePolicy(r.Context(), next, change)
	case err != nil:
	case ifv == nil:
		writeProblem(w, r, http.StatusPreconditionRequired, hubapi.CodePreconditionRequired, "The workload has an override: replacing it needs If-Match.")
		return
	default:
		out, err = s.Store.UpdatePolicy(r.Context(), cur.ID, *ifv, func(c hubapi.AdminPolicy, _ func(int) (hubapi.PolicyVersion, error)) (hubapi.AdminPolicy, string, int, error) {
			if c.DeletedAt != nil {
				return c, "", 0, store.ErrVersionConflict
			}
			c.Description, c.Spec = next.Description, next.Spec
			return c, hubapi.PolicyOpUpdate, 0, nil
		}, change)
	}
	if err != nil {
		s.storeError(w, r, err, "override")
		return
	}
	setETag(w, out.Version)
	writeJSON(w, out)
}

// deleteWorkloadOverride serves DELETE /v1/workloads/{id}/override.
func (s *Server) deleteWorkloadOverride(w http.ResponseWriter, r *http.Request) {
	v := s.workloadFromPath(w, r)
	if v == nil {
		return
	}
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	cur, err := s.Store.GetOverride(r.Context(), v.Key)
	if err == nil {
		_, err = s.Store.UpdatePolicy(r.Context(), cur.ID, ver, deleteMutation, s.change(r, hubapi.ActionOverrideCleared, in.Reason))
	}
	if err != nil {
		s.storeError(w, r, err, "override")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// approveRecommendation serves POST /v1/workloads/{id}/approve (D-024).
func (s *Server) approveRecommendation(w http.ResponseWriter, r *http.Request) {
	v := s.workloadFromPath(w, r)
	if v == nil {
		return
	}
	var in hubapi.ApprovalRequest
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if !s.prodGuard(w, r, v.Key.Cluster, false, in.OverrideReadiness) {
		return
	}
	if v.Rec == nil || v.Rec.ID != strings.ToLower(in.RecommendationID) {
		writeProblem(w, r, http.StatusConflict, hubapi.CodeRecommendationStale, "Only the workload's latest recommendation can be approved.")
		return
	}
	if m := v.Modes(); m.CPU != hubapi.ModeAudit && m.Memory != hubapi.ModeAudit {
		writeProblem(w, r, http.StatusConflict, hubapi.CodeNotInAudit, "Neither CPU nor memory of the workload is in audit mode.")
		return
	}
	now := s.now().UTC()
	exp := now.Add(hubapi.ApprovalDefaultTTL)
	if in.ExpiresAt != nil {
		if !in.ExpiresAt.After(now) || in.ExpiresAt.Sub(now) > hubapi.ApprovalMaxTTL {
			badRequest(w, r, "invalid_expiry", fmt.Sprintf("expiresAt must be in the future and at most %s away.", hubapi.ApprovalMaxTTL))
			return
		}
		exp = in.ExpiresAt.UTC()
	}
	extra, ok := s.readinessGate(w, r, []string{v.Key.Cluster}, true, in.OverrideReadiness)
	if !ok {
		return
	}
	c := s.change(r, hubapi.ActionWorkloadApproved, in.Reason)
	c.AfterExtra = extra
	a, err := s.Store.CreateApproval(r.Context(), v.Key, v.Rec.ID, exp, c.Actor, in.Reason, c)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, a)
}

// approvalAndOverride adds the latest approval and the override to a workload detail.
func (s *Server) approvalAndOverride(r *http.Request, d *hubapi.WorkloadDetail, k hubapi.WorkloadKey) error {
	a, err := s.Store.LatestApproval(r.Context(), k, s.now())
	if err != nil {
		return err
	}
	d.Approval = a
	p, err := s.Store.GetOverride(r.Context(), k)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	d.Override = &p
	return nil
}
