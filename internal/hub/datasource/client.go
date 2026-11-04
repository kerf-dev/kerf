package datasource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// Defaults of a datasource.
const (
	DefaultTimeout   = 30 * time.Second
	DefaultMaxPoints = 11000
	DefaultSigV4Svc  = "aps"
)

// AuthConfig is the non-secret auth of a datasource (stored in datasources.auth_config).
type AuthConfig struct {
	Username           string                  `json:"username,omitempty"`
	SigV4              *hubapi.DatasourceSigV4 `json:"sigv4,omitempty"`
	CACert             string                  `json:"caCert,omitempty"`
	ClientCert         string                  `json:"clientCert,omitempty"`
	InsecureSkipVerify bool                    `json:"insecureSkipVerify,omitempty"`
}

// Config is a resolved datasource: stored settings plus decrypted secrets. Never log it.
type Config struct {
	ID        string
	Name      string
	Type      string
	URL       string
	AuthType  string
	Auth      AuthConfig
	Secrets   Secrets
	Headers   map[string]string
	Timeout   time.Duration
	MaxPoints int
}

// String hides the secrets.
func (c Config) String() string {
	return fmt.Sprintf("datasource %s (%s %s, auth %s)", c.Name, c.Type, c.URL, c.AuthType)
}

// GoString hides the secrets from %#v.
func (c Config) GoString() string { return c.String() }

// CredentialsFunc returns AWS credentials for SigV4; tests replace it.
type CredentialsFunc func(ctx context.Context, s hubapi.DatasourceSigV4) (aws.CredentialsProvider, error)

// DefaultCredentials loads the default AWS chain (IRSA / Pod Identity on EKS) and assumes
// s.RoleARN when set.
func DefaultCredentials(ctx context.Context, s hubapi.DatasourceSigV4) (aws.CredentialsProvider, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.Region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if s.RoleARN == "" {
		return cfg.Credentials, nil
	}
	return aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), s.RoleARN,
		func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = "resource-optimizer-hub" })), nil
}

// HTTPClient returns an HTTP client that adds the datasource's headers and auth to every request.
// creds is used for SigV4; nil = DefaultCredentials.
func (c Config) HTTPClient(ctx context.Context, creds CredentialsFunc) (*http.Client, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	tlsCfg, err := c.tlsConfig()
	if err != nil {
		return nil, err
	}
	base.TLSClientConfig = tlsCfg
	var rt http.RoundTripper = base
	switch c.AuthType {
	case hubapi.AuthNone, hubapi.AuthMTLS, "":
	case hubapi.AuthBearer:
		rt = &headerAuth{next: rt, set: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+c.Secrets.Token) }}
	case hubapi.AuthBasic:
		rt = &headerAuth{next: rt, set: func(r *http.Request) { r.SetBasicAuth(c.Auth.Username, c.Secrets.Password) }}
	case hubapi.AuthSigV4:
		if c.Auth.SigV4 == nil {
			return nil, errors.New("sigv4 auth without region")
		}
		s := *c.Auth.SigV4
		if s.Service == "" {
			s.Service = DefaultSigV4Svc
		}
		if creds == nil {
			creds = DefaultCredentials
		}
		p, err := creds(ctx, s)
		if err != nil {
			return nil, err
		}
		rt = &sigV4{next: rt, creds: p, region: s.Region, service: s.Service, signer: v4.NewSigner(), now: time.Now}
	default:
		return nil, fmt.Errorf("unknown auth type %q", c.AuthType)
	}
	if len(c.Headers) > 0 {
		rt = &headerAuth{next: rt, set: func(r *http.Request) {
			for k, v := range c.Headers {
				r.Header.Set(k, v)
			}
		}}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{Transport: rt, Timeout: timeout}, nil
}

func (c Config) tlsConfig() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.Auth.InsecureSkipVerify} //nolint:gosec // opt-in per datasource
	if c.Auth.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.Auth.CACert)) {
			return nil, errors.New("tls.caCert: no PEM certificate")
		}
		cfg.RootCAs = pool
	}
	if c.AuthType == hubapi.AuthMTLS {
		cert, err := tls.X509KeyPair([]byte(c.Auth.ClientCert), []byte(c.Secrets.ClientKey))
		if err != nil {
			return nil, fmt.Errorf("tls client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// headerAuth sets headers on a copy of every request.
type headerAuth struct {
	next http.RoundTripper
	set  func(*http.Request)
}

func (h *headerAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	h.set(r)
	return h.next.RoundTrip(r)
}

// sigV4 signs every request with AWS Signature Version 4 (Amazon Managed Prometheus).
type sigV4 struct {
	next            http.RoundTripper
	creds           aws.CredentialsProvider
	region, service string
	signer          *v4.Signer
	now             func() time.Time
}

func (s *sigV4) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
		r.ContentLength = int64(len(b))
	}
	sum := sha256.Sum256(body)
	creds, err := s.creds.Retrieve(r.Context())
	if err != nil {
		return nil, fmt.Errorf("sigv4 credentials: %w", err)
	}
	if err := s.signer.SignHTTP(r.Context(), creds, r, hex.EncodeToString(sum[:]), s.service, s.region, s.now()); err != nil {
		return nil, fmt.Errorf("sigv4 sign: %w", err)
	}
	return s.next.RoundTrip(r)
}

// API is the part of the Prometheus HTTP API the registry and the checks need besides queries.
type API struct {
	BaseURL string
	HTTP    *http.Client
}

// NewAPI returns the API of a datasource.
func NewAPI(baseURL string, hc *http.Client) *API {
	return &API{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: hc}
}

// ProbeResult is the outcome of a reachability probe.
type ProbeResult struct {
	OK         bool
	Latency    time.Duration
	HTTPStatus int
	// Error is a short error text; Hint says what to fix.
	Error, Hint string
}

// Probe runs the instant query `vector(1)`: it checks URL, TLS, credentials and tenant header.
func (a *API) Probe(ctx context.Context) ProbeResult {
	start := time.Now()
	u := a.BaseURL + "/api/v1/query?" + url.Values{"query": {"vector(1)"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ProbeResult{Error: err.Error(), Hint: "check the datasource URL"}
	}
	res, err := a.HTTP.Do(req)
	lat := time.Since(start)
	if err != nil {
		return ProbeResult{Latency: lat, Error: err.Error(), Hint: transportHint(err)}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	out := ProbeResult{Latency: lat, HTTPStatus: res.StatusCode}
	switch res.StatusCode {
	case http.StatusOK:
		var r struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &r) != nil || r.Status != "success" {
			out.Error = "not a Prometheus query API response: " + truncate(string(body))
			out.Hint = "the URL must be the query base (the part before /api/v1/query); AMP: https://aps-workspaces.<region>.amazonaws.com/workspaces/<id>/"
			return out
		}
		out.OK = true
	case http.StatusUnauthorized, http.StatusForbidden:
		out.Error = fmt.Sprintf("HTTP %d: %s", res.StatusCode, truncate(string(body)))
		out.Hint = "check the credentials; Mimir/Cortex also need the tenant header X-Scope-OrgID; AMP needs aps:QueryMetrics for the hub's IAM role"
	case http.StatusNotFound:
		out.Error = fmt.Sprintf("HTTP 404: %s", truncate(string(body)))
		out.Hint = "the URL must be the query base (the part before /api/v1/query), e.g. http://prometheus:9090 or https://thanos-query/"
	default:
		out.Error = fmt.Sprintf("HTTP %d: %s", res.StatusCode, truncate(string(body)))
		out.Hint = "the datasource answered with an error; check its logs"
	}
	return out
}

func transportHint(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return "the host name does not resolve from the hub"
	case errors.As(err, &certErr), strings.Contains(err.Error(), "x509:"):
		return "the server certificate is not trusted: set tls.caCert (or insecureSkipVerify for testing)"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout"):
		return "no answer within the timeout: check network policies and the URL's port"
	case errors.As(err, &opErr):
		return "cannot connect: check the URL's host and port and network policies"
	}
	return "check the URL and network access from the hub"
}

// LabelValues returns the values of label on series matching any of the selectors (all series
// when none), sorted; at most limit (0 = all).
func (a *API) LabelValues(ctx context.Context, label string, match []string, since time.Time, limit int) ([]string, error) {
	q := url.Values{}
	for _, m := range match {
		q.Add("match[]", m)
	}
	if !since.IsZero() {
		q.Set("start", strconv.FormatInt(since.Unix(), 10))
	}
	u := a.BaseURL + "/api/v1/label/" + url.PathEscape(label) + "/values"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var r struct {
		Status string   `json:"status"`
		Error  string   `json:"error"`
		Data   []string `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("label values: HTTP %d: %s", res.StatusCode, truncate(string(body)))
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("label values: %s", r.Error)
	}
	sort.Strings(r.Data)
	if limit > 0 && len(r.Data) > limit {
		r.Data = r.Data[:limit]
	}
	return r.Data, nil
}

func truncate(s string) string {
	const n = 200
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
