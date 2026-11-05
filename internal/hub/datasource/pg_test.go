package datasource_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// testDB returns a fresh migrated database when KERF_TEST_DATABASE_URL is set (skipped otherwise).
func testDB(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("KERF_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("KERF_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "kerf_ds_test_" + hex.EncodeToString(b)
	adminConn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminConn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminConn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		adminConn.Close(context.Background())
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Migrate(ctx, os.DirFS("../../../db/migrations")); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestPG(t *testing.T) {
	st := testDB(t)
	ctx := context.Background()
	for _, c := range []string{"ro-spoke-1", "ro-spoke-2"} {
		if _, err := st.Pool.Exec(ctx, `INSERT INTO clusters (name) VALUES ($1)`, c); err != nil {
			t.Fatal(err)
		}
	}
	pg := &datasource.PG{DB: st.Pool}
	k := make([]byte, datasource.KeySize)
	_, _ = rand.Read(k)
	c, _ := datasource.NewCipher(k)
	reg := &datasource.Registry{Repo: pg, Cipher: c}
	actor := hubapi.Actor{Type: "user", Name: "alice", Role: "admin"}

	if err := reg.EnsureLocal(ctx, "http://vm:8428"); err != nil {
		t.Fatal(err)
	}
	if err := reg.EnsureLocal(ctx, "http://vm:8428"); err != nil {
		t.Fatal(err)
	}
	local, err := reg.Get(ctx, "local")
	if err != nil || !local.BuiltIn || local.Version != 1 || strings.Join(local.UsedBy, ",") != "ro-spoke-1,ro-spoke-2" {
		t.Fatalf("local %v %+v", err, local)
	}
	_ = reg.EnsureLocal(ctx, "http://vm2:8428")
	if local, _ = reg.Get(ctx, "local"); local.Version != 2 || local.URL != "http://vm2:8428" {
		t.Errorf("local after URL change %+v", local)
	}

	d, err := reg.Create(ctx, hubapi.DatasourceInput{Name: "mimir", Type: "mimir", URL: "https://mimir/prometheus",
		Auth:    &hubapi.DatasourceAuthInput{Type: "basic", Username: "u", Password: "pw"},
		Headers: map[string]string{"X-Scope-OrgID": "t1"}, MaxPoints: 5000, Timeout: "15s"}, actor)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := st.Pool.QueryRow(ctx, `SELECT auth_secret FROM datasources WHERE id = $1`, d.ID).Scan(&raw); err != nil || len(raw) == 0 || strings.Contains(string(raw), "pw") {
		t.Fatalf("stored secret %v %q", err, raw)
	}
	got, err := reg.Get(ctx, d.ID)
	if err != nil || got.Auth.Username != "u" || !got.Auth.SecretConfigured || got.Headers["X-Scope-Orgid"] != "t1" || got.MaxPoints != 5000 || got.Timeout != "15s" || got.CreatedBy.Name != "alice" {
		t.Fatalf("get %v %+v", err, got)
	}
	cfg, err := reg.Config(ctx, d.ID)
	if err != nil || cfg.Secrets.Password != "pw" {
		t.Fatalf("config %v", err)
	}
	if _, err := reg.Create(ctx, hubapi.DatasourceInput{Name: "mimir", Type: "prometheus", URL: "http://p"}, actor); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("duplicate name: %v", err)
	}
	if _, _, err := reg.Update(ctx, d.ID, 1, hubapi.DatasourceInput{Name: "mimir", Type: "mimir", URL: "https://mimir2/prometheus"}, actor); err != nil {
		t.Fatal(err)
	}
	if err := pg.UpdateDatasource(ctx, datasource.Record{ID: d.ID, Name: "mimir", Type: "mimir", URL: "http://x", AuthType: "none", Version: 2}); !errors.Is(err, datasource.ErrVersionConflict) {
		t.Errorf("stale repo update: %v", err)
	}

	// bindings
	b := hubapi.MetricsBinding{DatasourceID: d.ID, ClusterLabel: "k8s_cluster", ClusterLabelValue: "prod", ExtraMatchers: []string{` env = "prod"`},
		JobNames: &hubapi.MetricsJobNames{KubeStateMetrics: []string{"ksm"}}}
	if err := pg.PutBinding(ctx, "ro-spoke-2", b, actor); err != nil {
		t.Fatal(err)
	}
	gb, ok, err := pg.Binding(ctx, "ro-spoke-2")
	if err != nil || !ok || gb.DatasourceID != d.ID || gb.ClusterLabel != "k8s_cluster" || gb.ClusterLabelValue != "prod" ||
		strings.Join(gb.ExtraMatchers, ",") != `env="prod"` || gb.JobNames == nil || gb.JobNames.KubeStateMetrics[0] != "ksm" {
		t.Fatalf("binding %v %v %+v", err, ok, gb)
	}
	if gb, ok, _ := pg.Binding(ctx, "ro-spoke-1"); ok || gb.DatasourceID != "local" || gb.ClusterLabelValue != "ro-spoke-1" {
		t.Errorf("default binding %v %+v", ok, gb)
	}
	all, err := pg.Bindings(ctx)
	if err != nil || len(all) != 1 {
		t.Errorf("bindings %v %v", err, all)
	}
	if err := pg.PutBinding(ctx, "ro-spoke-1", hubapi.MetricsBinding{DatasourceID: "nope", ClusterLabel: "cluster"}, actor); err == nil {
		t.Error("unknown datasource accepted")
	}
	if err := pg.PutBinding(ctx, "ro-spoke-1", hubapi.MetricsBinding{ClusterLabel: "a-b"}, actor); err == nil {
		t.Error("bad label accepted")
	}
	used, _ := reg.Get(ctx, d.ID)
	if strings.Join(used.UsedBy, ",") != "ro-spoke-2" {
		t.Errorf("usedBy %v", used.UsedBy)
	}
	if _, err := reg.Delete(ctx, d.ID, 2, actor); !errors.Is(err, datasource.ErrInUse) {
		t.Errorf("delete in use: %v", err)
	}
	if err := pg.DeleteDatasource(ctx, d.ID, 2, actor); !errors.Is(err, datasource.ErrInUse) {
		t.Errorf("repo delete in use: %v", err)
	}
	// back to the default removes the row
	if err := pg.PutBinding(ctx, "ro-spoke-2", hubapi.MetricsBinding{ClusterLabel: "cluster", ClusterLabelValue: "ro-spoke-2"}, actor); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := pg.Binding(ctx, "ro-spoke-2"); ok {
		t.Error("default binding kept a row")
	}
	if err := pg.DeleteDatasource(ctx, d.ID, 1, actor); !errors.Is(err, datasource.ErrVersionConflict) {
		t.Errorf("stale delete: %v", err)
	}
	if _, err := reg.Delete(ctx, d.ID, 2, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Get(ctx, d.ID); !errors.Is(err, datasource.ErrNotFound) {
		t.Errorf("deleted: %v", err)
	}
	if list, _ := reg.List(ctx); len(list) != 1 {
		t.Errorf("list after delete %d", len(list))
	}
	// the name is free again
	if _, err := reg.Create(ctx, hubapi.DatasourceInput{Name: "mimir", Type: "prometheus", URL: "http://p"}, actor); err != nil {
		t.Errorf("reuse name: %v", err)
	}
}
