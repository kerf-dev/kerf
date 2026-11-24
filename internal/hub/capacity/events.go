package capacity

import (
	"context"
	"sort"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// maxTopWorkloads is the number of workloads listed per change event.
const maxTopWorkloads = 5

// Event is a marker on the capacity chart: Count occurrences of Type on one UTC day.
type Event struct {
	Day      time.Time
	Type     string
	Count    int
	NodePool string
	Top      []hubapi.WorkloadRef
}

// PoolEvents derives pool_added and pool_removed events from the pools present in each stored
// hour. Every stored hour is compared with the previous stored hour; gaps are ignored and the
// first hour produces no events.
func PoolEvents(hours map[time.Time][]string) []Event {
	keys := make([]time.Time, 0, len(hours))
	for h := range hours {
		keys = append(keys, h)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	type key struct {
		day  time.Time
		typ  string
		pool string
	}
	counts := map[key]int{}
	var prev map[string]bool
	for i, h := range keys {
		cur := make(map[string]bool, len(hours[h]))
		for _, p := range hours[h] {
			cur[p] = true
		}
		if i > 0 {
			day := h.UTC().Truncate(24 * time.Hour)
			for p := range cur {
				if !prev[p] {
					counts[key{day, "pool_added", p}]++
				}
			}
			for p := range prev {
				if !cur[p] {
					counts[key{day, "pool_removed", p}]++
				}
			}
		}
		prev = cur
	}
	out := make([]Event, 0, len(counts))
	for k, n := range counts {
		out = append(out, Event{Day: k.day, Type: k.typ, Count: n, NodePool: k.pool})
	}
	sortEvents(out)
	return out
}

func sortEvents(ev []Event) {
	sort.Slice(ev, func(i, j int) bool {
		a, b := ev[i], ev[j]
		if !a.Day.Equal(b.Day) {
			return a.Day.Before(b.Day)
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.NodePool < b.NodePool
	})
}

// ChangeEvents counts applied, rolled_back and released audit events per UTC day and type. Top
// lists the workloads with the most events. A nil workloads slice means no filter; otherwise only
// events of those workloads are counted.
func (d *DB) ChangeEvents(ctx context.Context, cluster string, from, to time.Time, workloads []hubapi.WorkloadRef) ([]Event, error) {
	q := `SELECT date_trunc('day', time AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS day, type,
			namespace, workload_kind, workload_name, count(*)
		FROM audit_events
		WHERE cluster = $1 AND time >= $2 AND time <= $3
		  AND type IN ('applied', 'rolled_back', 'released')`
	args := []any{cluster, from.UTC(), to.UTC()}
	if workloads != nil {
		ns := make([]string, len(workloads))
		kinds := make([]string, len(workloads))
		names := make([]string, len(workloads))
		for i, w := range workloads {
			ns[i], kinds[i], names[i] = w.Namespace, w.Kind, w.Name
		}
		q += ` AND (namespace, workload_kind, workload_name) IN (SELECT * FROM unnest($4::text[], $5::text[], $6::text[]))`
		args = append(args, ns, kinds, names)
	}
	q += ` GROUP BY 1, 2, 3, 4, 5`
	rows, err := d.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		day time.Time
		typ string
	}
	type wl struct {
		ref hubapi.WorkloadRef
		n   int
	}
	byKey := map[key][]wl{}
	for rows.Next() {
		var k key
		var w wl
		if err := rows.Scan(&k.day, &k.typ, &w.ref.Namespace, &w.ref.Kind, &w.ref.Name, &w.n); err != nil {
			return nil, err
		}
		k.day = k.day.UTC()
		byKey[k] = append(byKey[k], w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(byKey))
	for k, ws := range byKey {
		sort.Slice(ws, func(i, j int) bool {
			a, b := ws[i], ws[j]
			if a.n != b.n {
				return a.n > b.n
			}
			if a.ref.Namespace != b.ref.Namespace {
				return a.ref.Namespace < b.ref.Namespace
			}
			if a.ref.Kind != b.ref.Kind {
				return a.ref.Kind < b.ref.Kind
			}
			return a.ref.Name < b.ref.Name
		})
		e := Event{Day: k.day, Type: k.typ}
		for i, w := range ws {
			e.Count += w.n
			if i < maxTopWorkloads {
				e.Top = append(e.Top, w.ref)
			}
		}
		out = append(out, e)
	}
	sortEvents(out)
	return out, nil
}

// PoolWorkloads returns the distinct workloads that have pods in the pool in the report.
func PoolWorkloads(rep hubapi.CapacityReport, pool string) []hubapi.WorkloadRef {
	seen := map[hubapi.WorkloadRef]bool{}
	out := []hubapi.WorkloadRef{}
	for _, g := range rep.PodGroups {
		if g.NodePool != pool || g.Workload == nil || seen[*g.Workload] {
			continue
		}
		seen[*g.Workload] = true
		out = append(out, *g.Workload)
	}
	return out
}
