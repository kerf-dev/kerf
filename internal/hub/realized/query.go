package realized

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Scope selects the workloads a read covers. Empty slices select everything.
type Scope struct {
	Clusters   []string
	Namespaces []string
	// Workload, with one cluster in Clusters, selects one workload.
	Workload *WorkloadKey
}

func (s Scope) where(alias string, args *[]any) string {
	conds := []string{"true"}
	add := func(cond string, v any) {
		*args = append(*args, v)
		conds = append(conds, fmt.Sprintf(cond, alias, len(*args)))
	}
	if len(s.Clusters) > 0 {
		add("%s.cluster = ANY($%d)", s.Clusters)
	}
	if len(s.Namespaces) > 0 {
		add("%s.namespace = ANY($%d)", s.Namespaces)
	}
	if s.Workload != nil {
		add("%s.namespace = $%d", s.Workload.Namespace)
		add("%s.workload_kind = $%d", s.Workload.Kind)
		add("%s.workload_name = $%d", s.Workload.Name)
	}
	return strings.Join(conds, " AND ")
}

// Bucket sizes of a series.
const (
	BucketHour  = "hour"
	BucketDay   = "day"
	BucketMonth = "month"
)

// Amount is realized USD.
type Amount struct {
	CPU    float64
	Memory float64
}

// Total is CPU plus memory.
func (a Amount) Total() float64 { return a.CPU + a.Memory }

// Point is one bucket of a realized series: USD realized in the bucket and the average requests
// released over it (per-replica release x replicas; cores and bytes).
type Point struct {
	Time        time.Time
	USD         Amount
	CPUCores    float64
	MemoryBytes float64
}

// Coverage counts the workload-hours of the range by provenance.
type Coverage struct {
	Hours                  int
	HoursReplicasFromRecs  int // no metrics in that hour: replicas of the stored recommendation
	HoursWithoutReplicas   int // neither: not counted
	HoursWithDefaultPrices int
}

// Summary is a realized-savings read.
type Summary struct {
	// Since is the first hour with released requests in the scope (all time); nil = none yet.
	Since *time.Time
	// ComputedThrough: hours before it are computed for every cluster of the scope.
	ComputedThrough *time.Time
	// Total is everything realized since Since; InRange only [from, to).
	Total   Amount
	InRange Amount
	// ReleasedNow is the requests released in the last computed hour (x replicas).
	ReleasedNowCPUCores    float64
	ReleasedNowMemoryBytes float64
	Points                 []Point
	Coverage               Coverage
}

// BucketStart truncates t (UTC) to its bucket.
func BucketStart(t time.Time, bucket string) time.Time {
	t = t.UTC()
	switch bucket {
	case BucketMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	case BucketDay:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	default:
		return t.Truncate(time.Hour)
	}
}

// NextBucket returns the start of the bucket after the one starting at t.
func NextBucket(t time.Time, bucket string) time.Time {
	switch bucket {
	case BucketMonth:
		return t.AddDate(0, 1, 0)
	case BucketDay:
		return t.AddDate(0, 0, 1)
	default:
		return t.Add(time.Hour)
	}
}

// hourRange widens [from, to) to whole UTC hours.
func hourRange(from, to time.Time) (time.Time, time.Time) {
	from, to = from.UTC().Truncate(time.Hour), to.UTC()
	if t := to.Truncate(time.Hour); !t.Equal(to) {
		to = t.Add(time.Hour)
	}
	return from, to
}

// hoursIn is the number of hours of [a, b) clamped to [lo, hi).
func hoursIn(a, b, lo, hi time.Time) float64 {
	if a.Before(lo) {
		a = lo
	}
	if b.After(hi) {
		b = hi
	}
	if !b.After(a) {
		return 0
	}
	return b.Sub(a).Hours()
}

// Summary reads the realized savings of a scope with a series of buckets over [from, to).
//
// Rows are whole hours: the range is widened to [from truncated to the hour, to rounded up to the
// hour), so a bucket's amounts and its released average cover the same hours.
func (d *DB) Summary(ctx context.Context, sc Scope, from, to time.Time, bucket string) (*Summary, error) {
	out := &Summary{}
	from, to = hourRange(from, to)
	{
		var args []any
		w := sc.where("r", &args)
		if err := d.Pool.QueryRow(ctx, `SELECT min(r.hour), coalesce(sum(r.cpu_usd), 0), coalesce(sum(r.memory_usd), 0)
			FROM realized_savings_hourly r WHERE `+w, args...).Scan(&out.Since, &out.Total.CPU, &out.Total.Memory); err != nil {
			return nil, err
		}
	}
	{
		// The progress is per cluster: only the cluster filter applies.
		var args []any
		w := Scope{Clusters: sc.Clusters}.where("s", &args)
		if err := d.Pool.QueryRow(ctx, `SELECT min(s.computed_through) FROM realized_savings_state s
			WHERE s.computed_through IS NOT NULL AND `+w, args...).Scan(&out.ComputedThrough); err != nil {
			return nil, err
		}
	}
	{
		var args []any
		w := sc.where("r", &args)
		if err := d.Pool.QueryRow(ctx, `SELECT coalesce(sum(r.cpu_cores * r.replicas), 0), coalesce(sum(r.memory_bytes * r.replicas), 0)
			FROM realized_savings_hourly r JOIN realized_savings_state s
			  ON s.cluster = r.cluster AND r.hour = s.computed_through - interval '1 hour'
			WHERE `+w, args...).Scan(&out.ReleasedNowCPUCores, &out.ReleasedNowMemoryBytes); err != nil {
			return nil, err
		}
	}
	{
		args := []any{from, to}
		w := sc.where("r", &args)
		if err := d.Pool.QueryRow(ctx, `SELECT count(*),
				count(*) FILTER (WHERE r.replicas_source = 'recommendation'),
				count(*) FILTER (WHERE r.replicas_source = 'none'),
				count(*) FILTER (WHERE r.pricing_source = 'default'),
				coalesce(sum(r.cpu_usd), 0), coalesce(sum(r.memory_usd), 0)
			FROM realized_savings_hourly r WHERE r.hour >= $1 AND r.hour < $2 AND `+w, args...).Scan(
			&out.Coverage.Hours, &out.Coverage.HoursReplicasFromRecs, &out.Coverage.HoursWithoutReplicas,
			&out.Coverage.HoursWithDefaultPrices, &out.InRange.CPU, &out.InRange.Memory); err != nil {
			return nil, err
		}
	}
	args := []any{from, to, bucket}
	w := sc.where("r", &args)
	rows, err := d.Pool.Query(ctx, `SELECT date_trunc($3, r.hour AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS b,
			sum(r.cpu_usd), sum(r.memory_usd), sum(r.cpu_cores * r.replicas), sum(r.memory_bytes * r.replicas)
		FROM realized_savings_hourly r WHERE r.hour >= $1 AND r.hour < $2 AND `+w+`
		GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type agg struct{ cpu, mem, cores, bytes float64 }
	byBucket := map[time.Time]agg{}
	for rows.Next() {
		var b time.Time
		var a agg
		if err := rows.Scan(&b, &a.cpu, &a.mem, &a.cores, &a.bytes); err != nil {
			return nil, err
		}
		byBucket[b.UTC()] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hi := to
	if out.ComputedThrough != nil && out.ComputedThrough.Before(hi) {
		hi = *out.ComputedThrough
	}
	for b := BucketStart(from, bucket); b.Before(to); b = NextBucket(b, bucket) {
		a := byBucket[b]
		p := Point{Time: b, USD: Amount{CPU: a.cpu, Memory: a.mem}}
		if h := hoursIn(b, NextBucket(b, bucket), from, hi); h > 0 {
			p.CPUCores, p.MemoryBytes = a.cores/h, a.bytes/h
		}
		out.Points = append(out.Points, p)
	}
	return out, nil
}

// NodeCostPoint is one bucket of a cluster's node cost trend (hourly averages over the bucket).
type NodeCostPoint struct {
	Time                 time.Time
	NodeCostUSDPerHour   *float64
	Nodes                *float64
	CPURequestedRatio    *float64
	MemoryRequestedRatio *float64
	ReleasedCPUCores     float64
	ReleasedMemoryBytes  float64
}

// NodeCost reads a cluster's node cost trend with the requests released by Kerf over [from, to).
// Like Summary, the range is widened to whole hours.
func (d *DB) NodeCost(ctx context.Context, cluster string, from, to time.Time, bucket string) ([]NodeCostPoint, *time.Time, error) {
	from, to = hourRange(from, to)
	var through *time.Time
	if err := d.Pool.QueryRow(ctx, `SELECT least(computed_through, node_cost_through) FROM realized_savings_state WHERE cluster = $1`,
		cluster).Scan(&through); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	byBucket := map[time.Time]*NodeCostPoint{}
	get := func(b time.Time) *NodeCostPoint {
		b = b.UTC()
		if byBucket[b] == nil {
			byBucket[b] = &NodeCostPoint{Time: b}
		}
		return byBucket[b]
	}
	rows, err := d.Pool.Query(ctx, `SELECT date_trunc($4, hour AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
			avg(node_cost_usd), avg(nodes), avg(cpu_requested_ratio), avg(memory_requested_ratio)
		FROM node_cost_hourly WHERE cluster = $1 AND hour >= $2 AND hour < $3 GROUP BY 1`, cluster, from, to, bucket)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var b time.Time
		var cost, nodes, cpu, mem *float64
		if err := rows.Scan(&b, &cost, &nodes, &cpu, &mem); err != nil {
			rows.Close()
			return nil, nil, err
		}
		p := get(b)
		p.NodeCostUSDPerHour, p.Nodes, p.CPURequestedRatio, p.MemoryRequestedRatio = cost, nodes, cpu, mem
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = d.Pool.Query(ctx, `SELECT date_trunc($4, hour AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
			sum(cpu_cores * replicas), sum(memory_bytes * replicas)
		FROM realized_savings_hourly WHERE cluster = $1 AND hour >= $2 AND hour < $3 GROUP BY 1`, cluster, from, to, bucket)
	if err != nil {
		return nil, nil, err
	}
	sums := map[time.Time][2]float64{}
	for rows.Next() {
		var b time.Time
		var v [2]float64
		if err := rows.Scan(&b, &v[0], &v[1]); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sums[b.UTC()] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	hi := to
	if through != nil && through.Before(hi) {
		hi = *through
	}
	var out []NodeCostPoint
	for b := BucketStart(from, bucket); b.Before(to); b = NextBucket(b, bucket) {
		h := hoursIn(b, NextBucket(b, bucket), from, hi)
		if h <= 0 && byBucket[b] == nil {
			continue
		}
		p := get(b)
		if h > 0 {
			p.ReleasedCPUCores, p.ReleasedMemoryBytes = sums[b][0]/h, sums[b][1]/h
		}
		out = append(out, *p)
	}
	return out, through, nil
}
