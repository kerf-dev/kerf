package controller

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubstub"
)

const testCluster = "envtest"

var (
	k8s     client.Client
	stub    *hubstub.Server
	recsMu  sync.Mutex
	recs    = map[string]hubclient.WorkloadRecommendation{}
	testCtx context.Context
	// testReconciler is the reconciler under test.
	testReconciler *Reconciler
	skipEnv        string
)

// setRec adds or replaces the stub hub recommendation of a workload.
func setRec(r hubclient.WorkloadRecommendation) {
	recsMu.Lock()
	defer recsMu.Unlock()
	recs[r.Workload.Kind+"/"+r.Workload.Name] = r
	var all []hubclient.WorkloadRecommendation
	for _, v := range recs {
		all = append(all, v)
	}
	stub.SetRecommendations(testCluster, hubstub.Normalize(all, time.Now()))
}

// assetsDir finds envtest binaries: $KUBEBUILDER_ASSETS or ./bin/envtest/k8s/<version>-<os>-<arch>.
func assetsDir() string {
	if d := os.Getenv("KUBEBUILDER_ASSETS"); d != "" {
		return d
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	matches, _ := filepath.Glob(filepath.Join(root, "bin", "envtest", "k8s", "1.35*-"+runtime.GOOS+"-"+runtime.GOARCH))
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

func runSuite(m *testing.M) int {
	assets := assetsDir()
	if assets == "" {
		skipEnv = "envtest binaries not found: run `make agent-envtest` or set KUBEBUILDER_ASSETS"
		return m.Run()
	}
	_, file, _, _ := runtime.Caller(0)
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(filepath.Dir(file), "..", "..", "..", "api", "crds")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: assets,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest:", err)
		return 1
	}
	defer func() { _ = env.Stop() }()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if os.Getenv("AGENT_TEST_DEBUG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))

	scheme := k8sruntime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "manager:", err)
		return 1
	}
	stub = hubstub.New()
	stub.AddToken("t", testCluster)
	srv := httptest.NewServer(stub.Handler())
	defer srv.Close()
	hub, _ := hubclient.New(srv.URL, testCluster, hubclient.StaticToken("t"), nil)

	store := NewStore(2 * time.Second)
	rep := NewReporter(hub, "test", 100, log)
	r := &Reconciler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Store: store, Reporter: rep,
		Recorder: mgr.GetEventRecorder("resource-optimizer-agent"), Log: log,
		Cfg: Config{ClusterName: testCluster, ApplyingRequeue: 200 * time.Millisecond, OnDeleteSettle: 300 * time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	testCtx = ctx
	testReconciler = r
	trigger, err := r.SetupWithManager(ctx, mgr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return 1
	}
	if err := mgr.Add(&Puller{Hub: hub, Store: store, Reporter: rep, PullInterval: 300 * time.Millisecond,
		ReportInterval: 200 * time.Millisecond, Trigger: trigger, Log: log}); err != nil {
		fmt.Fprintln(os.Stderr, "puller:", err)
		return 1
	}
	go func() {
		if err := mgr.Start(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "manager stopped:", err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		fmt.Fprintln(os.Stderr, "cache sync failed")
		return 1
	}
	k8s, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		return 1
	}
	return m.Run()
}

func needEnv(t *testing.T) {
	t.Helper()
	if skipEnv != "" {
		t.Skip(skipEnv)
	}
}

// eventually retries f until it returns nil or the timeout expires.
func eventually(t *testing.T, timeout time.Duration, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: %v", what, err)
}
