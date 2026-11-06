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

	"golang.org/x/crypto/bcrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Dex for the OIDC test, pinned by digest. v2.45.1: groups on static users, re-read at refresh.
// Not v2.46.x: there a refresh deadlocks Dex's memory and SQLite storage (the identity is re-read
// inside the refresh token's storage update).
const dexImage = "ghcr.io/dexidp/dex:v2.45.1@sha256:8499afd690c437f52301efd2b05b2455da5bd2dfc20332cd697dc9937f808462"

// The hub dev chart (deploy/charts/hub/values-dev.yaml, auth.oidc) points at this Dex: fixed
// namespace and Service, client kerf with the dev client secret, groups kerf-admins → admin,
// kerf-operators → operator, kerf-viewers → viewer.
const (
	dexNamespace = "e2e-oidc"
	dexHost      = "dex.e2e-oidc.svc.cluster.local:5556"
	dexPassword  = "e2e-dex-password"
)

var jwtRE = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.`)

// TestOIDCSignIn signs in through the web proxy with Dex (a real OpenID Connect provider) and checks
// the mapped roles, the denial without a mapped group, the IdP identity in the audit log, a role
// refresh against Dex, and that no ID or access token reaches the hub's logs.
func TestOIDCSignIn(t *testing.T) {
	t.Parallel()
	web := webURL()
	start := time.Now()
	cfg := hubRESTConfig(t)
	cs, err := kubernetes.NewForConfig(cfg)
	must(t, err)
	hub, err := client.New(cfg, client.Options{Scheme: newScheme()})
	must(t, err)
	pod := deployDex(t, hub, cs, web)
	port := portForward(t, cfg, cs, pod)

	type user struct{ email, group, role string }
	users := []user{
		{"e2e-oidc-admin@example.com", "kerf-admins", hubapi.UserRoleAdmin},
		{"e2e-oidc-viewer@example.com", "kerf-viewers", hubapi.UserRoleViewer},
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
	if _, final := signIn("e2e-oidc-nogroup@example.com", "/"); final.Path != "/login" || final.Query().Get("error") != "oidc_denied" {
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

// deployDex (re)creates namespace e2e-oidc with Dex and returns its ready pod.
func deployDex(t *testing.T, c client.Client, cs *kubernetes.Clientset, web string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: dexNamespace, Labels: map[string]string{suiteLabel: "true"}}}
	// A namespace left by an earlier run may still be terminating.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		err := c.Create(ctx(t), ns)
		if err == nil {
			break
		}
		if !apierrors.IsAlreadyExists(err) || time.Now().After(deadline) {
			t.Fatalf("namespace %s: %v", dexNamespace, err)
		}
		_ = c.Delete(ctx(t), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: dexNamespace}})
		time.Sleep(3 * time.Second)
	}
	t.Cleanup(func() {
		if env.Keep {
			return
		}
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = c.Delete(cctx, ns)
	})

	hash, err := bcrypt.GenerateFromPassword([]byte(dexPassword), 10)
	must(t, err)
	type static struct {
		Email    string   `json:"email"`
		Hash     string   `json:"hash"`
		Username string   `json:"username"`
		UserID   string   `json:"userID"`
		Groups   []string `json:"groups"`
	}
	var users []static
	for i, u := range []struct{ name, group string }{{"admin", "kerf-admins"}, {"viewer", "kerf-viewers"}, {"nogroup", "sales"}} {
		users = append(users, static{Email: "e2e-oidc-" + u.name + "@example.com", Hash: string(hash), Username: "e2e-oidc-" + u.name,
			UserID: fmt.Sprintf("0e2e0000-0000-4000-8000-00000000000%d", i+1), Groups: []string{u.group}})
	}
	dexCfg, err := json.Marshal(map[string]any{
		"issuer":           "http://" + dexHost + "/dex",
		"storage":          map[string]any{"type": "memory"},
		"web":              map[string]any{"http": "0.0.0.0:5556"},
		"oauth2":           map[string]any{"skipApprovalScreen": true},
		"enablePasswordDB": true,
		"staticPasswords":  users,
		"staticClients": []map[string]any{{"id": "kerf", "name": "Kerf",
			"secret":       getenv("E2E_OIDC_CLIENT_SECRET", "ro-dev-oidc-3f9a6c1e8b2d7054e1a9c6b3"),
			"redirectURIs": []string{web + "/v1/auth/oidc/callback"}}},
	})
	must(t, err)
	must(t, c.Create(ctx(t), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "dex", Namespace: dexNamespace},
		Data: map[string]string{"config.yaml": string(dexCfg)}}))
	labels := map[string]string{"app": "dex"}
	must(t, c.Create(ctx(t), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dex", Namespace: dexNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "dex", Image: dexImage, Args: []string{"dex", "serve", "/etc/dex/config.yaml"},
						Ports:          []corev1.ContainerPort{{Name: "http", ContainerPort: 5556}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/dex/healthz", Port: intstr.FromInt32(5556)}}, PeriodSeconds: 2},
						VolumeMounts:   []corev1.VolumeMount{{Name: "config", MountPath: "/etc/dex"}},
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), RunAsNonRoot: ptr(true),
							Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					}},
					Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "dex"}}}}},
				},
			},
		},
	}))
	must(t, c.Create(ctx(t), &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "dex", Namespace: dexNamespace},
		Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Name: "http", Port: 5556, TargetPort: intstr.FromInt32(5556)}}}}))

	deadline = time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		pods, err := cs.CoreV1().Pods(dexNamespace).List(ctx(t), metav1.ListOptions{LabelSelector: "app=dex"})
		must(t, err)
		for _, p := range pods.Items {
			for _, cond := range p.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					t.Logf("dex ready: %s", p.Name)
					return p.Name
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("dex did not become ready")
	return ""
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
	resp, err = c.PostForm(action.String(), url.Values{"login": {email}, "password": {dexPassword}})
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
