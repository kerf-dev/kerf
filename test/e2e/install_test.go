//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// TestFreshInstall installs the umbrella chart from this tree with global.mode all and no token or admin
// password of its own, in a namespace of the hub cluster, as a first-time user would: the chart's bundled
// PostgreSQL and VictoriaMetrics (quickstart.enabled, without the demo), the images the fleet runs. Then:
//   - the chart created the agent and pricing tokens (Secret <release>-hub-local-tokens);
//   - the agent is Ready and its heartbeats reach the new hub, with no manual step;
//   - pricing is Ready (it has its token);
//   - the generated bootstrap admin must change its password at first sign-in.
//
// The agent is read-only and the webhook off, so nothing on the cluster is changed. Needs helm.
// E2E_INSTALL_CLUSTER_CONTEXT picks another cluster (default: the hub cluster).
func TestFreshInstall(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found")
	}
	kctx := getenv("E2E_INSTALL_CLUSTER_CONTEXT", getenv("E2E_HUB_CONTEXT", "k3d-ro-hub"))
	cl, err := newCluster("install", kctx)
	must(t, err)
	ns := newNamespace(t, cl, "install")
	rel := ns // release name = namespace: every cluster-scoped name the chart makes is unique
	clusterName := strings.TrimPrefix(ns, "e2e-")

	args := []string{"--kube-context", kctx, "upgrade", "--install", rel, "../../deploy/charts/kerf", "-n", ns,
		"--wait", "--timeout", "8m",
		"--set", "dev=true", // the fleet's images come from its local registry (no global.imageRegistry)
		"--set", "global.mode=all", "--set-string", "global.clusterName=" + clusterName,
		"--set", "quickstart.enabled=true", "--set", "quickstart.demo.enabled=false",
		"--set-string", "hub.database.existingSecret=kerf-quickstart-db",
		"--set-string", "hub.config.prometheusURL=http://kerf-vm:8428",
		"--set", "hub.replicaCount=1", "--set", "hub.podDisruptionBudget.enabled=false", "--set", "hub.auth.cookieSecure=false",
		"--set", "web.replicaCount=1",
		"--set", "agent.replicaCount=1", "--set", "agent.leaderElection=false", "--set", "agent.readOnly=true",
		"--set", "agent.pullInterval=15s",
		"--set", "pricing.interval=30s",
		"--set", "webhook.enabled=false",
	}
	// The CRDs are cluster-scoped and kept on uninstall: install them only where they are missing.
	var crd apiextensionsv1.CustomResourceDefinition
	crdClient, err := client.New(restConfig(t, kctx), client.Options{Scheme: crdScheme()})
	must(t, err)
	if err := crdClient.Get(ctx(t), client.ObjectKey{Name: "rightsizingpolicies.kerf.getkerf.dev"}, &crd); err == nil {
		args = append(args, "--set", "agent.crds.install=false")
	} else if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	for _, c := range []struct{ component, ctxName, name string }{
		{"hub", getenv("E2E_HUB_CONTEXT", "k3d-ro-hub"), "hub"},
		{"web", getenv("E2E_HUB_CONTEXT", "k3d-ro-hub"), "web"},
		{"pricing", getenv("E2E_HUB_CONTEXT", "k3d-ro-hub"), "pricing"},
		{"agent", env.Spoke1.Context, "kerf-agent"},
	} {
		repo, tag := fleetImage(t, c.ctxName, c.name)
		args = append(args, "--set-string", c.component+".image.repository="+repo, "--set-string", c.component+".image.tag="+tag)
	}

	t.Cleanup(func() {
		if env.Keep {
			t.Logf("E2E_KEEP set: leaving release %s", rel)
			return
		}
		out, err := exec.Command(helm, "--kube-context", kctx, "uninstall", rel, "-n", ns, "--wait", "--timeout", "3m").CombinedOutput()
		if err != nil {
			t.Logf("helm uninstall: %v: %s", err, out)
		}
		// The kept Secrets (generated tokens and admin password) go with the namespace.
	})
	t.Logf("helm %s", strings.Join(args, " "))
	if out, err := exec.Command(helm, args...).CombinedOutput(); err != nil {
		diagnoseInstall(t, cl, ns)
		t.Fatalf("helm install: %v\n%s", err, out)
	}
	c := ctx(t)

	// 1. The chart created both tokens.
	var tokens corev1.Secret
	must(t, cl.C.Get(c, client.ObjectKey{Namespace: ns, Name: rel + "-hub-local-tokens"}, &tokens))
	for _, k := range []string{"agent", "pricing"} {
		if len(bytes.TrimSpace(tokens.Data[k])) < 20 {
			t.Errorf("Secret %s-hub-local-tokens: key %s missing or short", rel, k)
		}
	}

	// 2. The agent and pricing are Ready (their readiness needs the token).
	for _, d := range []string{rel + "-agent", rel + "-pricing"} {
		eventually(t, 3*time.Minute, 5*time.Second, "Deployment "+d+" ready", func() (bool, string) {
			var dep appsv1.Deployment
			if err := cl.C.Get(c, client.ObjectKey{Namespace: ns, Name: d}, &dep); err != nil {
				return false, err.Error()
			}
			return dep.Status.ReadyReplicas >= 1, fmt.Sprintf("ready %d", dep.Status.ReadyReplicas)
		})
	}

	// 3. The generated admin must change the password; the agent's heartbeats reach the hub.
	var pw corev1.Secret
	must(t, cl.C.Get(c, client.ObjectKey{Namespace: ns, Name: rel + "-hub-bootstrap-admin"}, &pw))
	hub := newServiceProxy(t, kctx, ns, rel+"-hub", 8080)
	var me hubapi.Me
	cookie := hub.login(t, "admin", strings.TrimSpace(string(pw.Data["password"])), &me)
	if me.User == nil || !me.User.MustChangePassword {
		t.Errorf("generated bootstrap admin: mustChangePassword = %+v, want true", me.User)
	}
	eventually(t, 3*time.Minute, 5*time.Second, "agent of "+clusterName+" connected", func() (bool, string) {
		var list hubapi.ClusterList
		if err := hub.get(cookie, "/v1/clusters", &list); err != nil {
			return false, err.Error()
		}
		for _, cl := range list.Items {
			if cl.Name == clusterName {
				return cl.LastHeartbeatAt != nil && cl.AgentStatus == "connected", cl.AgentStatus
			}
		}
		return false, fmt.Sprintf("cluster %s not registered: %+v", clusterName, list.Items)
	})
}

func restConfig(t *testing.T, kctx string) *rest.Config {
	t.Helper()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kctx}).ClientConfig()
	must(t, err)
	return cfg
}

func crdScheme() *runtime.Scheme {
	s := newScheme()
	_ = apiextensionsv1.AddToScheme(s)
	return s
}

// fleetImage: repository and tag of the first container of the fleet Deployment labelled
// app.kubernetes.io/name=name in namespace kerf.
func fleetImage(t *testing.T, kctx, name string) (repo, tag string) {
	t.Helper()
	cl, err := newCluster(name, kctx)
	must(t, err)
	var list appsv1.DeploymentList
	must(t, cl.C.List(ctx(t), &list, client.InNamespace("kerf"), client.MatchingLabels{"app.kubernetes.io/name": name}))
	if len(list.Items) == 0 {
		t.Fatalf("no Deployment app.kubernetes.io/name=%s in namespace kerf of %s", name, kctx)
	}
	img := list.Items[0].Spec.Template.Spec.Containers[0].Image
	img, _, _ = strings.Cut(img, "@")
	i := strings.LastIndex(img, ":")
	if i < 0 || strings.Contains(img[i:], "/") {
		return img, "latest"
	}
	return img[:i], img[i+1:]
}

func diagnoseInstall(t *testing.T, cl cluster, ns string) {
	t.Helper()
	var pods corev1.PodList
	if err := cl.C.List(context.Background(), &pods, client.InNamespace(ns)); err != nil {
		return
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			t.Logf("pod %s/%s: ready %v, restarts %d, state %+v", p.Name, cs.Name, cs.Ready, cs.RestartCount, cs.State)
		}
	}
}

// serviceProxy reaches a Service through the API server's proxy (no port-forward).
type serviceProxy struct {
	base string
	hc   *http.Client
}

func newServiceProxy(t *testing.T, kctx, ns, svc string, port int) *serviceProxy {
	t.Helper()
	cfg := restConfig(t, kctx)
	hc, err := rest.HTTPClientFor(cfg)
	must(t, err)
	hc.Timeout = 30 * time.Second
	return &serviceProxy{base: fmt.Sprintf("%s/api/v1/namespaces/%s/services/http:%s:%d/proxy", strings.TrimRight(cfg.Host, "/"), ns, svc, port), hc: hc}
}

// login signs in and returns the session cookie header.
func (p *serviceProxy) login(t *testing.T, user, pw string, me *hubapi.Me) string {
	t.Helper()
	body, _ := json.Marshal(hubapi.LoginRequest{Username: user, Password: pw})
	var cookie string
	eventually(t, 2*time.Minute, 5*time.Second, "sign in as "+user, func() (bool, string) {
		res, err := p.hc.Post(p.base+"/v1/auth/login", "application/json", bytes.NewReader(body))
		if err != nil {
			return false, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			return false, fmt.Sprintf("HTTP %d: %s", res.StatusCode, b)
		}
		for _, c := range res.Cookies() {
			if c.Name == hubapi.SessionCookie {
				cookie = c.Name + "=" + c.Value
			}
		}
		return cookie != "" && json.Unmarshal(b, me) == nil, "no session cookie"
	})
	return cookie
}

func (p *serviceProxy) get(cookie, path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, p.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", cookie)
	res, err := p.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d: %s", path, res.StatusCode, b)
	}
	return json.Unmarshal(b, out)
}
