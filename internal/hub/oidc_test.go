package hub

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/auth/oidctest"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// lockedBuffer collects the hub's logs (handlers write from several goroutines).
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type oidcEnv struct {
	*testEnv
	idp  *oidctest.Provider
	logs *lockedBuffer
}

// newOIDCEnv is a hub with sign-in through a fake provider: platform-admins → admin,
// platform → operator, engineering → viewer, no default role.
func newOIDCEnv(t *testing.T, mod func(*auth.OIDCConfig)) *oidcEnv {
	t.Helper()
	e := newEnv(t)
	e.seedUsers()
	idp := oidctest.New(t)
	idp.RedirectURL = e.ts.URL + "/v1/auth/oidc/callback"
	cfg := auth.OIDCConfig{Enabled: true, Name: "Test IdP", Issuer: idp.Issuer, ClientID: idp.ClientID, RedirectURL: idp.RedirectURL,
		RoleMappings: []auth.RoleMapping{{Group: "platform-admins", Role: "admin"}, {Group: "platform", Role: "operator"}, {Group: "engineering", Role: "viewer"}}}
	if mod != nil {
		mod(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_OIDC_CLIENT_SECRET", idp.ClientSecret)
	o, err := auth.NewOIDC(cfg)
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	e.srv.Cfg.Auth.OIDC = cfg
	e.srv.OIDC = o
	e.srv.Log = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &oidcEnv{testEnv: e, idp: idp, logs: logs}
}

// browser is a client that does not follow redirects and keeps cookies by hand.
type browser struct {
	t       *testing.T
	c       *http.Client
	cookies map[string]string
}

func newBrowser(t *testing.T) *browser {
	return &browser{t: t, cookies: map[string]string{}, c: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// page is what the browser keeps of a response (the body is read and closed).
type page struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

func (p page) Cookies() []*http.Cookie { return p.cookies }

func (b *browser) get(rawURL string) page {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	for k, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	return page{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}
}

func (b *browser) session() session {
	return session{cookie: b.cookies[hubapi.SessionCookie]}
}

// oidcSignIn runs login → provider → callback for subject and returns the callback's redirect target.
func (e *oidcEnv) oidcSignIn(b *browser, subject, next string) string {
	e.t.Helper()
	r := b.get(e.ts.URL + "/v1/auth/oidc/login?next=" + url.QueryEscape(next))
	if r.StatusCode != http.StatusFound {
		e.t.Fatalf("login: %d", r.StatusCode)
	}
	cb, err := e.idp.Authorize(r.Header.Get("Location"), subject)
	if err != nil {
		e.t.Fatal(err)
	}
	r = b.get(cb)
	if r.StatusCode != http.StatusFound {
		e.t.Fatalf("callback: %d", r.StatusCode)
	}
	return r.Header.Get("Location")
}

func (e *oidcEnv) me(s session) (int, hubapi.Me) {
	e.t.Helper()
	r := e.sdo(s, http.MethodGet, "/v1/me", nil)
	var m hubapi.Me
	if r.code == http.StatusOK {
		mustJSON(e.t, r.body, &m)
	}
	return r.code, m
}

func TestOIDCSignIn(t *testing.T) {
	e := newOIDCEnv(t, nil)
	e.idp.AddUser(oidctest.User{Subject: "sub-alice", Email: "Alice@Example.com", Name: "Alice", Groups: []string{"platform", "sales"}})

	var prov hubapi.AuthProviders
	mustJSON(t, e.do(http.MethodGet, "/v1/auth/providers", "", nil).body, &prov)
	if !prov.Local || prov.OIDC == nil || prov.OIDC.Name != "Test IdP" || prov.OIDC.LoginURL != "/v1/auth/oidc/login" {
		t.Fatalf("providers: %+v", prov)
	}

	b := newBrowser(t)
	r := b.get(e.ts.URL + "/v1/auth/oidc/login?next=" + url.QueryEscape("/opportunities?kind=StatefulSet"))
	var stateCookie *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == "kerf_oidc" {
			stateCookie = c
		}
	}
	if stateCookie == nil || !stateCookie.HttpOnly || stateCookie.SameSite != http.SameSiteLaxMode || stateCookie.Path != "/v1/auth/oidc" {
		t.Fatalf("state cookie %+v", stateCookie)
	}
	cb, err := e.idp.Authorize(r.Header.Get("Location"), "sub-alice")
	if err != nil {
		t.Fatal(err)
	}
	// The callback in another browser (no state cookie) is refused.
	other := newBrowser(t)
	if loc := other.get(cb).Header.Get("Location"); loc != "/login?error=oidc_expired" {
		t.Errorf("callback without the state cookie → %q", loc)
	}
	r = b.get(cb)
	if loc := r.Header.Get("Location"); r.StatusCode != http.StatusFound || loc != "/opportunities?kind=StatefulSet" {
		t.Fatalf("callback → %d %q", r.StatusCode, loc)
	}
	if _, ok := b.cookies["kerf_oidc"]; ok {
		t.Error("state cookie not cleared")
	}
	// The same callback again (state used once).
	b2 := newBrowser(t)
	b2.cookies["kerf_oidc"] = stateCookie.Value
	if loc := b2.get(cb).Header.Get("Location"); loc != "/login?error=oidc_expired" {
		t.Errorf("replayed callback → %q", loc)
	}

	code, me := e.me(b.session())
	if code != http.StatusOK || me.AuthMethod != hubapi.AuthMethodSession || me.Role != hubapi.UserRoleOperator || me.User == nil ||
		me.User.Username != "alice@example.com" || me.User.Source != hubapi.UserSourceOIDC || me.User.Email != "Alice@Example.com" ||
		me.User.DisplayName != "Alice" || me.CSRFToken == "" {
		t.Fatalf("me: %d %+v %+v", code, me, me.User)
	}
	s := session{cookie: b.cookies[hubapi.SessionCookie], csrf: me.CSRFToken}

	// Every action records the IdP subject and e-mail.
	expectCode(t, "pause", e.sdo(s, http.MethodPut, "/v1/clusters/c1/automation", hubapi.AutomationUpdate{Paused: true, Reason: "oidc test"}), 200)
	var audit hubapi.ConfigChangeList
	mustJSON(t, e.getJSONBody("/v1/audit/config?limit=10", tokAdmin), &audit)
	var created, paused bool
	for _, c := range audit.Items {
		switch c.Action {
		case hubapi.ActionUserCreated:
			created = c.Target.ID == "alice@example.com" && c.Actor.Type == hubapi.ActorSystem && c.Actor.Name == "oidc"
		case hubapi.ActionAutomationPaused:
			paused = reflect.DeepEqual(c.Actor, hubapi.Actor{Type: hubapi.ActorUser, Name: "alice@example.com", Role: "operator",
				Subject: "sub-alice", Email: "Alice@Example.com"})
		}
	}
	if !created || !paused {
		t.Errorf("audit: user created %v, pause by the IdP identity %v: %+v", created, paused, audit.Items)
	}

	// An IdP user has no password: password sign-in, password change and reset are refused.
	e.problem(e.do(http.MethodPost, "/v1/auth/login", "", hubapi.LoginRequest{Username: "alice@example.com", Password: "anything-at-all"}),
		401, hubapi.CodeInvalidCredentials)
	e.problem(e.sdo(s, http.MethodPost, "/v1/me/password", hubapi.PasswordChange{CurrentPassword: "x", NewPassword: "y-long-enough"}), 409, hubapi.CodeIdPManaged)
	admin, _ := e.login("admin", testPassword)
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/users/alice@example.com/reset-password", hubapi.PasswordReset{Reason: "x"}), 409, hubapi.CodeIdPManaged)
	role := "admin"
	e.problem(e.sdo(admin, http.MethodPatch, "/v1/admin/users/alice@example.com", hubapi.UserUpdate{Role: &role, Reason: "x"}, ifMatchHdr(1)...),
		409, hubapi.CodeIdPManaged)

	// A second sign-in keeps the same user.
	b3 := newBrowser(t)
	e.oidcSignIn(b3, "sub-alice", "/")
	if _, me := e.me(b3.session()); me.User == nil || me.User.Username != "alice@example.com" {
		t.Errorf("second sign-in: %+v", me)
	}

	// Sign-out ends the session like a local one.
	expectCode(t, "logout", e.sdo(s, http.MethodPost, "/v1/auth/logout", nil), 204)
	if code, _ := e.me(s); code != http.StatusUnauthorized {
		t.Errorf("me after sign-out: %d", code)
	}
	e.checkNoTokensLogged(b, b3)
}

func (e *testEnv) getJSONBody(path, token string) []byte {
	e.t.Helper()
	r := e.do(http.MethodGet, path, token, nil)
	if r.code != http.StatusOK {
		e.t.Fatalf("GET %s: %d %s", path, r.code, r.body)
	}
	return r.body
}

// checkNoTokensLogged fails when any token of the provider or any session cookie is in the logs.
func (e *oidcEnv) checkNoTokensLogged(browsers ...*browser) {
	e.t.Helper()
	logs := e.logs.String()
	if !strings.Contains(logs, `"msg":"login"`) {
		e.t.Fatalf("no login logged; logs:\n%s", logs)
	}
	secrets := e.idp.Issued()
	for _, b := range browsers {
		for _, v := range b.cookies {
			secrets = append(secrets, v)
		}
	}
	secrets = append(secrets, e.idp.ClientSecret)
	for _, s := range secrets {
		if len(s) > 8 && strings.Contains(logs, s) {
			e.t.Errorf("a token or secret appears in the logs: %.12s…", s)
		}
	}
}

func TestOIDCRoleFollowsGroupsAtRefresh(t *testing.T) {
	e := newOIDCEnv(t, func(c *auth.OIDCConfig) { c.RefreshInterval = "5m" })
	e.idp.AddUser(oidctest.User{Subject: "sub-bob", Email: "bob@example.com", Groups: []string{"platform-admins"}})
	b := newBrowser(t)
	e.oidcSignIn(b, "sub-bob", "/")
	s := b.session()
	if _, me := e.me(s); me.Role != hubapi.UserRoleAdmin {
		t.Fatalf("role at sign-in: %q", me.Role)
	}

	// Removed from platform-admins: still admin until the refresh interval passes, then viewer.
	e.idp.SetGroups("sub-bob", "engineering")
	e.advance(2 * time.Minute)
	if _, me := e.me(s); me.Role != hubapi.UserRoleAdmin {
		t.Errorf("role before the refresh: %q", me.Role)
	}
	e.advance(4 * time.Minute)
	if _, me := e.me(s); me.Role != hubapi.UserRoleViewer {
		t.Errorf("role after the refresh: %q, want viewer", me.Role)
	}
	e.problem(e.sdo(s, http.MethodGet, "/v1/admin/users", nil), 403, "role_forbidden")
	var audit hubapi.ConfigChangeList
	mustJSON(t, e.getJSONBody("/v1/audit/config?limit=5", tokAdmin), &audit)
	if len(audit.Items) == 0 || audit.Items[0].Action != hubapi.ActionUserUpdated || !strings.Contains(audit.Items[0].Reason, "groups") {
		t.Errorf("role change not audited: %+v", audit.Items)
	}

	// Removed from every mapped group: the session ends at the next refresh.
	e.idp.SetGroups("sub-bob", "sales")
	e.advance(6 * time.Minute)
	if code, _ := e.me(s); code != http.StatusUnauthorized {
		t.Errorf("me without a mapped group: %d, want 401", code)
	}
	// And the next sign-in is refused.
	if loc := e.oidcSignIn(newBrowser(t), "sub-bob", "/clusters"); loc != "/login?error=oidc_denied&next=%2Fclusters" {
		t.Errorf("sign-in without a mapped group → %q", loc)
	}

	// The provider revokes the refresh token: the session ends at the next refresh.
	e.idp.SetGroups("sub-bob", "platform")
	b2 := newBrowser(t)
	e.oidcSignIn(b2, "sub-bob", "/")
	e.idp.RevokeRefreshTokens()
	e.advance(6 * time.Minute)
	if code, _ := e.me(b2.session()); code != http.StatusUnauthorized {
		t.Errorf("me after the provider revoked the refresh token: %d, want 401", code)
	}
	e.checkNoTokensLogged(b, b2)
}

func TestOIDCWithoutRefreshToken(t *testing.T) {
	e := newOIDCEnv(t, func(c *auth.OIDCConfig) { c.RefreshInterval = "10m" })
	e.idp.OmitRefreshToken = true
	e.idp.AddUser(oidctest.User{Subject: "sub-c", Email: "c@example.com", Groups: []string{"engineering"}})
	b := newBrowser(t)
	e.oidcSignIn(b, "sub-c", "/")
	_, me := e.me(b.session())
	if me.SessionExpiresAt == nil || me.SessionExpiresAt.Sub(e.now) > 10*time.Minute+time.Second {
		t.Fatalf("session without refresh token lasts until %v (now %v), want one refresh interval", me.SessionExpiresAt, e.now)
	}
	e.advance(11 * time.Minute)
	if code, _ := e.me(b.session()); code != http.StatusUnauthorized {
		t.Errorf("me after the interval: %d", code)
	}
}

func TestOIDCCallbackErrors(t *testing.T) {
	e := newOIDCEnv(t, nil)
	// "operator" is a local user.
	e.idp.AddUser(oidctest.User{Subject: "sub-op", Email: "operator", Groups: []string{"platform"}})
	e.idp.AddUser(oidctest.User{Subject: "sub-x", Email: "x@example.com", Groups: []string{"platform"}})
	// The username of a local user is never taken over.
	if loc := e.oidcSignIn(newBrowser(t), "sub-op", "/"); loc != "/login?error=oidc_conflict" {
		t.Errorf("username of a local user → %q", loc)
	}
	// The provider refuses (e.g. the user cancelled).
	b := newBrowser(t)
	r := b.get(e.ts.URL + "/v1/auth/oidc/login")
	state, _ := url.Parse(r.Header.Get("Location"))
	if loc := b.get(e.ts.URL + "/v1/auth/oidc/callback?error=access_denied&state=" + state.Query().Get("state")).Header.Get("Location"); loc != "/login?error=oidc_failed" {
		t.Errorf("provider error → %q", loc)
	}
	// next= to another site is ignored.
	if loc := e.oidcSignIn(newBrowser(t), "sub-x", "https://evil.example/"); loc != "/" {
		t.Errorf("next to another site → %q", loc)
	}
	// A disabled IdP user cannot sign in.
	admin, _ := e.login("admin", testPassword)
	off := true
	expectCode(t, "disable", e.sdo(admin, http.MethodPatch, "/v1/admin/users/x@example.com", hubapi.UserUpdate{Disabled: &off, Reason: "left"}, ifMatchHdr(1)...), 200)
	if loc := e.oidcSignIn(newBrowser(t), "sub-x", "/"); loc != "/login?error=oidc_denied" {
		t.Errorf("disabled user → %q", loc)
	}
	// The provider is down: the sign-in page says so.
	e.srv.OIDC.Cfg.Issuer = "http://127.0.0.1:1"
	e.srv.OIDC = mustOIDC(t, e.srv.OIDC.Cfg)
	if loc := newBrowser(t).get(e.ts.URL + "/v1/auth/oidc/login").Header.Get("Location"); loc != "/login?error=oidc_unavailable" {
		t.Errorf("provider down → %q", loc)
	}
}

func mustOIDC(t *testing.T, cfg auth.OIDCConfig) *auth.OIDC {
	t.Helper()
	o, err := auth.NewOIDC(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestLocalSignInOff(t *testing.T) {
	e := newOIDCEnv(t, nil)
	off := false
	e.srv.Cfg.Auth.Local.Enabled = &off
	var prov hubapi.AuthProviders
	mustJSON(t, e.do(http.MethodGet, "/v1/auth/providers", "", nil).body, &prov)
	if prov.Local || prov.OIDC == nil {
		t.Errorf("providers with local sign-in off: %+v", prov)
	}
	// Only the break-glass admin (the bootstrap admin's name, "admin") may use a password; the others
	// get the answer of a wrong password.
	e.login("admin", testPassword)
	for _, u := range []string{"operator", "viewer"} {
		e.problem(e.do(http.MethodPost, "/v1/auth/login", "", hubapi.LoginRequest{Username: u, Password: testPassword}), 401, hubapi.CodeInvalidCredentials)
	}
	e.srv.Cfg.Auth.Local.BreakGlassUser = "operator"
	e.login("operator", testPassword)
	e.problem(e.do(http.MethodPost, "/v1/auth/login", "", hubapi.LoginRequest{Username: "admin", Password: testPassword}), 401, hubapi.CodeInvalidCredentials)
}

func TestSafeNextPath(t *testing.T) {
	for in, want := range map[string]string{
		"/opportunities?kind=x": "/opportunities?kind=x", "/": "/", "": "/", "x": "/", "https://evil/": "/", "//evil/": "/",
		"/\\evil/": "/", "/\t/evil": "/", "/login": "/", "/login?next=/": "/", "/clusters#a": "/clusters#a",
	} {
		if got := safeNextPath(in); got != want {
			t.Errorf("safeNextPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOIDCTeamMappings: group mappings with teams give team-scoped access, re-read at refresh.
func TestOIDCTeamMappings(t *testing.T) {
	e := newOIDCEnv(t, func(c *auth.OIDCConfig) {
		c.RefreshInterval = "5m"
		c.RoleMappings = append(c.RoleMappings, auth.RoleMapping{Group: "payments-devs", Role: "operator", Teams: []string{"pay"}})
	})
	expectCode(t, "team", e.do(http.MethodPost, "/v1/admin/teams", tokAdmin, hubapi.TeamInput{ID: "pay", Namespaces: []string{"payments"}, Reason: "x"}), 201)
	e.idp.AddUser(oidctest.User{Subject: "sub-carol", Email: "carol@example.com", Groups: []string{"payments-devs"}})
	b := newBrowser(t)
	e.oidcSignIn(b, "sub-carol", "/")
	s := b.session()
	_, me := e.me(s)
	if me.Role != hubapi.UserRoleOperator || me.Scope != hubapi.ScopeTeams || !reflect.DeepEqual(me.TeamRoles, []hubapi.TeamRole{{Team: "pay", Role: "operator"}}) {
		t.Fatalf("me: %+v", me)
	}
	var l hubapi.WorkloadList
	mustJSON(t, e.sdo(s, http.MethodGet, "/v1/workloads", nil).body, &l)
	for _, w := range l.Items {
		if w.Team != "pay" {
			t.Errorf("team-scoped IdP user sees %s of team %q", w.ID, w.Team)
		}
	}
	if l.Total != 3 { // payments on c1 and c2
		t.Errorf("workloads: %d", l.Total)
	}
	// Also in engineering: fleet viewer, still operator on pay.
	e.idp.SetGroups("sub-carol", "payments-devs", "engineering")
	e.advance(6 * time.Minute)
	if _, me := e.me(s); me.Scope != hubapi.ScopeFleet || me.Role != hubapi.UserRoleOperator || len(me.TeamRoles) != 1 {
		t.Errorf("after the refresh: %+v", me)
	}
	mustJSON(t, e.sdo(s, http.MethodGet, "/v1/workloads", nil).body, &l)
	if l.Total != 6 {
		t.Errorf("fleet viewer sees %d workloads", l.Total)
	}
	// The IdP manages the bindings.
	var u hubapi.User
	e.getJSON("/v1/admin/users/carol@example.com", tokAdmin, &u)
	e.problem(e.do(http.MethodPatch, "/v1/admin/users/carol@example.com", tokAdmin, hubapi.UserUpdate{Scope: strPtr(hubapi.ScopeFleet), Reason: "x"},
		ifMatchHdr(u.Version)...), 409, hubapi.CodeIdPManaged)
}
