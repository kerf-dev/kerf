package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// RecordChange records a config change outside another transaction (datasource changes, D-025).
func (s *Store) RecordChange(ctx context.Context, c Change) error {
	return s.inTx(ctx, func(tx pgx.Tx) error { return recordChange(ctx, tx, c) })
}

// RecommendationCounts returns the live workloads of a cluster and how many of them have a
// recommendation (readiness check metrics.recommendations).
func (s *Store) RecommendationCounts(ctx context.Context, cluster string) (live, recommended int, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE EXISTS (SELECT 1 FROM recommendations r WHERE r.workload_id = w.id))
		FROM workloads w WHERE w.cluster = $1 AND w.deleted_at IS NULL`, cluster).Scan(&live, &recommended)
	return live, recommended, err
}
