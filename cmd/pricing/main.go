// Command pricing prices every spoke node and exports per-node, per-vCPU-hour and per-GiB-hour costs
// as Prometheus metrics. It runs in the hub and reads nodes from the hub's VictoriaMetrics, so it
// needs no credentials to spoke clusters. With --cur-source it prices AWS nodes at the effective
// rates of the Cost and Usage Report, falling back to list prices.
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
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	hubTokenSrc   string
	vmURLSetting  string
	cur           curOptions
}

// curOptions configure the Cost and Usage Report reader (off when source is empty).
type curOptions struct {
	source         string // "", athena or s3
	region         string
	interval       time.Duration
	window         time.Duration
	maxAge         time.Duration
	database       string
	table          string
	workgroup      string
	catalog        string
	outputLocation string
	bucket         string
	prefix         string
}

func main() {
	var o options
	fs := flag.NewFlagSet("pricing", flag.ExitOnError)
	fs.StringVar(&o.provider, "provider", envOr("PRICING_PROVIDER", "static"), "price provider: static or aws")
	fs.StringVar(&o.pricesFile, "prices", envOr("PRICING_PRICES_FILE", "/etc/kerf/prices.yaml"), "static provider: price file")
	fs.StringVar(&o.vmURL, "vm-url", envOr("PRICING_VM_URL", "http://localhost:8428"), "VictoriaMetrics/Prometheus query URL holding spoke node metrics; empty with --hub-url: the hub's own metrics datasource, through the hub")
	fs.StringVar(&o.vmURLSetting, "vm-url-setting", "", "the setting --vm-url comes from (e.g. pricing.vmURL), named when a query fails")
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
	fs.StringVar(&o.hubURL, "hub-url", envOr("PRICING_HUB_URL", ""), "hub API URL: clusters with a metrics binding are discovered in their datasource through the hub's query proxy; empty = only --vm-url")
	fs.StringVar(&o.hubTokenFile, "hub-token-file", envOr("PRICING_HUB_TOKEN_FILE", ""), "file with the hub token of role pricing (needed with --hub-url)")
	fs.StringVar(&o.hubTokenSrc, "hub-token-source", "", "where the token file comes from, for the error when it is missing, e.g. \"Secret kerf/pricing-hub-token key token\"")
	fs.StringVar(&o.cur.source, "cur-source", envOr("PRICING_CUR_SOURCE", ""), "AWS Cost and Usage Report reader: athena, s3, or empty for none (list prices only)")
	fs.StringVar(&o.cur.region, "cur-region", envOr("PRICING_CUR_REGION", ""), "region of the Athena workgroup or the export bucket (default: the SDK's region)")
	fs.DurationVar(&o.cur.interval, "cur-interval", pricing.DefaultCURInterval, "how often the report is read")
	fs.DurationVar(&o.cur.window, "cur-window", pricing.DefaultCURWindow, "usage window the effective rates are computed over")
	fs.DurationVar(&o.cur.maxAge, "cur-max-age", pricing.DefaultCURMaxAge, "serve the last effective rates this long while the report cannot be read; then list prices")
	fs.StringVar(&o.cur.database, "cur-athena-database", envOr("PRICING_CUR_ATHENA_DATABASE", ""), "athena: Glue database of the CUR 2.0 table")
	fs.StringVar(&o.cur.table, "cur-athena-table", envOr("PRICING_CUR_ATHENA_TABLE", ""), "athena: CUR 2.0 table")
	fs.StringVar(&o.cur.workgroup, "cur-athena-workgroup", envOr("PRICING_CUR_ATHENA_WORKGROUP", "primary"), "athena: workgroup")
	fs.StringVar(&o.cur.catalog, "cur-athena-catalog", envOr("PRICING_CUR_ATHENA_CATALOG", ""), "athena: data catalog (default AwsDataCatalog)")
	fs.StringVar(&o.cur.outputLocation, "cur-athena-output", envOr("PRICING_CUR_ATHENA_OUTPUT", ""), "athena: s3:// URL for query results (default: the workgroup's)")
	fs.StringVar(&o.cur.bucket, "cur-s3-bucket", envOr("PRICING_CUR_S3_BUCKET", ""), "s3: bucket of the CUR 2.0 export")
	fs.StringVar(&o.cur.prefix, "cur-s3-prefix", envOr("PRICING_CUR_S3_PREFIX", ""), "s3: the export's data path, <path prefix>/<export name>/data/")
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
	list := pricing.NewCached(base, o.priceTTL, log)
	// The CPU/memory weight comes from list prices: effective rates are discounted unevenly.
	splitter := &pricing.Splitter{Provider: list, Fixed: o.cpuWeight, Log: log}
	var provider pricing.Provider = list
	cur, err := newCUR(ctx, o.cur, list, log)
	if err != nil {
		return err
	}
	if cur != nil {
		provider = cur
	}
	vmURL, vmSrc, viaHub, err := vmSource(o.vmURL, o.vmURLSetting, o.hubURL)
	if err != nil {
		return err
	}
	disc := &pricing.Discovery{VMURL: vmURL, VMSource: vmSrc, ClusterRegex: o.clusters, DefaultRegion: o.defaultRegion, Lookback: o.nodeLookback, Log: log}
	var token *hubToken
	if o.hubURL != "" {
		if o.hubTokenFile == "" {
			return errors.New("--hub-url needs --hub-token-file")
		}
		token = newHubToken(o.hubTokenFile, o.hubTokenSrc, log)
		// The User-Agent tells the hub this service's version (Admin -> Maintenance).
		hc := &http.Client{Timeout: 60 * time.Second, Transport: userAgent{ua: hubUserAgent()}}
		disc.Targets = &pricing.HubTargets{HubURL: o.hubURL, Token: token.Token, HTTP: hc}
		if viaHub {
			disc.HTTP = &http.Client{Timeout: 60 * time.Second, Transport: bearerTransport{next: userAgent{ua: hubUserAgent()}, token: token.Token}}
		}
	}
	exp := pricing.NewExporter(disc, provider, splitter, log)

	reg := prometheus.NewRegistry()
	ops := prometheus.WrapRegistererWith(prometheus.Labels{"cluster": o.selfCluster}, reg)
	if err := exp.Register(reg, ops); err != nil {
		return err
	}
	ops.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	if cur != nil {
		if err := cur.Register(ops); err != nil {
			return err
		}
		go cur.Run(ctx, o.cur.interval)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if token != nil {
			if _, err := token.Token(); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
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
	log.InfoContext(ctx, "pricing started", "version", version, "provider", base.Name(), "cur_source", o.cur.source, "listen", o.listen, "vm_url", disc.VMURL, "vm_url_source", vmSrc, "interval", o.interval.String())

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

// newCUR returns the Cost and Usage Report provider, or nil when --cur-source is empty. Credentials
// come from the default chain (EKS Pod Identity or IRSA).
func newCUR(ctx context.Context, o curOptions, fallback pricing.Provider, log *slog.Logger) (*pricing.CUR, error) {
	var reader pricing.CURReader
	switch o.source {
	case "":
		return nil, nil
	case "athena", "s3":
	default:
		return nil, fmt.Errorf("unknown --cur-source %q (want athena, s3 or empty)", o.source)
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if o.region != "" {
		cfg.Region = o.region
	}
	switch o.source {
	case "athena":
		if o.database == "" || o.table == "" {
			return nil, errors.New("--cur-source athena needs --cur-athena-database and --cur-athena-table")
		}
		reader = &pricing.CURAthena{Athena: athena.NewFromConfig(cfg), Database: o.database, Table: o.table,
			Workgroup: o.workgroup, Catalog: o.catalog, OutputLocation: o.outputLocation}
	case "s3":
		if o.bucket == "" {
			return nil, errors.New("--cur-source s3 needs --cur-s3-bucket")
		}
		reader = &pricing.CURS3{S3: s3.NewFromConfig(cfg), Bucket: o.bucket, Prefix: o.prefix, TempDir: os.TempDir()}
	}
	c := pricing.NewCUR(reader, fallback, log)
	c.Window, c.MaxAge = o.window, o.maxAge
	log.InfoContext(ctx, "effective rates from the cost and usage report", "reader", reader.Name(), "region", cfg.Region, "window", o.window.String(), "interval", o.interval.String())
	return c, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// hubUserAgent is kerf-pricing/<version> (<pod name>), as the hub's Maintenance page reads it.
func hubUserAgent() string {
	ua := "kerf-pricing/" + version
	if h, err := os.Hostname(); err == nil && h != "" {
		ua += " (" + h + ")"
	}
	return ua
}

// userAgent sets the User-Agent of every request.
type userAgent struct {
	ua   string
	next http.RoundTripper
}

func (t userAgent) RoundTrip(r *http.Request) (*http.Response, error) {
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", t.ua)
	return next.RoundTrip(r)
}
