package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Hub HA: several hub replicas share one database. One replica, the leader, runs
// the background work; the others serve the API. What they must share goes through the database:
// recompute requests (table recompute_requests) and notifications (channel NotifyChannel).

// NotifyChannel is the Postgres LISTEN/NOTIFY channel the hub replicas use to tell each other
// about changes (payloads Notify*).
const NotifyChannel = "kerf_hub"

// Notification payloads on NotifyChannel.
const (
	NotifyRecompute = "recompute" // a recompute request was queued: the leader polls now
	NotifyTokens    = "tokens"    // API tokens were revoked: every replica drops its token cache
	NotifySettings  = "settings"  // recommender settings changed: the leader reloads the interval
	NotifyReleased  = "released"  // the leader stepped down (shutdown): followers try the lock now
	// NotifyReadinessPrefix + cluster: run the cluster's readiness checks now (leader).
	NotifyReadinessPrefix = "readiness:"
)

// Advisory lock keys. goose's migration lock uses its own key (lock.DefaultLockID).
const (
	// LeaderLockKey is held by the leader for as long as it leads.
	LeaderLockKey int64 = 0x726f2d6875622d4c // "ro-hub-L"
	// InitLockKey serialises the start-up writes after the migrations (dev seed, bootstrap admin,
	// local datasource) across replicas starting together.
	InitLockKey int64 = 0x726f2d6875622d49 // "ro-hub-I"
)

// Notify sends payload on NotifyChannel to every hub replica (delivered at commit; here at once).
func (s *Store) Notify(ctx context.Context, payload string) error {
	_, err := s.Pool.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, payload)
	return err
}

// WithInitLock runs fn while holding the start-up advisory lock (InitLockKey), so replicas
// starting together run their start-up writes one after the other.
func (s *Store) WithInitLock(ctx context.Context, fn func() error) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, InitLockKey); err != nil {
		return fmt.Errorf("start-up lock: %w", err)
	}
	defer func() {
		// A fresh context: unlock even when ctx was cancelled; if this fails the connection is
		// closed, which releases the lock too.
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, InitLockKey); err != nil {
			_ = conn.Conn().Close(uctx)
		}
	}()
	return fn()
}

// ---- recompute requests ----

// RecomputeRequest is a row of recompute_requests.
type RecomputeRequest struct {
	ID          uuid.UUID
	Cluster     string // "" = every cluster
	RequestedAt time.Time
	RequestedBy string
}

// QueueRecompute stores a recompute request for cluster ("" = all) unless one for the same scope is
// already queued (not started), in which case that one is returned with coalesced = true.
func (s *Store) QueueRecompute(ctx context.Context, req RecomputeRequest) (RecomputeRequest, bool, error) {
	for attempt := 0; attempt < 5; attempt++ {
		var out RecomputeRequest
		var coalesced bool
		// Both branches in one statement: the insert, or the queued row it conflicted with. A row
		// committed by a concurrent request after this statement's snapshot is invisible to the
		// second branch; then nothing comes back and the loop retries.
		err := s.Pool.QueryRow(ctx, `WITH ins AS (
				INSERT INTO recompute_requests (id, cluster, requested_at, requested_by) VALUES ($1, $2, $3, $4)
				ON CONFLICT (cluster) WHERE started_at IS NULL DO NOTHING
				RETURNING id, cluster, requested_at, requested_by)
			SELECT id, cluster, requested_at, requested_by, false FROM ins
			UNION ALL
			SELECT id, cluster, requested_at, requested_by, true FROM recompute_requests
			WHERE cluster = $2 AND started_at IS NULL AND NOT EXISTS (SELECT 1 FROM ins)
			LIMIT 1`, req.ID, req.Cluster, req.RequestedAt, req.RequestedBy).
			Scan(&out.ID, &out.Cluster, &out.RequestedAt, &out.RequestedBy, &coalesced)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return RecomputeRequest{}, false, err
		}
		out.RequestedAt = out.RequestedAt.UTC()
		return out, coalesced, nil
	}
	return RecomputeRequest{}, false, errors.New("queue recompute: lost the race with concurrent requests 5 times")
}

// ClaimRecompute marks the oldest queued recompute request started and returns it (ok false when
// none is queued). From then on a new request for its scope queues a new run.
func (s *Store) ClaimRecompute(ctx context.Context) (RecomputeRequest, bool, error) {
	var r RecomputeRequest
	err := s.Pool.QueryRow(ctx, `UPDATE recompute_requests SET started_at = now() WHERE id = (
			SELECT id FROM recompute_requests WHERE started_at IS NULL ORDER BY requested_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, cluster, requested_at, requested_by`).Scan(&r.ID, &r.Cluster, &r.RequestedAt, &r.RequestedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	r.RequestedAt = r.RequestedAt.UTC()
	return r, err == nil, err
}

// FinishRecompute records the end of a claimed request's run.
func (s *Store) FinishRecompute(ctx context.Context, id uuid.UUID, errMsg string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE recompute_requests SET finished_at = now(), error = $2 WHERE id = $1`, id, errMsg)
	return err
}

// UnfinishedRecomputes counts the requests that are queued or running.
func (s *Store) UnfinishedRecomputes(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM recompute_requests WHERE finished_at IS NULL`).Scan(&n)
	return n, err
}

// PruneRecomputes deletes requests that finished before cutoff.
func (s *Store) PruneRecomputes(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM recompute_requests WHERE finished_at < $1`, cutoff)
	return tag.RowsAffected(), err
}

// Interrupted is what a new leader found unfinished from the previous one.
type Interrupted struct {
	Runs       int64 // recommender runs marked failed
	Recomputes int64 // recompute requests marked failed (and queued again)
}

// RecoverInterrupted is called by a new leader before it starts any work: the runs and recompute
// requests the previous leader left running cannot finish any more (only the leader runs them and
// the lock guarantees there is one). Runs are marked failed; each interrupted recompute request
// is marked failed and queued again under a new id with the same scope and requested_at (unless a
// request for that scope is queued already), so the request is still served.
func (s *Store) RecoverInterrupted(ctx context.Context, newID func() uuid.UUID) (Interrupted, error) {
	var out Interrupted
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE recommender_runs SET status = 'failed', finished_at = now(),
		error = 'interrupted: the hub leader changed during the run' WHERE status = 'running'`)
	if err != nil {
		return out, err
	}
	out.Runs = tag.RowsAffected()
	rows, err := tx.Query(ctx, `UPDATE recompute_requests SET finished_at = now(), error = 'interrupted: the hub leader changed during the run'
		WHERE started_at IS NOT NULL AND finished_at IS NULL RETURNING cluster, requested_at, requested_by`)
	if err != nil {
		return out, err
	}
	reqs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (RecomputeRequest, error) {
		var x RecomputeRequest
		return x, r.Scan(&x.Cluster, &x.RequestedAt, &x.RequestedBy)
	})
	if err != nil {
		return out, err
	}
	for _, r := range reqs {
		if _, err := tx.Exec(ctx, `INSERT INTO recompute_requests (id, cluster, requested_at, requested_by) VALUES ($1, $2, $3, $4)
			ON CONFLICT (cluster) WHERE started_at IS NULL DO NOTHING`, newID(), r.Cluster, r.RequestedAt, r.RequestedBy); err != nil {
			return out, err
		}
	}
	out.Recomputes = int64(len(reqs))
	return out, tx.Commit(ctx)
}

// LastRegularRunAsOf returns the as-of time of the latest successful (or partial) non-backfill run.
func (s *Store) LastRegularRunAsOf(ctx context.Context) (time.Time, bool, error) {
	var t *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT max(as_of) FROM recommender_runs WHERE NOT backfill AND status IN ('succeeded', 'partial')`).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, false, err
	}
	return t.UTC(), true, nil
}

// LastClusterRun is the latest successful non-backfill run of a cluster.
type LastClusterRun struct {
	Cluster         string
	FinishedAt      time.Time
	Recommendations int
	PricingFallback bool // the run used the default rates
}

// LastClusterRuns returns, per registered cluster, its latest successful non-backfill run (the
// kerf_recommender_* gauges a new leader starts from).
func (s *Store) LastClusterRuns(ctx context.Context) ([]LastClusterRun, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ON (rc.cluster) rc.cluster, rc.finished_at, rc.recommendations, rc.pricing
		FROM recommender_run_clusters rc JOIN recommender_runs r ON r.id = rc.run_id JOIN clusters c ON c.name = rc.cluster
		WHERE rc.status = 'succeeded' AND NOT r.backfill AND c.deleted_at IS NULL
		ORDER BY rc.cluster, rc.finished_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (LastClusterRun, error) {
		var x LastClusterRun
		var p []byte
		if err := r.Scan(&x.Cluster, &x.FinishedAt, &x.Recommendations, &p); err != nil {
			return x, err
		}
		var pr struct {
			Source string `json:"source"`
		}
		_ = json.Unmarshal(p, &pr)
		x.PricingFallback = pr.Source == "default"
		x.FinishedAt = x.FinishedAt.UTC()
		return x, nil
	})
}
