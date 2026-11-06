package realized

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// Hub operational series (kerf_hub_* prefix, docs/realized-savings.md). Computed from the
// database, so the hub registers the collector as leader-only.
var (
	descRealized = prometheus.NewDesc("kerf_hub_realized_savings_usd",
		"Realized savings in USD since the workload's first applied change (released requests x replicas x rates of the time). Not an estimate.",
		[]string{"cluster", "namespace", "workload_kind", "workload", "resource"}, nil)
	descReleased = prometheus.NewDesc("kerf_hub_realized_released_requests",
		"Requests released by applied changes in the last computed hour, x replicas: cores for cpu, bytes for memory.",
		[]string{"cluster", "namespace", "workload_kind", "workload", "resource"}, nil)
	descThrough = prometheus.NewDesc("kerf_hub_realized_computed_through_timestamp_seconds",
		"Unix time up to which realized savings are computed for the cluster.", []string{"cluster"}, nil)
)

type workloadTotals struct {
	Cluster string
	Key     WorkloadKey
	USD     Amount
	Cores   float64
	Bytes   float64
}

type snapshot struct {
	workloads []workloadTotals
	through   map[string]time.Time
}

// Collector exports the materialised totals.
type Collector struct {
	snap atomic.Pointer[snapshot]
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descRealized
	ch <- descReleased
	ch <- descThrough
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	s := c.snap.Load()
	if s == nil {
		return
	}
	g := func(d *prometheus.Desc, v float64, l ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, l...)
	}
	for _, w := range s.workloads {
		l := []string{w.Cluster, w.Key.Namespace, w.Key.Kind, w.Key.Name}
		g(descRealized, w.USD.CPU, append(l, "cpu")...)
		g(descRealized, w.USD.Memory, append(l, "memory")...)
		g(descReleased, w.Cores, append(l, "cpu")...)
		g(descReleased, w.Bytes, append(l, "memory")...)
	}
	for c, t := range s.through {
		g(descThrough, float64(t.Unix()), c)
	}
}

// Refresh reloads the totals from the database.
func (c *Collector) Refresh(ctx context.Context, d *DB) error {
	rows, err := d.Pool.Query(ctx, `SELECT r.cluster, r.namespace, r.workload_kind, r.workload_name,
			sum(r.cpu_usd), sum(r.memory_usd),
			coalesce(sum(r.cpu_cores * r.replicas) FILTER (WHERE r.hour = s.computed_through - interval '1 hour'), 0),
			coalesce(sum(r.memory_bytes * r.replicas) FILTER (WHERE r.hour = s.computed_through - interval '1 hour'), 0)
		FROM realized_savings_hourly r JOIN realized_savings_state s ON s.cluster = r.cluster
		GROUP BY 1, 2, 3, 4`)
	if err != nil {
		return err
	}
	ws, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (workloadTotals, error) {
		var w workloadTotals
		err := r.Scan(&w.Cluster, &w.Key.Namespace, &w.Key.Kind, &w.Key.Name, &w.USD.CPU, &w.USD.Memory, &w.Cores, &w.Bytes)
		return w, err
	})
	if err != nil {
		return err
	}
	rows, err = d.Pool.Query(ctx, `SELECT cluster, computed_through FROM realized_savings_state WHERE computed_through IS NOT NULL`)
	if err != nil {
		return err
	}
	through := map[string]time.Time{}
	for rows.Next() {
		var c string
		var t time.Time
		if err := rows.Scan(&c, &t); err != nil {
			rows.Close()
			return err
		}
		through[c] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	c.snap.Store(&snapshot{workloads: ws, through: through})
	return nil
}
