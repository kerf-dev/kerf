//go:build e2e

// Package e2e is the end-to-end suite (WO-09). It runs against the shared k3d environment
// (docs/handover/WO-01-handover.md) with the deployed hub (WO-04) and agents (WO-06):
//
//	make e2e
//
// Every scenario creates its own namespace (e2e-<scenario>-<rand>) and its own cluster-scoped
// RightsizingPolicy (same name) selecting only that namespace, and deletes both at the end.
// Nothing in other namespaces is touched. The hub is driven through real usage: test pods burn a
// known amount of CPU/memory, the spoke vmagent ships it to VictoriaMetrics, the hub's periodic
// run (5 min in the dev chart) recommends, the agent (30 s pull) applies. Policies relax the
// learning period and confidence guardrails so a few minutes of data are enough.
package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kerf-dev/kerf/api/v1alpha1"
)

// cluster is one spoke as seen by the suite.
type cluster struct {
	Name    string // cluster name as known to the hub (and the `cluster` label)
	Context string // kubeconfig context
	C       client.Client
}

type suiteEnv struct {
	Spoke1, Spoke2 cluster
	Hub            *hubAPI
	GitURL         string // push URL from the host
	GitInCluster   string // repo URL Argo CD uses
	ArgoNamespace  string
	ApplyTimeout   time.Duration
	Keep           bool
}

var env suiteEnv

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		os.Exit(1)
	}
	if err := markRun(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		os.Exit(1)
	}
	if os.Getenv("E2E_KEEP") == "" {
		if err := sweepHub(func(f string, a ...any) { fmt.Printf(f+"\n", a...) }); err != nil {
			fmt.Fprintln(os.Stderr, "e2e setup: hub sweep:", err)
			unmarkRun()
			os.Exit(1)
		}
	}
	code := m.Run()
	logoutAll()
	unmarkRun()
	os.Exit(code)
}

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}

func newCluster(name, ctx string) (cluster, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: ctx}).ClientConfig()
	if err != nil {
		return cluster{}, fmt.Errorf("kubeconfig context %s: %w", ctx, err)
	}
	cfg.QPS, cfg.Burst = 50, 100
	c, err := client.New(cfg, client.Options{Scheme: newScheme()})
	if err != nil {
		return cluster{}, err
	}
	return cluster{Name: name, Context: ctx, C: client.WithFieldOwner(c, suiteFieldManager)}, nil
}

func setup() error {
	var err error
	if env.Spoke1, err = newCluster(getenv("E2E_SPOKE1", "ro-spoke-1"), getenv("E2E_SPOKE1_CONTEXT", "k3d-ro-spoke-1")); err != nil {
		return err
	}
	if env.Spoke2, err = newCluster(getenv("E2E_SPOKE2", "ro-spoke-2"), getenv("E2E_SPOKE2_CONTEXT", "k3d-ro-spoke-2")); err != nil {
		return err
	}
	hubCluster, err := newCluster("ro-hub", getenv("E2E_HUB_CONTEXT", "k3d-ro-hub"))
	if err != nil {
		return err
	}
	token, admin := os.Getenv("E2E_HUB_UI_TOKEN"), os.Getenv("E2E_HUB_ADMIN_TOKEN")
	if token == "" || admin == "" {
		// The dev hub chart seeds tokens into this Secret (deploy/charts/hub/README.md).
		var s corev1.Secret
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := hubCluster.C.Get(ctx, client.ObjectKey{Namespace: "kerf", Name: "hub-dev-tokens"}, &s); err != nil {
			if token == "" {
				return fmt.Errorf("hub UI token: set E2E_HUB_UI_TOKEN or deploy the hub with devSeed: %w", err)
			}
		}
		if token == "" {
			token = strings.TrimSpace(string(s.Data["ui"]))
		}
		if admin == "" {
			admin = strings.TrimSpace(string(s.Data["admin"]))
		}
	}
	env.Hub = &hubAPI{Base: strings.TrimRight(getenv("E2E_HUB_URL", "http://localhost:18080"), "/"), Token: token, AdminToken: admin}
	env.Hub.RecomputeEvery, err = time.ParseDuration(getenv("E2E_RECOMPUTE_EVERY", defaultRecomputeEvery.String()))
	if err != nil {
		return fmt.Errorf("E2E_RECOMPUTE_EVERY: %w", err)
	}
	if os.Getenv("E2E_HUB_RECOMPUTE") == "off" {
		env.Hub.AdminToken = ""
	}
	env.GitURL = getenv("E2E_GIT_URL", "git://localhost:19418/workloads.git")
	env.GitInCluster = getenv("E2E_GIT_IN_CLUSTER_URL", "git://git-server.git-server.svc.cluster.local/workloads.git")
	env.ArgoNamespace = getenv("E2E_ARGOCD_NAMESPACE", "argocd")
	env.ApplyTimeout, err = time.ParseDuration(getenv("E2E_APPLY_TIMEOUT", "18m"))
	if err != nil {
		return fmt.Errorf("E2E_APPLY_TIMEOUT: %w", err)
	}
	env.Keep = os.Getenv("E2E_KEEP") != ""
	return nil
}

func randSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// suiteLabel marks every object the suite creates.
const suiteLabel = "e2e.kerf.getkerf.dev/suite"

// newNamespace creates e2e-<scenario>-<rand> and deletes it (and the policy of the same name)
// when the test ends.
func newNamespace(t *testing.T, cl cluster, scenario string) string {
	t.Helper()
	name := fmt.Sprintf("e2e-%s-%s", scenario, randSuffix())
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{suiteLabel: "true"}}}
	must(t, cl.C.Create(ctx(t), ns))
	t.Logf("namespace %s on %s", name, cl.Name)
	t.Cleanup(func() {
		if env.Keep {
			t.Logf("E2E_KEEP set: leaving namespace %s on %s", name, cl.Name)
			return
		}
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = cl.C.Delete(c, &v1alpha1.RightsizingPolicy{ObjectMeta: metav1.ObjectMeta{Name: name}})
		_ = cl.C.Delete(c, ns)
	})
	return name
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return c
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
