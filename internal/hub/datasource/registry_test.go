package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// memRepo is an in-memory Repo.
type memRepo struct {
	recs     map[string]Record
	bindings map[string]string // cluster -> datasource
	clusters []string
}

func newMemRepo(clusters ...string) *memRepo {
	return &memRepo{recs: map[string]Record{}, bindings: map[string]string{}, clusters: clusters}
}

func (m *memRepo) ListDatasources(context.Context) ([]Record, error) {
	var out []Record
	for _, r := range m.recs {
		out = append(out, r)
	}
	return out, nil
}

func (m *memRepo) GetDatasource(_ context.Context, id string) (Record, error) {
	r, ok := m.recs[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (m *memRepo) InsertDatasource(_ context.Context, r Record) error {
	for _, o := range m.recs {
		if o.Name == r.Name {
			return &ValidationError{Problems: []string{"name is already used by another datasource"}}
		}
	}
	m.recs[r.ID] = r
	return nil
}

func (m *memRepo) UpdateDatasource(_ context.Context, r Record) error {
	if m.recs[r.ID].Version != r.Version-1 {
		return ErrVersionConflict
	}
	m.recs[r.ID] = r
	return nil
}

func (m *memRepo) DeleteDatasource(_ context.Context, id string, version int, _ hubapi.Actor) error {
	if m.recs[id].Version != version {
		return ErrVersionConflict
	}
	delete(m.recs, id)
	return nil
}

func (m *memRepo) UsedBy(context.Context) (map[string][]string, error) {
	out := map[string][]string{}
	for _, c := range m.clusters {
		id := m.bindings[c]
		if id == "" {
			id = hubapi.LocalDatasourceID
		}
		out[id] = append(out[id], c)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out, nil
}

func (m *memRepo) UpsertLocal(_ context.Context, url string) error {
	r := m.recs[hubapi.LocalDatasourceID]
	if r.Version == 0 {
		r = Record{ID: hubapi.LocalDatasourceID, Name: "local", Type: hubapi.DatasourceVictoriaMetrics, AuthType: hubapi.AuthNone, BuiltIn: true, Version: 1}
	} else if r.URL != url {
		r.Version++
	}
	r.URL = url
	m.recs[r.ID] = r
	return nil
}

func newRegistry(t *testing.T, repo Repo) *Registry {
	t.Helper()
	c, err := NewCipher(key(t))
	if err != nil {
		t.Fatal(err)
	}
	return &Registry{Repo: repo, Cipher: c, Credentials: staticCreds, Now: func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }}
}

var admin = hubapi.Actor{Type: "user", Name: "alice", Role: "admin"}

func TestRegistryCRUD(t *testing.T) {
	ctx := context.Background()
	repo := newMemRepo("ro-spoke-1", "ro-spoke-2")
	reg := newRegistry(t, repo)
	if err := reg.EnsureLocal(ctx, "http://vm:8428/"); err != nil {
		t.Fatal(err)
	}

	d, err := reg.Create(ctx, hubapi.DatasourceInput{Name: "mimir-eu", Type: "mimir", URL: "https://mimir.example/prometheus",
		Auth:    &hubapi.DatasourceAuthInput{Type: "bearer", Token: "s3cret"},
		Headers: map[string]string{"x-scope-orgid": "tenant-1"}, Timeout: "20s", Reason: "r"}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Auth.SecretConfigured || d.Version != 1 || d.Headers["X-Scope-Orgid"] != "tenant-1" || d.CreatedBy.Name != "alice" {
		t.Errorf("created %+v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "s3cret") {
		t.Fatal("API response contains the secret")
	}
	if strings.Contains(string(repo.recs[d.ID].Secret), "s3cret") {
		t.Fatal("secret stored in plain text")
	}
	cfg, err := reg.Config(ctx, d.ID)
	if err != nil || cfg.Secrets.Token != "s3cret" || cfg.Timeout != 20*time.Second || cfg.MaxPoints != DefaultMaxPoints {
		t.Fatalf("config %v %+v", err, cfg)
	}

	// update without auth keeps type and secret
	_, after, err := reg.Update(ctx, d.ID, 1, hubapi.DatasourceInput{Name: "mimir-eu", Type: "mimir", URL: "https://mimir2.example/prometheus", Reason: "r"}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != 2 || after.Auth.Type != "bearer" || !after.Auth.SecretConfigured || len(after.Headers) != 0 {
		t.Errorf("after keep %+v", after)
	}
	if cfg, _ := reg.Config(ctx, d.ID); cfg.Secrets.Token != "s3cret" {
		t.Error("omitted token not kept")
	}
	// stale If-Match
	if _, _, err := reg.Update(ctx, d.ID, 1, hubapi.DatasourceInput{Name: "mimir-eu", Type: "mimir", URL: "https://x"}, admin); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale update: %v", err)
	}
	// new token replaces
	_, _, err = reg.Update(ctx, d.ID, 2, hubapi.DatasourceInput{Name: "mimir-eu", Type: "mimir", URL: "https://x", Auth: &hubapi.DatasourceAuthInput{Type: "bearer", Token: "new"}}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := reg.Config(ctx, d.ID); cfg.Secrets.Token != "new" {
		t.Error("token not replaced")
	}
	// clearSecrets with bearer fails validation (token required)
	_, _, err = reg.Update(ctx, d.ID, 3, hubapi.DatasourceInput{Name: "mimir-eu", Type: "mimir", URL: "https://x", ClearSecrets: true, Auth: &hubapi.DatasourceAuthInput{Type: "bearer"}}, admin)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("clearSecrets on bearer: %v", err)
	}
	// switching to none drops the secret
	_, after, err = reg.Update(ctx, d.ID, 3, hubapi.DatasourceInput{Name: "mimir-eu", Type: "mimir", URL: "https://x", Auth: &hubapi.DatasourceAuthInput{Type: "none"}}, admin)
	if err != nil || after.Auth.SecretConfigured || len(repo.recs[d.ID].Secret) != 0 {
		t.Errorf("auth none: %v %+v", err, after.Auth)
	}

	// local is built in
	if _, _, err := reg.Update(ctx, "local", 1, hubapi.DatasourceInput{Name: "local", Type: "victoriametrics", URL: "http://x"}, admin); !errors.Is(err, ErrBuiltin) {
		t.Errorf("update local: %v", err)
	}
	if _, err := reg.Delete(ctx, "local", 1, admin); !errors.Is(err, ErrBuiltin) {
		t.Errorf("delete local: %v", err)
	}

	list, err := reg.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %d", err, len(list))
	}
	if list[0].Name != "local" || strings.Join(list[0].UsedBy, ",") != "ro-spoke-1,ro-spoke-2" || list[0].URL != "http://vm:8428" {
		t.Errorf("local %+v", list[0])
	}

	// in use
	repo.bindings["ro-spoke-2"] = d.ID
	if _, err := reg.Delete(ctx, d.ID, 4, admin); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "ro-spoke-2") {
		t.Errorf("delete in use: %v", err)
	}
	got, _ := reg.Get(ctx, d.ID)
	if strings.Join(got.UsedBy, ",") != "ro-spoke-2" {
		t.Errorf("usedBy %v", got.UsedBy)
	}
	delete(repo.bindings, "ro-spoke-2")
	if _, err := reg.Delete(ctx, d.ID, 3, admin); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale delete: %v", err)
	}
	if _, err := reg.Delete(ctx, d.ID, 4, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Get(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}

func TestRegistryValidation(t *testing.T) {
	reg := newRegistry(t, newMemRepo())
	for _, tc := range []struct {
		name string
		in   hubapi.DatasourceInput
		want string
	}{
		{"name", hubapi.DatasourceInput{Name: "Bad_Name", Type: "prometheus", URL: "http://p"}, "name must be"},
		{"type", hubapi.DatasourceInput{Name: "a", Type: "influx", URL: "http://p"}, "type must be"},
		{"url", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "p:9090"}, "absolute http(s)"},
		{"url creds", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://u:p@p"}, "credentials"},
		{"url query", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p/?x=1"}, "query"},
		{"auth header", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Headers: map[string]string{"Authorization": "x"}}, "set by the hub"},
		{"header name", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Headers: map[string]string{"a b": "x"}}, "invalid name"},
		{"header crlf", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Headers: map[string]string{"X": "a\r\nB: c"}}, "one line"},
		{"timeout", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Timeout: "10m"}, "timeout"},
		{"maxPoints", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", MaxPoints: 5}, "maxPoints"},
		{"auth type", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Auth: &hubapi.DatasourceAuthInput{Type: "ntlm"}}, "auth.type"},
		{"bearer", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Auth: &hubapi.DatasourceAuthInput{Type: "bearer"}}, "auth.token"},
		{"basic", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Auth: &hubapi.DatasourceAuthInput{Type: "basic", Username: "u"}}, "auth.password"},
		{"mtls", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Auth: &hubapi.DatasourceAuthInput{Type: "mtls"}}, "clientKey"},
		{"mtls pair", hubapi.DatasourceInput{Name: "a", Type: "prometheus", URL: "http://p", Auth: &hubapi.DatasourceAuthInput{Type: "mtls", TLS: &hubapi.DatasourceTLSInput{ClientCert: "x", ClientKey: "y"}}}, "client certificate"},
		{"sigv4", hubapi.DatasourceInput{Name: "a", Type: "amp", URL: "https://aps", Auth: &hubapi.DatasourceAuthInput{Type: "sigv4"}}, "region"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Create(context.Background(), tc.in, admin)
			var ve *ValidationError
			if !errors.As(err, &ve) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// sigv4 defaults the service; unrelated fields are dropped
	d, err := reg.Create(context.Background(), hubapi.DatasourceInput{Name: "amp", Type: "amp", URL: "https://aps-workspaces.eu-west-1.amazonaws.com/workspaces/ws-1/",
		Auth: &hubapi.DatasourceAuthInput{Type: "sigv4", Username: "ignored", Token: "ignored", SigV4: &hubapi.DatasourceSigV4{Region: "eu-west-1"}}}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if d.Auth.SigV4.Service != "aps" || d.Auth.Username != "" || d.Auth.SecretConfigured {
		t.Errorf("sigv4 %+v", d.Auth)
	}
	// duplicate name
	if _, err := reg.Create(context.Background(), hubapi.DatasourceInput{Name: "amp", Type: "prometheus", URL: "http://p"}, admin); err == nil {
		t.Error("duplicate name accepted")
	}
}

func TestRegistryClientCache(t *testing.T) {
	ctx := context.Background()
	var last seen
	srv := promServer(t, &last, http.StatusOK)
	defer srv.Close()
	reg := newRegistry(t, newMemRepo())
	d, err := reg.Create(ctx, hubapi.DatasourceInput{Name: "p", Type: "prometheus", URL: srv.URL, Auth: &hubapi.DatasourceAuthInput{Type: "bearer", Token: "t1"}}, admin)
	if err != nil {
		t.Fatal(err)
	}
	cfg, hc1, err := reg.Client(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, hc2, _ := reg.Client(ctx, d.ID)
	if hc1 != hc2 {
		t.Error("client not cached")
	}
	if r := NewAPI(cfg.URL, hc1).Probe(ctx); !r.OK || last.header.Get("Authorization") != "Bearer t1" {
		t.Errorf("probe %+v %q", r, last.header.Get("Authorization"))
	}
	if _, _, err := reg.Update(ctx, d.ID, 1, hubapi.DatasourceInput{Name: "p", Type: "prometheus", URL: srv.URL, Auth: &hubapi.DatasourceAuthInput{Type: "bearer", Token: "t2"}}, admin); err != nil {
		t.Fatal(err)
	}
	cfg, hc3, _ := reg.Client(ctx, d.ID)
	if hc3 == hc1 {
		t.Error("client not rebuilt after update")
	}
	NewAPI(cfg.URL, hc3).Probe(ctx)
	if last.header.Get("Authorization") != "Bearer t2" {
		t.Errorf("after update %q", last.header.Get("Authorization"))
	}
	if _, _, err := reg.Client(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}
