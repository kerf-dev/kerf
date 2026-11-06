// Command kerf is the Kerf command-line tool. `kerf report` runs the recommender against an
// existing Prometheus, VictoriaMetrics or Amazon Managed Service for Prometheus and prints what the
// cluster over-requests, without installing anything in the cluster.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/report"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = ""

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

const usage = `kerf: Kubernetes rightsizing from the command line.

Usage:
  kerf report [flags]    recommendations and savings from your metrics, nothing installed
  kerf version           print the version
  kerf help [command]

Run "kerf report -h" for the flags of the report.
Automation, the UI and history need the full install: ` + report.InstallURL + `
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "report":
		return cmdReport(ctx, args[1:], stdout, stderr)
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, "kerf "+buildVersion())
		return 0
	case "help", "-h", "--help", "-help":
		if len(args) > 1 && args[1] == "report" {
			return cmdReport(ctx, []string{"-h"}, stdout, stderr)
		}
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "kerf: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func cmdReport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kerf report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		o        report.Options
		label    string
		format   string
		headers  multi
		extra    multi
		nss      multi
		limit    int
		minSave  float64
		tokenF   string
		passF    string
		caF      string
		certF    string
		keyF     string
		sigRegn  string
		sigSvc   string
		sigRole  string
		quiet    bool
		timeout  time.Duration
		useSigV4 bool
	)
	fs.StringVar(&o.MetricsURL, "prometheus-url", os.Getenv("KERF_PROMETHEUS_URL"), "query base URL of Prometheus, VictoriaMetrics or an Amazon Managed Service for Prometheus workspace (env KERF_PROMETHEUS_URL)")
	fs.StringVar(&label, "cluster-label", "", "label that selects the cluster's series, as <label>=<value>, e.g. cluster=prod-eu-1; empty when the source holds one cluster")
	fs.Var(&extra, "matcher", `extra label matcher such as env="prod" (repeatable)`)
	fs.StringVar(&o.Profile, "profile", hubapi.ProfileProduction, "recommendation profile: dev, production or high-availability")
	fs.StringVar(&format, "format", report.FormatTable, "output format: table, csv, json or markdown")
	fs.Var(&nss, "namespace", "only this namespace (repeatable)")
	fs.IntVar(&limit, "limit", 0, "show only the N workloads with the highest saving (totals still cover all)")
	fs.Float64Var(&minSave, "min-saving", 0, "hide workloads that save less than this many USD per month")
	fs.StringVar(&o.Prices, "prices", "", `node prices: the path of a static price file (the format of the chart's static table), or "aws" (AWS default credentials, read-only Pricing API); default rates are used when empty`)
	fs.StringVar(&o.DefaultRegion, "default-region", "", "AWS region of nodes that have no region label")
	fs.StringVar(&o.Kubeconfig, "kubeconfig", "", "kubeconfig file: read HPAs and JVM settings from the cluster (read-only) to flag hpa-coupled and JVM workloads")
	fs.StringVar(&o.KubeContext, "kube-context", "", "context of the kubeconfig (also turns the cluster read on, with the default kubeconfig)")

	fs.Var(&headers, "header", `HTTP header for every request, "Name: value" (repeatable), e.g. "X-Scope-OrgID: tenant-1"`)
	fs.StringVar(&tokenF, "token-file", "", "file with a bearer token (env KERF_PROMETHEUS_TOKEN also works)")
	fs.StringVar(&o.Auth.Username, "username", "", "basic auth user name")
	fs.StringVar(&passF, "password-file", "", "file with the basic auth password (env KERF_PROMETHEUS_PASSWORD also works)")
	fs.StringVar(&caF, "ca-file", "", "PEM file with the CA certificates that sign the server certificate")
	fs.StringVar(&certF, "client-cert", "", "PEM client certificate (mTLS)")
	fs.StringVar(&keyF, "client-key", "", "PEM client key (mTLS)")
	fs.BoolVar(&o.Auth.InsecureSkipVerify, "insecure-skip-verify", false, "do not verify the server certificate (testing only)")
	fs.BoolVar(&useSigV4, "sigv4", false, "sign requests with AWS SigV4 (automatic for aps-workspaces URLs)")
	fs.StringVar(&sigRegn, "sigv4-region", "", "AWS region for SigV4 (inferred from an aps-workspaces URL)")
	fs.StringVar(&sigSvc, "sigv4-service", "aps", "AWS service name for SigV4")
	fs.StringVar(&sigRole, "sigv4-role-arn", "", "IAM role to assume for SigV4")
	fs.DurationVar(&timeout, "timeout", report.DefaultTimeout, "timeout of one request to the metrics source")
	fs.BoolVar(&quiet, "quiet", false, "no progress messages on stderr")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: kerf report --prometheus-url URL [flags]\n\nRecommendations and savings from your metrics. Reads only; nothing is written to the metrics source\nor the cluster.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "kerf report: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "kerf report: %v\n", err)
		return 1
	}
	if !slicesContains(report.Formats, format) && format != "md" {
		fmt.Fprintf(stderr, "kerf report: --format %q: want table, csv, json or markdown\n", format)
		return 2
	}

	var err error
	if o.ClusterLabel, o.ClusterValue, err = report.ParseClusterLabel(label); err != nil {
		return fail(err)
	}
	o.ExtraMatchers, o.Namespaces, o.Timeout = extra, nss, timeout
	o.Kube = o.Kubeconfig != "" || o.KubeContext != ""
	if len(headers) > 0 {
		o.Auth.Headers = map[string]string{}
		for _, h := range headers {
			k, v, err := report.ParseHeader(h)
			if err != nil {
				return fail(err)
			}
			o.Auth.Headers[k] = v
		}
	}
	if o.Auth.Token, err = secret(tokenF, "KERF_PROMETHEUS_TOKEN"); err != nil {
		return fail(err)
	}
	if o.Auth.Password, err = secret(passF, "KERF_PROMETHEUS_PASSWORD"); err != nil {
		return fail(err)
	}
	for _, f := range []struct {
		path string
		dst  *string
	}{{caF, &o.Auth.CACertPEM}, {certF, &o.Auth.ClientCertPEM}, {keyF, &o.Auth.ClientKeyPEM}} {
		if f.path == "" {
			continue
		}
		b, err := os.ReadFile(f.path)
		if err != nil {
			return fail(err)
		}
		*f.dst = string(b)
	}
	if useSigV4 || sigRegn != "" || sigRole != "" {
		o.Auth.SigV4 = &hubapi.DatasourceSigV4{Region: sigRegn, Service: sigSvc, RoleARN: sigRole}
	}
	if o.Auth.Token != "" && o.Auth.Username != "" {
		o.Auth.Token = "" // an explicit user name wins over a token left in the environment
	}
	if o.Auth.Username == "" {
		o.Auth.Password = ""
	}

	var log *slog.Logger
	if !quiet {
		log = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
		fmt.Fprintf(stderr, "Reading %s of usage history from the metrics source; this can take a minute...\n", profileWindow(o.Profile))
	}
	rep, err := report.Run(ctx, o, report.Deps{Log: log})
	if err != nil {
		return fail(err)
	}
	if err := report.Render(stdout, rep, format, report.RenderOptions{Limit: limit, MinSavingUSD: minSave}); err != nil {
		return fail(err)
	}
	if format == report.FormatCSV && !quiet {
		fmt.Fprintln(stderr, report.NextStep)
	}
	return 0
}

func profileWindow(p string) string {
	switch p {
	case hubapi.ProfileDev:
		return "7 days"
	case hubapi.ProfileHighAvailability:
		return "28 days"
	}
	return "14 days"
}

// secret returns the contents of a file (trimmed) or, without a file, the environment variable.
func secret(file, env string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv(env), nil
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
