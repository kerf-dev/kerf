package realized

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// DB reads and writes the realized-savings tables (db/migrations/0016_realized_savings.sql).
type DB struct {
	Pool *pgxpool.Pool
}

// WorkloadKey identifies a workload as the audit log does (it survives the workload's deletion).
type WorkloadKey struct {
	Namespace string
	Kind      string
	Name      string
}

type state struct {
	ComputedThrough *time.Time
	EventsThrough   time.Time
	NodeCostThrough *time.Time
}

// liveClusters lists registered clusters that are not retired.
func (d *DB) liveClusters(ctx context.Context) ([]string, error) {
	rows, err := d.Pool.Query(ctx, `SELECT name FROM clusters WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (d *DB) state(ctx context.Context, cluster string) (state, bool, error) {
	var s state
	err := d.Pool.QueryRow(ctx, `SELECT computed_through, events_through, node_cost_through
		FROM realized_savings_state WHERE cluster = $1`, cluster).Scan(&s.ComputedThrough, &s.EventsThrough, &s.NodeCostThrough)
	if errors.Is(err, pgx.ErrNoRows) {
		return state{}, false, nil
	}
	return s, err == nil, err
}

func (d *DB) saveState(ctx context.Context, tx pgx.Tx, cluster string, s state) error {
	_, err := tx.Exec(ctx, `INSERT INTO realized_savings_state (cluster, computed_through, events_through, node_cost_through, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (cluster) DO UPDATE SET computed_through = EXCLUDED.computed_through,
			events_through = EXCLUDED.events_through, node_cost_through = EXCLUDED.node_cost_through, updated_at = now()`,
		cluster, s.ComputedThrough, s.EventsThrough, s.NodeCostThrough)
	return err
}

// firstChange returns the time of the cluster's earliest request-changing event.
func (d *DB) firstChange(ctx context.Context, cluster string) (*time.Time, error) {
	var t *time.Time
	err := d.Pool.QueryRow(ctx, `SELECT min(time) FROM audit_events WHERE cluster = $1 AND type = ANY($2)`,
		cluster, LedgerTypes).Scan(&t)
	return t, err
}

// lateChange returns the earliest event time among the request-changing events received in
// (after, until]: events the agent buffered while the hub was unreachable can be older than what
// was already computed.
func (d *DB) lateChange(ctx context.Context, cluster string, after, until time.Time) (*time.Time, error) {
	var t *time.Time
	err := d.Pool.QueryRow(ctx, `SELECT min(time) FROM audit_events
		WHERE cluster = $1 AND type = ANY($2) AND received_at > $3 AND received_at <= $4`,
		cluster, LedgerTypes, after, until).Scan(&t)
	return t, err
}

// changes loads the request-changing events of a cluster before `before`, per workload.
func (d *DB) changes(ctx context.Context, cluster string, before time.Time) (map[WorkloadKey][]Change, error) {
	rows, err := d.Pool.Query(ctx, `SELECT namespace, workload_kind, workload_name, time, type, resources, containers
		FROM audit_events WHERE cluster = $1 AND type = ANY($2) AND time < $3 ORDER BY time, id`,
		cluster, LedgerTypes, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[WorkloadKey][]Change{}
	for rows.Next() {
		var k WorkloadKey
		var c Change
		var containers []byte
		if err := rows.Scan(&k.Namespace, &k.Kind, &k.Name, &c.Time, &c.Type, &c.Resources, &containers); err != nil {
			return nil, err
		}
		if len(containers) > 0 {
			var cs []hubapi.ContainerChange
			if err := json.Unmarshal(containers, &cs); err != nil {
				return nil, err
			}
			c.Containers = cs
		}
		out[k] = append(out[k], c)
	}
	return out, rows.Err()
}

type rateSnapshot struct {
	AsOf  time.Time
	Rates Rates
}

// rateSnapshots returns the rates the recommender stored for the cluster's successful runs with
// as-of in [from, to), plus the last one before from, oldest first.
func (d *DB) rateSnapshots(ctx context.Context, cluster string, from, to time.Time) ([]rateSnapshot, error) {
	rows, err := d.Pool.Query(ctx, `(SELECT as_of, pricing FROM recommender_run_clusters
			WHERE cluster = $1 AND status = 'succeeded' AND as_of < $2 ORDER BY as_of DESC LIMIT 1)
		UNION ALL
		(SELECT as_of, pricing FROM recommender_run_clusters
			WHERE cluster = $1 AND status = 'succeeded' AND as_of >= $2 AND as_of < $3)
		ORDER BY 1`, cluster, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rateSnapshot
	for rows.Next() {
		var s rateSnapshot
		var b []byte
		if err := rows.Scan(&s.AsOf, &b); err != nil {
			return nil, err
		}
		var p struct {
			CPU    float64 `json:"cpuCoreHourUSD"`
			Memory float64 `json:"memoryGiBHourUSD"`
			Source string  `json:"source"`
		}
		if err := json.Unmarshal(b, &p); err != nil || p.CPU <= 0 || p.Memory <= 0 {
			continue
		}
		if p.Source != hubapi.PricingSourceMetrics {
			p.Source = hubapi.PricingSourceDefault
		}
		s.Rates = Rates{CPUCoreHour: p.CPU, MemoryGiBHour: p.Memory, Source: p.Source}
		out = append(out, s)
	}
	return out, rows.Err()
}

type replicaPoint struct {
	At       time.Time
	Replicas float64
}

// recommendationReplicas returns the average replicas of the stored recommendations of the
// cluster's workloads generated in [from - 14 days, to), oldest first.
func (d *DB) recommendationReplicas(ctx context.Context, cluster string, from, to time.Time) (map[WorkloadKey][]replicaPoint, error) {
	rows, err := d.Pool.Query(ctx, `SELECT w.namespace, w.kind, w.name, r.generated_at, r.average_replicas
		FROM recommendations r JOIN workloads w ON w.id = r.workload_id
		WHERE w.cluster = $1 AND r.generated_at >= $2 AND r.generated_at < $3
		ORDER BY r.generated_at`, cluster, from.Add(-14*24*time.Hour), to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[WorkloadKey][]replicaPoint{}
	for rows.Next() {
		var k WorkloadKey
		var p replicaPoint
		if err := rows.Scan(&k.Namespace, &k.Kind, &k.Name, &p.At, &p.Replicas); err != nil {
			return nil, err
		}
		out[k] = append(out[k], p)
	}
	return out, rows.Err()
}

// workloadHour is one row of realized_savings_hourly.
type workloadHour struct {
	Key WorkloadKey
	Hour
}

// NodeCostHour is one row of node_cost_hourly.
type NodeCostHour struct {
	Hour                 time.Time
	NodeCostUSD          *float64
	Nodes                *float64
	CPURequestedRatio    *float64
	MemoryRequestedRatio *float64
}

// replaceHours replaces the cluster's realized rows in [from, to) and saves the state, atomically.
func (d *DB) replaceHours(ctx context.Context, cluster string, from, to time.Time, rows []workloadHour, s state) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM realized_savings_hourly WHERE cluster = $1 AND hour >= $2 AND hour < $3`,
		cluster, from, to); err != nil {
		return err
	}
	if len(rows) > 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"realized_savings_hourly"},
			[]string{"cluster", "namespace", "workload_kind", "workload_name", "hour", "cpu_cores", "memory_bytes",
				"replicas", "replicas_source", "cpu_core_hour_usd", "memory_gib_hour_usd", "pricing_source", "cpu_usd", "memory_usd"},
			pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
				r := rows[i]
				return []any{cluster, r.Key.Namespace, r.Key.Kind, r.Key.Name, r.Start.UTC(), r.Released.CPU, r.Released.Memory,
					r.Replicas, r.ReplicaSource, r.Rates.CPUCoreHour, r.Rates.MemoryGiBHour, r.Rates.Source, r.CPUUSD, r.MemoryUSD}, nil
			}))
		if err != nil {
			return err
		}
	}
	if err := d.saveState(ctx, tx, cluster, s); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// replaceNodeCost replaces the cluster's node cost rows in [from, to) and saves the state.
func (d *DB) replaceNodeCost(ctx context.Context, cluster string, from, to time.Time, rows []NodeCostHour, s state) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM node_cost_hourly WHERE cluster = $1 AND hour >= $2 AND hour < $3`,
		cluster, from, to); err != nil {
		return err
	}
	if len(rows) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"node_cost_hourly"},
			[]string{"cluster", "hour", "node_cost_usd", "nodes", "cpu_requested_ratio", "memory_requested_ratio"},
			pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
				r := rows[i]
				return []any{cluster, r.Hour.UTC(), r.NodeCostUSD, r.Nodes, r.CPURequestedRatio, r.MemoryRequestedRatio}, nil
			})); err != nil {
			return err
		}
	}
	if err := d.saveState(ctx, tx, cluster, s); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
