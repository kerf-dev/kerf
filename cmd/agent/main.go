// Command agent is the per-cluster Kerf agent. It pulls recommendations
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
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent"
	"github.com/kerf-dev/kerf/internal/agent/capacity"
	"github.com/kerf-dev/kerf/internal/agent/configsync"
	"github.com/kerf-dev/kerf/internal/agent/controller"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
	"github.com/kerf-dev/kerf/internal/agent/hubstub"
	"github.com/kerf-dev/kerf/internal/agent/selfcheck"
	"github.com/kerf-dev/kerf/internal/agent/windows"
	"github.com/kerf-dev/kerf/internal/health"
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

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ";") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// metricsSelector is the cluster's metrics binding at --metrics-url; nil = label cluster = the
// cluster name (before D-025).
func metricsSelector(label, value string, extra []string) *health.MetricsSelector {
	if label == "cluster" && value == "" && len(extra) == 0 {
		return nil
	}
	return &health.MetricsSelector{Label: label, Value: value, Extra: extra}
}

// newSelfCheck starts the readiness self-checks (D-025): their results go with the next status
// batch; the hub's recheck flag re-runs them.
func newSelfCheck(mgr manager.Manager, hub *hubclient.Client, rep *controller.Reporter, puller *controller.Puller, cfg selfcheck.Config,
	interval time.Duration, log *slog.Logger) (*selfcheck.Runner, error) {
	c, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return nil, fmt.Errorf("self-check client: %w", err)
	}
	dc, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
	if err != nil {
		return nil, fmt.Errorf("self-check discovery: %w", err)
	}
	var syncer *configsync.Syncer
	if s, ok := puller.Config.(*configsync.Syncer); ok {
		syncer = s
	}
	checker := &selfcheck.Checker{Client: c, Discovery: dc, Cfg: cfg, Inputs: func() selfcheck.Inputs {
		var in selfcheck.Inputs
		if syncer != nil {
			if hc := syncer.State.Config(); hc != nil {
				in.HubVersion, in.MinAgentVersion = hc.HubVersion, hc.MinAgentVersion
			}
		}
		in.HubDate, in.LocalAt = hub.HubClock()
		in.Namespaces, in.Excluded = rep.Coverage()
		if in.Namespaces == 0 {
			in.Excluded = nil // nothing reported yet
		}
		return in
	}}
	runner := &selfcheck.Runner{Checker: checker, Interval: interval, Log: log,
		OnResult: func(res selfcheck.Result) { rep.SetSelfCheckResult(res.Capabilities, res.Checks, res.Components) }}
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		// Let the first pull and reports happen, so version, clock and coverage are known.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(15 * time.Second):
		}
		runner.Run(ctx)
		return nil
	})); err != nil {
		return nil, err
	}
	return runner, nil
}

// addCapacity starts the capacity report (read only): its result goes with the next status batch.
func addCapacity(mgr manager.Manager, rep *controller.Reporter, interval time.Duration, caStatus string, poolLabels []string, log *slog.Logger) error {
	var ca types.NamespacedName
	if caStatus != "" {
		ns, name, ok := strings.Cut(caStatus, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("--cluster-autoscaler-status %q: want namespace/name", caStatus)
		}
		ca = types.NamespacedName{Namespace: ns, Name: name}
	}
	r := &capacity.Runner{
		Collector: &capacity.Collector{Pods: mgr.GetClient(), Reader: mgr.GetAPIReader(), PoolLabels: poolLabels, CAStatus: ca, Log: log},
		Interval:  interval,
		OnReport:  rep.SetCapacity,
		Log:       log,
	}
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		// Let the pod cache sync and the first pull happen.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(20 * time.Second):
		}
		r.Run(ctx)
		return nil
	}))
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
		tokenFile      = fs.String("hub-token-file", "/var/run/secrets/kerf/hub/token", "file with the hub bearer token (re-read on every request)")
		defaultsFile   = fs.String("cluster-defaults", "", "YAML file with the cluster default policy (a RightsizingPolicy spec)")
		freezeFile     = fs.String("freeze-file", "", "YAML file with the cluster-wide freeze {now: bool, windows: [TimeWindow]}")
		pullInterval   = fs.Duration("pull-interval", time.Minute, "how often recommendations are pulled")
		staleAfter     = fs.Duration("hub-stale-after", 5*time.Minute, "after this long without a successful pull the hub counts as unreachable and nothing new is applied")
		reportInterval = fs.Duration("report-interval", 10*time.Second, "how often changed statuses and events are sent")
		eventBuffer    = fs.Int("event-buffer", 10000, "events buffered while the hub is unreachable (oldest dropped first)")
		readOnly       = fs.Bool("read-only", false, "cap every mode at audit; never write workloads or pods")
		tolerance      = fs.Float64("tolerance", 0.05, "relative change below which values are left alone")
		applyingCheck  = fs.Duration("applying-requeue", 5*time.Second, "how often in-progress in-place resizes are checked")
		maxPodDeletes  = fs.Int("max-pod-deletes-per-run", controller.DefaultMaxPodDeletesPerRun, "pods of an OnDelete StatefulSet evicted at once, after all its pods are Ready")
		onDeleteSettle = fs.Duration("ondelete-settle", controller.DefaultOnDeleteSettle, "how long a replaced OnDelete StatefulSet pod must be Ready (health watch) before the next is evicted")
		metricsAddr    = fs.String("metrics-bind-address", ":8080", "metrics endpoint")
		probeAddr      = fs.String("health-probe-bind-address", ":8081", "health probe endpoint")
		leaderElect    = fs.Bool("leader-elect", false, "enable leader election (for more than one replica)")
		logLevel       = fs.String("log-level", "info", "debug, info, warn or error")
		unsupported    labelSelectors

		healthEnabled    = fs.Bool("health-watch", true, "watch workloads after each change and roll back on a breach")
		healthInterval   = fs.Duration("health-check-interval", 15*time.Second, "how often a watched workload is re-evaluated (pod events act immediately)")
		metricsURL       = fs.String("metrics-url", "http://victoria-metrics.hub.svc:8428", "Prometheus-compatible endpoint for the CPU throttling ratio; empty disables it")
		metricsTokenFile = fs.String("metrics-token-file", "", "optional bearer token file for --metrics-url")
		cadvisorFallback = fs.Bool("health-cadvisor-fallback", false, "read throttling from the kubelets' cAdvisor (needs nodes/proxy) when --metrics-url fails")
		minThrottleWin   = fs.Duration("health-min-throttling-window", 2*time.Minute, "time after a change before throttling is evaluated")

		metricsLabel      = fs.String("metrics-cluster-label", "cluster", "label selecting this cluster's series at --metrics-url (its metrics binding, D-025); empty = none")
		metricsLabelValue = fs.String("metrics-cluster-label-value", "", "value of --metrics-cluster-label (default: --cluster-name)")
		metricsExtra      stringList

		selfCheck         = fs.Bool("self-check", true, "run the readiness self-checks and report them to the hub (D-025)")
		selfCheckInterval = fs.Duration("self-check-interval", selfcheck.DefaultInterval, "how often the self-checks run (also at start and on the hub's recheck flag)")
		webhookConfig     = fs.String("webhook-config-name", "", "MutatingWebhookConfiguration of the webhook (default: found by webhook name)")
		webhookDryRun     = fs.Bool("self-check-webhook-dry-run", false, "dry-run create a pod to see the webhook mutate it (needs create pods)")
		argocdNamespaces  stringList
		docsBaseURL       = fs.String("docs-base-url", "", "base of the readiness docs links")

		capacityOn       = fs.Bool("capacity", true, "report nodes, node pools (Karpenter, Cluster Autoscaler) and consolidation blockers for the node estimate (read only)")
		capacityInterval = fs.Duration("capacity-interval", capacity.DefaultInterval, "how often the capacity report is built (sent when it changed)")
		caStatus         = fs.String("cluster-autoscaler-status", capacity.DefaultCAStatus.String(), "namespace/name of the Cluster Autoscaler status ConfigMap; empty disables it")
		poolLabels       stringList

		configSync    = fs.Bool("config-sync", true, "pull the hub config: hub-managed policies, pause and approve-once (D-022)")
		configStateCM = fs.String("config-state-configmap", "kerf-agent-config-state", "ConfigMap in $POD_NAMESPACE that persists the last hub config; empty disables persistence")
	)
	fs.Var(&metricsExtra, "metrics-extra-matcher", "extra PromQL label matcher for --metrics-url, e.g. env=\"prod\"; repeatable")
	fs.Var(&poolLabels, "node-pool-label", "node label naming a node's pool when it is not a Karpenter node (default: EKS, GKE, AKS, kOps and KWOK labels); repeatable")
	fs.Var(&argocdNamespaces, "argocd-namespace", "namespace searched for argocd-cm (default argocd); repeatable")
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

	freeze, err := windows.LoadFreeze(*freezeFile)
	if err != nil {
		return err
	}
	if freeze.Now || len(freeze.Windows) > 0 {
		log.Info("cluster-wide freeze configured", "now", freeze.Now, "windows", len(freeze.Windows))
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		// Pods are cached for the health watch: strip their managedFields. Never strip workloads':
		// the agent reads its own field ownership from them (release, D-006).
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {Transform: cache.TransformStripManagedFields()},
		}},
		Metrics:                 metricsserver.Options{BindAddress: *metricsAddr},
		HealthProbeBindAddress:  *probeAddr,
		LeaderElection:          *leaderElect,
		LeaderElectionID:        "kerf-agent",
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
		Recorder: mgr.GetEventRecorder("kerf-agent"),
		Log:      log.With("component", "reconciler"),
		Cfg: controller.Config{
			ClusterName:              *cluster,
			ClusterDefaults:          defaults,
			ReadOnly:                 *readOnly,
			Tolerance:                *tolerance,
			UnsupportedNodeSelectors: unsupported,
			ApplyingRequeue:          *applyingCheck,
			Freeze:                   freeze,
			MaxPodDeletesPerRun:      *maxPodDeletes,
			OnDeleteSettle:           *onDeleteSettle,
		},
	}
	ctx := ctrl.SetupSignalHandler()
	trigger, err := r.SetupWithManager(ctx, mgr)
	if err != nil {
		return err
	}
	if err := agent.SetupHealth(mgr, r, agent.HealthConfig{
		Enabled: *healthEnabled, MetricsURL: *metricsURL, MetricsTokenFile: *metricsTokenFile,
		MetricsSelector:  metricsSelector(*metricsLabel, *metricsLabelValue, metricsExtra),
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
	if *configSync {
		state := configsync.NewState()
		r.Admin = state
		ns := os.Getenv("POD_NAMESPACE")
		if ns == "" && *configStateCM != "" {
			log.Warn("POD_NAMESPACE is not set: the hub config is not persisted across restarts")
		}
		puller.Config = &configsync.Syncer{
			Hub: hub, Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), State: state,
			Namespace: ns, ConfigMap: *configStateCM, ReadOnly: *readOnly,
			Inventory: rep.SetConfig,
			Log:       log.With("component", "configsync"),
		}
	}
	if *selfCheck {
		runner, err := newSelfCheck(mgr, hub, rep, puller, selfcheck.Config{
			AgentVersion: version, ReadOnly: *readOnly, WebhookConfigName: *webhookConfig, WebhookDryRun: *webhookDryRun,
			ArgoCDNamespaces: argocdNamespaces, UnsupportedNodeSelectors: unsupported, DocsBase: *docsBaseURL,
		}, *selfCheckInterval, log.With("component", "selfcheck"))
		if err != nil {
			return err
		}
		if syncer, ok := puller.Config.(*configsync.Syncer); ok {
			syncer.Recheck = func(at time.Time) { runner.Recheck(at) }
		}
	}
	if *capacityOn {
		if err := addCapacity(mgr, rep, *capacityInterval, *caStatus, poolLabels, log.With("component", "capacity")); err != nil {
			return err
		}
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
	configFile := fs.String("config", "", "YAML/JSON file: cluster name -> ClusterConfig served at GET config (re-read every 10s; configVersion derived when empty)")
	upstream := fs.String("upstream", "", "real hub URL: forward recommendations, statuses and events there; only config is served by the stub")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := newLogger("info")
	s := hubstub.New()
	if *upstream != "" {
		u, err := url.Parse(*upstream)
		if err != nil {
			return fmt.Errorf("upstream: %w", err)
		}
		s.SetUpstream(u)
	}
	for _, pair := range strings.Split(*tokens, ",") {
		if t, c, ok := strings.Cut(strings.TrimSpace(pair), "="); ok {
			s.AddToken(t, c)
		}
	}
	var last, lastCfg string
	loadConfig := func() {
		if *configFile == "" {
			return
		}
		b, err := os.ReadFile(*configFile)
		if err != nil {
			log.Warn("read config", "err", err)
			return
		}
		if string(b) == lastCfg {
			return
		}
		var cfgs map[string]*hubclient.ClusterConfig
		if err := yaml.Unmarshal(b, &cfgs); err != nil {
			log.Warn("parse config", "err", err)
			return
		}
		lastCfg = string(b)
		for cluster, c := range cfgs {
			s.SetConfig(cluster, c)
			got := s.Config(cluster)
			log.Info("loaded config", "cluster", cluster, "configVersion", got.ConfigVersion, "policies", len(got.Policies),
				"approvals", len(got.Approvals), "paused", got.Flags.Paused)
		}
	}
	load := func() {
		loadConfig()
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
