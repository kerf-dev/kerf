package capacity

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Point is one sample of a capacity history series.
type Point struct {
	Time                                                              time.Time
	Nodes, CPURequested, CPUAllocatable, MemRequested, MemAllocatable float64
}

// SnapshotRef is a stored report with its cluster.
type SnapshotRef struct {
	Cluster    string
	ReportedAt time.Time
	Report     hubapi.CapacityReport
}

// SaveHour stores the rows for one hour (truncated to the UTC hour), replacing earlier values.
func (d *DB) SaveHour(ctx context.Context, cluster string, hour time.Time, rows []HourRow) error {
	hour = hour.UTC().Truncate(time.Hour)
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO capacity_hourly (cluster, node_pool, hour, nodes, cpu_requested, cpu_allocatable, mem_requested, mem_allocatable, truncated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (cluster, node_pool, hour) DO UPDATE SET
				nodes = EXCLUDED.nodes, cpu_requested = EXCLUDED.cpu_requested, cpu_allocatable = EXCLUDED.cpu_allocatable,
				mem_requested = EXCLUDED.mem_requested, mem_allocatable = EXCLUDED.mem_allocatable, truncated = EXCLUDED.truncated`,
			cluster, r.NodePool, hour, r.Nodes, r.CPURequested, r.CPUAllocatable, r.MemRequested, r.MemAllocatable, r.Truncated)
	}
	br := d.Pool.SendBatch(ctx, batch)
	for range rows {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

// History returns the series for a node pool ("" is the cluster total) between from and to,
// hourly or, when daily is set, averaged per UTC day.
func (d *DB) History(ctx context.Context, cluster, nodePool string, from, to time.Time, daily bool) ([]Point, error) {
	bucket := "hour"
	agg := func(c string) string { return c }
	if daily {
		bucket = "date_trunc('day', hour AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'"
		agg = func(c string) string { return "avg(" + c + ")" }
	}
	q := `SELECT ` + bucket + ` AS t, ` + agg("nodes") + `, ` + agg("cpu_requested") + `, ` + agg("cpu_allocatable") + `, ` +
		agg("mem_requested") + `, ` + agg("mem_allocatable") + `
		FROM capacity_hourly WHERE cluster = $1 AND node_pool = $2 AND hour >= $3 AND hour <= $4`
	if daily {
		q += ` GROUP BY t`
	}
	q += ` ORDER BY t`
	rows, err := d.Pool.Query(ctx, q, cluster, nodePool, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Point{}
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.Time, &p.Nodes, &p.CPURequested, &p.CPUAllocatable, &p.MemRequested, &p.MemAllocatable); err != nil {
			return nil, err
		}
		p.Time = p.Time.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// Pools lists the named node pools with rows between from and to, sorted.
func (d *DB) Pools(ctx context.Context, cluster string, from, to time.Time) ([]string, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT DISTINCT node_pool FROM capacity_hourly
		WHERE cluster = $1 AND node_pool <> '' AND hour >= $2 AND hour <= $3 ORDER BY node_pool`,
		cluster, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PoolHours maps every stored hour (one with a cluster total row) to the node pools present in it.
func (d *DB) PoolHours(ctx context.Context, cluster string, from, to time.Time) (map[time.Time][]string, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT hour, node_pool FROM capacity_hourly
		WHERE cluster = $1 AND hour >= $2 AND hour <= $3
		  AND hour IN (SELECT hour FROM capacity_hourly WHERE cluster = $1 AND node_pool = '')
		ORDER BY hour, node_pool`, cluster, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[time.Time][]string{}
	for rows.Next() {
		var h time.Time
		var p string
		if err := rows.Scan(&h, &p); err != nil {
			return nil, err
		}
		h = h.UTC()
		if _, ok := out[h]; !ok {
			out[h] = []string{}
		}
		if p != "" {
			out[h] = append(out[h], p)
		}
	}
	return out, rows.Err()
}

// AnyTruncated reports whether any stored hour between from and to came from a truncated report.
func (d *DB) AnyTruncated(ctx context.Context, cluster string, from, to time.Time) (bool, error) {
	var b bool
	err := d.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM capacity_hourly WHERE cluster = $1 AND truncated AND hour >= $2 AND hour <= $3)`,
		cluster, from.UTC(), to.UTC()).Scan(&b)
	return b, err
}

// Since returns the first stored hour of the cluster; ok is false when there is none.
func (d *DB) Since(ctx context.Context, cluster string) (time.Time, bool, error) {
	var t *time.Time
	if err := d.Pool.QueryRow(ctx, `SELECT min(hour) FROM capacity_hourly WHERE cluster = $1`, cluster).Scan(&t); err != nil {
		return time.Time{}, false, err
	}
	if t == nil {
		return time.Time{}, false, nil
	}
	return t.UTC(), true, nil
}

// PruneHistory deletes rows older than before and returns how many.
func (d *DB) PruneHistory(ctx context.Context, before time.Time) (int64, error) {
	tag, err := d.Pool.Exec(ctx, `DELETE FROM capacity_hourly WHERE hour < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Snapshots returns the stored report of every cluster.
func (d *DB) Snapshots(ctx context.Context) ([]SnapshotRef, error) {
	rows, err := d.Pool.Query(ctx, `SELECT cluster, reported_at, report FROM cluster_capacity ORDER BY cluster`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotRef
	for rows.Next() {
		var s SnapshotRef
		var b []byte
		if err := rows.Scan(&s.Cluster, &s.ReportedAt, &b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &s.Report); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
