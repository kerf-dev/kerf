package hub

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/auth/samltest"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

type samlEnv struct {
	*testEnv
	idp  *samltest.IdP
	logs *lockedBuffer
}

// newSAMLEnv is a hub with sign-in through a test SAML IdP: platform-admins → admin, platform →
// operator, engineering → viewer, no default role.
func newSAMLEnv(t *testing.T, mod func(*auth.SAMLConfig)) *samlEnv {
	t.Helper()
	e := newEnv(t)
	e.seedUsers()
	idp := samltest.New(t)
	idp.Now = e.srv.now
	cfg := auth.SAMLConfig{Enabled: true, Name: "Example IdP", ACSURL: e.ts.URL + "/v1/auth/saml/acs", IdPMetadataFile: idp.MetadataFile(t),
		RoleMappings: []auth.RoleMapping{{Group: "platform-admins", Role: "admin"}, {Group: "platform", Role: "operator"}, {Group: "engineering", Role: "viewer"}}}
	if mod != nil {
		mod(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	sp, err := auth.NewSAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sp.Now = e.srv.now
	logs := &lockedBuffer{}
	e.srv.Cfg.Auth.SAML = cfg
	e.srv.SAML = sp
	e.srv.Log = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &samlEnv{testEnv: e, idp: idp, logs: logs}
}

// postForm posts a form with the browser's cookies, as a browser does for the IdP's auto-submitted
// form. withCookies false: a cross-site POST, which carries no SameSite=Lax cookie.
func (b *browser) postForm(rawURL string, form url.Values, withCookies bool) page {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if withCookies {
		for k, v := range b.cookies {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}
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

// samlStart runs login → IdP and returns the IdP's form for nameID.
func (e *samlEnv) samlStart(b *browser, nameID, next string, o samltest.Options) url.Values {
	e.t.Helper()
	r := b.get(e.ts.URL + "/v1/auth/saml/login?next=" + url.QueryEscape(next))
	if r.StatusCode != http.StatusFound {
		e.t.Fatalf("login: %d", r.StatusCode)
	}
	form, acs, err := e.idp.Respond(r.Header.Get("Location"), nameID, o)
	if err != nil {
		e.t.Fatal(err)
	}
	if acs != e.ts.URL+"/v1/auth/saml/acs" {
		e.t.Fatalf("ACS %s", acs)
	}
	return form
}

// samlSignIn runs login → IdP → ACS → finish and returns where the browser ends up.
func (e *samlEnv) samlSignIn(b *browser, nameID, next string, o samltest.Options) string {
	e.t.Helper()
	form := e.samlStart(b, nameID, next, o)
	r := b.postForm(e.ts.URL+"/v1/auth/saml/acs", form, false)
	if r.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("acs: %d", r.StatusCode)
	}
	loc := r.Header.Get("Location")
	if loc != "/v1/auth/saml/finish" {
		return loc
	}
	r = b.get(e.ts.URL + loc)
	if r.StatusCode != http.StatusFound && r.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("finish: %d", r.StatusCode)
	}
	return r.Header.Get("Location")
}

func (e *samlEnv) me(s session) (int, hubapi.Me) {
	e.t.Helper()
	r := e.sdo(s, http.MethodGet, "/v1/me", nil)
	var m hubapi.Me
	if r.code == http.StatusOK {
		mustJSON(e.t, r.body, &m)
	}
	return r.code, m
}

func TestSAMLSignIn(t *testing.T) {
	e := newSAMLEnv(t, nil)
	e.idp.AddUser(samltest.User{NameID: "u-alice", Email: "Alice@Example.com", Name: "Alice", Groups: []string{"platform", "sales"}})

	var prov hubapi.AuthProviders
	mustJSON(t, e.do(http.MethodGet, "/v1/auth/providers", "", nil).body, &prov)
	if !prov.Local || prov.OIDC != nil || prov.SAML == nil || prov.SAML.Name != "Example IdP" || prov.SAML.LoginURL != "/v1/auth/saml/login" {
		t.Fatalf("providers: %+v", prov)
	}
	md := e.do(http.MethodGet, "/v1/auth/saml/metadata", "", nil)
	if md.code != http.StatusOK || md.header.Get("Content-Type") != "application/samlmetadata+xml" ||
		!strings.Contains(string(md.body), `entityID="`+e.ts.URL+`/v1/auth/saml/metadata"`) {
		t.Fatalf("SP metadata: %d %s %s", md.code, md.header.Get("Content-Type"), md.body)
	}

	b := newBrowser(t)
	r := b.get(e.ts.URL + "/v1/auth/saml/login?next=" + url.QueryEscape("/opportunities?kind=StatefulSet"))
	var stateCookie *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == "kerf_saml" {
			stateCookie = c
		}
	}
	if r.StatusCode != http.StatusFound || stateCookie == nil || !stateCookie.HttpOnly || stateCookie.SameSite != http.SameSiteLaxMode ||
		stateCookie.Path != "/v1/auth/saml" || stateCookie.MaxAge != 600 {
		t.Fatalf("login %d, state cookie %+v", r.StatusCode, stateCookie)
	}
	req, err := e.idp.ParseRedirect(r.Header.Get("Location"))
	if err != nil || req.RelayState != stateCookie.Value {
		t.Fatalf("authn request %+v (%v): RelayState must be the cookie's", req, err)
	}
	form, _, err := e.idp.Respond(r.Header.Get("Location"), "u-alice", samltest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The IdP's POST is cross-site: no cookie. The ACS starts no session, it sends the browser on.
	r = b.postForm(e.ts.URL+"/v1/auth/saml/acs", form, false)
	if loc := r.Header.Get("Location"); r.StatusCode != http.StatusSeeOther || loc != "/v1/auth/saml/finish" {
		t.Fatalf("acs → %d %q", r.StatusCode, loc)
	}
	for _, c := range r.Cookies() {
		if c.Name == hubapi.SessionCookie {
			t.Fatal("the ACS set a session cookie")
		}
	}
	// The same response again (a replay before finish): refused.
	if loc := newBrowser(t).postForm(e.ts.URL+"/v1/auth/saml/acs", form, false).Header.Get("Location"); loc != "/login?error=saml_expired" {
		t.Errorf("replayed response → %q", loc)
	}
	// Finish in another browser (no state cookie, or another one): refused, and the sign-in stays for its browser.
	if loc := newBrowser(t).get(e.ts.URL + "/v1/auth/saml/finish").Header.Get("Location"); loc != "/login?error=saml_expired" {
		t.Errorf("finish without the state cookie → %q", loc)
	}
	other := newBrowser(t)
	other.cookies["kerf_saml"] = "not-the-state-of-this-sign-in"
	if loc := other.get(e.ts.URL + "/v1/auth/saml/finish").Header.Get("Location"); loc != "/login?error=saml_expired" {
		t.Errorf("finish with another state cookie → %q", loc)
	}
	r = b.get(e.ts.URL + "/v1/auth/saml/finish")
	if loc := r.Header.Get("Location"); r.StatusCode != http.StatusFound || loc != "/opportunities?kind=StatefulSet" {
		t.Fatalf("finish → %d %q", r.StatusCode, loc)
	}
	if _, ok := b.cookies["kerf_saml"]; ok {
		t.Error("state cookie not cleared")
	}
	// Finish again with the same cookie: used once.
	b2 := newBrowser(t)
	b2.cookies["kerf_saml"] = stateCookie.Value
	if loc := b2.get(e.ts.URL + "/v1/auth/saml/finish").Header.Get("Location"); loc != "/login?error=saml_expired" {
		t.Errorf("second finish → %q", loc)
	}

	code, me := e.me(b.session())
	if code != http.StatusOK || me.Role != hubapi.UserRoleOperator || me.User == nil || me.User.Username != "alice@example.com" ||
		me.User.Source != hubapi.UserSourceSAML || me.User.Email != "Alice@Example.com" || me.User.DisplayName != "Alice" || me.CSRFToken == "" {
		t.Fatalf("me: %d %+v %+v", code, me, me.User)
	}
	if me.SessionExpiresAt == nil || !me.SessionExpiresAt.Equal(e.now.Add(12*time.Hour)) {
		t.Errorf("session until %v, want sessionTTL (12h)", me.SessionExpiresAt)
	}
	s := session{cookie: b.cookies[hubapi.SessionCookie], csrf: me.CSRFToken}

	// Every action records the IdP subject (the NameID) and e-mail.
	expectCode(t, "pause", e.sdo(s, http.MethodPut, "/v1/clusters/c1/automation", hubapi.AutomationUpdate{Paused: true, Reason: "saml test"}), 200)
	var audit hubapi.ConfigChangeList
	mustJSON(t, e.getJSONBody("/v1/audit/config?limit=10", tokAdmin), &audit)
	var created, paused bool
	for _, c := range audit.Items {
		switch c.Action {
		case hubapi.ActionUserCreated:
			created = c.Target.ID == "alice@example.com" && c.Actor.Type == hubapi.ActorSystem && c.Actor.Name == "saml" &&
				strings.Contains(c.Reason, "Example IdP")
		case hubapi.ActionAutomationPaused:
			paused = reflect.DeepEqual(c.Actor, hubapi.Actor{Type: hubapi.ActorUser, Name: "alice@example.com", Role: "operator",
				Subject: "u-alice", Email: "Alice@Example.com"})
		}
	}
	if !created || !paused {
		t.Errorf("audit: user created by saml %v, pause by the IdP identity %v: %+v", created, paused, audit.Items)
	}

	// An IdP user has no password, and its role is not edited in Kerf.
	e.problem(e.do(http.MethodPost, "/v1/auth/login", "", hubapi.LoginRequest{Username: "alice@example.com", Password: "anything-at-all"}),
		401, hubapi.CodeInvalidCredentials)
	e.problem(e.sdo(s, http.MethodPost, "/v1/me/password", hubapi.PasswordChange{CurrentPassword: "x", NewPassword: "y-long-enough"}), 409, hubapi.CodeIdPManaged)
	admin, _ := e.login("admin", testPassword)
	e.problem(e.sdo(admin, http.MethodPost, "/v1/admin/users/alice@example.com/reset-password", hubapi.PasswordReset{Reason: "x"}), 409, hubapi.CodeIdPManaged)
	role := "admin"
	e.problem(e.sdo(admin, http.MethodPatch, "/v1/admin/users/alice@example.com", hubapi.UserUpdate{Role: &role, Reason: "x"}, ifMatchHdr(1)...),
		409, hubapi.CodeIdPManaged)

	// Sign-out ends the session like a local one.
	expectCode(t, "logout", e.sdo(s, http.MethodPost, "/v1/auth/logout", nil), 204)
	if code, _ := e.me(s); code != http.StatusUnauthorized {
		t.Errorf("me after sign-out: %d", code)
	}

	// No response, assertion or cookie value in the logs.
	logs := e.logs.String()
	if !strings.Contains(logs, `"method":"saml"`) {
		t.Fatalf("no SAML login logged:\n%s", logs)
	}
	for _, v := range []string{form.Get("SAMLResponse")[:40], stateCookie.Value, s.cookie, s.csrf} {
		if strings.Contains(logs, v) {
			t.Errorf("a secret appears in the logs: %.12s…", v)
		}
	}
}

// TestSAMLGroupsAtNextSignIn: the role follows the IdP's groups at each sign-in; a user removed from
// every mapped group is refused and loses its sessions.
func TestSAMLGroupsAtNextSignIn(t *testing.T) {
	e := newSAMLEnv(t, nil)
	e.idp.AddUser(samltest.User{NameID: "u-bob", Email: "bob@example.com", Groups: []string{"platform-admins"}})
	b := newBrowser(t)
	if loc := e.samlSignIn(b, "u-bob", "/", samltest.Options{}); loc != "/" {
		t.Fatalf("sign-in → %q", loc)
	}
	s := b.session()
	if _, me := e.me(s); me.Role != hubapi.UserRoleAdmin {
		t.Fatalf("role at sign-in: %q", me.Role)
	}

	// Moved to engineering: the next sign-in makes Bob a viewer (every session of the user follows).
	e.idp.SetGroups("u-bob", "engineering")
	e.advance(time.Minute)
	b2 := newBrowser(t)
	e.samlSignIn(b2, "u-bob", "/", samltest.Options{})
	if _, me := e.me(b2.session()); me.Role != hubapi.UserRoleViewer {
		t.Errorf("role after the next sign-in: %q, want viewer", me.Role)
	}
	if _, me := e.me(s); me.Role != hubapi.UserRoleViewer {
		t.Errorf("role of the earlier session: %q, want viewer", me.Role)
	}
	e.problem(e.sdo(s, http.MethodGet, "/v1/admin/users", nil), 403, "role_forbidden")
	var audit hubapi.ConfigChangeList
	mustJSON(t, e.getJSONBody("/v1/audit/config?limit=5", tokAdmin), &audit)
	if len(audit.Items) == 0 || audit.Items[0].Action != hubapi.ActionUserUpdated || audit.Items[0].Actor.Name != "saml" ||
		!strings.Contains(audit.Items[0].Reason, "groups") {
		t.Errorf("role change not audited: %+v", audit.Items)
	}

	// Removed from every mapped group: the next sign-in is refused and the user's sessions end.
	e.idp.SetGroups("u-bob", "sales")
	e.advance(time.Minute)
	if loc := e.samlSignIn(newBrowser(t), "u-bob", "/clusters", samltest.Options{}); loc != "/login?error=saml_denied&next=%2Fclusters" {
		t.Errorf("sign-in without a mapped group → %q", loc)
	}
	for i, ss := range []session{s, b2.session()} {
		if code, _ := e.me(ss); code != http.StatusUnauthorized {
			t.Errorf("session %d after the IdP removed every mapped group: %d, want 401", i, code)
		}
	}
	// No groups at all: refused too.
	e.idp.SetGroups("u-bob")
	if loc := e.samlSignIn(newBrowser(t), "u-bob", "/", samltest.Options{}); loc != "/login?error=saml_denied" {
		t.Errorf("sign-in without groups → %q", loc)
	}
	// Back in a group: signs in again as the same user.
	e.idp.SetGroups("u-bob", "platform")
	b3 := newBrowser(t)
	e.samlSignIn(b3, "u-bob", "/", samltest.Options{})
	if _, me := e.me(b3.session()); me.Role != hubapi.UserRoleOperator || me.User.Username != "bob@example.com" {
		t.Errorf("back in a group: %+v", me)
	}
}

func TestSAMLErrors(t *testing.T) {
	e := newSAMLEnv(t, nil)
	// "operator" is a local user.
	e.idp.AddUser(samltest.User{NameID: "u-op", Email: "operator", Groups: []string{"platform"}})
	e.idp.AddUser(samltest.User{NameID: "u-x", Email: "x@example.com", Groups: []string{"platform"}})
	// The username of a local user is never taken over.
	if loc := e.samlSignIn(newBrowser(t), "u-op", "/", samltest.Options{}); loc != "/login?error=saml_conflict" {
		t.Errorf("username of a local user → %q", loc)
	}
	// Invalid responses: unsigned, for another audience, expired.
	for name, o := range map[string]samltest.Options{
		"unsigned":         {Sign: samltest.SignNone},
		"another audience": {Audience: "https://other.example.com/saml"},
		"expired":          {Skew: -15 * time.Minute},
		"wrapped":          {Wrap: "nested", Wrapper: samltest.User{NameID: "u-evil", Email: "evil@example.com", Groups: []string{"platform-admins"}}},
	} {
		if loc := e.samlSignIn(newBrowser(t), "u-x", "/", o); loc != "/login?error=saml_failed" {
			t.Errorf("%s → %q", name, loc)
		}
	}
	// A response to another sign-in, posted with this browser's RelayState: refused.
	b1, b2 := newBrowser(t), newBrowser(t)
	f1 := e.samlStart(b1, "u-x", "/", samltest.Options{})
	f2 := e.samlStart(b2, "u-x", "/", samltest.Options{})
	f1.Set("RelayState", f2.Get("RelayState"))
	if loc := b2.postForm(e.ts.URL+"/v1/auth/saml/acs", f1, false).Header.Get("Location"); loc != "/login?error=saml_failed" {
		t.Errorf("response of another sign-in → %q", loc)
	}
	// IdP-initiated (no RelayState of ours): refused.
	f1.Del("RelayState")
	if loc := b1.postForm(e.ts.URL+"/v1/auth/saml/acs", f1, false).Header.Get("Location"); loc != "/login?error=saml_expired" {
		t.Errorf("IdP-initiated → %q", loc)
	}
	// The sign-in took longer than 10 minutes.
	b3 := newBrowser(t)
	f3 := e.samlStart(b3, "u-x", "/", samltest.Options{})
	e.advance(11 * time.Minute)
	if loc := b3.postForm(e.ts.URL+"/v1/auth/saml/acs", f3, false).Header.Get("Location"); loc != "/login?error=saml_expired" {
		t.Errorf("late response → %q", loc)
	}
	// next= to another site is ignored.
	if loc := e.samlSignIn(newBrowser(t), "u-x", "https://evil.example/", samltest.Options{}); loc != "/" {
		t.Errorf("next to another site → %q", loc)
	}
	// The IdP limits the session.
	until := e.now.Add(2 * time.Hour).UTC().Truncate(time.Second)
	b4 := newBrowser(t)
	e.samlSignIn(b4, "u-x", "/", samltest.Options{SessionNotOnOrAfter: &until})
	if _, me := e.me(b4.session()); me.SessionExpiresAt == nil || !me.SessionExpiresAt.Equal(until) {
		t.Errorf("session until %v, want the IdP's SessionNotOnOrAfter %v", me.SessionExpiresAt, until)
	}
	// A disabled IdP user cannot sign in.
	admin, _ := e.login("admin", testPassword)
	off := true
	expectCode(t, "disable", e.sdo(admin, http.MethodPatch, "/v1/admin/users/x@example.com", hubapi.UserUpdate{Disabled: &off, Reason: "left"}, ifMatchHdr(1)...), 200)
	if loc := e.samlSignIn(newBrowser(t), "u-x", "/", samltest.Options{}); loc != "/login?error=saml_denied" {
		t.Errorf("disabled user → %q", loc)
	}
	// The IdP's metadata cannot be read: the sign-in page says so.
	cfg := e.srv.SAML.Cfg
	cfg.IdPMetadataFile = t.TempDir() + "/missing.xml"
	sp, err := auth.NewSAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SAML = sp
	if loc := newBrowser(t).get(e.ts.URL + "/v1/auth/saml/login").Header.Get("Location"); loc != "/login?error=saml_unavailable" {
		t.Errorf("metadata unavailable → %q", loc)
	}
}

// TestSAMLTurnedOff: without SAML configured its endpoints are 404 and SAML sessions end.
func TestSAMLTurnedOff(t *testing.T) {
	e := newSAMLEnv(t, nil)
	e.idp.AddUser(samltest.User{NameID: "u-c", Email: "c@example.com", Groups: []string{"engineering"}})
	b := newBrowser(t)
	e.samlSignIn(b, "u-c", "/", samltest.Options{})
	if code, _ := e.me(b.session()); code != http.StatusOK {
		t.Fatalf("me: %d", code)
	}
	e.srv.SAML = nil
	if code, _ := e.me(b.session()); code != http.StatusUnauthorized {
		t.Errorf("me after SAML was turned off: %d, want 401", code)
	}
	for _, p := range []string{"/v1/auth/saml/metadata", "/v1/auth/saml/login", "/v1/auth/saml/finish"} {
		if r := e.do(http.MethodGet, p, "", nil); r.code != http.StatusNotFound {
			t.Errorf("GET %s: %d", p, r.code)
		}
	}
	var prov hubapi.AuthProviders
	mustJSON(t, e.do(http.MethodGet, "/v1/auth/providers", "", nil).body, &prov)
	if prov.SAML != nil {
		t.Errorf("providers: %+v", prov)
	}
}
