// Command pricing prices every spoke node and exports per-node, per-vCPU-hour and per-GiB-hour costs
// as Prometheus metrics (WO-03, D-008). It runs in the hub and reads nodes from the hub's
// VictoriaMetrics, so it needs no credentials to spoke clusters (D-004).
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
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kerf-dev/kerf/internal/pricing"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

type options struct {
	provider      string
	pricesFile    string
	vmURL         string
	listen        string
	interval      time.Duration
	nodeLookback  time.Duration
	priceTTL      time.Duration
	cpuWeight     float64
	clusters      string
	defaultRegion string
	selfCluster   string
	pricingRegion string
	logLevel      string
	hubURL        string
	hubTokenFile  string
}

func main() {
	var o options
	fs := flag.NewFlagSet("pricing", flag.ExitOnError)
	fs.StringVar(&o.provider, "provider", envOr("PRICING_PROVIDER", "static"), "price provider: static or aws")
	fs.StringVar(&o.pricesFile, "prices", envOr("PRICING_PRICES_FILE", "/etc/kerf/prices.yaml"), "static provider: price file")
	fs.StringVar(&o.vmURL, "vm-url", envOr("PRICING_VM_URL", "http://localhost:8428"), "VictoriaMetrics/Prometheus query URL holding spoke node metrics")
	fs.StringVar(&o.listen, "listen", envOr("PRICING_LISTEN", ":9090"), "address serving /metrics, /healthz, /readyz")
	fs.DurationVar(&o.interval, "interval", 5*time.Minute, "node discovery and pricing interval")
	fs.DurationVar(&o.nodeLookback, "node-lookback", pricing.DefaultNodeLookback, "keep pricing a node this long after its last kube_node_labels sample")
	fs.DurationVar(&o.priceTTL, "price-ttl", 24*time.Hour, "how long a fetched price is reused before it is refreshed")
	fs.Float64Var(&o.cpuWeight, "cpu-memory-weight", 0, "weight of 1 vCPU in GiB of memory for the cost split; 0 derives it per instance family (fallback 9)")
	fs.StringVar(&o.clusters, "clusters", envOr("PRICING_CLUSTERS", ""), "regex of cluster label values to price (default all)")
	fs.StringVar(&o.defaultRegion, "default-region", envOr("PRICING_DEFAULT_REGION", ""), "region for nodes without region/zone labels")
	fs.StringVar(&o.selfCluster, "cluster-name", envOr("CLUSTER_NAME", "ro-hub"), "cluster this service runs in (cluster label of its own kerf_pricing_* series)")
	fs.StringVar(&o.pricingRegion, "aws-pricing-region", pricing.DefaultPricingRegion, "aws provider: AWS Pricing API endpoint region")
	fs.StringVar(&o.logLevel, "log-level", envOr("LOG_LEVEL", "info"), "debug, info, warn or error")
	fs.StringVar(&o.hubURL, "hub-url", envOr("PRICING_HUB_URL", ""), "hub API URL: clusters with a metrics binding are discovered in their datasource through the hub's query proxy (D-025, D-026); empty = only --vm-url")
	fs.StringVar(&o.hubTokenFile, "hub-token-file", envOr("PRICING_HUB_TOKEN_FILE", ""), "file with the hub token of role pricing (needed with --hub-url)")
	_ = fs.Parse(os.Args[1:])

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(o.logLevel)); err != nil {
		fmt.Fprintln(os.Stderr, "invalid --log-level:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, o, log)
	stop()
	if err != nil {
		log.Error("pricing exited", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options, log *slog.Logger) error {
	base, err := newProvider(ctx, o)
	if err != nil {
		return err
	}
	provider := pricing.NewCached(base, o.priceTTL, log)
	splitter := &pricing.Splitter{Provider: provider, Fixed: o.cpuWeight, Log: log}
	disc := &pricing.Discovery{VMURL: o.vmURL, ClusterRegex: o.clusters, DefaultRegion: o.defaultRegion, Lookback: o.nodeLookback, Log: log}
	if o.hubURL != "" {
		if o.hubTokenFile == "" {
			return errors.New("--hub-url needs --hub-token-file")
		}
		disc.Targets = &pricing.HubTargets{HubURL: o.hubURL, Token: pricing.FileToken(o.hubTokenFile)}
	}
	exp := pricing.NewExporter(disc, provider, splitter, log)

	reg := prometheus.NewRegistry()
	ops := prometheus.WrapRegistererWith(prometheus.Labels{"cluster": o.selfCluster}, reg)
	if err := exp.Register(reg, ops); err != nil {
		return err
	}
	ops.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !exp.Ready() {
			http.Error(w, "no successful refresh yet", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	srv := &http.Server{Addr: o.listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go exp.Run(ctx, o.interval)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.InfoContext(ctx, "pricing started", "version", version, "provider", base.Name(), "listen", o.listen, "vm_url", o.vmURL, "interval", o.interval.String())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newProvider(ctx context.Context, o options) (pricing.Provider, error) {
	switch o.provider {
	case "static":
		return pricing.LoadStatic(o.pricesFile)
	case "aws":
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("load aws config: %w", err)
		}
		return pricing.NewAWS(cfg, o.pricingRegion), nil
	}
	return nil, fmt.Errorf("unknown --provider %q (want static or aws)", o.provider)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
