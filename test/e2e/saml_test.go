//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// The fleet's Keycloak (dev/manifests/hub/keycloak.yaml), the SAML IdP the hub dev chart signs in with:
// browsers reach it at http://localhost:18180, realm kerf, users kc-<name>@example.com with password
// ro-dev-kc-<name>-pass, groups kerf-admins → admin, kerf-operators → operator, kerf-viewers → viewer.
const (
	keycloakNamespace = "keycloak"
	keycloakHost      = "localhost:18180"
	keycloakRealm     = "kerf"
)

func keycloakPassword(user string) string {
	name := strings.TrimPrefix(user, "kc-")
	return getenv("E2E_KEYCLOAK_"+strings.ToUpper(name)+"_PASSWORD", "ro-dev-kc-"+name+"-pass")
}

var hiddenInputRE = regexp.MustCompile(`<input[^>]*name="([^"]+)"[^>]*value="([^"]*)"`)

// TestSAMLSignIn signs in through the web proxy with the fleet's Keycloak (a real SAML 2.0 IdP, signed
// assertions) and checks the mapped roles, the denial without a mapped group, the IdP identity in the
// audit log, that a user removed from its group at the IdP is refused at the next sign-in and loses
// its session, and that no SAML response reaches the hub's logs.
func TestSAMLSignIn(t *testing.T) {
	t.Parallel()
	web := webURL()
	start := time.Now()
	cfg := hubRESTConfig(t)
	cs, err := kubernetes.NewForConfig(cfg)
	must(t, err)
	port := keycloakPort(t, cfg, cs)

	// The SP metadata is public, through the proxy.
	resp, err := http.Get(web + "/v1/auth/saml/metadata")
	must(t, err)
	md, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(md, []byte(`entityID="http://localhost:19090/v1/auth/saml/metadata"`)) {
		t.Fatalf("SP metadata: %d %.300s", resp.StatusCode, md)
	}

	var responses []string // the SAMLResponses posted, never to be logged
	signIn := func(user, password, next string) (*http.Client, *url.URL) {
		t.Helper()
		c := keycloakBrowser(t, port)
		final, samlResponse := keycloakSignIn(t, c, web, user, password, next)
		responses = append(responses, samlResponse)
		return c, final
	}

	for _, u := range []struct{ user, role string }{
		{"kc-admin", hubapi.UserRoleAdmin}, {"kc-operator", hubapi.UserRoleOperator}, {"kc-viewer", hubapi.UserRoleViewer},
	} {
		c, final := signIn(u.user, keycloakPassword(u.user), "/clusters")
		if final.Path != "/clusters" {
			t.Fatalf("%s: sign-in ended at %s, want %s/clusters", u.user, final, web)
		}
		me := getMe(t, c, web)
		if me.AuthMethod != hubapi.AuthMethodSession || me.Role != u.role || me.User == nil || me.User.Username != u.user+"@example.com" ||
			me.User.Source != hubapi.UserSourceSAML {
			t.Errorf("%s: me %+v / %+v, want role %s, source saml", u.user, me, me.User, u.role)
		}
	}

	// No mapped group: Keycloak signs the user in, Kerf refuses.
	if _, final := signIn("kc-nobody", keycloakPassword("kc-nobody"), "/"); final.Path != "/login" || final.Query().Get("error") != "saml_denied" {
		t.Errorf("user without a mapped group ended at %s, want /login?error=saml_denied", final)
	}

	// The admin acts: the audit log records the IdP subject (the persistent NameID) and e-mail.
	admin, _ := signIn("kc-admin", keycloakPassword("kc-admin"), "/")
	me := getMe(t, admin, web)
	tok := sessionJSON(t, admin, web, me.CSRFToken, http.MethodPost, "/v1/admin/tokens",
		map[string]any{"role": hubapi.RoleUI, "description": "e2e saml audit", "reason": "e2e saml audit"}, http.StatusCreated)
	var tc hubapi.TokenCreated
	must(t, json.Unmarshal(tok, &tc))
	sessionJSON(t, admin, web, me.CSRFToken, http.MethodDelete, "/v1/admin/tokens/"+tc.Token.ID, map[string]any{"reason": "e2e saml audit"}, http.StatusOK)
	var audit hubapi.ConfigChangeList
	must(t, json.Unmarshal(sessionJSON(t, admin, web, "", http.MethodGet, "/v1/audit/config?limit=20", nil, http.StatusOK), &audit))
	found := false
	for _, ev := range audit.Items {
		if ev.Action == hubapi.ActionTokenCreated && ev.Target.ID == tc.Token.ID {
			found = true
			if ev.Actor.Name != "kc-admin@example.com" || ev.Actor.Email != "kc-admin@example.com" || ev.Actor.Subject == "" {
				t.Errorf("token created by %+v, want the IdP identity (subject and e-mail) of kc-admin", ev.Actor)
			}
		}
	}
	if !found {
		t.Errorf("no TokenCreated event for token %s in %d entries", tc.Token.ID, len(audit.Items))
	}

	// A user of its own (parallel runs never share it): an operator, then removed from the group at the
	// IdP. Its next sign-in is refused and its session ends.
	kc := newKeycloakAdmin(t, port)
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "kc-e2e-" + hex.EncodeToString(suffix)
	password := "ro-dev-" + name + "-pass"
	uid := kc.createUser(t, name, password, "kerf-operators")
	t.Cleanup(func() { kc.deleteUser(t, uid) })
	op, final := signIn(name, password, "/")
	if final.Path != "/" {
		t.Fatalf("%s: sign-in ended at %s", name, final)
	}
	if me := getMe(t, op, web); me.Role != hubapi.UserRoleOperator {
		t.Fatalf("%s: role %q, want operator", name, me.Role)
	}
	kc.removeFromGroup(t, uid, "kerf-operators")
	if _, final := signIn(name, password, "/"); final.Path != "/login" || final.Query().Get("error") != "saml_denied" {
		t.Errorf("%s removed from kerf-operators: sign-in ended at %s, want /login?error=saml_denied", name, final)
	}
	req, err := http.NewRequest(http.MethodGet, web+"/v1/me", nil)
	must(t, err)
	r, err := op.Do(req)
	must(t, err)
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("%s: the earlier session after the removal: %d, want 401", name, r.StatusCode)
	}

	// No SAML response in the hub's logs.
	pods, err := cs.CoreV1().Pods("kerf").List(ctx(t), metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=hub"})
	must(t, err)
	since := metav1.NewTime(start.Add(-time.Minute))
	for _, p := range pods.Items {
		raw, err := cs.CoreV1().Pods("kerf").GetLogs(p.Name, &corev1.PodLogOptions{SinceTime: &since}).Do(ctx(t)).Raw()
		must(t, err)
		for _, sr := range responses {
			if len(sr) > 64 && bytes.Contains(raw, []byte(sr[:64])) {
				t.Errorf("hub pod %s logged a SAML response", p.Name)
			}
		}
		if !bytes.Contains(raw, []byte(`"method":"saml"`)) {
			t.Logf("hub pod %s: no SAML sign-in in its logs (another replica served it)", p.Name)
		}
	}
}

// keycloakPort returns the local port of the fleet's Keycloak: 18180 when the k3d port mapping exists,
// else a port-forward to its pod (a fleet created before the mapping).
func keycloakPort(t *testing.T, cfg *rest.Config, cs *kubernetes.Clientset) int {
	t.Helper()
	if r, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + keycloakHost + "/realms/" + keycloakRealm); err == nil {
		r.Body.Close()
		if r.StatusCode == http.StatusOK {
			return 18180
		}
	}
	pods, err := cs.CoreV1().Pods(keycloakNamespace).List(ctx(t), metav1.ListOptions{LabelSelector: "app=keycloak"})
	must(t, err)
	for _, p := range pods.Items {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				t.Logf("%s unreachable: port-forward to keycloak/%s", keycloakHost, p.Name)
				return portForwardPod(t, cfg, cs, keycloakNamespace, p.Name, 8080)
			}
		}
	}
	t.Fatalf("no ready Keycloak pod in namespace %s: kubectl --context k3d-ro-hub apply -f dev/manifests/hub/keycloak.yaml", keycloakNamespace)
	return 0
}

// portForwardPod forwards a local port to a pod's port and returns it.
func portForwardPod(t *testing.T, cfg *rest.Config, cs *kubernetes.Clientset, namespace, pod string, podPort int) int {
	t.Helper()
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	must(t, err)
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward")
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())
	stop, ready := make(chan struct{}), make(chan struct{})
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", podPort)}, stop, ready, io.Discard, io.Discard)
	must(t, err)
	errc := make(chan error, 1)
	go func() { errc <- fw.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-errc:
		t.Fatalf("port-forward to %s/%s: %v", namespace, pod, err)
	case <-time.After(time.Minute):
		t.Fatalf("port-forward to %s/%s: timeout", namespace, pod)
	}
	t.Cleanup(func() { close(stop) })
	ports, err := fw.GetPorts()
	must(t, err)
	return int(ports[0].Local)
}

// keycloakBrowser is an HTTP client with cookies that reaches Keycloak's browser address (where the
// hub's authentication request points) through the local port.
func keycloakBrowser(t *testing.T, port int) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	must(t, err)
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == keycloakHost {
			addr = fmt.Sprintf("127.0.0.1:%d", port)
		}
		return d.DialContext(ctx, network, addr)
	}
	return &http.Client{Jar: jar, Transport: tr, Timeout: time.Minute}
}

// keycloakSignIn starts the sign-in at the web proxy, fills Keycloak's password form, posts the IdP's
// response to the assertion consumer service as a browser does (a cross-site POST: no cookies), follows
// the redirect with the browser's cookies, and returns where the browser ends up and the response it
// posted.
func keycloakSignIn(t *testing.T, c *http.Client, web, username, password, next string) (*url.URL, string) {
	t.Helper()
	resp, err := c.Get(web + "/v1/auth/saml/login?next=" + url.QueryEscape(next))
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Request.URL.Host != keycloakHost {
		t.Fatalf("sign-in did not reach Keycloak: ended at %s (%d): %.300s", resp.Request.URL, resp.StatusCode, bytes.TrimSpace(body))
	}
	m := formActionRE.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no login form at %s: %.500s", resp.Request.URL, body)
	}
	resp, err = c.PostForm(html.UnescapeString(string(m[1])), url.Values{"username": {username}, "password": {password}, "credentialId": {""}})
	must(t, err)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	m = formActionRE.FindSubmatch(body)
	if m == nil || !bytes.Contains(body, []byte("SAMLResponse")) {
		t.Fatalf("Keycloak did not answer with the SAML POST form (%d at %s): %.500s", resp.StatusCode, resp.Request.URL, body)
	}
	acs := html.UnescapeString(string(m[1]))
	form := url.Values{}
	for _, in := range hiddenInputRE.FindAllSubmatch(body, -1) {
		form.Set(string(in[1]), html.UnescapeString(string(in[2])))
	}
	if !strings.HasPrefix(acs, web+"/") {
		t.Fatalf("Keycloak posts to %s, want the web proxy's ACS", acs)
	}
	noCookies := &http.Client{Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = noCookies.PostForm(acs, form)
	must(t, err)
	resp.Body.Close()
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("ACS answered %d %v", resp.StatusCode, err)
	}
	resp, err = c.Get(loc.String())
	must(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.Request.URL, form.Get("SAMLResponse")
}

// keycloakAdmin calls Keycloak's admin REST API of the kerf realm.
type keycloakAdmin struct {
	c     *http.Client
	base  string
	token string
}

func newKeycloakAdmin(t *testing.T, port int) *keycloakAdmin {
	t.Helper()
	k := &keycloakAdmin{c: &http.Client{Timeout: 30 * time.Second}, base: fmt.Sprintf("http://127.0.0.1:%d", port)}
	resp, err := k.c.PostForm(k.base+"/realms/master/protocol/openid-connect/token", url.Values{"grant_type": {"password"},
		"client_id": {"admin-cli"}, "username": {getenv("E2E_KEYCLOAK_ADMIN", "admin")},
		"password": {getenv("E2E_KEYCLOAK_ADMIN_PASSWORD", "ro-dev-kc-console-pass")}})
	must(t, err)
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&tok))
	if resp.StatusCode != http.StatusOK || tok.AccessToken == "" {
		t.Fatalf("Keycloak admin token: %d", resp.StatusCode)
	}
	k.token = tok.AccessToken
	return k
}

func (k *keycloakAdmin) do(t *testing.T, method, path string, body any, want int) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		must(t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, k.base+"/admin/realms/"+keycloakRealm+path, rd)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+k.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := k.c.Do(req)
	must(t, err)
	if resp.StatusCode != want {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("Keycloak %s %s: %d, want %d: %.300s", method, path, resp.StatusCode, want, b)
	}
	return resp
}

// createUser creates a user in group and returns its id.
func (k *keycloakAdmin) createUser(t *testing.T, name, password, group string) string {
	t.Helper()
	resp := k.do(t, http.MethodPost, "/users", map[string]any{"username": name, "email": name + "@example.com", "emailVerified": true,
		"firstName": "E2e", "lastName": "User", "enabled": true, "groups": []string{"/" + group},
		"credentials": []map[string]any{{"type": "password", "value": password, "temporary": false}}}, http.StatusCreated)
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	return loc[strings.LastIndex(loc, "/")+1:]
}

func (k *keycloakAdmin) removeFromGroup(t *testing.T, uid, group string) {
	t.Helper()
	resp := k.do(t, http.MethodGet, "/groups?exact=true&search="+url.QueryEscape(group), nil, http.StatusOK)
	var groups []struct{ ID, Name string }
	must(t, json.NewDecoder(resp.Body).Decode(&groups))
	resp.Body.Close()
	for _, g := range groups {
		if g.Name == group {
			k.do(t, http.MethodDelete, "/users/"+uid+"/groups/"+g.ID, nil, http.StatusNoContent).Body.Close()
			return
		}
	}
	t.Fatalf("Keycloak group %s not found", group)
}

func (k *keycloakAdmin) deleteUser(t *testing.T, uid string) {
	k.do(t, http.MethodDelete, "/users/"+uid, nil, http.StatusNoContent).Body.Close()
}
