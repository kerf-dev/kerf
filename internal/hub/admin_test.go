package hub

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// ---- session helpers ----

type session struct{ cookie, csrf string }

const testPassword = "correct horse battery"

// seedUsers creates viewer, operator and admin (password testPassword) through the dev seed.
func (e *testEnv) seedUsers() {
	e.t.Helper()
	cfg := e.srv.Cfg
	cfg.DevSeed.Enabled = true
	for _, r := range hubapi.UserRoles {
		cfg.DevSeed.Users = append(cfg.DevSeed.Users, DevUser{Username: r, DisplayName: strings.ToUpper(r[:1]) + r[1:], Role: r, Password: testPassword})
	}
	if err := BootstrapUsers(e.ctx, e.st, cfg, discardLog()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) login(user, pw string) (session, hubapi.Me) {
	e.t.Helper()
	r := e.do(http.MethodPost, "/v1/auth/login", "", hubapi.LoginRequest{Username: user, Password: pw})
	if r.code != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", user, r.code, r.body)
	}
	var me hubapi.Me
	mustJSON(e.t, r.body, &me)
	var cookie string
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == hubapi.SessionCookie {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
				e.t.Errorf("session cookie attributes: %+v", c)
			}
			cookie = c.Value
		}
	}
	if cookie == "" || me.CSRFToken == "" {
		e.t.Fatalf("login %s: no cookie or csrf: %v %s", user, r.header, r.body)
	}
	return session{cookie: cookie, csrf: me.CSRFToken}, me
}

// sdo sends a request with the session cookie and CSRF token (plus extra headers).
func (e *testEnv) sdo(s session, method, path string, body any, hdr ...string) resp {
	e.t.Helper()
	h := append([]string{"Cookie", hubapi.SessionCookie + "=" + s.cookie, hubapi.CSRFHeader, s.csrf}, hdr...)
	return e.do(method, path, "", body, h...)
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func expectCode(t *testing.T, what string, r resp, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("%s: %d, want %d: %s", what, r.code, code, r.body)
	}
}

func ifMatchHdr(v any) []string {
	switch x := v.(type) {
	case int:
		return []string{"If-Match", `"` + itoa(x) + `"`}
	case string:
		return []string{"If-Match", x}
	}
	return nil
}

func itoa(i int) string { return strconv.Itoa(i) }

// ---- auth ----

func TestAuthSessions(t *testing.T) {
	e := newEnv(t)
	e.seedUsers()

	// Wrong password and unknown user answer the same.
	for _, u := range []string{"admin", "nobody"} {
		e.problem(e.do(http.MethodPost, "/v1/auth/login", "", hubapi.LoginRequest{Username: u, Password: "wrong-password"}), 401, hubapi.CodeInvalidCredentials)
	}
	admin, me := e.login("Admin ", testPassword) // usernames are case-insensitive
	if me.AuthMethod != hubapi.AuthMethodSession || me.Role != hubapi.UserRoleAdmin || me.User == nil || me.User.Username != "admin" || me.SessionExpiresAt == nil {
		t.Errorf("login: %+v", me)
	}
	var got hubapi.Me
	mustJSON(t, e.sdo(admin, http.MethodGet, "/v1/me", nil).body, &got)
	if got.CSRFToken != admin.csrf || got.User.LastLoginAt == nil {
		t.Errorf("me: %+v", got)
	}
	// Bearer tokens: ui = viewer, admin = admin; agent tokens may not call /v1/me.
	mustJSON(t, e.do(http.MethodGet, "/v1/me", tokUI, nil).body, &got)
	if got.AuthMethod != hubapi.AuthMethodToken || got.Role != hubapi.UserRoleViewer || got.TokenID == "" {
		t.Errorf("me (ui token): %+v", got)
	}
	e.problem(e.do(http.MethodGet, "/v1/me", tokC1, nil), 403, "role_forbidden")

	// The session wins over a bearer token: a viewer session with the admin token stays a viewer.
	viewer, _ := e.login("viewer", testPassword)
	r := e.do(http.MethodPost, "/v1/admin/recompute", tokAdmin, nil,
		"Cookie", hubapi.SessionCookie+"="+viewer.cookie, hubapi.CSRFHeader, viewer.csrf)
	e.problem(r, 403, "role_forbidden")
	// Reads work for viewers; CSRF is needed for writes only.
	expectCode(t, "viewer summary", e.do(http.MethodGet, "/v1/summary", "", nil, "Cookie", hubapi.SessionCookie+"="+viewer.cookie), 200)
	e.problem(e.do(http.MethodPost, "/v1/admin/recompute", "", nil, "Cookie", hubapi.SessionCookie+"="+admin.cookie), 403, hubapi.CodeCSRFInvalid)
	e.problem(e.sdo(session{admin.cookie, "wrong"}, http.MethodPost, "/v1/admin/recompute", nil), 403, hubapi.CodeCSRFInvalid)
	expectCode(t, "admin recompute", e.sdo(admin, http.MethodPost, "/v1/admin/recompute", nil), 202)
	e.job.WaitTriggered()
	// An unknown session falls back to the bearer token (the web nginx injects the ui token).
	expectCode(t, "stale cookie + ui token", e.do(http.MethodGet, "/v1/summary", tokUI, nil, "Cookie", hubapi.SessionCookie+"=stale"), 200)
	e.problem(e.do(http.MethodGet, "/v1/summary", "", nil, "Cookie", hubapi.SessionCookie+"=stale"), 401, "unauthorized")
	// Session-only endpoints refuse bearer tokens.
	e.problem(e.do(http.MethodPost, "/v1/me/password", tokAdmin, hubapi.PasswordChange{CurrentPassword: "x", NewPassword: "y"}), 401, "unauthorized")

	// Own password change: other sessions are revoked, the current one stays.
	op1, _ := e.login("operator", testPassword)
	op2, _ := e.login("operator", testPassword)
	e.problem(e.sdo(op1, http.MethodPost, "/v1/me/password", hubapi.PasswordChange{CurrentPassword: "nope-nope", NewPassword: "another password"}), 401, hubapi.CodeInvalidCredentials)
	e.problem(e.sdo(op1, http.MethodPost, "/v1/me/password", hubapi.PasswordChange{CurrentPassword: testPassword, NewPassword: "short"}), 400, "invalid_password")
	expectCode(t, "change password", e.sdo(op1, http.MethodPost, "/v1/me/password", hubapi.PasswordChange{CurrentPassword: testPassword, NewPassword: "another password"}), 204)
	expectCode(t, "current session", e.sdo(op1, http.MethodGet, "/v1/me", nil), 200)
	e.problem(e.sdo(op2, http.MethodGet, "/v1/me", nil), 401, "unauthorized")
	e.login("operator", "another password")

	// Logout needs the CSRF token of a live session, then ends it and clears the cookie.
	e.problem(e.do(http.MethodPost, "/v1/auth/logout", "", nil, "Cookie", hubapi.SessionCookie+"="+viewer.cookie), 403, hubapi.CodeCSRFInvalid)
	r = e.sdo(viewer, http.MethodPost, "/v1/auth/logout", nil)
	expectCode(t, "logout", r, 204)
	expectClearedCookie(t, "logout", r)
	e.problem(e.sdo(viewer, http.MethodGet, "/v1/me", nil), 401, "unauthorized")
	// Signing out again, with a dead cookie or none at all, still clears the cookie.
	r = e.sdo(viewer, http.MethodPost, "/v1/auth/logout", nil)
	expectCode(t, "logout with an ended session", r, 204)
	expectClearedCookie(t, "logout with an ended session", r)
	expectCode(t, "logout without a cookie", e.do(http.MethodPost, "/v1/auth/logout", "", nil), 204)
}

func expectClearedCookie(t *testing.T, what string, r resp) {
	t.Helper()
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == hubapi.SessionCookie && c.Value == "" && c.MaxAge < 0 {
			return
		}
	}
	t.Errorf("%s: the session cookie is not cleared (Set-Cookie %q)", what, r.header.Values("Set-Cookie"))
}

func TestLoginLockoutAndRateLimit(t *testing.T) {
	e := newEnv(t)
	e.seedUsers()
	login := func(user, pw string, hdr ...string) resp {
		return e.do(http.MethodPost, "/v1/auth/login", "", hubapi.LoginRequest{Username: user, Password: pw}, hdr...)
	}
	// 5 failures lock the account. While it is locked even the right password fails, with the same
	// answer as an unknown user, so the response never tells whether the user exists.
	for i := 0; i < 5; i++ {
		e.problem(login("viewer", "wrong-password"), 401, hubapi.CodeInvalidCredentials)
	}
	locked := login("viewer", testPassword)
	unknown := login("nobody-here", "wrong-password")
	e.problem(locked, 401, hubapi.CodeInvalidCredentials)
	e.problem(unknown, 401, hubapi.CodeInvalidCredentials)
	if strip := func(b []byte) string { return strings.ReplaceAll(string(b), "nobody-here", "viewer") }; strip(locked.body) != strip(unknown.body) {
		t.Errorf("locked and unknown users get different answers:\n%s\n%s", locked.body, unknown.body)
	}
	// The lock ends.
	e.advance(16 * time.Minute)
	e.login("viewer", testPassword)

	// Per-user rate limit (10 a minute), the same answer for a known and an unknown user.
	for _, user := range []string{"ghost", "operator"} {
		for i := 0; i < 10; i++ {
			login(user, "whatever-pw", "X-Forwarded-For", "192.0.2."+strconv.Itoa(i))
		}
		r := login(user, "whatever-pw", "X-Forwarded-For", "192.0.2.99")
		e.problem(r, 429, hubapi.CodeRateLimited)
		if r.header.Get("Retry-After") == "" {
			t.Errorf("%s: no Retry-After", user)
		}
		if !strings.Contains(string(r.body), "Too many sign-in attempts. Try again later.") {
			t.Errorf("%s: rate limit answer %s", user, r.body)
		}
	}

	// Per-address rate limit (30 a minute). A forged X-Forwarded-For entry does not escape it: only
	// the last entry (added by the proxy in front of the hub) counts.
	e.advance(2 * time.Minute)
	for i := 0; i < 30; i++ {
		login("user-"+strconv.Itoa(i), "whatever-pw", "X-Forwarded-For", "6.6.6."+strconv.Itoa(i)+", 198.51.100.7")
	}
	e.problem(login("someone-else", "whatever-pw", "X-Forwarded-For", "6.6.6.250, 198.51.100.7"), 429, hubapi.CodeRateLimited)
	e.problem(login("someone-else", "whatever-pw", "X-Forwarded-For", "198.51.100.8"), 401, hubapi.CodeInvalidCredentials)
}

// ---- settings ----

func TestSettings(t *testing.T) {
	e := newEnv(t)
	e.seedUsers()
	admin, _ := e.login("admin", testPassword)
	operator, _ := e.login("operator", testPassword)

	var doc hubapi.RecommenderSettingsDoc
	r := e.getJSON("/v1/admin/settings/recommender", tokUI, &doc)
	if doc.Version != 0 || r.header.Get("ETag") != `"0"` || len(doc.Value.Profiles) != 3 || doc.Value.Interval != "24h0m0s" {
		t.Fatalf("initial recommender settings: %+v %v", doc, r.header)
	}
	v := doc.Value
	v.Interval = "10m"
	v.Profiles[1].CPUPercentile = 0.9 // production
	upd := hubapi.RecommenderSettingsUpdate{Value: v, Reason: "longer prod window"}
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/settings/recommender", upd), 428, hubapi.CodePreconditionRequired)
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/settings/recommender", upd, ifMatchHdr(3)...), 412, hubapi.CodeVersionConflict)
	e.problem(e.sdo(operator, http.MethodPut, "/v1/admin/settings/recommender", upd, ifMatchHdr(0)...), 403, "role_forbidden")
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/settings/recommender", hubapi.RecommenderSettingsUpdate{Value: v}, ifMatchHdr(0)...), 400, "invalid_reason")
	bad := upd
	bad.Value.Profiles = bad.Value.Profiles[:2]
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/settings/recommender", bad, ifMatchHdr(0)...), 400, "invalid_settings")
	r = e.sdo(admin, http.MethodPut, "/v1/admin/settings/recommender", upd, ifMatchHdr(0)...)
	expectCode(t, "put recommender", r, 200)
	mustJSON(t, r.body, &doc)
	if doc.Version != 1 || r.header.Get("ETag") != `"1"` || doc.Value.Interval != "10m0s" || doc.UpdatedBy == nil || doc.UpdatedBy.Name != "admin" {
		t.Errorf("after put: %+v", doc)
	}
	// Hot reload: the next run uses the stored profile.
	e.advance(time.Minute)
	e.run(e.now, false)
	if p := e.job.cur.Config.Profiles[hubapi.ProfileProduction]; p.CPUPercentile != 0.9 || e.job.cur.Interval != 10*time.Minute {
		t.Errorf("job settings not reloaded: %+v %v", p, e.job.cur.Interval)
	}
	select {
	case d := <-e.job.resetChan():
		if d != 10*time.Minute {
			t.Errorf("interval reset %v", d)
		}
	default:
		t.Error("interval change not signalled")
	}

	// Pricing: a 50 % discount on c1 halves its savings.
	var before hubapi.Summary
	e.getJSON("/v1/summary", tokUI, &before)
	var pd hubapi.PricingSettingsDoc
	e.getJSON("/v1/admin/settings/pricing", tokUI, &pd)
	if pd.Version != 0 || pd.Value.Clusters == nil {
		t.Errorf("initial pricing: %+v", pd)
	}
	half := 50.0
	pu := hubapi.PricingSettingsUpdate{Value: hubapi.PricingSettings{Clusters: []hubapi.ClusterPricingAdjustment{{Cluster: "c1", DiscountPercent: &half}}}, Reason: "EDP 2027"}
	bp := pu
	bp.Value.Clusters = []hubapi.ClusterPricingAdjustment{{Cluster: "nope", DiscountPercent: &half}}
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/settings/pricing", bp, ifMatchHdr(0)...), 400, "invalid_settings")
	expectCode(t, "put pricing", e.sdo(admin, http.MethodPut, "/v1/admin/settings/pricing", pu, ifMatchHdr(0)...), 200)
	views := e.workloadDetail(kCheckout)
	cpuRate := views.Recommendation.Pricing.CPUCoreHourUSD
	e.advance(time.Minute)
	e.run(e.now, false)
	after := e.workloadDetail(kCheckout)
	if !approx(after.Recommendation.Pricing.CPUCoreHourUSD, cpuRate/2, 1e-9) {
		t.Errorf("discounted rate %v, want %v", after.Recommendation.Pricing.CPUCoreHourUSD, cpuRate/2)
	}
	if c2 := e.workloadDetail(kAPI); c2.Recommendation.Pricing.CPUCoreHourUSD != cpuRate {
		t.Errorf("c2 rate changed: %v", c2.Recommendation.Pricing.CPUCoreHourUSD)
	}

	var all hubapi.Settings
	e.getJSON("/v1/admin/settings", tokUI, &all)
	if all.Recommender.Version != 1 || all.Pricing.Version != 1 {
		t.Errorf("settings: %+v", all)
	}
	var vl hubapi.SettingsVersionList
	e.getJSON("/v1/admin/settings/recommender/versions?limit=1", tokUI, &vl)
	if len(vl.Items) != 1 || vl.Items[0].Version != 1 || vl.Items[0].Actor.Name != "admin" || vl.Items[0].Reason != "longer prod window" {
		t.Errorf("versions: %+v", vl)
	}
	expectCode(t, "unknown section", e.do(http.MethodGet, "/v1/admin/settings/other/versions", tokUI, nil), 404)

	// Both writes are in the config audit, not in the workload audit.
	var cl hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?targetType=settings", tokUI, &cl)
	if len(cl.Items) != 2 || cl.Items[0].Action != hubapi.ActionSettingsUpdated || cl.Items[0].Target.ID != "pricing" ||
		cl.Items[0].Actor.Name != "admin" || cl.Items[0].Reason != "EDP 2027" || len(cl.Items[0].After) == 0 {
		t.Errorf("config audit: %+v", cl)
	}
	var al hubapi.AuditEventList
	e.getJSON("/v1/audit?limit=1000", tokUI, &al)
	for _, ev := range al.Items {
		if ev.Type == hubapi.EventConfigChanged {
			t.Errorf("config change in /v1/audit: %+v", ev)
		}
	}
}

func (e *testEnv) workloadDetail(k hubapi.WorkloadKey) hubapi.WorkloadDetail {
	e.t.Helper()
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(k, ""), tokUI, &d)
	if d.Recommendation == nil || d.Recommendation.Pricing == nil {
		e.t.Fatalf("%s has no recommendation pricing", k.ID())
	}
	return d
}

// ---- policies, config, inventory ----

func autoPatchPolicy(name string, prio int32, clusters []string, ns string) hubapi.PolicyInput {
	return hubapi.PolicyInput{Type: v1alpha1.HubPolicyTypePolicy, Name: name, Clusters: clusters, Reason: "rollout",
		Spec: v1alpha1.RightsizingPolicySpec{Priority: prio, Selector: v1alpha1.PolicySelector{Namespaces: []string{ns}},
			CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAutoPatch}}}
}

func (e *testEnv) config(tok, cluster, etag string) (hubapi.ClusterConfig, resp) {
	e.t.Helper()
	var hdr []string
	if etag != "" {
		hdr = []string{"If-None-Match", etag}
	}
	r := e.do(http.MethodGet, "/v1/clusters/"+cluster+"/config", tok, nil, hdr...)
	var c hubapi.ClusterConfig
	if r.code == 200 {
		mustJSON(e.t, r.body, &c)
		if r.header.Get("ETag") != `"`+c.ConfigVersion+`"` {
			e.t.Errorf("ETag %q, configVersion %q", r.header.Get("ETag"), c.ConfigVersion)
		}
	}
	return c, r
}

func TestPoliciesAndConfig(t *testing.T) {
	e := newEnv(t)
	e.seedUsers()
	admin, _ := e.login("admin", testPassword)

	cfg, r := e.config(tokC1, "c1", "")
	expectCode(t, "config", r, 200)
	if len(cfg.Policies) != 0 || cfg.Flags.Paused || cfg.PolicySource != hubapi.PolicySourceBoth || len(cfg.Approvals) != 0 {
		t.Errorf("empty config: %+v", cfg)
	}
	etag0 := r.header.Get("ETag")
	_, r = e.config(tokC1, "c1", etag0)
	expectCode(t, "config 304", r, 304)
	e.problem(e.do(http.MethodGet, "/v1/clusters/c2/config", tokC1, nil), 403, "cluster_forbidden")

	// Create: global default, cluster default, policy.
	gd := hubapi.PolicyInput{Type: v1alpha1.HubPolicyTypeGlobalDefault, Reason: "fleet defaults",
		Spec: v1alpha1.RightsizingPolicySpec{Guardrails: &v1alpha1.Guardrails{MinConfidence: "0.8"}}}
	r = e.sdo(admin, http.MethodPost, "/v1/admin/policies", gd)
	expectCode(t, "create global default", r, 201)
	var gdp hubapi.AdminPolicy
	mustJSON(t, r.body, &gdp)
	if gdp.Name != v1alpha1.GlobalDefaultsPolicyName || gdp.Priority != 0 || gdp.Version != 1 || r.header.Get("ETag") != `"1"` || gdp.CreatedBy.Name != "admin" {
		t.Errorf("global default: %+v", gdp)
	}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/policies", gd), 409, hubapi.CodeAlreadyExists)
	cd := hubapi.PolicyInput{Type: v1alpha1.HubPolicyTypeClusterDefault, Clusters: []string{"c2"}, Reason: "c2 audit",
		Spec: v1alpha1.RightsizingPolicySpec{Memory: &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeOff}}}
	expectCode(t, "cluster default", e.sdo(admin, http.MethodPost, "/v1/admin/policies", cd), 201)
	pol := autoPatchPolicy("payments-cpu-auto", 100, []string{"c1"}, "payments")
	r = e.sdo(admin, http.MethodPost, "/v1/admin/policies", pol)
	expectCode(t, "policy", r, 201)
	var p hubapi.AdminPolicy
	mustJSON(t, r.body, &p)

	// Validation.
	for name, in := range map[string]hubapi.PolicyInput{
		"reserved name":   autoPatchPolicy("ro-mine", 100, nil, "x"),
		"priority band":   autoPatchPolicy("too-high", 900000, nil, "x"),
		"unknown cluster": autoPatchPolicy("elsewhere", 5, []string{"c9"}, "x"),
		"override type":   {Type: v1alpha1.HubPolicyTypeWorkloadOverride, Reason: "x"},
		"bad mode":        {Type: v1alpha1.HubPolicyTypePolicy, Name: "bad", Reason: "x", Spec: v1alpha1.RightsizingPolicySpec{Priority: 3, CPU: &v1alpha1.CPUPolicy{Mode: "always"}}},
		"two clusters":    {Type: v1alpha1.HubPolicyTypeClusterDefault, Clusters: []string{"c1", "c2"}, Reason: "x"},
	} {
		r := e.sdo(admin, http.MethodPost, "/v1/admin/policies", in)
		if r.code != 400 {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/policies", autoPatchPolicy("payments-cpu-auto", 5, nil, "x")), 409, hubapi.CodeAlreadyExists)

	// The config changes; each cluster gets its own policies.
	c1, r := e.config(tokC1, "c1", etag0)
	expectCode(t, "config after create", r, 200)
	names := func(c hubapi.ClusterConfig) (out []string) {
		for _, p := range c.Policies {
			out = append(out, p.Name)
		}
		return out
	}
	if got := strings.Join(names(c1), ","); got != "ro-global-defaults,payments-cpu-auto" {
		t.Errorf("c1 policies: %s", got)
	}
	if c1.Policies[1].Spec.Priority != 100 || c1.Policies[1].Version != 1 || c1.Policies[1].Type != v1alpha1.HubPolicyTypePolicy {
		t.Errorf("c1 policy: %+v", c1.Policies[1])
	}
	c2, _ := e.config(tokC2, "c2", "")
	if got := strings.Join(names(c2), ","); got != "ro-global-defaults,ro-cluster-defaults" {
		t.Errorf("c2 policies: %s", got)
	}

	// Update with If-Match, type immutable, delete, versions, revert.
	upd := pol
	upd.Spec.Priority = 200
	upd.Reason = "raise priority"
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/policies/"+p.ID, upd), 428, hubapi.CodePreconditionRequired)
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/policies/"+p.ID, upd, ifMatchHdr(9)...), 412, hubapi.CodeVersionConflict)
	ti := upd
	ti.Type = v1alpha1.HubPolicyTypeGlobalDefault
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/policies/"+p.ID, ti, ifMatchHdr(1)...), 400, hubapi.CodeTypeImmutable)
	r = e.sdo(admin, http.MethodPut, "/v1/admin/policies/"+p.ID, upd, ifMatchHdr(1)...)
	expectCode(t, "update", r, 200)
	mustJSON(t, r.body, &p)
	if p.Version != 2 || p.Priority != 200 || p.Spec.Priority != 200 {
		t.Errorf("updated: %+v", p)
	}
	r = e.sdo(admin, http.MethodDelete, "/v1/admin/policies/"+p.ID, hubapi.ChangeReason{Reason: "replaced"}, ifMatchHdr(2)...)
	expectCode(t, "delete", r, 200)
	mustJSON(t, r.body, &p)
	if p.DeletedAt == nil || p.Version != 3 {
		t.Errorf("deleted: %+v", p)
	}
	c1, _ = e.config(tokC1, "c1", "")
	if got := strings.Join(names(c1), ","); got != "ro-global-defaults" {
		t.Errorf("c1 after delete: %s", got)
	}
	var pl hubapi.AdminPolicyList
	e.getJSON("/v1/admin/policies?cluster=c1", tokUI, &pl)
	if len(pl.Items) != 1 {
		t.Errorf("live c1 policies: %+v", pl.Items)
	}
	e.getJSON("/v1/admin/policies?includeDeleted=true&type=policy", tokUI, &pl)
	if len(pl.Items) != 1 || pl.Items[0].DeletedAt == nil {
		t.Errorf("deleted policies: %+v", pl.Items)
	}
	e.problem(e.sdo(admin, http.MethodPut, "/v1/admin/policies/"+p.ID, upd, ifMatchHdr(3)...), 409, "policy_deleted")
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/policies/"+p.ID+"/revert", hubapi.PolicyRevert{Version: 3, Reason: "x"}, ifMatchHdr(3)...), 400, "invalid_version")
	r = e.sdo(admin, http.MethodPost, "/v1/admin/policies/"+p.ID+"/revert", hubapi.PolicyRevert{Version: 1, Reason: "rollback"}, ifMatchHdr(3)...)
	expectCode(t, "revert", r, 200)
	p = hubapi.AdminPolicy{}
	mustJSON(t, r.body, &p)
	if p.Version != 4 || p.DeletedAt != nil || p.Priority != 100 {
		t.Errorf("reverted: %+v", p)
	}
	var vl hubapi.PolicyVersionList
	e.getJSON("/v1/admin/policies/"+p.ID+"/versions?limit=2", tokUI, &vl)
	if len(vl.Items) != 2 || vl.Items[0].Operation != hubapi.PolicyOpRevert || vl.Items[0].RevertedFrom != 1 || !vl.Items[1].Deleted || vl.Continue == "" {
		t.Fatalf("versions page 1: %+v", vl)
	}
	cont := vl.Continue
	vl = hubapi.PolicyVersionList{}
	e.getJSON("/v1/admin/policies/"+p.ID+"/versions?limit=2&continue="+url.QueryEscape(cont), tokUI, &vl)
	if len(vl.Items) != 2 || vl.Items[1].Operation != hubapi.PolicyOpCreate || vl.Continue != "" {
		t.Errorf("versions page 2: %+v", vl)
	}
	var got hubapi.AdminPolicy
	if r := e.getJSON("/v1/admin/policies/"+p.ID, tokUI, &got); r.header.Get("ETag") != `"4"` {
		t.Errorf("get ETag %q", r.header.Get("ETag"))
	}
	expectCode(t, "unknown policy", e.do(http.MethodGet, "/v1/admin/policies/not-a-uuid", tokUI, nil), 404)
	var cl hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?targetId="+p.ID+"&order=asc", tokUI, &cl)
	var actions []string
	for _, c := range cl.Items {
		actions = append(actions, c.Action)
	}
	if strings.Join(actions, ",") != "PolicyCreated,PolicyUpdated,PolicyDeleted,PolicyReverted" {
		t.Errorf("policy audit: %v", actions)
	}

	// The agent reports what it materialised and the Git policies.
	c1, _ = e.config(tokC1, "c1", "")
	git := v1alpha1.RightsizingPolicySpec{Priority: 50, Selector: v1alpha1.PolicySelector{Namespaces: []string{"web"}}, Memory: &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeOff}}
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{ConfigVersion: c1.ConfigVersion, Items: []hubapi.WorkloadStatus{},
		PolicyInventory: []hubapi.PolicyInventoryItem{
			{Name: "payments-cpu-auto", Source: hubapi.PolicySourceHub, Priority: 100, Generation: 1, Valid: true, HubPolicyID: p.ID, HubVersion: 4},
			{Name: "team-web", Source: hubapi.PolicySourceGit, Priority: 50, Generation: 3, Valid: true, Spec: &git},
		}}, &res)
	var ca hubapi.ClusterAdmin
	e.getJSON("/v1/admin/clusters/c1", tokUI, &ca)
	if !ca.Agent.InSync || ca.Agent.ConfigVersion != c1.ConfigVersion || ca.PolicyInventory == nil || len(ca.PolicyInventory.Items) != 2 ||
		ca.Metrics.DatasourceID != hubapi.LocalDatasourceID || ca.Agent.LastConfigPullAt == nil {
		t.Errorf("cluster admin: %+v", ca)
	}
	var inv hubapi.PolicyInventoryList
	e.getJSON("/v1/admin/policy-inventory?cluster=c1", tokUI, &inv)
	if len(inv.Items) != 1 || len(inv.Items[0].Items) != 2 || inv.Items[0].ConfigVersion != c1.ConfigVersion {
		t.Errorf("inventory: %+v", inv)
	}
	// An absent inventory leaves it unchanged.
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{}}, &res)
	e.getJSON("/v1/admin/policy-inventory?cluster=c1", tokUI, &inv)
	if len(inv.Items[0].Items) != 2 {
		t.Errorf("inventory dropped: %+v", inv)
	}
	expectCode(t, "bad inventory", e.do(http.MethodPost, "/v1/clusters/c1/workload-status", tokC1,
		hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{}, PolicyInventory: []hubapi.PolicyInventoryItem{{Name: "x", Source: "svn"}}}), 400)

	// Effective policy: the hub policy wins in payments, the Git one in web.
	var ep hubapi.EffectivePolicy
	e.getJSON(wlPath(kCheckout, "/effective-policy"), tokUI, &ep)
	if ep.Modes.CPU != hubapi.ModeAutoPatch || ep.Modes.CPUSource != "policy" {
		t.Errorf("checkout effective: %+v", ep.Modes)
	}
	e.getJSON(wlPath(kFrontend, "/effective-policy"), tokUI, &ep)
	if ep.Modes.Memory != hubapi.ModeOff || ep.Modes.CPU != hubapi.ModeAudit {
		t.Errorf("frontend effective: %+v", ep.Modes)
	}

	// Preview: CPU auto-patch for every namespace of c1.
	var pv hubapi.ImpactPreview
	r = e.sdo(admin, http.MethodPost, "/v1/admin/preview", hubapi.PreviewRequest{Changes: []hubapi.PolicyChange{{Op: hubapi.PreviewUpsert,
		Policy: &hubapi.PolicyDraft{Type: v1alpha1.HubPolicyTypePolicy, Name: "all-c1", Clusters: []string{"c1"},
			Spec: v1alpha1.RightsizingPolicySpec{Priority: 300, CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAutoPatch}}}}}})
	expectCode(t, "preview", r, 200)
	mustJSON(t, r.body, &pv)
	if pv.WorkloadsEvaluated < 5 || pv.WorkloadsChanged < 2 || pv.SavingsIntoAutoPatchUSD.CPU <= 0 {
		t.Errorf("preview: %+v", pv)
	}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/preview", hubapi.PreviewRequest{Changes: []hubapi.PolicyChange{{Op: hubapi.PreviewDelete, PolicyID: "0192f7c4-0000-7000-8000-000000000000"}}}), 404, "not_found")
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/preview", hubapi.PreviewRequest{}), 400, "invalid_changes")

	// policySource git: no hub policies for the cluster.
	r = e.sdo(admin, http.MethodPatch, "/v1/admin/clusters/c1", hubapi.ClusterUpdate{PolicySource: strPtr(hubapi.PolicySourceGit), Reason: "gitops"}, ifMatchHdr(ca.Version)...)
	expectCode(t, "policySource git", r, 200)
	c1, _ = e.config(tokC1, "c1", "")
	if len(c1.Policies) != 0 || c1.PolicySource != hubapi.PolicySourceGit {
		t.Errorf("git config: %+v", c1)
	}
}

func strPtr(s string) *string { return &s }

// ---- overrides, approvals, automation ----

// TestApprovalSupersededBeforeEvent (the TestApproveOnce e2e flake): the agent applies
// approval A, a recommender run supersedes A before the hub stores A's applied event, and the
// operator approves the new recommendation B. A's late event consumes A only; B stays pending until
// the agent's applied/ApprovedOnce event for B (B's values are live already) consumes it.
func TestApprovalSupersededBeforeEvent(t *testing.T) {
	e := newEnv(t)
	e.seedUsers()
	operator, _ := e.login("operator", testPassword)
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	r := e.sdo(operator, http.MethodPost, wlPath(kCheckout, "/approve"), hubapi.ApprovalRequest{RecommendationID: d.Recommendation.ID, Reason: "A"})
	expectCode(t, "approve A", r, 201)
	var a hubapi.Approval
	mustJSON(t, r.body, &a)

	e.advance(10 * time.Minute)
	e.run(e.now, false)
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Approval == nil || d.Approval.ID != a.ID || d.Approval.State != hubapi.ApprovalSuperseded || d.Recommendation.ID == a.RecommendationID {
		t.Fatalf("after the recommender run: approval %+v, recommendation %s", d.Approval, d.Recommendation.ID)
	}
	r = e.sdo(operator, http.MethodPost, wlPath(kCheckout, "/approve"), hubapi.ApprovalRequest{RecommendationID: d.Recommendation.ID, Reason: "B"})
	expectCode(t, "approve B", r, 201)
	var b hubapi.Approval
	mustJSON(t, r.body, &b)

	applied := func(id int, ap hubapi.Approval) hubapi.Event {
		return hubapi.Event{ID: newUUID(id), Type: "applied", Reason: v1alpha1.ReasonApprovedOnce, Time: e.now, Workload: kCheckout.Ref(),
			RecommendationID: ap.RecommendationID, Details: map[string]string{"approvalId": ap.ID, "approvedBy": "operator"}}
	}
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: []hubapi.Event{applied(291, a)}}, &res)
	var consumedEvent *string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT consumed_event_id::text FROM workload_approvals WHERE id = $1`, a.ID).Scan(&consumedEvent); err != nil ||
		consumedEvent == nil || *consumedEvent != newUUID(291) {
		t.Fatalf("approval A after its late event: consumed by %v, %v", consumedEvent, err)
	}
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Approval == nil || d.Approval.ID != b.ID || d.Approval.State != hubapi.ApprovalPending {
		t.Fatalf("approval B after A's event: %+v", d.Approval)
	}
	c1, _ := e.config(tokC1, "c1", "")
	if len(c1.Approvals) != 1 || c1.Approvals[0].ID != b.ID {
		t.Fatalf("config approvals: %+v", c1.Approvals)
	}

	e.postJSON("/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: []hubapi.Event{applied(292, b)}}, &res)
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Approval == nil || d.Approval.ID != b.ID || d.Approval.State != hubapi.ApprovalConsumed || d.Approval.ConsumedEventID != newUUID(292) {
		t.Fatalf("approval B after its event: %+v", d.Approval)
	}
	if c1, _ = e.config(tokC1, "c1", ""); len(c1.Approvals) != 0 {
		t.Fatalf("consumed approvals still sent: %+v", c1.Approvals)
	}
}

func TestOverridesApprovalsAutomation(t *testing.T) {
	e := newEnv(t)
	e.seedUsers()
	admin, _ := e.login("admin", testPassword)
	operator, _ := e.login("operator", testPassword)
	viewer, _ := e.login("viewer", testPassword)

	// Override: exclude checkout.
	excl := hubapi.WorkloadOverrideInput{Spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeOff}, Memory: &v1alpha1.MemoryPolicy{Mode: v1alpha1.ModeOff}},
		Reason: "Exclude: batch job"}
	e.problem(e.sdo(viewer, http.MethodPut, wlPath(kCheckout, "/override"), excl), 403, "role_forbidden")
	expectCode(t, "no override", e.do(http.MethodGet, wlPath(kCheckout, "/override"), tokUI, nil), 404)
	bad := excl
	bad.Spec.Priority = 5
	e.problem(e.sdo(operator, http.MethodPut, wlPath(kCheckout, "/override"), bad), 400, hubapi.CodeInvalidOverride)
	r := e.sdo(operator, http.MethodPut, wlPath(kCheckout, "/override"), excl)
	expectCode(t, "put override", r, 200)
	var ov hubapi.AdminPolicy
	mustJSON(t, r.body, &ov)
	if ov.Name != v1alpha1.OverridePolicyName("payments", v1alpha1.KindDeployment, "checkout") || ov.Priority != 900000 || ov.WorkloadID != kCheckout.ID() ||
		len(ov.Spec.Selector.Workloads) != 1 || ov.Clusters[0] != "c1" {
		t.Errorf("override: %+v", ov)
	}
	e.problem(e.sdo(operator, http.MethodPut, wlPath(kCheckout, "/override"), excl), 428, hubapi.CodePreconditionRequired)
	expectCode(t, "replace override", e.sdo(operator, http.MethodPut, wlPath(kCheckout, "/override"), excl, ifMatchHdr(1)...), 200)
	c1, _ := e.config(tokC1, "c1", "")
	if len(c1.Policies) != 1 || c1.Policies[0].Type != v1alpha1.HubPolicyTypeWorkloadOverride || c1.Policies[0].Version != 2 {
		t.Errorf("config override: %+v", c1.Policies)
	}
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Override == nil || d.Override.Version != 2 {
		t.Errorf("detail override: %+v", d.Override)
	}
	var ep hubapi.EffectivePolicy
	e.getJSON(wlPath(kCheckout, "/effective-policy"), tokUI, &ep)
	if ep.Modes.CPU != hubapi.ModeOff || ep.Modes.Memory != hubapi.ModeOff {
		t.Errorf("effective with override: %+v", ep.Modes)
	}

	// Prod: an operator may not set auto-patch; an admin may.
	var ca hubapi.ClusterAdmin
	e.getJSON("/v1/admin/clusters/c1", tokUI, &ca)
	expectCode(t, "make c1 prod", e.sdo(admin, http.MethodPatch, "/v1/admin/clusters/c1", hubapi.ClusterUpdate{Environment: strPtr(hubapi.EnvironmentProd), Reason: "prod"}, ifMatchHdr(ca.Version)...), 200)
	auto := hubapi.WorkloadOverrideInput{Spec: v1alpha1.RightsizingPolicySpec{CPU: &v1alpha1.CPUPolicy{Mode: v1alpha1.ModeAutoPatch}}, Reason: "go"}
	e.problem(e.sdo(operator, http.MethodPut, wlPath(kLedger, "/override"), auto), 403, hubapi.CodeAdminRequired)
	ro := auto
	ro.OverrideReadiness = true
	e.problem(e.sdo(operator, http.MethodPut, wlPath(kFrontend, "/override"), hubapi.WorkloadOverrideInput{Spec: excl.Spec, Reason: "x", OverrideReadiness: true}), 403, hubapi.CodeAdminRequired)
	expectCode(t, "admin auto-patch on prod", e.sdo(admin, http.MethodPut, wlPath(kLedger, "/override"), ro), 200)

	// Clear the override.
	e.problem(e.sdo(operator, http.MethodDelete, wlPath(kCheckout, "/override"), hubapi.ChangeReason{Reason: "back"}), 428, hubapi.CodePreconditionRequired)
	expectCode(t, "clear override", e.sdo(operator, http.MethodDelete, wlPath(kCheckout, "/override"), hubapi.ChangeReason{Reason: "back"}, ifMatchHdr(2)...), 204)
	expectCode(t, "cleared", e.do(http.MethodGet, wlPath(kCheckout, "/override"), tokUI, nil), 404)

	// Approve once.
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	e.problem(e.sdo(operator, http.MethodPost, wlPath(kCheckout, "/approve"), hubapi.ApprovalRequest{RecommendationID: newUUID(1), Reason: "ok"}), 409, hubapi.CodeRecommendationStale)
	r = e.sdo(operator, http.MethodPost, wlPath(kCheckout, "/approve"), hubapi.ApprovalRequest{RecommendationID: d.Recommendation.ID, Reason: "reviewed with owner"})
	expectCode(t, "approve", r, 201)
	var ap hubapi.Approval
	mustJSON(t, r.body, &ap)
	if ap.State != hubapi.ApprovalPending || ap.ApprovedBy.Name != "operator" || !ap.ExpiresAt.Equal(ap.ApprovedAt.Add(hubapi.ApprovalDefaultTTL)) {
		t.Errorf("approval: %+v", ap)
	}
	c1, _ = e.config(tokC1, "c1", "")
	if len(c1.Approvals) != 1 || c1.Approvals[0].ID != ap.ID || c1.Approvals[0].Workload.Name != "checkout" || c1.Approvals[0].ApprovedBy != "operator" {
		t.Errorf("config approvals: %+v", c1.Approvals)
	}
	// Workload in auto-patch everywhere: not in audit.
	var res hubapi.BatchResult
	st := hubapi.WorkloadStatus{Workload: kFrontend.Ref(), Phase: "Applied", Modes: hubapi.EffectiveModes{CPU: hubapi.ModeAutoPatch, Memory: hubapi.ModeAutoPatch}}
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{st}}, &res)
	e.getJSON(wlPath(kFrontend, ""), tokUI, &d)
	e.problem(e.sdo(operator, http.MethodPost, wlPath(kFrontend, "/approve"), hubapi.ApprovalRequest{RecommendationID: d.Recommendation.ID, Reason: "x"}), 409, hubapi.CodeNotInAudit)
	// The agent applies it: consumed, no longer sent.
	ev := hubapi.Event{ID: newUUID(77), Type: "applied", Reason: v1alpha1.ReasonApprovedOnce, Time: e.now, Workload: kCheckout.Ref(),
		Resources: []string{"cpu"}, Details: map[string]string{"approvalId": ap.ID, "approvedBy": "operator"}}
	e.postJSON("/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: []hubapi.Event{ev}}, &res)
	c1, _ = e.config(tokC1, "c1", "")
	if len(c1.Approvals) != 0 {
		t.Errorf("consumed approval still sent: %+v", c1.Approvals)
	}
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Approval == nil || d.Approval.State != hubapi.ApprovalConsumed || d.Approval.ConsumedEventID != newUUID(77) {
		t.Errorf("detail approval: %+v", d.Approval)
	}
	var h hubapi.WorkloadHistory
	e.advance(time.Minute) // the history range ends (exclusively) now
	e.getJSON(wlPath(kCheckout, "/history"), tokUI, &h)
	if len(h.ConfigChanges) != 4 || h.ConfigChanges[0].Action != hubapi.ActionWorkloadApproved || len(h.Events) != 1 {
		t.Errorf("history: %d config changes %+v, %d events", len(h.ConfigChanges), h.ConfigChanges, len(h.Events))
	}
	// A new recommendation supersedes an open approval.
	e.getJSON(wlPath(kLedger, ""), tokUI, &d)
	expectCode(t, "approve ledger", e.sdo(operator, http.MethodPost, wlPath(kLedger, "/approve"), hubapi.ApprovalRequest{RecommendationID: d.Recommendation.ID, Reason: "x"}), 201)
	e.advance(10 * time.Minute)
	e.run(e.now, false)
	e.getJSON(wlPath(kLedger, ""), tokUI, &d)
	if d.Approval == nil || d.Approval.State != hubapi.ApprovalSuperseded {
		t.Errorf("superseded approval: %+v", d.Approval)
	}

	// Automation: operators pause; resuming prod needs an admin; resumeAt ends a pause.
	resume := e.now.Add(time.Hour)
	r = e.sdo(operator, http.MethodPut, "/v1/clusters/c1/automation", hubapi.AutomationUpdate{Paused: true, Reason: "INC-4711", ResumeAt: &resume})
	expectCode(t, "pause", r, 200)
	var a hubapi.Automation
	mustJSON(t, r.body, &a)
	if !a.Paused || a.Version != 1 || a.By == nil || a.By.Name != "operator" {
		t.Errorf("paused: %+v", a)
	}
	c1, _ = e.config(tokC1, "c1", "")
	if !c1.Flags.Paused || c1.Flags.PauseScope != hubapi.PauseScopeCluster || c1.Flags.PauseReason != "INC-4711" || c1.Flags.ResumeAt == nil {
		t.Errorf("flags: %+v", c1.Flags)
	}
	e.problem(e.sdo(operator, http.MethodPut, "/v1/clusters/c1/automation", hubapi.AutomationUpdate{Reason: "done"}), 403, hubapi.CodeAdminRequired)
	e.problem(e.sdo(operator, http.MethodPut, "/v1/clusters/c1/automation", hubapi.AutomationUpdate{Paused: true, Reason: "x"}, ifMatchHdr(7)...), 412, hubapi.CodeVersionConflict)
	e.problem(e.sdo(operator, http.MethodPut, "/v1/clusters/c1/automation", hubapi.AutomationUpdate{Reason: "x", ResumeAt: &resume}), 400, "invalid_resume_at")
	expectCode(t, "operator resumes non-prod", e.sdo(operator, http.MethodPut, "/v1/clusters/c2/automation", hubapi.AutomationUpdate{Reason: "nothing to do"}), 200)
	// Fleet pause wins.
	expectCode(t, "fleet pause", e.sdo(operator, http.MethodPut, "/v1/admin/automation", hubapi.AutomationUpdate{Paused: true, Reason: "freeze"}), 200)
	c2, _ := e.config(tokC2, "c2", "")
	if !c2.Flags.Paused || c2.Flags.PauseScope != hubapi.PauseScopeFleet {
		t.Errorf("fleet flags: %+v", c2.Flags)
	}
	e.problem(e.sdo(operator, http.MethodPut, "/v1/admin/automation", hubapi.AutomationUpdate{Reason: "unfreeze"}), 403, hubapi.CodeAdminRequired)
	expectCode(t, "fleet resume", e.sdo(admin, http.MethodPut, "/v1/admin/automation", hubapi.AutomationUpdate{Reason: "unfreeze"}), 200)
	var fa hubapi.FleetAutomation
	e.getJSON("/v1/admin/automation", tokUI, &fa)
	if fa.Fleet.Paused || len(fa.Clusters) != 2 || !fa.Clusters[0].Paused {
		t.Errorf("fleet automation: %+v", fa)
	}
	e.advance(2 * time.Hour)
	c1, _ = e.config(tokC1, "c1", "")
	if c1.Flags.Paused {
		t.Error("pause did not end at resumeAt")
	}
	var cl hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?targetType=automation&cluster=c1&limit=1", tokUI, &cl)
	if len(cl.Items) != 1 || cl.Items[0].Action != hubapi.ActionAutomationResumed || cl.Items[0].Actor.Type != hubapi.ActorSystem || cl.Continue == "" {
		t.Errorf("auto resume audit: %+v", cl)
	}
	var cl2 hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?targetType=automation&cluster=c1&limit=1&continue="+url.QueryEscape(cl.Continue), tokUI, &cl2)
	if len(cl2.Items) != 1 || cl2.Items[0].Action != hubapi.ActionAutomationPaused || cl2.Items[0].Actor.Name != "operator" {
		t.Errorf("page 2: %+v", cl2)
	}
	expectCode(t, "continue for other query", e.do(http.MethodGet, "/v1/audit/config?continue="+url.QueryEscape(cl.Continue), tokUI, nil), 400)
	var cls hubapi.ClusterList
	e.getJSON("/v1/clusters", tokUI, &cls)
	if cls.Items[0].Environment != hubapi.EnvironmentProd || cls.Items[0].Paused == nil || *cls.Items[0].Paused || cls.Items[0].ConfigSync == nil || cls.Items[0].Readiness == nil {
		t.Errorf("cluster list: %+v", cls.Items[0])
	}
}

// ---- clusters, tokens, users ----

func TestClustersTokensUsers(t *testing.T) {
	e := newEnv(t)
	e.seedUsers()
	admin, _ := e.login("admin", testPassword)

	in := hubapi.ClusterCreate{Name: "eks-prod-eu1", Environment: hubapi.EnvironmentProd, Owner: "platform", Reason: "new region",
		Provider: "eks", Region: "eu-west-1", Metrics: &hubapi.MetricsBinding{DatasourceID: hubapi.LocalDatasourceID, ClusterLabel: "cluster_name"}}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/clusters", hubapi.ClusterCreate{Name: "x", Environment: "staging", Reason: "x"}), 400, "invalid_environment")
	r := e.sdo(admin, http.MethodPost, "/v1/admin/clusters", in)
	expectCode(t, "create cluster", r, 201)
	var cc hubapi.ClusterCreated
	mustJSON(t, r.body, &cc)
	if cc.AgentToken == nil || !strings.HasPrefix(cc.AgentToken.Value, "ro-") || cc.Cluster.PolicySource != hubapi.PolicySourceBoth ||
		cc.Cluster.Metrics.ClusterLabel != "cluster_name" || cc.Cluster.Provider != "eks" || cc.Cluster.Readiness.State != "Registered" {
		t.Fatalf("created: %+v", cc)
	}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/clusters", in), 409, hubapi.CodeAlreadyExists)
	_, r = e.config(cc.AgentToken.Value, "eks-prod-eu1", "")
	expectCode(t, "new agent token", r, 200)
	bad := in
	bad.Name, bad.Metrics = "other", &hubapi.MetricsBinding{DatasourceID: "0192f7c4-0000-7000-8000-000000000000"}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/clusters", bad), 400, "unknown_datasource")

	r = e.sdo(admin, http.MethodPatch, "/v1/admin/clusters/eks-prod-eu1", hubapi.ClusterUpdate{Owner: strPtr("payments"), Reason: "ownership change"}, ifMatchHdr(1)...)
	expectCode(t, "patch cluster", r, 200)
	var ca hubapi.ClusterAdmin
	mustJSON(t, r.body, &ca)
	if ca.Owner != "payments" || ca.Version != 2 || r.header.Get("ETag") != `"2"` || ca.UpdatedBy == nil {
		t.Errorf("patched: %+v", ca)
	}
	e.problem(e.sdo(admin, http.MethodDelete, "/v1/admin/clusters/eks-prod-eu1", hubapi.ChangeReason{Reason: "gone"}, ifMatchHdr(1)...), 412, hubapi.CodeVersionConflict)
	expectCode(t, "delete cluster", e.sdo(admin, http.MethodDelete, "/v1/admin/clusters/eks-prod-eu1", hubapi.ChangeReason{Reason: "gone"}, ifMatchHdr(2)...), 204)
	_, r = e.config(cc.AgentToken.Value, "eks-prod-eu1", "")
	e.problem(r, 401, "invalid_token")
	var cls hubapi.ClusterList
	e.getJSON("/v1/clusters", tokUI, &cls)
	if len(cls.Items) != 2 {
		t.Errorf("deleted cluster listed: %+v", cls.Items)
	}
	e.getJSON("/v1/admin/clusters/eks-prod-eu1", tokUI, &ca)
	if ca.DeletedAt == nil {
		t.Errorf("deleted cluster: %+v", ca)
	}
	// Registering it again revives it.
	expectCode(t, "re-create", e.sdo(admin, http.MethodPost, "/v1/admin/clusters", in), 201)

	// Tokens: create, use, revoke.
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/tokens", hubapi.TokenCreate{Role: hubapi.RoleAgent, Description: "x", Reason: "x"}), 400, "invalid_cluster")
	r = e.sdo(admin, http.MethodPost, "/v1/admin/tokens", hubapi.TokenCreate{Role: hubapi.RoleUI, Description: "grafana link", Reason: "dashboards"})
	expectCode(t, "create token", r, 201)
	var tc hubapi.TokenCreated
	mustJSON(t, r.body, &tc)
	expectCode(t, "use token", e.do(http.MethodGet, "/v1/summary", tc.Value, nil), 200)
	var tl hubapi.TokenList
	e.getJSON("/v1/admin/tokens", tokAdmin, &tl)
	found := false
	for _, ti := range tl.Items {
		if ti.ID == tc.Token.ID {
			found = ti.LastUsedAt != nil && ti.CreatedBy == "admin"
		}
	}
	if !found {
		t.Errorf("token list: %+v", tl.Items)
	}
	r = e.sdo(admin, http.MethodDelete, "/v1/admin/tokens/"+tc.Token.ID, hubapi.ChangeReason{Reason: "leaked"})
	expectCode(t, "revoke", r, 200)
	e.problem(e.do(http.MethodGet, "/v1/summary", tc.Value, nil), 401, "invalid_token")
	expectCode(t, "unknown token", e.sdo(admin, http.MethodDelete, "/v1/admin/tokens/99999", hubapi.ChangeReason{Reason: "x"}), 404)
	e.getJSON("/v1/admin/tokens?includeRevoked=true&cluster=eks-prod-eu1", tokAdmin, &tl)
	if len(tl.Items) != 2 || tl.Items[1].RevokedAt == nil {
		t.Errorf("cluster tokens: %+v", tl.Items)
	}

	// Users.
	r = e.sdo(admin, http.MethodPost, "/v1/admin/users", hubapi.UserCreate{Username: "bob", DisplayName: "Bob", Role: hubapi.UserRoleOperator, Password: "bobs password", Reason: "new"})
	expectCode(t, "create user", r, 201)
	var u hubapi.User
	mustJSON(t, r.body, &u)
	if !u.MustChangePassword || u.CreatedBy != "admin" || u.Version != 1 {
		t.Errorf("user: %+v", u)
	}
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/users", hubapi.UserCreate{Username: "bob", Role: hubapi.UserRoleViewer, Password: "bobs password", Reason: "x"}), 409, hubapi.CodeAlreadyExists)
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/users", hubapi.UserCreate{Username: "B!", Role: hubapi.UserRoleViewer, Password: "bobs password", Reason: "x"}), 400, "invalid_username")
	bob, _ := e.login("bob", "bobs password")
	r = e.sdo(admin, http.MethodPatch, "/v1/admin/users/bob", hubapi.UserUpdate{Role: strPtr(hubapi.UserRoleAdmin), Reason: "promote"}, ifMatchHdr(1)...)
	expectCode(t, "promote", r, 200)
	var me hubapi.Me
	mustJSON(t, e.sdo(bob, http.MethodGet, "/v1/me", nil).body, &me)
	if me.Role != hubapi.UserRoleAdmin {
		t.Errorf("role not live: %+v", me)
	}
	// Last admin: with bob and admin as admins, demoting one works, the other not.
	expectCode(t, "demote admin", e.sdo(bob, http.MethodPatch, "/v1/admin/users/admin", hubapi.UserUpdate{Role: strPtr(hubapi.UserRoleViewer), Reason: "x"}, ifMatchHdr(1)...), 200)
	e.problem(e.sdo(bob, http.MethodPatch, "/v1/admin/users/bob", hubapi.UserUpdate{Disabled: boolPtr(true), Reason: "x"}, ifMatchHdr(2)...), 409, hubapi.CodeLastAdmin)
	e.problem(e.sdo(bob, http.MethodDelete, "/v1/admin/users/bob", hubapi.ChangeReason{Reason: "x"}, ifMatchHdr(2)...), 409, hubapi.CodeLastAdmin)
	// Reset: temporary password, sessions revoked.
	r = e.sdo(bob, http.MethodPost, "/v1/admin/users/viewer/reset-password", hubapi.PasswordReset{Reason: "forgot"})
	expectCode(t, "reset", r, 200)
	var pr hubapi.PasswordResetResult
	mustJSON(t, r.body, &pr)
	if pr.TemporaryPassword == "" || !pr.MustChangePassword {
		t.Errorf("reset: %+v", pr)
	}
	_, me = e.login("viewer", pr.TemporaryPassword)
	if !me.User.MustChangePassword {
		t.Error("mustChangePassword not set")
	}
	expectCode(t, "delete viewer", e.sdo(bob, http.MethodDelete, "/v1/admin/users/viewer", hubapi.ChangeReason{Reason: "left"}, ifMatchHdr(me.User.Version)...), 204)
	e.problem(e.do(http.MethodPost, "/v1/auth/login", "", hubapi.LoginRequest{Username: "viewer", Password: pr.TemporaryPassword}), 401, hubapi.CodeInvalidCredentials)
	var ul hubapi.UserList
	e.getJSON("/v1/admin/users", tokAdmin, &ul)
	if len(ul.Items) != 3 {
		t.Errorf("users: %+v", ul.Items)
	}
	e.getJSON("/v1/admin/users?includeDeleted=true", tokAdmin, &ul)
	if len(ul.Items) != 4 {
		t.Errorf("users incl. deleted: %d", len(ul.Items))
	}
	var gu hubapi.User
	if r := e.getJSON("/v1/admin/users/bob", tokAdmin, &gu); r.header.Get("ETag") != `"2"` {
		t.Errorf("user ETag %q", r.header.Get("ETag"))
	}
	var cl hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?targetType=user&actor=bob", tokUI, &cl)
	if len(cl.Items) != 3 {
		t.Errorf("bob's user changes: %+v", cl.Items)
	}
	for _, c := range cl.Items {
		if strings.Contains(string(c.After), "password") && !strings.Contains(string(c.After), "mustChangePassword") {
			t.Errorf("password material in audit: %s", c.After)
		}
	}

	// D-025 endpoints answer 501 for now.
	e.problem(e.do(http.MethodGet, "/v1/admin/datasources", tokUI, nil), 501, hubapi.CodeNotImplemented)
	e.problem(e.do(http.MethodGet, "/v1/clusters/c1/readiness", tokUI, nil), 501, hubapi.CodeNotImplemented)
}

func boolPtr(b bool) *bool { return &b }

func TestBootstrapUsers(t *testing.T) {
	st := newTestStore(t, "")
	dir := t.TempDir()
	pwFile, usersFile := dir+"/password", dir+"/users.yaml"
	if err := os.WriteFile(pwFile, []byte("bootstrap password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(usersFile, []byte("- {username: alice, role: operator, password: alices password}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Auth.BootstrapAdmin = BootstrapAdmin{Username: "root", PasswordFile: pwFile}
	cfg.DevSeed = DevSeed{Enabled: true, UsersFile: usersFile, Users: []DevUser{{Username: "bob", Role: hubapi.UserRoleViewer, Password: "bobs password"}}}
	ctx := t.Context()
	for i := 0; i < 2; i++ { // idempotent
		if err := BootstrapUsers(ctx, st, cfg, discardLog()); err != nil {
			t.Fatal(err)
		}
	}
	users, err := st.ListUsers(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range users {
		got = append(got, u.Username+":"+u.Role)
	}
	if strings.Join(got, ",") != "alice:operator,bob:viewer,root:admin" {
		t.Errorf("users: %v", got)
	}
	root, err := st.GetUser(ctx, "root")
	if err != nil || !auth.CheckPassword(root.PasswordHash, "bootstrap password") {
		t.Errorf("root password: %v", err)
	}
	// Not on a database that already has users.
	cfg.Auth.BootstrapAdmin.Username = "second"
	if err := BootstrapUsers(ctx, st, cfg, discardLog()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUser(ctx, "second"); err == nil {
		t.Error("bootstrap admin created on a non-empty database")
	}
	cfg.DevSeed.Users = []DevUser{{Username: "eve", Role: "root", Password: "eves password"}}
	if err := BootstrapUsers(ctx, st, cfg, discardLog()); err == nil {
		t.Error("invalid role accepted")
	}
	cfg.DevSeed.Users, cfg.DevSeed.UsersFile = nil, dir+"/missing.yaml"
	if err := BootstrapUsers(ctx, st, cfg, discardLog()); err == nil {
		t.Error("missing users file accepted")
	}
}
