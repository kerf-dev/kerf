// Command agent is the per-cluster resource-optimizer agent (WO-06). It pulls recommendations
// from the hub, resolves the effective policy of every workload, enforces the guardrails and
// applies changes in place (pods/resize) or by rollout (server-side apply on the pod template).
//
//	agent [flags]            run the agent
//	agent stub-hub [flags]   serve a stub of the hub's agent endpoints (local development)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent"
	"github.com/tomer1983/resource-optimizer/internal/agent/controller"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubstub"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "stub-hub" {
		if err := runStubHub(os.Args[2:]); err != nil {
			slog.Error("stub-hub", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		slog.Error("agent", "err", err)
		os.Exit(1)
	}
}

type labelSelectors []map[string]string

func (l *labelSelectors) String() string { return fmt.Sprint(*l) }
func (l *labelSelectors) Set(v string) error {
	sel := map[string]string{}
	for _, part := range strings.Split(v, ",") {
		k, val, _ := strings.Cut(strings.TrimSpace(part), "=")
		if k == "" {
			return fmt.Errorf("bad selector %q", v)
		}
		sel[k] = val
	}
	*l = append(*l, sel)
	return nil
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	_ = lv.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

func run(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	var (
		cluster        = fs.String("cluster-name", os.Getenv("CLUSTER_NAME"), "cluster name, as known to the hub (required)")
		hubURL         = fs.String("hub-url", "http://hub-api.hub.svc:8080", "hub base URL")
		tokenFile      = fs.String("hub-token-file", "/var/run/secrets/resource-optimizer/hub/token", "file with the hub bearer token (re-read on every request)")
		defaultsFile   = fs.String("cluster-defaults", "", "YAML file with the cluster default policy (a RightsizingPolicy spec)")
		pullInterval   = fs.Duration("pull-interval", time.Minute, "how often recommendations are pulled")
		staleAfter     = fs.Duration("hub-stale-after", 5*time.Minute, "after this long without a successful pull the hub counts as unreachable and nothing new is applied")
		reportInterval = fs.Duration("report-interval", 10*time.Second, "how often changed statuses and events are sent")
		eventBuffer    = fs.Int("event-buffer", 10000, "events buffered while the hub is unreachable (oldest dropped first)")
		readOnly       = fs.Bool("read-only", false, "cap every mode at audit; never write workloads or pods")
		tolerance      = fs.Float64("tolerance", 0.05, "relative change below which values are left alone")
		applyingCheck  = fs.Duration("applying-requeue", 5*time.Second, "how often in-progress in-place resizes are checked")
		metricsAddr    = fs.String("metrics-bind-address", ":8080", "metrics endpoint")
		probeAddr      = fs.String("health-probe-bind-address", ":8081", "health probe endpoint")
		leaderElect    = fs.Bool("leader-elect", false, "enable leader election (for more than one replica)")
		logLevel       = fs.String("log-level", "info", "debug, info, warn or error")
		unsupported    labelSelectors

		healthEnabled    = fs.Bool("health-watch", true, "watch workloads after each change and roll back on a breach (WO-08)")
		healthInterval   = fs.Duration("health-check-interval", 15*time.Second, "how often a watched workload is re-evaluated (pod events act immediately)")
		metricsURL       = fs.String("metrics-url", "http://victoria-metrics.hub.svc:8428", "Prometheus-compatible endpoint for the CPU throttling ratio; empty disables it")
		metricsTokenFile = fs.String("metrics-token-file", "", "optional bearer token file for --metrics-url")
		cadvisorFallback = fs.Bool("health-cadvisor-fallback", false, "read throttling from the kubelets' cAdvisor (needs nodes/proxy) when --metrics-url fails")
		minThrottleWin   = fs.Duration("health-min-throttling-window", 2*time.Minute, "time after a change before throttling is evaluated")
	)
	fs.Var(&unsupported, "unsupported-node-selector", "label selector (k=v,k2=v2; empty value = key exists) of nodes where in-place resize is not used; repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := newLogger(*logLevel)
	slog.SetDefault(log)
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))
	if *cluster == "" {
		return errors.New("--cluster-name is required")
	}

	var defaults *v1alpha1.RightsizingPolicySpec
	if *defaultsFile != "" {
		b, err := os.ReadFile(*defaultsFile)
		if err != nil {
			return fmt.Errorf("cluster defaults: %w", err)
		}
		defaults = &v1alpha1.RightsizingPolicySpec{}
		if err := yaml.UnmarshalStrict(b, defaults); err != nil {
			return fmt.Errorf("cluster defaults %s: %w", *defaultsFile, err)
		}
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Cache:                   cache.Options{DefaultTransform: cache.TransformStripManagedFields()},
		Metrics:                 metricsserver.Options{BindAddress: *metricsAddr},
		HealthProbeBindAddress:  *probeAddr,
		LeaderElection:          *leaderElect,
		LeaderElectionID:        "resource-optimizer-agent",
		LeaderElectionNamespace: os.Getenv("POD_NAMESPACE"),
	})
	if err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return err
	}

	hub, err := hubclient.New(*hubURL, *cluster, hubclient.FileToken(*tokenFile), nil)
	if err != nil {
		return err
	}
	store := controller.NewStore(*staleAfter)
	rep := controller.NewReporter(hub, version, *eventBuffer, log.With("component", "reporter"))
	r := &controller.Reconciler{
		Client:   mgr.GetClient(),
		Reader:   mgr.GetAPIReader(),
		Store:    store,
		Reporter: rep,
		Recorder: mgr.GetEventRecorder("resource-optimizer-agent"),
		Log:      log.With("component", "reconciler"),
		Cfg: controller.Config{
			ClusterName:              *cluster,
			ClusterDefaults:          defaults,
			ReadOnly:                 *readOnly,
			Tolerance:                *tolerance,
			UnsupportedNodeSelectors: unsupported,
			ApplyingRequeue:          *applyingCheck,
		},
	}
	ctx := ctrl.SetupSignalHandler()
	trigger, err := r.SetupWithManager(ctx, mgr)
	if err != nil {
		return err
	}
	if err := agent.SetupHealth(mgr, r, agent.HealthConfig{
		Enabled: *healthEnabled, MetricsURL: *metricsURL, MetricsTokenFile: *metricsTokenFile,
		CadvisorFallback: *cadvisorFallback, CheckInterval: *healthInterval, MinThrottlingWindow: *minThrottleWin,
	}, log.With("component", "health")); err != nil {
		return err
	}
	puller := &controller.Puller{
		Hub: hub, Store: store, Reporter: rep,
		PullInterval: *pullInterval, ReportInterval: *reportInterval,
		Trigger: trigger,
		Log:     log.With("component", "puller"),
	}
	if err := mgr.Add(puller); err != nil {
		return err
	}
	log.Info("starting agent", "version", version, "cluster", *cluster, "hub", *hubURL, "readOnly", *readOnly,
		"clusterDefaults", *defaultsFile != "")
	return mgr.Start(ctx)
}

func runStubHub(args []string) error {
	fs := flag.NewFlagSet("stub-hub", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "listen address")
	recsFile := fs.String("recommendations", "", "YAML/JSON file: cluster name -> list of WorkloadRecommendation (re-read every 10s)")
	tokens := fs.String("tokens", "", "comma-separated token=cluster pairs; empty disables auth")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := newLogger("info")
	s := hubstub.New()
	for _, pair := range strings.Split(*tokens, ",") {
		if t, c, ok := strings.Cut(strings.TrimSpace(pair), "="); ok {
			s.AddToken(t, c)
		}
	}
	var last string
	load := func() {
		if *recsFile == "" {
			return
		}
		b, err := os.ReadFile(*recsFile)
		if err != nil {
			log.Warn("read recommendations", "err", err)
			return
		}
		if string(b) == last {
			return
		}
		var recs hubstub.FileRecommendations
		if err := yaml.Unmarshal(b, &recs); err != nil {
			log.Warn("parse recommendations", "err", err)
			return
		}
		last = string(b)
		for cluster, items := range recs {
			s.SetRecommendations(cluster, hubstub.Normalize(items, time.Now()))
			log.Info("loaded recommendations", "cluster", cluster, "items", len(items))
		}
	}
	load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				load()
			}
		}
	}()
	srv := &http.Server{Addr: *listen, Handler: logRequests(log, s.Handler()), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	log.Info("stub hub listening", "addr", *listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func logRequests(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			log.InfoContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}
