//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// The fleet's Dex (dev/manifests/hub/dex.yaml), which the hub dev chart signs in with: issuer
// http://localhost:15556/dex, users dex-<name>@example.com with password ro-dev-dex-<name>-pass, groups
// kerf-admins → admin, kerf-operators → operator, kerf-viewers → viewer.
const (
	dexNamespace = "dex"
	dexHost      = "localhost:15556"
)

func dexPassword(email string) string {
	name := strings.TrimSuffix(strings.TrimPrefix(email, "dex-"), "@example.com")
	return getenv("E2E_DEX_"+strings.ToUpper(name)+"_PASSWORD", "ro-dev-dex-"+name+"-pass")
}

var jwtRE = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.`)

// TestOIDCSignIn signs in through the web proxy with the fleet's Dex (a real OpenID Connect provider)
// and checks the mapped roles, the denial without a mapped group, the IdP identity in the audit log, a
// role refresh against Dex, and that no ID or access token reaches the hub's logs.
func TestOIDCSignIn(t *testing.T) {
	t.Parallel()
	web := webURL()
	start := time.Now()
	cfg := hubRESTConfig(t)
	cs, err := kubernetes.NewForConfig(cfg)
	must(t, err)
	port := dexPort(t, cfg, cs)

	type user struct{ email, group, role string }
	users := []user{
		{"dex-admin@example.com", "kerf-admins", hubapi.UserRoleAdmin},
		{"dex-operator@example.com", "kerf-operators", hubapi.UserRoleOperator},
		{"dex-viewer@example.com", "kerf-viewers", hubapi.UserRoleViewer},
	}
	signIn := func(email, next string) (*http.Client, *url.URL) {
		t.Helper()
		c := dexBrowser(t, port)
		final := dexSignIn(t, c, web, email, next)
		return c, final
	}

	for _, u := range users {
		c, final := signIn(u.email, "/clusters")
		if final.Path != "/clusters" {
			t.Fatalf("%s: sign-in ended at %s, want %s/clusters", u.email, final, web)
		}
		me := getMe(t, c, web)
		if me.AuthMethod != hubapi.AuthMethodSession || me.Role != u.role || me.User == nil || me.User.Username != u.email ||
			me.User.Source != hubapi.UserSourceOIDC {
			t.Errorf("%s (group %s): me %+v / %+v, want role %s, source oidc", u.email, u.group, me, me.User, u.role)
		}
	}

	// No mapped group: Dex signs the user in, Kerf refuses.
	if _, final := signIn("dex-nobody@example.com", "/"); final.Path != "/login" || final.Query().Get("error") != "oidc_denied" {
		t.Errorf("user without a mapped group ended at %s, want /login?error=oidc_denied", final)
	}

	// The admin acts: the audit log records the IdP subject and e-mail.
	admin, _ := signIn(users[0].email, "/")
	me := getMe(t, admin, web)
	tok := sessionJSON(t, admin, web, me.CSRFToken, http.MethodPost, "/v1/admin/tokens",
		map[string]any{"role": hubapi.RoleUI, "description": "e2e oidc audit", "reason": "e2e oidc audit"}, http.StatusCreated)
	var tc hubapi.TokenCreated
	must(t, json.Unmarshal(tok, &tc))
	created := tc.Token
	sessionJSON(t, admin, web, me.CSRFToken, http.MethodDelete, "/v1/admin/tokens/"+created.ID, map[string]any{"reason": "e2e oidc audit"}, http.StatusOK)
	var audit hubapi.ConfigChangeList
	must(t, json.Unmarshal(sessionJSON(t, admin, web, "", http.MethodGet, "/v1/audit/config?limit=20", nil, http.StatusOK), &audit))
	found := false
	for _, ev := range audit.Items {
		if ev.Action == hubapi.ActionTokenCreated && ev.Target.ID == created.ID {
			found = true
			if ev.Actor.Name != users[0].email || ev.Actor.Email != users[0].email || ev.Actor.Subject == "" {
				t.Errorf("token created by %+v, want the IdP identity (subject and e-mail) of %s", ev.Actor, users[0].email)
			}
		}
	}
	if !found {
		t.Errorf("no TokenCreated event for token %s in %d entries", created.ID, len(audit.Items))
	}

	// The dev chart re-reads the groups every minute: the session refreshes against Dex and keeps
	// its role.
	time.Sleep(70 * time.Second)
	if me := getMe(t, admin, web); me.Role != hubapi.UserRoleAdmin {
		t.Errorf("after a refresh against Dex: role %q, want admin", me.Role)
	}

	// No ID or access token (JWTs) in the hub's logs.
	pods, err := cs.CoreV1().Pods("kerf").List(ctx(t), metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=hub"})
	must(t, err)
	since := metav1.NewTime(start.Add(-time.Minute))
	for _, p := range pods.Items {
		raw, err := cs.CoreV1().Pods("kerf").GetLogs(p.Name, &corev1.PodLogOptions{SinceTime: &since}).Do(ctx(t)).Raw()
		must(t, err)
		if m := jwtRE.Find(raw); m != nil {
			t.Errorf("hub pod %s logged a JWT: %.20s…", p.Name, m)
		}
		if !bytes.Contains(raw, []byte(`"method":"oidc"`)) {
			t.Logf("hub pod %s: no OIDC sign-in in its logs (another replica served it)", p.Name)
		}
	}
}

func hubRESTConfig(t *testing.T) *rest.Config {
	t.Helper()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: getenv("E2E_HUB_CONTEXT", "k3d-ro-hub")}).ClientConfig()
	must(t, err)
	return cfg
}

// dexPort returns the local port of the fleet's Dex: 15556 when the k3d port mapping exists, else a
// port-forward to its pod (a fleet created before the mapping).
func dexPort(t *testing.T, cfg *rest.Config, cs *kubernetes.Clientset) int {
	t.Helper()
	if r, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + dexHost + "/dex/healthz"); err == nil {
		r.Body.Close()
		if r.StatusCode == http.StatusOK {
			return 15556
		}
	}
	pods, err := cs.CoreV1().Pods(dexNamespace).List(ctx(t), metav1.ListOptions{LabelSelector: "app=dex"})
	must(t, err)
	for _, p := range pods.Items {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				t.Logf("%s unreachable: port-forward to dex/%s", dexHost, p.Name)
				return portForward(t, cfg, cs, p.Name)
			}
		}
	}
	t.Fatalf("no ready Dex pod in namespace %s: kubectl --context k3d-ro-hub apply -f dev/manifests/hub/dex.yaml", dexNamespace)
	return 0
}

// portForward forwards a local port to Dex's pod and returns it.
func portForward(t *testing.T, cfg *rest.Config, cs *kubernetes.Clientset, pod string) int {
	t.Helper()
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	must(t, err)
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(dexNamespace).Name(pod).SubResource("portforward")
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())
	stop, ready := make(chan struct{}), make(chan struct{})
	fw, err := portforward.New(dialer, []string{"0:5556"}, stop, ready, io.Discard, io.Discard)
	must(t, err)
	errc := make(chan error, 1)
	go func() { errc <- fw.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-errc:
		t.Fatalf("port-forward to dex: %v", err)
	case <-time.After(time.Minute):
		t.Fatal("port-forward to dex: timeout")
	}
	t.Cleanup(func() { close(stop) })
	ports, err := fw.GetPorts()
	must(t, err)
	return int(ports[0].Local)
}

// dexBrowser is an HTTP client with cookies that reaches Dex's in-cluster address (the issuer the
// hub redirects to) through the port-forward.
func dexBrowser(t *testing.T, port int) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	must(t, err)
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == dexHost {
			addr = fmt.Sprintf("127.0.0.1:%d", port)
		}
		return d.DialContext(ctx, network, addr)
	}
	return &http.Client{Jar: jar, Transport: tr, Timeout: time.Minute}
}

var formActionRE = regexp.MustCompile(`<form[^>]*action="([^"]+)"`)

// dexSignIn starts the sign-in at the web proxy, fills Dex's password form and returns where the
// browser ends up.
func dexSignIn(t *testing.T, c *http.Client, web, email, next string) *url.URL {
	t.Helper()
	resp, err := c.Get(web + "/v1/auth/oidc/login?next=" + url.QueryEscape(next))
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Request.URL.Host != dexHost {
		t.Fatalf("sign-in did not reach Dex: ended at %s (%d): %s", resp.Request.URL, resp.StatusCode, bytes.TrimSpace(body))
	}
	m := formActionRE.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no login form at %s: %s", resp.Request.URL, body)
	}
	action, err := resp.Request.URL.Parse(strings.ReplaceAll(string(m[1]), "&amp;", "&"))
	must(t, err)
	resp, err = c.PostForm(action.String(), url.Values{"login": {email}, "password": {dexPassword(email)}})
	must(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.Request.URL
}

func getMe(t *testing.T, c *http.Client, web string) hubapi.Me {
	t.Helper()
	b := sessionJSON(t, c, web, "", http.MethodGet, "/v1/me", nil, http.StatusOK)
	var me hubapi.Me
	must(t, json.Unmarshal(b, &me))
	return me
}

// sessionJSON calls the API through the web proxy with the client's session cookie.
func sessionJSON(t *testing.T, c *http.Client, web, csrf, method, path string, body any, want int) []byte {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		must(t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, web+path, rd)
	must(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set(hubapi.CSRFHeader, csrf)
	}
	resp, err := c.Do(req)
	must(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, resp.StatusCode, want, bytes.TrimSpace(b))
	}
	return b
}
