package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// ---- hub-managed policies (D-022) ----

const policyColumns = `id, type, name, description, clusters, workload_cluster, workload_namespace, workload_kind, workload_name,
	priority, spec, version, created_at, created_by, updated_at, updated_by, deleted_at`

func scanPolicy(r pgx.Row) (hubapi.AdminPolicy, error) {
	var (
		p                 hubapi.AdminPolicy
		id                uuid.UUID
		wc, wn, wk, wname *string
		spec, cby, uby    []byte
	)
	err := r.Scan(&id, &p.Type, &p.Name, &p.Description, &p.Clusters, &wc, &wn, &wk, &wname, &p.Priority, &spec, &p.Version,
		&p.CreatedAt, &cby, &p.UpdatedAt, &uby, &p.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.ID = id.String()
	if wc != nil && wn != nil && wk != nil && wname != nil {
		p.WorkloadID = hubapi.WorkloadKey{Cluster: *wc, Namespace: *wn, Kind: *wk, Name: *wname}.ID()
	}
	if p.Clusters == nil {
		p.Clusters = []string{}
	}
	if err := json.Unmarshal(spec, &p.Spec); err != nil {
		return p, err
	}
	_ = json.Unmarshal(cby, &p.CreatedBy)
	_ = json.Unmarshal(uby, &p.UpdatedBy)
	p.CreatedAt, p.UpdatedAt, p.DeletedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC(), utc(p.DeletedAt)
	return p, nil
}

func collectPolicies(rows pgx.Rows, err error) ([]hubapi.AdminPolicy, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hubapi.AdminPolicy{}
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PolicyFilter restricts ListPolicies.
type PolicyFilter struct {
	// Cluster: policies sent to this cluster (global defaults, its cluster default, policies for
	// all clusters or listing it, its workloads' overrides).
	Cluster        string
	Type           string
	IncludeDeleted bool
}

// policyTypeOrder sorts defaults first, then policies by priority, overrides last.
const policyOrder = ` ORDER BY CASE type WHEN 'global-default' THEN 0 WHEN 'cluster-default' THEN 1 WHEN 'policy' THEN 2 ELSE 3 END,
	priority DESC, name, id`

// ListPolicies lists hub policies.
func (s *Store) ListPolicies(ctx context.Context, f PolicyFilter) ([]hubapi.AdminPolicy, error) {
	return collectPolicies(s.Pool.Query(ctx, `SELECT `+policyColumns+` FROM policies
		WHERE ($1 OR deleted_at IS NULL) AND ($2 = '' OR type = $2)
		AND ($3 = '' OR type = 'global-default' OR cardinality(clusters) = 0 OR $3 = ANY(clusters))`+policyOrder,
		f.IncludeDeleted, f.Type, f.Cluster))
}

// GetPolicy returns a policy, deleted ones included.
func (s *Store) GetPolicy(ctx context.Context, id string) (hubapi.AdminPolicy, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return hubapi.AdminPolicy{}, ErrNotFound
	}
	return scanPolicy(s.Pool.QueryRow(ctx, `SELECT `+policyColumns+` FROM policies WHERE id = $1`, u))
}

// GetOverride returns the live workload override of a workload.
func (s *Store) GetOverride(ctx context.Context, k hubapi.WorkloadKey) (hubapi.AdminPolicy, error) {
	return scanPolicy(s.Pool.QueryRow(ctx, `SELECT `+policyColumns+` FROM policies WHERE type = 'workload-override' AND deleted_at IS NULL
		AND workload_cluster = $1 AND workload_namespace = $2 AND workload_kind = $3 AND workload_name = $4`, k.Cluster, k.Namespace, k.Kind, k.Name))
}

// policyWrite is one policy write: the version row and its audit.
type policyWrite struct {
	op           string
	revertedFrom int
	deleted      bool
}

func insertPolicyVersion(ctx context.Context, tx pgx.Tx, p hubapi.AdminPolicy, w policyWrite, c Change) error {
	spec, _ := json.Marshal(p.Spec)
	actor, _ := json.Marshal(c.Actor)
	var rf *int
	if w.op == hubapi.PolicyOpRevert {
		rf = &w.revertedFrom
	}
	_, err := tx.Exec(ctx, `INSERT INTO policy_versions (policy_id, version, operation, reverted_from, name, description, clusters, priority,
			spec, deleted, created_at, actor, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		uuid.MustParse(p.ID), p.Version, w.op, rf, p.Name, p.Description, p.Clusters, p.Priority, spec, w.deleted, c.Time, actor, c.Reason)
	return err
}

func policyTarget(p hubapi.AdminPolicy) (hubapi.ConfigTarget, *hubapi.WorkloadKey) {
	t := hubapi.ConfigTarget{Type: hubapi.TargetPolicy, ID: p.ID, Name: p.Name}
	if p.Type == v1alpha1.HubPolicyTypeClusterDefault && len(p.Clusters) == 1 {
		t.Cluster = p.Clusters[0]
	}
	if p.Type == v1alpha1.HubPolicyTypeWorkloadOverride {
		t.Type = hubapi.TargetWorkloadOverride
		if k, err := hubapi.ParseWorkloadID(p.WorkloadID); err == nil {
			t.Cluster, t.WorkloadID = k.Cluster, p.WorkloadID
			return t, &k
		}
	}
	return t, nil
}

func clustersOrEmpty(c []string) []string {
	if c == nil {
		return []string{}
	}
	return c
}

// CreatePolicy stores a new policy (version 1, operation create) and records c. p.ID, version and
// timestamps are set here. A live policy of the same identity gives ErrAlreadyExists.
func (s *Store) CreatePolicy(ctx context.Context, p hubapi.AdminPolicy, c Change) (hubapi.AdminPolicy, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return p, err
	}
	var out hubapi.AdminPolicy
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		spec, _ := json.Marshal(p.Spec)
		actor, _ := json.Marshal(c.Actor)
		var wc, wn, wk, wname *string
		if p.WorkloadID != "" {
			k, err := hubapi.ParseWorkloadID(p.WorkloadID)
			if err != nil {
				return err
			}
			wc, wn, wk, wname = &k.Cluster, &k.Namespace, &k.Kind, &k.Name
		}
		var err error
		out, err = scanPolicy(tx.QueryRow(ctx, `INSERT INTO policies (id, type, name, description, clusters, workload_cluster, workload_namespace,
				workload_kind, workload_name, priority, spec, version, created_at, created_by, updated_at, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 1, $12, $13, $12, $13) RETURNING `+policyColumns,
			id, p.Type, p.Name, p.Description, clustersOrEmpty(p.Clusters), wc, wn, wk, wname, p.Priority, spec, c.Time, actor))
		if IsUniqueViolation(err) {
			return ErrAlreadyExists
		}
		if err != nil {
			return err
		}
		if err := insertPolicyVersion(ctx, tx, out, policyWrite{op: hubapi.PolicyOpCreate}, c); err != nil {
			return err
		}
		c.Target, c.Workload = policyTarget(out)
		c.Version, c.After = out.Version, out
		return recordChange(ctx, tx, c)
	})
	return out, err
}

// PolicyMutation changes a policy in UpdatePolicy. It returns the operation (update, delete,
// revert), whether the result is deleted and, for a revert, the source version.
type PolicyMutation func(cur hubapi.AdminPolicy, version func(int) (hubapi.PolicyVersion, error)) (next hubapi.AdminPolicy, op string, revertedFrom int, err error)

// UpdatePolicy changes a policy at version ifVersion (deleted ones only through a revert) and
// records the new version and c.
func (s *Store) UpdatePolicy(ctx context.Context, id string, ifVersion int, mut PolicyMutation, c Change) (hubapi.AdminPolicy, error) {
	pid, err := uuid.Parse(id)
	if err != nil {
		return hubapi.AdminPolicy{}, ErrNotFound
	}
	var out hubapi.AdminPolicy
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanPolicy(tx.QueryRow(ctx, `SELECT `+policyColumns+` FROM policies WHERE id = $1 FOR UPDATE`, pid))
		if err != nil {
			return err
		}
		if cur.Version != ifVersion {
			return ErrVersionConflict
		}
		next, op, rf, err := mut(cur, func(v int) (hubapi.PolicyVersion, error) { return policyVersion(ctx, tx, pid, v) })
		if err != nil {
			return err
		}
		deleted := op == hubapi.PolicyOpDelete
		spec, _ := json.Marshal(next.Spec)
		actor, _ := json.Marshal(c.Actor)
		var delAt *time.Time
		if deleted {
			delAt = &c.Time
		}
		out, err = scanPolicy(tx.QueryRow(ctx, `UPDATE policies SET name = $2, description = $3, clusters = $4, priority = $5, spec = $6,
				version = version + 1, updated_at = $7, updated_by = $8, deleted_at = $9
			WHERE id = $1 RETURNING `+policyColumns, pid, next.Name, next.Description, clustersOrEmpty(next.Clusters), next.Priority, spec, c.Time, actor, delAt))
		if IsUniqueViolation(err) {
			return ErrAlreadyExists
		}
		if err != nil {
			return err
		}
		if err := insertPolicyVersion(ctx, tx, out, policyWrite{op: op, revertedFrom: rf, deleted: deleted}, c); err != nil {
			return err
		}
		c.Target, c.Workload = policyTarget(out)
		c.Version, c.Before, c.After = out.Version, cur, out
		return recordChange(ctx, tx, c)
	})
	return out, err
}

const policyVersionColumns = `version, operation, coalesce(reverted_from, 0), name, description, clusters, priority, spec, deleted, created_at, actor, reason`

func scanPolicyVersion(r pgx.Row) (hubapi.PolicyVersion, error) {
	var v hubapi.PolicyVersion
	var spec, actor []byte
	err := r.Scan(&v.Version, &v.Operation, &v.RevertedFrom, &v.Name, &v.Description, &v.Clusters, &v.Priority, &spec, &v.Deleted,
		&v.CreatedAt, &actor, &v.Reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if v.Clusters == nil {
		v.Clusters = []string{}
	}
	v.CreatedAt = v.CreatedAt.UTC()
	_ = json.Unmarshal(actor, &v.Actor)
	return v, json.Unmarshal(spec, &v.Spec)
}

func policyVersion(ctx context.Context, q pgx.Tx, id uuid.UUID, version int) (hubapi.PolicyVersion, error) {
	return scanPolicyVersion(q.QueryRow(ctx, `SELECT `+policyVersionColumns+` FROM policy_versions WHERE policy_id = $1 AND version = $2`, id, version))
}

// PolicyVersions returns versions of a policy below before (0 = from the newest), newest first.
func (s *Store) PolicyVersions(ctx context.Context, id string, before, limit int) ([]hubapi.PolicyVersion, bool, error) {
	pid, err := uuid.Parse(id)
	if err != nil {
		return nil, false, ErrNotFound
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+policyVersionColumns+` FROM policy_versions WHERE policy_id = $1 AND ($2 = 0 OR version < $2)
		ORDER BY version DESC LIMIT $3`, pid, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []hubapi.PolicyVersion{}
	for rows.Next() {
		v, err := scanPolicyVersion(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, v)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, rows.Err()
}

// ---- settings ----

// SettingsDoc is a stored settings section; Version 0 means none is stored.
type SettingsDoc struct {
	Version   int
	Value     json.RawMessage
	UpdatedAt *time.Time
	UpdatedBy *hubapi.Actor
	Reason    string
}

// GetSettings returns a section (Version 0 when none is stored).
func (s *Store) GetSettings(ctx context.Context, section string) (SettingsDoc, error) {
	var d SettingsDoc
	var by []byte
	var at time.Time
	err := s.Pool.QueryRow(ctx, `SELECT version, value, updated_at, updated_by, reason FROM settings WHERE section = $1`, section).
		Scan(&d.Version, &d.Value, &at, &by, &d.Reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return SettingsDoc{}, nil
	}
	if err != nil {
		return d, err
	}
	at = at.UTC()
	d.UpdatedAt = &at
	var a hubapi.Actor
	if json.Unmarshal(by, &a) == nil {
		d.UpdatedBy = &a
	}
	return d, nil
}

// PutSettings stores a new version of a section if the current one is ifVersion (0: none) and
// records c (Before = the previous value, After = the new one). It returns the new version.
func (s *Store) PutSettings(ctx context.Context, section string, ifVersion int, value any, before any, c Change) (int, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	actor, _ := json.Marshal(c.Actor)
	next := ifVersion + 1
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var cur int
		err := tx.QueryRow(ctx, `SELECT version FROM settings WHERE section = $1 FOR UPDATE`, section).Scan(&cur)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if cur != ifVersion {
			return ErrVersionConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO settings (section, version, value, updated_at, updated_by, reason) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (section) DO UPDATE SET version = excluded.version, value = excluded.value, updated_at = excluded.updated_at,
				updated_by = excluded.updated_by, reason = excluded.reason`, section, next, b, c.Time, actor, c.Reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO settings_versions (section, version, value, created_at, actor, reason) VALUES ($1, $2, $3, $4, $5, $6)`,
			section, next, b, c.Time, actor, c.Reason); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetSettings, ID: section, Name: section}
		c.Version, c.Before, c.After = next, before, value
		return recordChange(ctx, tx, c)
	})
	if IsUniqueViolation(err) {
		return 0, ErrVersionConflict
	}
	return next, err
}

// SettingsVersions lists a section's versions below before (0 = from the newest), newest first.
func (s *Store) SettingsVersions(ctx context.Context, section string, before, limit int) ([]hubapi.SettingsVersion, bool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT version, value, created_at, actor, reason FROM settings_versions
		WHERE section = $1 AND ($2 = 0 OR version < $2) ORDER BY version DESC LIMIT $3`, section, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []hubapi.SettingsVersion{}
	for rows.Next() {
		v := hubapi.SettingsVersion{Section: section}
		var value, actor []byte
		if err := rows.Scan(&v.Version, &value, &v.CreatedAt, &actor, &v.Reason); err != nil {
			return nil, false, err
		}
		v.Value, v.CreatedAt = json.RawMessage(value), v.CreatedAt.UTC()
		_ = json.Unmarshal(actor, &v.Actor)
		out = append(out, v)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, rows.Err()
}
