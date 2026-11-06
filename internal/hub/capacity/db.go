package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// DB stores the latest capacity report per cluster (table cluster_capacity).
type DB struct{ Pool *pgxpool.Pool }

// Stored is a stored report.
type Stored struct {
	ReportedAt time.Time
	Report     hubapi.CapacityReport
}

// Save replaces the cluster's report.
func (d *DB) Save(ctx context.Context, cluster string, rep *hubapi.CapacityReport, at time.Time) error {
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	_, err = d.Pool.Exec(ctx, `
		INSERT INTO cluster_capacity (cluster, reported_at, observed_at, report) VALUES ($1, $2, $3, $4)
		ON CONFLICT (cluster) DO UPDATE SET reported_at = EXCLUDED.reported_at, observed_at = EXCLUDED.observed_at, report = EXCLUDED.report`,
		cluster, at.UTC(), rep.ObservedAt.UTC(), b)
	return err
}

// Load returns the cluster's report; ok is false when it never sent one.
func (d *DB) Load(ctx context.Context, cluster string) (Stored, bool, error) {
	var s Stored
	var b []byte
	err := d.Pool.QueryRow(ctx, `SELECT reported_at, report FROM cluster_capacity WHERE cluster = $1`, cluster).Scan(&s.ReportedAt, &b)
	if errors.Is(err, pgx.ErrNoRows) {
		return Stored{}, false, nil
	}
	if err != nil {
		return Stored{}, false, err
	}
	if err := json.Unmarshal(b, &s.Report); err != nil {
		return Stored{}, false, err
	}
	return s, true, nil
}
