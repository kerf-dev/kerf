package hub

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/config"
	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// ---- agent configuration (GET /v1/clusters/{cluster}/config) ----

// automationFor returns the effective pause of a cluster: the fleet pause wins.
func automationFor(cluster string, all map[string]hubapi.Automation) (hubapi.ConfigFlags, hubapi.Automation) {
	own := all[cluster]
	if fleet := all[hubapi.FleetScope]; fleet.Paused {
		return hubapi.ConfigFlags{Paused: true, PauseScope: hubapi.PauseScopeFleet, PauseReason: fleet.Reason, PausedAt: fleet.Since,
			ResumeAt: fleet.ResumeAt}, own
	}
	if own.Paused {
		return hubapi.ConfigFlags{Paused: true, PauseScope: hubapi.PauseScopeCluster, PauseReason: own.Reason, PausedAt: own.Since,
			ResumeAt: own.ResumeAt}, own
	}
	return hubapi.ConfigFlags{}, own
}

// expirePauses ends pauses whose resumeAt passed (recorded as AutomationResumed by system).
func (s *Server) expirePauses(r *http.Request) {
	scopes, err := s.Store.ExpirePauses(r.Context(), s.now())
	if err != nil {
		s.Log.WarnContext(r.Context(), "expire pauses", "err", err)
	}
	for _, sc := range scopes {
		s.Log.InfoContext(r.Context(), "automation resumed (resumeAt reached)", "scope", sc)
	}
}

// clusterConfig builds the configuration of a cluster's agent.
func (s *Server) clusterConfig(r *http.Request, c store.ClusterRow) (hubapi.ClusterConfig, error) {
	cfg := hubapi.ClusterConfig{Cluster: c.Name, GeneratedAt: s.now().UTC(), PolicySource: c.PolicySource,
		Policies: []hubapi.ConfigPolicy{}, Approvals: []hubapi.ConfigApproval{}}
	if cfg.PolicySource != hubapi.PolicySourceGit {
		ps, err := s.Store.ListPolicies(r.Context(), store.PolicyFilter{Cluster: c.Name})
		if err != nil {
			return cfg, err
		}
		for _, p := range ps {
			if p.Type == v1alpha1.HubPolicyTypeClusterDefault && (len(p.Clusters) != 1 || p.Clusters[0] != c.Name) {
				continue
			}
			spec := p.Spec
			spec.Priority = p.Priority
			cfg.Policies = append(cfg.Policies, hubapi.ConfigPolicy{ID: p.ID, Name: p.Name, Type: p.Type, Version: p.Version, Priority: p.Priority, Spec: spec})
		}
	}
	all, err := s.Store.Automations(r.Context())
	if err != nil {
		return cfg, err
	}
	cfg.Flags, _ = automationFor(c.Name, all)
	if cfg.Approvals, err = s.Store.PendingApprovals(r.Context(), c.Name, s.now()); err != nil {
		return cfg, err
	}
	if err := s.readinessFlags(r.Context(), &cfg); err != nil {
		return cfg, err
	}
	cfg.ConfigVersion = config.Version(cfg)
	return cfg, nil
}

// getClusterConfig serves GET /v1/clusters/{cluster}/config (agent, ETag).
func (s *Server) getClusterConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("cluster")
	s.expirePauses(r)
	c, err := s.Store.GetCluster(r.Context(), name)
	if err != nil {
		s.storeError(w, r, err, "cluster")
		return
	}
	cfg, err := s.clusterConfig(r, c)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.Store.RecordConfigPull(r.Context(), name, s.now()); err != nil {
		s.Log.WarnContext(r.Context(), "record config pull", "cluster", name, "err", err)
	}
	etag := `"` + cfg.ConfigVersion + `"`
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, cfg)
}

// ---- clusters ----

func (s *Server) clusterAdmin(r *http.Request, c store.ClusterRow, all map[string]hubapi.Automation) (hubapi.ClusterAdmin, error) {
	created := c.CreatedAt
	out := hubapi.ClusterAdmin{
		Name: c.Name, Description: c.Description, Environment: c.Environment, Owner: c.Owner, PolicySource: c.PolicySource,
		CreatedAt: &created, UpdatedAt: c.UpdatedAt, UpdatedBy: c.UpdatedBy, DeletedAt: c.DeletedAt, Version: c.Version,
		Provider: c.Provider, Region: c.Region, Metrics: hubapi.DefaultMetricsBinding(c.Name),
		Readiness: hubapi.ReadinessSummary{State: c.ReadinessState, CheckedAt: c.ReadinessCheckedAt},
	}
	if c.Binding != nil {
		out.Metrics = *c.Binding
	}
	if s.Onb != nil {
		sum, err := s.readinessSummary(r.Context(), c.Name)
		if err != nil {
			return out, err
		}
		out.Readiness = sum
	}
	if out.Readiness.State == "" {
		out.Readiness.State = "Registered"
	}
	flags, own := automationFor(c.Name, all)
	out.Automation, out.Paused = own, flags.Paused
	out.Agent = hubapi.AgentConfigSync{AgentVersion: c.AgentVersion, LastHeartbeatAt: c.LastSeenAt, LastConfigPullAt: c.ConfigPulledAt,
		AppliedConfigVersion: c.AppliedConfigVersion}
	cfg, err := s.clusterConfig(r, c)
	if err != nil {
		return out, err
	}
	out.Agent.ConfigVersion = cfg.ConfigVersion
	out.Agent.InSync = c.AppliedConfigVersion == cfg.ConfigVersion
	inv := clusterInventory(c)
	out.PolicyInventory = &inv
	return out, nil
}

func (s *Server) writeClusterAdmin(w http.ResponseWriter, r *http.Request, c store.ClusterRow, status int) {
	all, err := s.Store.Automations(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := s.clusterAdmin(r, c, all)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	setETag(w, c.Version)
	writeJSONStatus(w, status, out)
}

func validEnum(w http.ResponseWriter, r *http.Request, field, v string, allowed ...string) bool {
	if !slices.Contains(allowed, v) {
		badRequest(w, r, "invalid_"+field, fmt.Sprintf("%s must be one of %v.", field, allowed))
		return false
	}
	return true
}

func validBinding(w http.ResponseWriter, r *http.Request, b *hubapi.MetricsBinding) bool {
	if b == nil {
		return true
	}
	if err := datasource.ValidateBinding(*b); err != nil {
		badRequest(w, r, "invalid_metrics", "metrics: "+err.Error())
		return false
	}
	for i, m := range b.ExtraMatchers {
		b.ExtraMatchers[i], _ = datasource.NormalizeMatcher(m)
	}
	if b.DatasourceID == "" {
		badRequest(w, r, "invalid_metrics", "metrics.datasourceId is required.")
		return false
	}
	for _, c := range b.ClusterLabel {
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			badRequest(w, r, "invalid_metrics", "metrics.clusterLabel must be a Prometheus label name or empty.")
			return false
		}
	}
	return true
}

// createCluster serves POST /v1/admin/clusters.
func (s *Server) createCluster(w http.ResponseWriter, r *http.Request) {
	var in hubapi.ClusterCreate
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if !hubapi.ClusterNameRE.MatchString(in.Name) {
		badRequest(w, r, "invalid_cluster", "Invalid cluster name (DNS label).")
		return
	}
	if in.PolicySource == "" {
		in.PolicySource = hubapi.PolicySourceBoth
	}
	if !validEnum(w, r, "environment", in.Environment, hubapi.EnvironmentProd, hubapi.EnvironmentNonProd) ||
		!validEnum(w, r, "policySource", in.PolicySource, hubapi.PolicySourceHub, hubapi.PolicySourceGit, hubapi.PolicySourceBoth) ||
		!validBinding(w, r, in.Metrics) {
		return
	}
	var value string
	var hash []byte
	if in.CreateAgentToken == nil || *in.CreateAgentToken {
		v, err := auth.RandomToken("ro-", 32)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		value, hash = v, store.HashToken(v)
	}
	res, err := s.Store.CreateCluster(r.Context(), in, hash, s.Cfg.PrometheusURL, s.change(r, hubapi.ActionClusterCreated, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "cluster")
		return
	}
	all, err := s.Store.Automations(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ca, err := s.clusterAdmin(r, res.Cluster, all)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := hubapi.ClusterCreated{Cluster: ca}
	if res.Token != nil {
		out.AgentToken = &hubapi.TokenCreated{Token: *res.Token, Value: value}
	}
	s.triggerChecks(in.Name)
	s.Log.InfoContext(r.Context(), "cluster registered", "cluster", in.Name, "by", PrincipalFrom(r.Context()).Actor().Name)
	setETag(w, res.Cluster.Version)
	writeJSONStatus(w, http.StatusCreated, out)
}

// getClusterAdmin serves GET /v1/admin/clusters/{cluster}.
func (s *Server) getClusterAdmin(w http.ResponseWriter, r *http.Request) {
	s.expirePauses(r)
	c, err := s.Store.GetCluster(r.Context(), r.PathValue("cluster"))
	if err != nil {
		s.storeError(w, r, err, "cluster")
		return
	}
	s.writeClusterAdmin(w, r, c, http.StatusOK)
}

// updateCluster serves PATCH /v1/admin/clusters/{cluster}.
func (s *Server) updateCluster(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ClusterUpdate
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if (in.Environment != nil && !validEnum(w, r, "environment", *in.Environment, hubapi.EnvironmentProd, hubapi.EnvironmentNonProd)) ||
		(in.PolicySource != nil && !validEnum(w, r, "policySource", *in.PolicySource, hubapi.PolicySourceHub, hubapi.PolicySourceGit, hubapi.PolicySourceBoth)) ||
		!validBinding(w, r, in.Metrics) {
		return
	}
	c, err := s.Store.UpdateCluster(r.Context(), r.PathValue("cluster"), ver, in, s.Cfg.PrometheusURL, s.change(r, hubapi.ActionClusterUpdated, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "cluster")
		return
	}
	if in.Metrics != nil {
		s.triggerChecks(c.Name)
	}
	s.writeClusterAdmin(w, r, c, http.StatusOK)
}

// deleteCluster serves DELETE /v1/admin/clusters/{cluster} (soft delete, tokens revoked).
func (s *Server) deleteCluster(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if err := s.Store.DeleteCluster(r.Context(), r.PathValue("cluster"), ver, s.change(r, hubapi.ActionClusterDeleted, in.Reason)); err != nil {
		s.storeError(w, r, err, "cluster")
		return
	}
	s.forgetTokens(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// ---- automation (pause / resume) ----

func (s *Server) setAutomation(w http.ResponseWriter, r *http.Request, scope string) {
	ifv, ok := optionalIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.AutomationUpdate
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	now := s.now()
	if in.ResumeAt != nil && (!in.Paused || !in.ResumeAt.After(now)) {
		badRequest(w, r, "invalid_resume_at", "resumeAt must be in the future and only comes with paused: true.")
		return
	}
	if !in.Paused && !PrincipalFrom(r.Context()).IsAdmin() {
		prod := scope == hubapi.FleetScope
		if !prod {
			c, err := s.Store.GetCluster(r.Context(), scope)
			if err != nil {
				s.storeError(w, r, err, "cluster")
				return
			}
			prod = c.Environment == hubapi.EnvironmentProd
		}
		if prod {
			writeProblem(w, r, http.StatusForbidden, hubapi.CodeAdminRequired, "Resuming the fleet or a prod cluster needs an admin.")
			return
		}
	}
	action := hubapi.ActionAutomationPaused
	if !in.Paused {
		action = hubapi.ActionAutomationResumed
	}
	a, err := s.Store.SetAutomation(r.Context(), scope, ifv, in, s.change(r, action, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "automation state")
		return
	}
	s.Log.InfoContext(r.Context(), "automation changed", "scope", scope, "paused", a.Paused, "by", PrincipalFrom(r.Context()).Actor().Name)
	setETag(w, a.Version)
	writeJSON(w, a)
}

// setClusterAutomation serves PUT /v1/clusters/{cluster}/automation.
func (s *Server) setClusterAutomation(w http.ResponseWriter, r *http.Request) {
	s.setAutomation(w, r, r.PathValue("cluster"))
}

// setFleetAutomation serves PUT /v1/admin/automation.
func (s *Server) setFleetAutomation(w http.ResponseWriter, r *http.Request) {
	s.setAutomation(w, r, hubapi.FleetScope)
}

// getFleetAutomation serves GET /v1/admin/automation.
func (s *Server) getFleetAutomation(w http.ResponseWriter, r *http.Request) {
	s.expirePauses(r)
	all, err := s.Store.Automations(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	clusters, err := s.Store.Clusters(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := hubapi.FleetAutomation{Fleet: all[hubapi.FleetScope], Clusters: []hubapi.ClusterAutomationState{}}
	for _, c := range clusters {
		flags, own := automationFor(c.Name, all)
		out.Clusters = append(out.Clusters, hubapi.ClusterAutomationState{Cluster: c.Name, Automation: own, Paused: flags.Paused})
	}
	setETag(w, out.Fleet.Version)
	writeJSON(w, out)
}

// ---- tokens ----

// listTokens serves GET /v1/admin/tokens.
func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.Store.ListTokens(r.Context(), q.Get("cluster"), q.Get("includeRevoked") == "true")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, hubapi.TokenList{Items: items})
}

// createToken serves POST /v1/admin/tokens.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var in hubapi.TokenCreate
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if !validEnum(w, r, "role", in.Role, hubapi.RoleAgent, hubapi.RoleUI, hubapi.RoleAdmin, hubapi.RolePricing) {
		return
	}
	if (in.Role == hubapi.RoleAgent) != (in.Cluster != "") {
		badRequest(w, r, "invalid_cluster", "An agent token needs a cluster; ui, admin and pricing tokens have none.")
		return
	}
	if in.Cluster != "" && !s.knownClusters(w, r, []string{in.Cluster}) {
		return
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.now()) {
		badRequest(w, r, "invalid_expiry", "expiresAt must be in the future.")
		return
	}
	if len(in.Description) > 256 {
		badRequest(w, r, "invalid_description", "description is at most 256 characters.")
		return
	}
	value, err := auth.RandomToken("ro-", 32)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	t, err := s.Store.CreateToken(r.Context(), store.HashToken(value), in, s.change(r, hubapi.ActionTokenCreated, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "token")
		return
	}
	writeJSONStatus(w, http.StatusCreated, hubapi.TokenCreated{Token: t, Value: value})
}

// revokeToken serves DELETE /v1/admin/tokens/{tokenId}.
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("tokenId"), 10, 64)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Unknown token.")
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	t, err := s.Store.RevokeToken(r.Context(), id, s.change(r, hubapi.ActionTokenRevoked, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "token")
		return
	}
	s.forgetTokens(r.Context())
	writeJSON(w, t)
}

// ---- users ----

// usernameRE is the Username schema.
var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)

// listUsers serves GET /v1/admin/users.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListUsers(r.Context(), r.URL.Query().Get("includeDeleted") == "true")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, hubapi.UserList{Items: items})
}

// createUser serves POST /v1/admin/users.
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in hubapi.UserCreate
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if !usernameRE.MatchString(in.Username) {
		badRequest(w, r, "invalid_username", "Invalid username (lowercase letters, digits, . _ -; 2-63 characters).")
		return
	}
	if !validEnum(w, r, "role", in.Role, hubapi.UserRoles...) {
		return
	}
	hash, err := auth.HashPassword(in.Password, s.Cfg.Auth.BcryptCost)
	if err != nil {
		badRequest(w, r, "invalid_password", err.Error())
		return
	}
	in.Password = ""
	must := in.MustChangePassword == nil || *in.MustChangePassword
	u, err := s.Store.CreateUser(r.Context(), in, hash, must, s.change(r, hubapi.ActionUserCreated, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	setETag(w, u.Version)
	writeJSONStatus(w, http.StatusCreated, u)
}

// getUser serves GET /v1/admin/users/{username}.
func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.GetUser(r.Context(), r.PathValue("username"))
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	setETag(w, u.Version)
	writeJSON(w, u.User)
}

// updateUser serves PATCH /v1/admin/users/{username}.
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.UserUpdate
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if in.Role != nil && !validEnum(w, r, "role", *in.Role, hubapi.UserRoles...) {
		return
	}
	u, err := s.Store.UpdateUser(r.Context(), r.PathValue("username"), ver, in, s.change(r, hubapi.ActionUserUpdated, in.Reason))
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	setETag(w, u.Version)
	writeJSON(w, u)
}

// deleteUser serves DELETE /v1/admin/users/{username}.
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	if err := s.Store.DeleteUser(r.Context(), r.PathValue("username"), ver, s.change(r, hubapi.ActionUserDeleted, in.Reason)); err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resetUserPassword serves POST /v1/admin/users/{username}/reset-password.
func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	var in hubapi.PasswordReset
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	out := hubapi.PasswordResetResult{MustChangePassword: true}
	pw := in.Password
	if pw == "" {
		var err error
		if pw, err = auth.TemporaryPassword(); err != nil {
			s.internal(w, r, err)
			return
		}
		out.TemporaryPassword = pw
	}
	hash, err := auth.HashPassword(pw, s.Cfg.Auth.BcryptCost)
	if err != nil {
		badRequest(w, r, "invalid_password", err.Error())
		return
	}
	c := s.change(r, hubapi.ActionPasswordReset, in.Reason)
	if err := s.Store.SetPassword(r.Context(), r.PathValue("username"), hash, true, nil, c.Time, &c); err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	writeJSON(w, out)
}

// ---- config audit ----

// queryConfigAudit serves GET /v1/audit/config.
func (s *Server) queryConfigAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"), 100, 1000)
	if err != nil {
		badRequest(w, r, "invalid_limit", "limit "+err.Error())
		return
	}
	cq := store.ConfigQuery{Cluster: q.Get("cluster"), TargetTypes: q["targetType"], TargetID: q.Get("targetId"), Actor: q.Get("actor"),
		Actions: q["action"], Desc: q.Get("order") != "asc", Limit: limit}
	for _, t := range cq.TargetTypes {
		if !slices.Contains(hubapi.ConfigTargetTypes, t) {
			badRequest(w, r, "invalid_target_type", "Unknown targetType "+t+".")
			return
		}
	}
	for _, a := range cq.Actions {
		if !slices.Contains(hubapi.ConfigActions, a) {
			badRequest(w, r, "invalid_action", "Unknown action "+a+".")
			return
		}
	}
	if o := q.Get("order"); o != "" && o != "asc" && o != "desc" {
		badRequest(w, r, "invalid_order", "order must be asc or desc.")
		return
	}
	if id := q.Get("workloadId"); id != "" {
		k, err := hubapi.ParseWorkloadID(id)
		if err != nil {
			badRequest(w, r, "invalid_workload_id", err.Error())
			return
		}
		cq.Workload = &k
	}
	if cq.From, err = timeParam(q.Get("from")); err != nil {
		badRequest(w, r, "invalid_from", err.Error())
		return
	}
	if cq.To, err = timeParam(q.Get("to")); err != nil {
		badRequest(w, r, "invalid_to", err.Error())
		return
	}
	key := strings.Join([]string{cq.Cluster, strings.Join(cq.TargetTypes, ","), cq.TargetID, q.Get("workloadId"), cq.Actor,
		strings.Join(cq.Actions, ","), q.Get("from"), q.Get("to"), q.Get("order")}, "|")
	if c := q.Get("continue"); c != "" {
		cur, err := decodeCursor(c)
		if err != nil || cur.Q != key || cur.Time.IsZero() {
			badRequest(w, r, "invalid_continue", "Invalid continue token.")
			return
		}
		cq.AfterTime, cq.AfterID = &cur.Time, cur.ID
	}
	items, more, err := s.Store.QueryConfigChanges(r.Context(), cq)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := hubapi.ConfigChangeList{Items: items}
	if more && len(items) > 0 {
		last := items[len(items)-1]
		out.Continue = encodeCursor(cursor{Time: last.Time, ID: last.ID, Q: key})
	}
	writeJSON(w, out)
}

// workloadConfigChanges returns the config changes of a workload (override, approval), newest first.
func (s *Server) workloadConfigChanges(r *http.Request, k hubapi.WorkloadKey, from, to time.Time, limit int) ([]hubapi.ConfigChange, error) {
	items, _, err := s.Store.QueryConfigChanges(r.Context(), store.ConfigQuery{Workload: &k, From: &from, To: &to, Desc: true, Limit: limit})
	return items, err
}
