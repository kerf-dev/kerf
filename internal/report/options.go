// Package report builds the `kerf report` output: the recommender run against a metrics source
// (Prometheus, VictoriaMetrics or Amazon Managed Service for Prometheus) without the hub. It
// reuses the hub's queries, profiles, strategy and risk rules, so for the same history and
// profile the numbers are the ones the hub would show.
package report

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// DefaultCluster is the cluster name used when the metrics have no cluster label.
const DefaultCluster = "default"

// DefaultTimeout is the timeout of one request to the metrics source.
const DefaultTimeout = 2 * time.Minute

// Options are the settings of one report.
type Options struct {
	// MetricsURL is the query base URL, the part before /api/v1/query.
	MetricsURL string
	// ClusterLabel and ClusterValue select the cluster's series (`cluster=prod-eu-1`). Both empty
	// = the metrics source holds one cluster and no matcher is added.
	ClusterLabel, ClusterValue string
	// ExtraMatchers are further label matchers such as env="prod".
	ExtraMatchers []string
	// Profile is dev, production or high-availability.
	Profile string
	// Namespaces limits the report to these namespaces; empty = all.
	Namespaces []string

	Auth Auth

	// Prices is "" (the default rates, labelled as such), "aws", or the path of a static price file.
	Prices string
	// DefaultRegion prices nodes that have no region label.
	DefaultRegion string

	// Kube enables reading HPAs and JVM settings from the cluster (read-only). Kubeconfig is the
	// file ("" = the default loading rules) and KubeContext picks a context in it.
	Kube                    bool
	Kubeconfig, KubeContext string

	// Timeout is the timeout of one request to the metrics source; 0 = DefaultTimeout.
	Timeout time.Duration
	// Now is the end of the data window; zero = the current time.
	Now time.Time
}

// Auth is how to authenticate to the metrics source. Like the hub's datasources: headers, basic,
// bearer, TLS and mTLS, and SigV4.
type Auth struct {
	// Headers are sent on every request (e.g. X-Scope-OrgID for Mimir and Cortex).
	Headers map[string]string
	Token   string
	// Username and Password are basic auth.
	Username, Password string
	// CACertPEM, ClientCertPEM and ClientKeyPEM are file contents.
	CACertPEM, ClientCertPEM, ClientKeyPEM string
	InsecureSkipVerify                     bool
	// SigV4 signs requests with the AWS credentials of the environment (Amazon Managed Service
	// for Prometheus). nil = off, unless the URL is an AMP workspace.
	SigV4 *hubapi.DatasourceSigV4
}

var (
	ampHostRE      = regexp.MustCompile(`^aps-workspaces(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?$`)
	labelNameRE    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	profileNameSet = map[string]bool{hubapi.ProfileDev: true, hubapi.ProfileProduction: true, hubapi.ProfileHighAvailability: true}
)

// ParseClusterLabel splits `key=value` ("" = no cluster matcher).
func ParseClusterLabel(s string) (label, value string, err error) {
	if s == "" {
		return "", "", nil
	}
	label, value, ok := strings.Cut(s, "=")
	label, value = strings.TrimSpace(label), strings.TrimSpace(value)
	if !ok || value == "" || !labelNameRE.MatchString(label) {
		return "", "", fmt.Errorf("--cluster-label %q: want <label>=<value>, e.g. cluster=prod-eu-1", s)
	}
	return label, value, nil
}

// ParseHeader splits `Name: value` or `Name=value`.
func ParseHeader(s string) (name, value string, err error) {
	i := strings.IndexAny(s, ":=")
	if i <= 0 {
		return "", "", fmt.Errorf("--header %q: want Name: value", s)
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), nil
}

// Validate checks the options and fills in what can be inferred (the SigV4 region of an AMP URL).
func (o *Options) Validate() error {
	var errs []error
	u, err := url.Parse(o.MetricsURL)
	switch {
	case o.MetricsURL == "":
		errs = append(errs, errors.New("--prometheus-url is required"))
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		errs = append(errs, fmt.Errorf("--prometheus-url %q: want an http(s) URL", o.MetricsURL))
	}
	if o.Profile == "" {
		o.Profile = hubapi.ProfileProduction
	}
	if !profileNameSet[o.Profile] {
		errs = append(errs, fmt.Errorf("--profile %q: want dev, production or high-availability", o.Profile))
	}
	for _, m := range o.ExtraMatchers {
		if _, err := datasource.NormalizeMatcher(m); err != nil {
			errs = append(errs, err)
		}
	}
	a := &o.Auth
	if a.Token != "" && a.Username != "" {
		errs = append(errs, errors.New("use either a bearer token or a username and password, not both"))
	}
	if a.ClientCertPEM != "" && (a.Token != "" || a.Username != "" || a.SigV4 != nil) {
		errs = append(errs, errors.New("a client certificate (mTLS) cannot be combined with a token, a password or SigV4"))
	}
	if (a.ClientCertPEM == "") != (a.ClientKeyPEM == "") {
		errs = append(errs, errors.New("--client-cert and --client-key go together"))
	}
	if a.SigV4 == nil && u != nil {
		if m := ampHostRE.FindStringSubmatch(u.Hostname()); m != nil {
			a.SigV4 = &hubapi.DatasourceSigV4{Region: m[1]}
		}
	}
	if a.SigV4 != nil {
		if a.SigV4.Region == "" && u != nil {
			if m := ampHostRE.FindStringSubmatch(u.Hostname()); m != nil {
				a.SigV4.Region = m[1]
			}
		}
		if a.SigV4.Region == "" {
			errs = append(errs, errors.New("--sigv4-region is required (it can only be inferred from an aps-workspaces URL)"))
		}
	}
	return errors.Join(errs...)
}

// ClusterName is the cluster name used in the report.
func (o *Options) ClusterName() string {
	if o.ClusterValue != "" {
		return o.ClusterValue
	}
	return DefaultCluster
}

// Matcher returns the matcher selecting the cluster's series.
func (o *Options) Matcher() (datasource.Matcher, error) {
	m := datasource.Matcher{Cluster: o.ClusterName(), Label: o.ClusterLabel, Value: o.ClusterValue}
	for _, s := range o.ExtraMatchers {
		n, err := datasource.NormalizeMatcher(s)
		if err != nil {
			return datasource.Matcher{}, err
		}
		m.Extra = append(m.Extra, n)
	}
	return m, nil
}

// datasourceConfig maps the auth options to the hub's datasource client.
func (o *Options) datasourceConfig() datasource.Config {
	a := o.Auth
	c := datasource.Config{
		Name: "kerf report", Type: "prometheus", URL: o.MetricsURL, AuthType: hubapi.AuthNone,
		Headers: a.Headers, Timeout: o.Timeout,
		Auth: datasource.AuthConfig{CACert: a.CACertPEM, ClientCert: a.ClientCertPEM, InsecureSkipVerify: a.InsecureSkipVerify},
	}
	switch {
	case a.SigV4 != nil:
		c.AuthType, c.Auth.SigV4 = hubapi.AuthSigV4, a.SigV4
	case a.Token != "":
		c.AuthType, c.Secrets.Token = hubapi.AuthBearer, a.Token
	case a.Username != "":
		c.AuthType, c.Auth.Username, c.Secrets.Password = hubapi.AuthBasic, a.Username, a.Password
	case a.ClientCertPEM != "":
		c.AuthType, c.Secrets.ClientKey = hubapi.AuthMTLS, a.ClientKeyPEM
	}
	return c
}
