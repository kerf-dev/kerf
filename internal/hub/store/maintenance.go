package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Maintenance: component versions and the stored update check.

// Components in component_versions.
const (
	ComponentHub     = "hub"
	ComponentPricing = "pricing"
	ComponentWebhook = "webhook"
)

// ComponentVersion is a row of component_versions.
type ComponentVersion struct {
	Component string
	Cluster   string // workload cluster; "" for hub-side components
	Instance  string
	Version   string
	StartedAt *time.Time
	SeenAt    time.Time
}

// UpsertComponent records that instance of component (cluster "" for hub-side ones) runs version
// at seenAt.
func (s *Store) UpsertComponent(ctx context.Context, c ComponentVersion) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO component_versions (component, cluster, instance, version, started_at, seen_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (component, cluster, instance) DO UPDATE SET version = excluded.version,
			started_at = coalesce(excluded.started_at, component_versions.started_at), seen_at = excluded.seen_at`,
		c.Component, c.Cluster, c.Instance, c.Version, c.StartedAt, c.SeenAt)
	return err
}

// DeleteComponent removes one instance (a hub replica that stops).
func (s *Store) DeleteComponent(ctx context.Context, component, cluster, instance string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM component_versions WHERE component = $1 AND cluster = $2 AND instance = $3`,
		component, cluster, instance)
	return err
}

// ReplaceClusterComponents replaces every component row of a workload cluster (what its agent
// reported) with items, seen at seenAt.
func (s *Store) ReplaceClusterComponents(ctx context.Context, cluster string, items []ComponentVersion, seenAt time.Time) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM component_versions WHERE cluster = $1`, cluster); err != nil {
			return err
		}
		for _, c := range items {
			if _, err := tx.Exec(ctx, `INSERT INTO component_versions (component, cluster, instance, version, seen_at)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT (component, cluster, instance) DO UPDATE SET version = excluded.version`,
				c.Component, cluster, c.Instance, c.Version, seenAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// Components lists the rows seen since since, by component, cluster and instance.
func (s *Store) Components(ctx context.Context, since time.Time) ([]ComponentVersion, error) {
	rows, err := s.Pool.Query(ctx, `SELECT component, cluster, instance, version, started_at, seen_at
		FROM component_versions WHERE seen_at >= $1 ORDER BY component, cluster, instance`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ComponentVersion, error) {
		var c ComponentVersion
		err := r.Scan(&c.Component, &c.Cluster, &c.Instance, &c.Version, &c.StartedAt, &c.SeenAt)
		return c, err
	})
}

// PruneComponents deletes hub-side rows (hub replicas, pricing instances) not seen since before.
// Rows of workload clusters are replaced by their agents' reports instead.
func (s *Store) PruneComponents(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM component_versions WHERE cluster = '' AND seen_at < $1`, before)
	return tag.RowsAffected(), err
}

// LeaderApplicationName returns the application_name of the session holding the leader lock
// (LeaderLockKey); "" when no session holds it or it is not visible.
func (s *Store) LeaderApplicationName(ctx context.Context) (string, error) {
	// pg_locks shows a bigint advisory key as classid (high 32 bits), objid (low 32 bits), objsubid 1.
	key := uint64(LeaderLockKey)
	hi, lo := int64(key>>32), int64(key&0xffffffff)
	var name string
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(a.application_name, '') FROM pg_locks l
		JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'advisory' AND l.granted AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
			AND l.classid::bigint = $1 AND l.objid::bigint = $2 AND l.objsubid = 1
		LIMIT 1`, hi, lo).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// UpdateCheck is the row of update_check.
type UpdateCheck struct {
	CheckedAt      time.Time
	CurrentVersion string
	LatestVersion  string
	ReleaseName    string
	ReleaseURL     string
	PublishedAt    *time.Time
	Error          string
	ETag           string
	LastSuccessAt  *time.Time
}

// GetUpdateCheck returns the stored check; ok is false when none ran yet.
func (s *Store) GetUpdateCheck(ctx context.Context) (UpdateCheck, bool, error) {
	var u UpdateCheck
	err := s.Pool.QueryRow(ctx, `SELECT checked_at, current_version, latest_version, release_name, release_url,
		published_at, error, etag, last_success_at FROM update_check WHERE id = 1`).Scan(
		&u.CheckedAt, &u.CurrentVersion, &u.LatestVersion, &u.ReleaseName, &u.ReleaseURL, &u.PublishedAt, &u.Error, &u.ETag, &u.LastSuccessAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, false, nil
	}
	return u, err == nil, err
}

// PutUpdateCheck stores the check result.
func (s *Store) PutUpdateCheck(ctx context.Context, u UpdateCheck) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO update_check (id, checked_at, current_version, latest_version, release_name,
		release_url, published_at, error, etag, last_success_at) VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET checked_at = excluded.checked_at, current_version = excluded.current_version,
			latest_version = excluded.latest_version, release_name = excluded.release_name, release_url = excluded.release_url,
			published_at = excluded.published_at, error = excluded.error, etag = excluded.etag, last_success_at = excluded.last_success_at`,
		u.CheckedAt, u.CurrentVersion, u.LatestVersion, u.ReleaseName, u.ReleaseURL, u.PublishedAt, u.Error, u.ETag, u.LastSuccessAt)
	return err
}
