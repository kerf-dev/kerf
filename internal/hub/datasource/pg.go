package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// DB is the part of pgxpool.Pool the repo uses.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PG is the Postgres Repo (tables datasources and cluster_metrics_bindings, migration 0010).
type PG struct{ DB DB }

const dsColumns = `id, name, type, url, auth_type, auth_config, auth_secret, headers, coalesce(timeout, ''),
	coalesce(max_points, 0), built_in, version, created_at, created_by, updated_at, updated_by`

func scanRecord(row pgx.Row) (Record, error) {
	var r Record
	var authCfg, headers, createdBy, updatedBy []byte
	err := row.Scan(&r.ID, &r.Name, &r.Type, &r.URL, &r.AuthType, &authCfg, &r.Secret, &headers, &r.Timeout,
		&r.MaxPoints, &r.BuiltIn, &r.Version, &r.CreatedAt, &createdBy, &r.UpdatedAt, &updatedBy)
	if err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal(authCfg, &r.Auth); err != nil {
		return Record{}, fmt.Errorf("datasource %s auth_config: %w", r.ID, err)
	}
	if err := json.Unmarshal(headers, &r.Headers); err != nil {
		return Record{}, fmt.Errorf("datasource %s headers: %w", r.ID, err)
	}
	r.CreatedBy, r.UpdatedBy = actorOf(createdBy), actorOf(updatedBy)
	return r, nil
}

func actorOf(b []byte) *hubapi.Actor {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var a hubapi.Actor
	if json.Unmarshal(b, &a) != nil {
		return nil
	}
	return &a
}

// ListDatasources implements Repo.
func (p *PG) ListDatasources(ctx context.Context) ([]Record, error) {
	rows, err := p.DB.Query(ctx, `SELECT `+dsColumns+` FROM datasources WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetDatasource implements Repo.
func (p *PG) GetDatasource(ctx context.Context, id string) (Record, error) {
	r, err := scanRecord(p.DB.QueryRow(ctx, `SELECT `+dsColumns+` FROM datasources WHERE id = $1 AND deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return r, err
}

func recordArgs(r Record) (authCfg, headers []byte, timeout, maxPoints any, err error) {
	if authCfg, err = json.Marshal(r.Auth); err != nil {
		return
	}
	h := r.Headers
	if h == nil {
		h = map[string]string{}
	}
	if headers, err = json.Marshal(h); err != nil {
		return
	}
	if r.Timeout != "" {
		timeout = r.Timeout
	}
	if r.MaxPoints != 0 {
		maxPoints = r.MaxPoints
	}
	return
}

// InsertDatasource implements Repo.
func (p *PG) InsertDatasource(ctx context.Context, r Record) error {
	authCfg, headers, timeout, maxPoints, err := recordArgs(r)
	if err != nil {
		return err
	}
	_, err = p.DB.Exec(ctx, `INSERT INTO datasources (id, name, type, url, auth_type, auth_config, auth_secret, headers, timeout,
		max_points, version, created_at, created_by, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		r.ID, r.Name, r.Type, r.URL, r.AuthType, authCfg, r.Secret, headers, timeout, maxPoints, r.Version,
		r.CreatedAt, r.CreatedBy, r.UpdatedAt, r.UpdatedBy)
	return nameConflict(err)
}

// nameConflict maps the unique live-name index to a ValidationError.
func nameConflict(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "datasources_name_live_idx" {
		return &ValidationError{Problems: []string{"name is already used by another datasource"}}
	}
	return err
}

// UpdateDatasource implements Repo.
func (p *PG) UpdateDatasource(ctx context.Context, r Record) error {
	authCfg, headers, timeout, maxPoints, err := recordArgs(r)
	if err != nil {
		return err
	}
	tag, err := p.DB.Exec(ctx, `UPDATE datasources SET name = $2, type = $3, url = $4, auth_type = $5, auth_config = $6,
		auth_secret = $7, headers = $8, timeout = $9, max_points = $10, version = $11, updated_at = $12, updated_by = $13
		WHERE id = $1 AND version = $11 - 1 AND deleted_at IS NULL AND NOT built_in`,
		r.ID, r.Name, r.Type, r.URL, r.AuthType, authCfg, r.Secret, headers, timeout, maxPoints, r.Version, r.UpdatedAt, r.UpdatedBy)
	if err != nil {
		return nameConflict(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

// DeleteDatasource implements Repo. It refuses while a cluster is bound to the datasource.
func (p *PG) DeleteDatasource(ctx context.Context, id string, version int, actor hubapi.Actor) error {
	tag, err := p.DB.Exec(ctx, `UPDATE datasources SET deleted_at = now(), updated_at = now(), updated_by = $3, auth_secret = NULL
		WHERE id = $1 AND version = $2 AND deleted_at IS NULL AND NOT built_in
		  AND NOT EXISTS (SELECT 1 FROM cluster_metrics_bindings WHERE datasource_id = $1)`, id, version, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var bound bool
		if err := p.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cluster_metrics_bindings WHERE datasource_id = $1)`, id).Scan(&bound); err != nil {
			return err
		}
		if bound {
			return ErrInUse
		}
		return ErrVersionConflict
	}
	return nil
}

// UsedBy implements Repo.
func (p *PG) UsedBy(ctx context.Context) (map[string][]string, error) {
	rows, err := p.DB.Query(ctx, `SELECT coalesce(b.datasource_id, 'local'), c.name
		FROM clusters c LEFT JOIN cluster_metrics_bindings b ON b.cluster = c.name ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, cluster string
		if err := rows.Scan(&id, &cluster); err != nil {
			return nil, err
		}
		out[id] = append(out[id], cluster)
	}
	return out, rows.Err()
}

// UpsertLocal implements Repo.
func (p *PG) UpsertLocal(ctx context.Context, url string) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO datasources (id, name, type, url, built_in) VALUES ('local', 'local', 'victoriametrics', $1, true)
		ON CONFLICT (id) DO UPDATE SET url = EXCLUDED.url, deleted_at = NULL,
			version = datasources.version + CASE WHEN datasources.url = EXCLUDED.url THEN 0 ELSE 1 END,
			updated_at = CASE WHEN datasources.url = EXCLUDED.url THEN datasources.updated_at ELSE now() END`, url)
	return err
}

// Binding returns the stored binding of cluster; ok is false without a row (default binding).
func (p *PG) Binding(ctx context.Context, cluster string) (b hubapi.MetricsBinding, ok bool, err error) {
	var value *string
	var jobs []byte
	err = p.DB.QueryRow(ctx, `SELECT datasource_id, cluster_label, cluster_label_value, extra_matchers, job_names
		FROM cluster_metrics_bindings WHERE cluster = $1`, cluster).Scan(&b.DatasourceID, &b.ClusterLabel, &value, &b.ExtraMatchers, &jobs)
	if errors.Is(err, pgx.ErrNoRows) {
		return hubapi.DefaultMetricsBinding(cluster), false, nil
	}
	if err != nil {
		return b, false, err
	}
	if value != nil {
		b.ClusterLabelValue = *value
	}
	b.JobNames = jobNamesOf(jobs)
	return b, true, nil
}

// Bindings returns every stored binding by cluster (clusters without a row are absent).
func (p *PG) Bindings(ctx context.Context) (map[string]hubapi.MetricsBinding, error) {
	rows, err := p.DB.Query(ctx, `SELECT cluster, datasource_id, cluster_label, cluster_label_value, extra_matchers, job_names
		FROM cluster_metrics_bindings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]hubapi.MetricsBinding{}
	for rows.Next() {
		var cluster string
		var b hubapi.MetricsBinding
		var value *string
		var jobs []byte
		if err := rows.Scan(&cluster, &b.DatasourceID, &b.ClusterLabel, &value, &b.ExtraMatchers, &jobs); err != nil {
			return nil, err
		}
		if value != nil {
			b.ClusterLabelValue = *value
		}
		b.JobNames = jobNamesOf(jobs)
		out[cluster] = b
	}
	return out, rows.Err()
}

func jobNamesOf(b []byte) *hubapi.MetricsJobNames {
	var j hubapi.MetricsJobNames
	if len(b) == 0 || json.Unmarshal(b, &j) != nil || (len(j.CAdvisor) == 0 && len(j.KubeStateMetrics) == 0) {
		return nil
	}
	return &j
}

// PutBinding validates and stores the binding of cluster. The datasource must exist. A binding
// equal to the default (local, label cluster = the name, nothing else) removes the row.
func (p *PG) PutBinding(ctx context.Context, cluster string, b hubapi.MetricsBinding, actor hubapi.Actor) error {
	if err := ValidateBinding(b); err != nil {
		return &ValidationError{Problems: []string{err.Error()}}
	}
	if b.DatasourceID == "" {
		b.DatasourceID = hubapi.LocalDatasourceID
	}
	if _, err := p.GetDatasource(ctx, b.DatasourceID); errors.Is(err, ErrNotFound) {
		return &ValidationError{Problems: []string{fmt.Sprintf("metrics.datasourceId %q does not exist", b.DatasourceID)}}
	} else if err != nil {
		return err
	}
	if IsDefaultBinding(cluster, b) {
		_, err := p.DB.Exec(ctx, `DELETE FROM cluster_metrics_bindings WHERE cluster = $1`, cluster)
		return err
	}
	var value any
	if b.ClusterLabelValue != "" && b.ClusterLabelValue != cluster {
		value = b.ClusterLabelValue
	}
	extra := make([]string, 0, len(b.ExtraMatchers))
	for _, m := range b.ExtraMatchers {
		n, _ := NormalizeMatcher(m) // validated above
		extra = append(extra, n)
	}
	jobs := []byte("{}")
	if b.JobNames != nil {
		var err error
		if jobs, err = json.Marshal(b.JobNames); err != nil {
			return err
		}
	}
	_, err := p.DB.Exec(ctx, `INSERT INTO cluster_metrics_bindings (cluster, datasource_id, cluster_label, cluster_label_value, extra_matchers, job_names, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, now(), $7)
		ON CONFLICT (cluster) DO UPDATE SET datasource_id = EXCLUDED.datasource_id, cluster_label = EXCLUDED.cluster_label,
			cluster_label_value = EXCLUDED.cluster_label_value, extra_matchers = EXCLUDED.extra_matchers,
			job_names = EXCLUDED.job_names, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		cluster, b.DatasourceID, b.ClusterLabel, value, extra, jobs, actor)
	return err
}

// IsDefaultBinding reports whether b is the behaviour of a cluster without a binding.
func IsDefaultBinding(cluster string, b hubapi.MetricsBinding) bool {
	return (b.DatasourceID == "" || b.DatasourceID == hubapi.LocalDatasourceID) && b.ClusterLabel == ClusterLabel &&
		(b.ClusterLabelValue == "" || b.ClusterLabelValue == cluster) && len(b.ExtraMatchers) == 0 && b.JobNames == nil
}
