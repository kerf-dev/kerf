package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Errors of versioned admin writes.
var (
	// ErrVersionConflict: the If-Match version is not the current one.
	ErrVersionConflict = errors.New("version conflict")
	// ErrAlreadyExists: a live object with the same identity exists.
	ErrAlreadyExists = errors.New("already exists")
	// ErrLastAdmin: the change would leave no enabled admin.
	ErrLastAdmin = errors.New("last enabled admin")
)

// Change is the audit record of one configuration write (type config_changed).
type Change struct {
	Actor  hubapi.Actor
	Action string
	Reason string
	Time   time.Time
	// Target: Type and ID are required; Cluster sets the audit row's cluster.
	Target hubapi.ConfigTarget
	// Workload sets the row's workload columns (override and approval targets).
	Workload *hubapi.WorkloadKey
	Version  int
	Before   any
	After    any
	// AfterExtra adds keys to the stored after object (e.g. overrideReadiness).
	AfterExtra map[string]any
}

func jsonOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return nil, nil
	}
	return b, nil
}

// recordChange inserts the config_changed row of c in tx.
func recordChange(ctx context.Context, tx pgx.Tx, c Change) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	actor, _ := json.Marshal(c.Actor)
	before, err := jsonOrNil(c.Before)
	if err != nil {
		return err
	}
	after, err := jsonOrNil(c.After)
	if err != nil {
		return err
	}
	if len(c.AfterExtra) > 0 {
		m := map[string]any{}
		if after != nil {
			if err := json.Unmarshal(after, &m); err != nil {
				return err
			}
		}
		for k, v := range c.AfterExtra {
			m[k] = v
		}
		if after, err = json.Marshal(m); err != nil {
			return err
		}
	}
	var ns, kind, name *string
	cluster := nullStr(c.Target.Cluster)
	if c.Workload != nil {
		cluster, ns, kind, name = &c.Workload.Cluster, &c.Workload.Namespace, &c.Workload.Kind, &c.Workload.Name
	}
	var version *int
	if c.Version > 0 {
		version = &c.Version
	}
	at := c.Time
	if at.IsZero() {
		at = time.Now()
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events (id, type, reason, time, received_at, message, cluster, namespace, workload_kind, workload_name,
			workload_id, actor, target_type, target_id, target_name, version, before, after)
		VALUES ($1, 'config_changed', $2, $3, $3, $4, $5, $6, $7, $8,
			(SELECT id FROM workloads WHERE cluster = $5 AND namespace = $6 AND kind = $7 AND name = $8), $9, $10, $11, $12, $13, $14, $15)`,
		id, c.Action, at, c.Reason, cluster, ns, kind, name, actor, c.Target.Type, c.Target.ID, nullStr(c.Target.Name), version, before, after)
	return err
}

// inTx runs fn in a transaction.
func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ConfigQuery filters configuration changes.
type ConfigQuery struct {
	Cluster     string
	TargetTypes []string
	TargetID    string
	Workload    *hubapi.WorkloadKey
	Actor       string
	Actions     []string
	From, To    *time.Time
	Desc        bool
	Limit       int
	AfterTime   *time.Time
	AfterID     string
}

// QueryConfigChanges returns configuration changes and whether more exist.
func (s *Store) QueryConfigChanges(ctx context.Context, q ConfigQuery) ([]hubapi.ConfigChange, bool, error) {
	conds := []string{"e.type = 'config_changed'"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if q.Cluster != "" {
		add("e.cluster = $%d", q.Cluster)
	}
	if len(q.TargetTypes) > 0 {
		add("e.target_type = ANY($%d)", q.TargetTypes)
	}
	if q.TargetID != "" {
		add("e.target_id = $%d", q.TargetID)
	}
	if k := q.Workload; k != nil {
		add("e.cluster = $%d", k.Cluster)
		add("e.namespace = $%d", k.Namespace)
		add("e.workload_kind = $%d", k.Kind)
		add("e.workload_name = $%d", k.Name)
	}
	if q.Actor != "" {
		add("e.actor->>'name' = $%d", q.Actor)
	}
	if len(q.Actions) > 0 {
		add("e.reason = ANY($%d)", q.Actions)
	}
	// A team-scoped caller sees only the changes to its teams' workloads (overrides, approvals).
	conds = append(conds, workloadIDsInScope(ctx, "e.workload_id", &args))
	if q.From != nil {
		add("e.time >= $%d", *q.From)
	}
	if q.To != nil {
		add("e.time < $%d", *q.To)
	}
	order, cmp := "ASC", ">"
	if q.Desc {
		order, cmp = "DESC", "<"
	}
	if q.AfterTime != nil {
		args = append(args, *q.AfterTime, q.AfterID)
		conds = append(conds, fmt.Sprintf("(e.time, e.id) %s ($%d, $%d::uuid)", cmp, len(args)-1, len(args)))
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	args = append(args, q.Limit+1)
	rows, err := s.Pool.Query(ctx, `SELECT e.id, e.time, e.actor, e.reason, e.target_type, e.target_id, coalesce(e.target_name, ''),
			e.cluster, e.namespace, e.workload_kind, e.workload_name, e.message, coalesce(e.version, 0), e.before, e.after
		FROM audit_events e WHERE `+joinAnd(conds)+fmt.Sprintf(` ORDER BY e.time %s, e.id %s LIMIT $%d`, order, order, len(args)), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []hubapi.ConfigChange{}
	for rows.Next() {
		var (
			c                 hubapi.ConfigChange
			id                uuid.UUID
			actor             []byte
			cluster, ns, k, n *string
			before, after     []byte
		)
		if err := rows.Scan(&id, &c.Time, &actor, &c.Action, &c.Target.Type, &c.Target.ID, &c.Target.Name,
			&cluster, &ns, &k, &n, &c.Reason, &c.Version, &before, &after); err != nil {
			return nil, false, err
		}
		c.ID, c.Type, c.Time = id.String(), hubapi.EventConfigChanged, c.Time.UTC()
		_ = json.Unmarshal(actor, &c.Actor)
		c.Target.Cluster = deref(cluster)
		if ns != nil && k != nil && n != nil && cluster != nil {
			c.Target.WorkloadID = hubapi.WorkloadKey{Cluster: *cluster, Namespace: *ns, Kind: *k, Name: *n}.ID()
		}
		if len(before) > 0 {
			c.Before = json.RawMessage(before)
		}
		if len(after) > 0 {
			c.After = json.RawMessage(after)
		}
		out = append(out, c)
	}
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	return out, more, rows.Err()
}
