// Package datasource is the registry of Prometheus-compatible metrics datasources (D-025): their
// settings, encrypted secrets, HTTP clients with auth (bearer, basic, SigV4, mTLS, headers), the
// reachability test, and the per-cluster metrics binding (Matcher). docs/contracts/readiness.md §1.
package datasource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// Record is a datasource as stored (secrets encrypted).
type Record struct {
	ID        string
	Name      string
	Type      string
	URL       string
	AuthType  string
	Auth      AuthConfig
	Secret    []byte
	Headers   map[string]string
	Timeout   string
	MaxPoints int
	BuiltIn   bool
	Version   int
	CreatedAt time.Time
	CreatedBy *hubapi.Actor
	UpdatedAt time.Time
	UpdatedBy *hubapi.Actor
}

// Repo stores datasources and bindings (Postgres: PG).
type Repo interface {
	ListDatasources(ctx context.Context) ([]Record, error)
	// GetDatasource returns ErrNotFound for unknown and deleted ids.
	GetDatasource(ctx context.Context, id string) (Record, error)
	InsertDatasource(ctx context.Context, r Record) error
	// UpdateDatasource stores r with version r.Version if the stored version is r.Version-1
	// (ErrVersionConflict otherwise).
	UpdateDatasource(ctx context.Context, r Record) error
	// DeleteDatasource soft-deletes id at version (ErrVersionConflict, ErrInUse).
	DeleteDatasource(ctx context.Context, id string, version int, actor hubapi.Actor) error
	// UsedBy maps datasource ids to the clusters bound to them (clusters without a binding use
	// local).
	UsedBy(ctx context.Context) (map[string][]string, error)
	// UpsertLocal creates or updates the built-in datasource local.
	UpsertLocal(ctx context.Context, url string) error
}

// Errors of the registry; the hub maps them to problem codes.
var (
	ErrNotFound        = errors.New("datasource not found")
	ErrVersionConflict = errors.New("datasource version changed")
	ErrInUse           = errors.New("datasource in use")
	ErrBuiltin         = errors.New("the built-in datasource cannot be changed or deleted")
)

// ValidationError lists invalid fields (400).
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string { return "invalid datasource: " + strings.Join(e.Problems, "; ") }

// Registry manages datasources and hands out their HTTP clients (cached per id and version).
type Registry struct {
	Repo   Repo
	Cipher *Cipher
	// Credentials resolves SigV4 credentials; nil = DefaultCredentials.
	Credentials CredentialsFunc
	// Now is the clock; nil = time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cachedClient
}

type cachedClient struct {
	version int
	cfg     Config
	hc      *http.Client
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	headerRE = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	// headers the hub sets itself
	reservedHeaders = map[string]bool{"authorization": true, "host": true, "content-length": true, "content-type": true,
		"x-amz-date": true, "x-amz-security-token": true, "x-amz-content-sha256": true}
	types     = map[string]bool{hubapi.DatasourcePrometheus: true, hubapi.DatasourceVictoriaMetrics: true, hubapi.DatasourceThanos: true, hubapi.DatasourceMimir: true, hubapi.DatasourceAMP: true}
	authTypes = map[string]bool{hubapi.AuthNone: true, hubapi.AuthBearer: true, hubapi.AuthBasic: true, hubapi.AuthSigV4: true, hubapi.AuthMTLS: true}
)

// apply validates in and returns the record and secrets it produces. prev is the stored record on
// update (nil on create) with its decrypted secrets: omitted secret fields keep them unless
// in.ClearSecrets.
func apply(in hubapi.DatasourceInput, prev *Record, prevSecrets Secrets) (Record, Secrets, error) {
	var p []string
	r := Record{Name: in.Name, Type: in.Type, URL: strings.TrimSpace(in.URL), Headers: map[string]string{}, Timeout: in.Timeout, MaxPoints: in.MaxPoints}
	if !nameRE.MatchString(r.Name) {
		p = append(p, "name must be a DNS label (lower case letters, digits, '-'; at most 63)")
	}
	if !types[r.Type] {
		p = append(p, "type must be one of prometheus, victoriametrics, thanos, mimir, amp")
	}
	if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p = append(p, "url must be an absolute http(s) URL")
	} else if u.User != nil {
		p = append(p, "url must not contain credentials; use auth")
	} else if u.RawQuery != "" || u.Fragment != "" {
		p = append(p, "url must not have a query or fragment")
	}
	for k, v := range in.Headers {
		if !headerRE.MatchString(k) {
			p = append(p, fmt.Sprintf("header %q: invalid name", k))
		} else if reservedHeaders[strings.ToLower(k)] {
			p = append(p, fmt.Sprintf("header %q is set by the hub; use auth", k))
		}
		if strings.ContainsAny(v, "\r\n") {
			p = append(p, fmt.Sprintf("header %q: value must be one line", k))
		}
		r.Headers[http.CanonicalHeaderKey(k)] = v
	}
	if r.Timeout != "" {
		if d, err := time.ParseDuration(r.Timeout); err != nil || d < time.Second || d > 5*time.Minute {
			p = append(p, "timeout must be a duration between 1s and 5m")
		}
	}
	if r.MaxPoints != 0 && (r.MaxPoints < 100 || r.MaxPoints > 1_000_000) {
		p = append(p, "maxPoints must be between 100 and 1000000")
	}

	auth := in.Auth
	if auth == nil {
		auth = &hubapi.DatasourceAuthInput{Type: hubapi.AuthNone}
		if prev != nil {
			// auth omitted on update: keep it
			auth = &hubapi.DatasourceAuthInput{Type: prev.AuthType, Username: prev.Auth.Username, SigV4: prev.Auth.SigV4}
			if prev.Auth.CACert != "" || prev.Auth.ClientCert != "" || prev.Auth.InsecureSkipVerify {
				auth.TLS = &hubapi.DatasourceTLSInput{CACert: prev.Auth.CACert, ClientCert: prev.Auth.ClientCert, InsecureSkipVerify: prev.Auth.InsecureSkipVerify}
			}
		}
	}
	r.AuthType = auth.Type
	if r.AuthType == "" {
		r.AuthType = hubapi.AuthNone
	}
	if !authTypes[r.AuthType] {
		p = append(p, "auth.type must be one of none, bearer, basic, sigv4, mtls")
	}
	r.Auth = AuthConfig{Username: auth.Username, SigV4: auth.SigV4}
	if auth.TLS != nil {
		r.Auth.CACert, r.Auth.ClientCert, r.Auth.InsecureSkipVerify = auth.TLS.CACert, auth.TLS.ClientCert, auth.TLS.InsecureSkipVerify
	}

	// Secrets: new values win; omitted ones are kept from prev of the same auth type.
	var sec Secrets
	if prev != nil && !in.ClearSecrets && prev.AuthType == r.AuthType {
		sec = prevSecrets
	}
	if auth.Token != "" {
		sec.Token = auth.Token
	}
	if auth.Password != "" {
		sec.Password = auth.Password
	}
	if auth.TLS != nil && auth.TLS.ClientKey != "" {
		sec.ClientKey = auth.TLS.ClientKey
	}
	// keep only the secret the auth type uses
	switch r.AuthType {
	case hubapi.AuthBearer:
		sec = Secrets{Token: sec.Token}
		if sec.Token == "" {
			p = append(p, "auth.token is required for bearer auth")
		}
	case hubapi.AuthBasic:
		sec = Secrets{Password: sec.Password}
		if r.Auth.Username == "" || sec.Password == "" {
			p = append(p, "auth.username and auth.password are required for basic auth")
		}
	case hubapi.AuthMTLS:
		sec = Secrets{ClientKey: sec.ClientKey}
		if r.Auth.ClientCert == "" || sec.ClientKey == "" {
			p = append(p, "auth.tls.clientCert and auth.tls.clientKey are required for mtls auth")
		}
	case hubapi.AuthSigV4:
		sec = Secrets{}
		if r.Auth.SigV4 == nil || r.Auth.SigV4.Region == "" {
			p = append(p, "auth.sigv4.region is required for sigv4 auth")
		} else if r.Auth.SigV4.Service == "" {
			s := *r.Auth.SigV4
			s.Service = DefaultSigV4Svc
			r.Auth.SigV4 = &s
		}
	default:
		sec = Secrets{}
	}
	if r.AuthType != hubapi.AuthSigV4 {
		r.Auth.SigV4 = nil
	}
	if r.AuthType != hubapi.AuthBasic {
		r.Auth.Username = ""
	}
	if r.AuthType != hubapi.AuthMTLS {
		r.Auth.ClientCert = ""
	}
	if len(p) > 0 {
		return Record{}, Secrets{}, &ValidationError{Problems: p}
	}
	// the TLS settings must load
	if _, err := (Config{AuthType: r.AuthType, Auth: r.Auth, Secrets: sec}).tlsConfig(); err != nil {
		return Record{}, Secrets{}, &ValidationError{Problems: []string{err.Error()}}
	}
	return r, sec, nil
}

// ToAPI returns the API form of a record (never secrets).
func ToAPI(r Record, usedBy []string) hubapi.Datasource {
	created, updated := r.CreatedAt, r.UpdatedAt
	d := hubapi.Datasource{
		ID: r.ID, Name: r.Name, Type: r.Type, URL: r.URL, BuiltIn: r.BuiltIn,
		Auth: hubapi.DatasourceAuthInfo{
			Type: r.AuthType, Username: r.Auth.Username, SigV4: r.Auth.SigV4,
			SecretConfigured: len(r.Secret) > 0, CAConfigured: r.Auth.CACert != "",
			ClientCertConfigured: r.Auth.ClientCert != "", InsecureSkipVerify: r.Auth.InsecureSkipVerify,
		},
		Headers: r.Headers, Timeout: r.Timeout, MaxPoints: r.MaxPoints, UsedBy: usedBy, Version: r.Version,
		CreatedBy: r.CreatedBy, UpdatedBy: r.UpdatedBy,
	}
	if d.Headers == nil {
		d.Headers = map[string]string{}
	}
	if d.UsedBy == nil {
		d.UsedBy = []string{}
	}
	if !created.IsZero() {
		d.CreatedAt = &created
	}
	if !updated.IsZero() {
		d.UpdatedAt = &updated
	}
	return d
}

// EnsureLocal creates or updates the built-in datasource local (hub start-up, prometheusURL).
func (r *Registry) EnsureLocal(ctx context.Context, url string) error {
	if err := r.Repo.UpsertLocal(ctx, strings.TrimRight(url, "/")); err != nil {
		return err
	}
	r.invalidate(hubapi.LocalDatasourceID)
	return nil
}

// List returns every live datasource with the clusters using it, by name.
func (r *Registry) List(ctx context.Context) ([]hubapi.Datasource, error) {
	recs, err := r.Repo.ListDatasources(ctx)
	if err != nil {
		return nil, err
	}
	used, err := r.Repo.UsedBy(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]hubapi.Datasource, 0, len(recs))
	for _, rec := range recs {
		out = append(out, ToAPI(rec, used[rec.ID]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one datasource.
func (r *Registry) Get(ctx context.Context, id string) (hubapi.Datasource, error) {
	rec, err := r.Repo.GetDatasource(ctx, id)
	if err != nil {
		return hubapi.Datasource{}, err
	}
	used, err := r.Repo.UsedBy(ctx)
	if err != nil {
		return hubapi.Datasource{}, err
	}
	return ToAPI(rec, used[id]), nil
}

// Create validates and stores a new datasource.
func (r *Registry) Create(ctx context.Context, in hubapi.DatasourceInput, actor hubapi.Actor) (hubapi.Datasource, error) {
	rec, sec, err := apply(in, nil, Secrets{})
	if err != nil {
		return hubapi.Datasource{}, err
	}
	rec.ID = uuid.NewString()
	rec.Secret, err = r.Cipher.Seal(rec.ID, sec)
	if err != nil {
		return hubapi.Datasource{}, err
	}
	now := r.now()
	rec.Version, rec.CreatedAt, rec.UpdatedAt, rec.CreatedBy, rec.UpdatedBy = 1, now, now, &actor, &actor
	if err := r.Repo.InsertDatasource(ctx, rec); err != nil {
		return hubapi.Datasource{}, err
	}
	return ToAPI(rec, nil), nil
}

// Update replaces a datasource if its version is still ifMatch. It returns the stored datasource
// before and after the change (for the config audit).
func (r *Registry) Update(ctx context.Context, id string, ifMatch int, in hubapi.DatasourceInput, actor hubapi.Actor) (before, after hubapi.Datasource, err error) {
	prev, err := r.Repo.GetDatasource(ctx, id)
	if err != nil {
		return before, after, err
	}
	if prev.BuiltIn {
		return before, after, ErrBuiltin
	}
	if prev.Version != ifMatch {
		return before, after, ErrVersionConflict
	}
	prevSec, err := r.Cipher.Open(id, prev.Secret)
	if err != nil {
		return before, after, err
	}
	rec, sec, err := apply(in, &prev, prevSec)
	if err != nil {
		return before, after, err
	}
	rec.ID, rec.Version, rec.CreatedAt, rec.CreatedBy = id, prev.Version+1, prev.CreatedAt, prev.CreatedBy
	rec.UpdatedAt, rec.UpdatedBy = r.now(), &actor
	if rec.Secret, err = r.Cipher.Seal(id, sec); err != nil {
		return before, after, err
	}
	if err := r.Repo.UpdateDatasource(ctx, rec); err != nil {
		return before, after, err
	}
	r.invalidate(id)
	used, err := r.Repo.UsedBy(ctx)
	if err != nil {
		return before, after, err
	}
	return ToAPI(prev, used[id]), ToAPI(rec, used[id]), nil
}

// Delete soft-deletes a datasource at version ifMatch; it returns the deleted datasource.
func (r *Registry) Delete(ctx context.Context, id string, ifMatch int, actor hubapi.Actor) (hubapi.Datasource, error) {
	prev, err := r.Repo.GetDatasource(ctx, id)
	if err != nil {
		return hubapi.Datasource{}, err
	}
	if prev.BuiltIn {
		return hubapi.Datasource{}, ErrBuiltin
	}
	if prev.Version != ifMatch {
		return hubapi.Datasource{}, ErrVersionConflict
	}
	used, err := r.Repo.UsedBy(ctx)
	if err != nil {
		return hubapi.Datasource{}, err
	}
	if len(used[id]) > 0 {
		return hubapi.Datasource{}, fmt.Errorf("%w by %s", ErrInUse, strings.Join(used[id], ", "))
	}
	if err := r.Repo.DeleteDatasource(ctx, id, ifMatch, actor); err != nil {
		return hubapi.Datasource{}, err
	}
	r.invalidate(id)
	return ToAPI(prev, nil), nil
}

// Config returns the resolved datasource id with decrypted secrets.
func (r *Registry) Config(ctx context.Context, id string) (Config, error) {
	rec, err := r.Repo.GetDatasource(ctx, id)
	if err != nil {
		return Config{}, err
	}
	return r.config(rec)
}

func (r *Registry) config(rec Record) (Config, error) {
	sec, err := r.Cipher.Open(rec.ID, rec.Secret)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{ID: rec.ID, Name: rec.Name, Type: rec.Type, URL: strings.TrimRight(rec.URL, "/"), AuthType: rec.AuthType,
		Auth: rec.Auth, Secrets: sec, Headers: rec.Headers, MaxPoints: rec.MaxPoints}
	if rec.Timeout != "" {
		cfg.Timeout, _ = time.ParseDuration(rec.Timeout)
	}
	if cfg.MaxPoints == 0 {
		cfg.MaxPoints = DefaultMaxPoints
	}
	return cfg, nil
}

// Client returns the datasource's config and HTTP client, cached until the datasource changes.
func (r *Registry) Client(ctx context.Context, id string) (Config, *http.Client, error) {
	rec, err := r.Repo.GetDatasource(ctx, id)
	if err != nil {
		return Config{}, nil, err
	}
	r.mu.Lock()
	c, ok := r.cache[id]
	r.mu.Unlock()
	if ok && c.version == rec.Version && c.cfg.URL == strings.TrimRight(rec.URL, "/") {
		return c.cfg, c.hc, nil
	}
	cfg, err := r.config(rec)
	if err != nil {
		return Config{}, nil, err
	}
	hc, err := cfg.HTTPClient(ctx, r.Credentials)
	if err != nil {
		return Config{}, nil, err
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cachedClient{}
	}
	r.cache[id] = cachedClient{version: rec.Version, cfg: cfg, hc: hc}
	r.mu.Unlock()
	return cfg, hc, nil
}

func (r *Registry) invalidate(id string) {
	r.mu.Lock()
	delete(r.cache, id)
	r.mu.Unlock()
}
