// Command webhook is the resource-optimizer mutating admission webhook (WO-07, D-007). On pod
// CREATE it sets the recommended requests/limits of Applied in-place RightsizingRecommendations, so
// new pods (scale-out, eviction, redeploy) start with the values the agent resized running pods to,
// without touching the pod template.
package main

import (
	"errors"
	"flag"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	rowebhook "github.com/tomer1983/resource-optimizer/internal/webhook"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("webhook", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("webhook", flag.ContinueOnError)
	var (
		port        = fs.Int("port", 9443, "webhook HTTPS port")
		certDir     = fs.String("cert-dir", "/etc/resource-optimizer/webhook/certs", "directory with tls.crt and tls.key (reloaded on change)")
		metricsAddr = fs.String("metrics-bind-address", ":8080", "metrics endpoint")
		probeAddr   = fs.String("health-probe-bind-address", ":8081", "health probe endpoint")
		logLevel    = fs.String("log-level", "info", "debug, info, warn or error")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(*logLevel)); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
	slog.SetDefault(log)
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: *metricsAddr},
		HealthProbeBindAddress: *probeAddr,
		WebhookServer:          webhook.NewServer(webhook.Options{Port: *port, CertDir: *certDir}),
	})
	if err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	ctx := ctrl.SetupSignalHandler()
	if _, err := rowebhook.Setup(ctx, mgr, rowebhook.NewMetrics(crmetrics.Registry), log.With("component", "webhook")); err != nil {
		return err
	}
	if _, err := os.Stat(*certDir); err != nil {
		return errors.New("--cert-dir: " + err.Error())
	}
	log.InfoContext(ctx, "starting webhook", "version", version, "port", *port, "certDir", *certDir)
	return mgr.Start(ctx)
}
