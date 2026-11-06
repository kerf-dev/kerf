// Package store is the hub's Postgres persistence (schema in db/migrations).
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/teams"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps a pgx pool.
type Store struct {
	Pool *pgxpool.Pool
	// OwnerLabel is the label key on workloads and namespaces that names their team (hub config
	// teams.ownerLabel; empty = "team").
	OwnerLabel string
}

// Open connects to Postgres.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

// Close closes the pool.
func (s *Store) Close() { s.Pool.Close() }

// Migrate applies the goose migrations in fsys (db/migrations) under goose's Postgres session
// locker: hub replicas starting together apply each migration once; the others wait
// (up to 10 minutes) for the lock and then find nothing left to apply.
func (s *Store) Migrate(ctx context.Context, fsys fs.FS) ([]string, error) {
	db := stdlib.OpenDBFromPool(s.Pool)
	defer db.Close()
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 600))
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, err
	}
	res, err := p.Up(ctx)
	var applied []string
	for _, r := range res {
		applied = append(applied, r.Source.Path)
	}
	return applied, err
}

// SQLDB returns a database/sql handle (tests).
func (s *Store) SQLDB() *sql.DB { return stdlib.OpenDBFromPool(s.Pool) }

// ---- clusters and tokens ----

// Token is an authenticated API token.
type Token struct {
	ID      int64
	Role    string
	Cluster string
}

// HashToken returns the SHA-256 of a token.
func HashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

// LookupToken resolves a bearer token. Revoked and expired tokens are not found.
func (s *Store) LookupToken(ctx context.Context, tok string) (Token, error) {
	var t Token
	var cluster *string
	err := s.Pool.QueryRow(ctx, `SELECT id, role, cluster FROM api_tokens
		WHERE token_sha256 = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`,
		HashToken(tok)).Scan(&t.ID, &t.Role, &cluster)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if cluster != nil {
		t.Cluster = *cluster
	}
	return t, err
}

// EnsureCluster registers a cluster if missing.
func (s *Store) EnsureCluster(ctx context.Context, name, description string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO clusters (name, description) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`, name, description)
	return err
}

// EnsureToken stores the hash of tok with role and cluster if not already present.
func (s *Store) EnsureToken(ctx context.Context, tok, role, cluster, description string) error {
	var c *string
	if cluster != "" {
		c = &cluster
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO api_tokens (token_sha256, role, cluster, description) VALUES ($1, $2, $3, $4)
		ON CONFLICT (token_sha256) DO NOTHING`, HashToken(tok), role, c, description)
	return err
}

// Cluster is a registered cluster.
type Cluster struct {
	Name         string
	Description  string
	CreatedAt    time.Time
	LastSeenAt   *time.Time
	AgentVersion string
}

// Clusters lists registered (not deleted) clusters by name.
func (s *Store) Clusters(ctx context.Context) ([]Cluster, error) {
	rows, err := s.Pool.Query(ctx, `SELECT name, description, created_at, last_seen_at, coalesce(agent_version, '') FROM clusters WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Cluster, error) {
		var c Cluster
		err := r.Scan(&c.Name, &c.Description, &c.CreatedAt, &c.LastSeenAt, &c.AgentVersion)
		return c, err
	})
}

// ClusterExists reports whether a cluster is registered.
func (s *Store) ClusterExists(ctx context.Context, name string) (bool, error) {
	var ok bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM clusters WHERE name = $1)`, name).Scan(&ok)
	return ok, err
}

// Heartbeat records an authenticated agent request.
func (s *Store) Heartbeat(ctx context.Context, cluster, agentVersion string) error {
	return s.HeartbeatAt(ctx, cluster, agentVersion, time.Time{})
}

// HeartbeatAt records an agent request at a given time (the hub's clock, which the stale check
// also uses); zero means the database clock.
func (s *Store) HeartbeatAt(ctx context.Context, cluster, agentVersion string, at time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE clusters SET last_seen_at = coalesce($3, now()),
		agent_version = coalesce(nullif($2, ''), agent_version) WHERE name = $1`, cluster, agentVersion, nullTime(at))
	return err
}

// ---- workloads ----

// Attributes are stored in workloads.attributes.
type Attributes struct {
	Containers      []string           `json:"containers,omitempty"`
	AverageReplicas float64            `json:"averageReplicas"`
	HPA             *recommender.HPA   `json:"hpa,omitempty"`
	Used24h         map[string]float64 `json:"used24h,omitempty"` // cpu (cores), memory (bytes)
	// Live: the workload had pods at the end of the latest regular run's data. Absent in
	// attributes written before workload pruning, which count as live.
	Live bool `json:"live"`
}

// liveSQL is true for a workload with pods in the latest metrics: Attributes.Live, or attributes
// written before workload pruning. A workload only an agent reported (empty attributes) is not live.
const liveSQL = `(attributes @> '{"live": true}'::jsonb OR (attributes <> '{}'::jsonb AND NOT attributes ? 'live'))`

// UpsertInventory records the workloads found by a run. When markDeleted is true, live workloads
// of the cluster that are not in inv get deleted_at = seenAt. Returns workload ids by key.
func (s *Store) UpsertInventory(ctx context.Context, cluster string, inv []recommender.WorkloadInventory, seenAt time.Time, markDeleted bool) (map[hubapi.WorkloadKey]int64, error) {
	return s.UpsertInventoryAt(ctx, cluster, inv, seenAt, markDeleted, seenAt)
}

// UpsertInventoryAt is UpsertInventory for an inventory whose metrics end at dataEnd. A regular
// run revives deleted workloads found in its inventory, except those deleted because the agent no
// longer reports them (PruneAbsent) that have no pods now: the inventory spans the whole profile
// window, so it still lists workloads deleted days ago. A workload
// no inventory has seen yet (created by an agent status report, empty attributes) is only marked
// deleted when it was reported before dataEnd: one reported after the run's data is kept until a
// run can see it.
func (s *Store) UpsertInventoryAt(ctx context.Context, cluster string, inv []recommender.WorkloadInventory, seenAt time.Time, markDeleted bool, dataEnd time.Time) (map[hubapi.WorkloadKey]int64, error) {
	ids := map[hubapi.WorkloadKey]int64{}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	batch := &pgx.Batch{}
	for _, w := range inv {
		attrs, _ := json.Marshal(Attributes{
			Containers: w.Containers, AverageReplicas: w.AverageReplicas, HPA: w.HPA,
			Used24h: map[string]float64{"cpu": w.Used24hCPU, "memory": w.Used24hMemory}, Live: w.Live,
		})
		k := w.Key
		batch.Queue(`INSERT INTO workloads (cluster, namespace, kind, name, first_seen_at, last_seen_at, attributes)
			VALUES ($1, $2, $3, $4, $5, $5, $6)
			ON CONFLICT (cluster, namespace, kind, name) DO UPDATE SET
				first_seen_at = least(workloads.first_seen_at, excluded.first_seen_at),
				last_seen_at  = CASE WHEN $7 THEN greatest(workloads.last_seen_at, excluded.last_seen_at) ELSE workloads.last_seen_at END,
				deleted_at    = CASE WHEN $7 AND (workloads.deleted_by IS DISTINCT FROM 'agent' OR $8) THEN NULL ELSE workloads.deleted_at END,
				deleted_by    = CASE WHEN $7 AND (workloads.deleted_by IS DISTINCT FROM 'agent' OR $8) THEN NULL ELSE workloads.deleted_by END,
				attributes    = CASE WHEN $7 OR workloads.attributes = '{}'::jsonb THEN excluded.attributes ELSE workloads.attributes END
			RETURNING id`, cluster, k.Namespace, k.Kind, k.Name, seenAt, attrs, markDeleted, w.Live).QueryRow(func(r pgx.Row) error {
			var id int64
			if err := r.Scan(&id); err != nil {
				return err
			}
			ids[k] = id
			return nil
		})
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return nil, err
	}
	// New workloads get their team (from the namespace until the agent reports labels).
	if err := s.recomputeTeams(ctx, tx, cluster, nil); err != nil {
		return nil, err
	}
	if markDeleted {
		live := make([]int64, 0, len(ids))
		for _, id := range ids {
			live = append(live, id)
		}
		if _, err := tx.Exec(ctx, `UPDATE workloads SET deleted_at = $2, deleted_by = 'metrics'
			WHERE cluster = $1 AND deleted_at IS NULL AND NOT (id = ANY($3))
			  AND (attributes <> '{}'::jsonb OR last_seen_at < $4)`, cluster, seenAt, live, dataEnd); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit(ctx)
}

// ---- recommendations ----

// InsertRecommendations stores one history row per result.
func (s *Store) InsertRecommendations(ctx context.Context, runID uuid.UUID, ids map[hubapi.WorkloadKey]int64, cluster string, results []recommender.Result) error {
	batch := &pgx.Batch{}
	for _, r := range results {
		rec := r.Recommendation
		k := hubapi.WorkloadKey{Cluster: cluster, Namespace: rec.Workload.Namespace, Kind: rec.Workload.Kind, Name: rec.Workload.Name}
		wid, ok := ids[k]
		if !ok {
			return fmt.Errorf("workload %s missing from inventory", k.ID())
		}
		settings, _ := json.Marshal(rec.Settings)
		containers, _ := json.Marshal(rec.Containers)
		coverage, _ := json.Marshal(rec.DataCoverage)
		pricing, _ := json.Marshal(r.Pricing)
		var hpa, gpu []byte
		if rec.HPA != nil {
			hpa, _ = json.Marshal(rec.HPA)
		}
		if rec.GPU != nil {
			gpu, _ = json.Marshal(rec.GPU)
		}
		batch.Queue(`INSERT INTO recommendations (id, workload_id, run_id, generated_at, strategy_name, settings, containers,
			confidence, risk_flags, data_coverage, average_replicas, monthly_savings_cpu_usd, monthly_savings_memory_usd, pricing, hpa, gpu)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			rec.ID, wid, runID, rec.GeneratedAt, rec.StrategyName, settings, containers, rec.Confidence, rec.RiskFlags,
			coverage, rec.AverageReplicas, rec.MonthlySavingsUSD.CPU, rec.MonthlySavingsUSD.Memory, pricing, hpa, gpu)
	}
	return s.Pool.SendBatch(ctx, batch).Close()
}

const recColumns = `r.id, r.generated_at, r.strategy_name, r.settings, r.containers, r.confidence, r.risk_flags,
	r.data_coverage, r.average_replicas, r.monthly_savings_cpu_usd, r.monthly_savings_memory_usd, r.pricing, r.hpa, r.gpu`

type recRow struct {
	id                             uuid.UUID
	generatedAt                    time.Time
	strategy                       string
	settings, containers, coverage []byte
	confidence                     float64
	flags                          []string
	replicas, cpuSav, memSav       float64
	pricing, hpa, gpu              []byte
}

func (r *recRow) dest() []any {
	return []any{&r.id, &r.generatedAt, &r.strategy, &r.settings, &r.containers, &r.confidence, &r.flags,
		&r.coverage, &r.replicas, &r.cpuSav, &r.memSav, &r.pricing, &r.hpa, &r.gpu}
}

func (r *recRow) build(ref hubapi.WorkloadRef) (*hubapi.WorkloadRecommendation, error) {
	rec := hubapi.WorkloadRecommendation{
		ID: r.id.String(), Workload: ref, GeneratedAt: r.generatedAt.UTC(), StrategyName: r.strategy,
		Confidence: r.confidence, RiskFlags: r.flags, AverageReplicas: r.replicas,
		MonthlySavingsUSD: hubapi.Savings{CPU: r.cpuSav, Memory: r.memSav, Total: round2(r.cpuSav + r.memSav)},
	}
	if err := errors.Join(json.Unmarshal(r.settings, &rec.Settings), json.Unmarshal(r.containers, &rec.Containers),
		json.Unmarshal(r.coverage, &rec.DataCoverage)); err != nil {
		return nil, err
	}
	if rec.RiskFlags == nil {
		rec.RiskFlags = []string{}
	}
	// Rows from before D-016 store '{}': no pricing in the response.
	var p hubapi.RecommendationPricing
	if json.Unmarshal(r.pricing, &p) == nil && p.Source != "" {
		rec.Pricing = &p
	}
	if len(r.hpa) > 0 {
		var h hubapi.HPACoordination
		if err := json.Unmarshal(r.hpa, &h); err != nil {
			return nil, err
		}
		rec.HPA = &h
	}
	if len(r.gpu) > 0 {
		var g hubapi.GPUUsage
		if err := json.Unmarshal(r.gpu, &g); err != nil {
			return nil, err
		}
		rec.GPU = &g
	}
	return &rec, nil
}

// nullRecRow scans a LEFT JOINed recommendation.
type nullRecRow struct {
	id                             *uuid.UUID
	generatedAt                    *time.Time
	strategy                       *string
	settings, containers, coverage []byte
	confidence                     *float64
	flags                          []string
	replicas, cpuSav, memSav       *float64
	pricing, hpa, gpu              []byte
}

func (r *nullRecRow) dest() []any {
	return []any{&r.id, &r.generatedAt, &r.strategy, &r.settings, &r.containers, &r.confidence, &r.flags,
		&r.coverage, &r.replicas, &r.cpuSav, &r.memSav, &r.pricing, &r.hpa, &r.gpu}
}

func (r *nullRecRow) row() *recRow {
	if r.id == nil {
		return nil
	}
	return &recRow{id: *r.id, generatedAt: *r.generatedAt, strategy: deref(r.strategy), settings: r.settings,
		containers: r.containers, coverage: r.coverage, confidence: *r.confidence, flags: r.flags,
		replicas: *r.replicas, cpuSav: *r.cpuSav, memSav: *r.memSav, pricing: r.pricing, hpa: r.hpa, gpu: r.gpu}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// WorkloadView is a live workload with its latest recommendation and status.
type WorkloadView struct {
	DBID int64
	Key  hubapi.WorkloadKey
	// Team owns the workload ("" = Unassigned).
	Team        string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Attributes  Attributes
	Rec         *hubapi.WorkloadRecommendation // with explanations
	Status      *hubapi.WorkloadStatus
}

// Modes returns the effective modes (defaults when never reported).
func (v *WorkloadView) Modes() hubapi.EffectiveModes {
	if v.Status == nil {
		return hubapi.DefaultModes()
	}
	return v.Status.Modes
}

// Phase returns the phase (Pending when never reported).
func (v *WorkloadView) Phase() string {
	if v.Status == nil {
		return hubapi.PhasePending
	}
	return v.Status.Phase
}

// WorkloadFilter restricts LoadWorkloads.
type WorkloadFilter struct {
	Cluster string
	Key     *hubapi.WorkloadKey
}

// LoadWorkloads returns live workloads with their latest recommendation and status. Only the
// workloads of the caller's teams are returned (teams.WithScope): every list, count and total built
// from them is scoped.
func (s *Store) LoadWorkloads(ctx context.Context, f WorkloadFilter) ([]*WorkloadView, error) {
	// Workloads of deleted clusters are hidden like deleted workloads.
	where := "w.deleted_at IS NULL AND w.cluster NOT IN (SELECT name FROM clusters WHERE deleted_at IS NOT NULL)"
	args := []any{}
	if f.Key != nil {
		// A single workload is returned even when deleted (detail pages of old links).
		where = "w.cluster = $1 AND w.namespace = $2 AND w.kind = $3 AND w.name = $4"
		args = append(args, f.Key.Cluster, f.Key.Namespace, f.Key.Kind, f.Key.Name)
	} else if f.Cluster != "" {
		where += " AND w.cluster = $1"
		args = append(args, f.Cluster)
	}
	where += " AND " + teams.SQL(ctx, "w.team", &args)
	rows, err := s.Pool.Query(ctx, `SELECT w.id, w.cluster, w.namespace, w.kind, w.name, coalesce(w.team, ''), w.first_seen_at, w.last_seen_at, w.attributes,
		`+recColumns+`,
		ws.workload_id IS NOT NULL, ws.recommendation_id, ws.policy, ws.cpu_mode, ws.cpu_mode_source, ws.memory_mode, ws.memory_mode_source,
		ws.strategy, ws.settings, ws.phase, ws.reason, ws.message, ws.live, ws.applied_at, ws.observed_at, ws.next_window
		FROM workloads w
		LEFT JOIN LATERAL (SELECT * FROM recommendations rr WHERE rr.workload_id = w.id ORDER BY rr.generated_at DESC, rr.id DESC LIMIT 1) r ON true
		LEFT JOIN workload_status ws ON ws.workload_id = w.id
		WHERE `+where+` ORDER BY w.cluster COLLATE "C", w.namespace COLLATE "C", w.kind COLLATE "C", w.name COLLATE "C"`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WorkloadView
	for rows.Next() {
		v := &WorkloadView{}
		var (
			attrs                               []byte
			hasStatus                           bool
			rec                                 nullRecRow
			sRecID                              *uuid.UUID
			sPolicy, sCPUMode, sMemMode, sPhase *string
			sCPUSrc, sMemSrc, sStrategy         *string
			sReason, sMessage                   *string
			sSettings, sLive                    []byte
			sApplied, sObserved, sNextWindow    *time.Time
		)
		dest := []any{&v.DBID, &v.Key.Cluster, &v.Key.Namespace, &v.Key.Kind, &v.Key.Name, &v.Team, &v.FirstSeenAt, &v.LastSeenAt, &attrs}
		dest = append(dest, rec.dest()...)
		dest = append(dest, &hasStatus, &sRecID, &sPolicy, &sCPUMode, &sCPUSrc, &sMemMode, &sMemSrc, &sStrategy, &sSettings, &sPhase,
			&sReason, &sMessage, &sLive, &sApplied, &sObserved, &sNextWindow)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(attrs, &v.Attributes)
		v.FirstSeenAt, v.LastSeenAt = v.FirstSeenAt.UTC(), v.LastSeenAt.UTC()
		if rr := rec.row(); rr != nil {
			r, err := rr.build(v.Key.Ref())
			if err != nil {
				return nil, err
			}
			v.Rec = r
		}
		if hasStatus {
			st := &hubapi.WorkloadStatus{
				Workload: v.Key.Ref(), Policy: deref(sPolicy), Strategy: deref(sStrategy), Phase: deref(sPhase),
				Reason: deref(sReason), Message: deref(sMessage),
				Modes: hubapi.EffectiveModes{CPU: deref(sCPUMode), Memory: deref(sMemMode), CPUSource: deref(sCPUSrc), MemorySource: deref(sMemSrc)},
			}
			if sRecID != nil {
				st.RecommendationID = sRecID.String()
			}
			var vs hubapi.ValueSettings
			if json.Unmarshal(sSettings, &vs) == nil && vs != (hubapi.ValueSettings{}) {
				st.Settings = &vs
			}
			_ = json.Unmarshal(sLive, &st.Live)
			st.JVM = statusJVM(sSettings)
			st.Override = statusOverride(sSettings)
			st.AppliedAt, st.ObservedAt, st.NextWindow = utc(sApplied), utc(sObserved), utc(sNextWindow)
			v.Status = st
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// RecommendationHistory returns a workload's recommendations in [from, to), newest first.
func (s *Store) RecommendationHistory(ctx context.Context, workloadID int64, ref hubapi.WorkloadRef, from, to time.Time, limit int) ([]hubapi.WorkloadRecommendation, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+recColumns+` FROM recommendations r
		WHERE r.workload_id = $1 AND r.generated_at >= $2 AND r.generated_at < $3 ORDER BY r.generated_at DESC, r.id DESC LIMIT $4`,
		workloadID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hubapi.WorkloadRecommendation{}
	for rows.Next() {
		var rr recRow
		if err := rows.Scan(rr.dest()...); err != nil {
			return nil, err
		}
		rec, err := rr.build(ref)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// RecommendedPoint is a recommendation's recommended requests per container at a time.
type RecommendedPoint struct {
	At         time.Time
	Containers []hubapi.ContainerRecommendation
}

// RecommendationSteps returns the recommendations of a workload that were current in [from, to]:
// the latest one before from plus all inside, oldest first.
func (s *Store) RecommendationSteps(ctx context.Context, workloadID int64, from, to time.Time) ([]RecommendedPoint, error) {
	rows, err := s.Pool.Query(ctx, `(SELECT generated_at, containers, id FROM recommendations WHERE workload_id = $1 AND generated_at < $2 ORDER BY generated_at DESC, id DESC LIMIT 1)
		UNION ALL
		(SELECT generated_at, containers, id FROM recommendations WHERE workload_id = $1 AND generated_at >= $2 AND generated_at <= $3)
		ORDER BY 1, 3`, workloadID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (RecommendedPoint, error) {
		var p RecommendedPoint
		var c []byte
		var id uuid.UUID
		if err := r.Scan(&p.At, &c, &id); err != nil {
			return p, err
		}
		return p, json.Unmarshal(c, &p.Containers)
	})
}

// PruneHistory deletes recommendations older than fullResolution except the newest per workload
// and day, deletes everything older than retention except the newest per workload, and deletes
// old runs.
func (s *Store) PruneHistory(ctx context.Context, now time.Time, fullResolution, retention time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM recommendations r
		WHERE r.generated_at < $1
		  AND r.id NOT IN (SELECT DISTINCT ON (workload_id) id FROM recommendations ORDER BY workload_id, generated_at DESC, id DESC)
		  AND (r.generated_at < $2 OR r.id NOT IN (
		        SELECT DISTINCT ON (workload_id, date_trunc('day', generated_at)) id FROM recommendations
		        WHERE generated_at < $1 ORDER BY workload_id, date_trunc('day', generated_at), generated_at DESC, id DESC))`,
		now.Add(-fullResolution), now.Add(-retention))
	if err != nil {
		return 0, err
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM recommender_runs WHERE as_of < $1`, now.Add(-retention)); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- runs ----

// StartRun records a new run.
func (s *Store) StartRun(ctx context.Context, id uuid.UUID, asOf time.Time, backfill bool) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO recommender_runs (id, started_at, as_of, backfill, status) VALUES ($1, now(), $2, $3, 'running')`, id, asOf, backfill)
	return err
}

// FinishRun records the end of a run.
func (s *Store) FinishRun(ctx context.Context, id uuid.UUID, status, errMsg string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE recommender_runs SET finished_at = now(), status = $2, error = $3 WHERE id = $1`, id, status, errMsg)
	return err
}

// RunCluster is the per-cluster result of a run.
type RunCluster struct {
	RunID           uuid.UUID
	Cluster         string
	AsOf            time.Time
	Status          string
	Error           string
	Workloads       int
	Recommendations int
	Savings         hubapi.Savings
	Pricing         recommender.Pricing
	// FinishedAt defaults to the database clock.
	FinishedAt time.Time
}

// RecordRunCluster stores the per-cluster outcome of a run.
func (s *Store) RecordRunCluster(ctx context.Context, rc RunCluster) error {
	p, _ := json.Marshal(rc.Pricing)
	_, err := s.Pool.Exec(ctx, `INSERT INTO recommender_run_clusters (run_id, cluster, as_of, finished_at, status, error, workloads,
		recommendations, monthly_savings_cpu_usd, monthly_savings_memory_usd, pricing)
		VALUES ($1, $2, $3, coalesce($11, now()), $4, $5, $6, $7, $8, $9, $10)`,
		rc.RunID, rc.Cluster, rc.AsOf, rc.Status, rc.Error, rc.Workloads, rc.Recommendations, rc.Savings.CPU, rc.Savings.Memory, p,
		nullTime(rc.FinishedAt))
	return err
}

// RunInfo identifies a run.
type RunInfo struct {
	ID     uuid.UUID
	AsOf   time.Time
	Status string
	// RepricingNeeded: a cluster of the run used the default rates although the cluster's latest
	// regular run had pricing series (backfills from before the series existed).
	RepricingNeeded bool
}

// BackfillRuns lists the backfill runs, oldest first.
func (s *Store) BackfillRuns(ctx context.Context) ([]RunInfo, error) {
	rows, err := s.Pool.Query(ctx, `WITH latest AS (
			SELECT DISTINCT ON (rc.cluster) rc.cluster, rc.pricing->>'source' AS source
			FROM recommender_run_clusters rc JOIN recommender_runs r ON r.id = rc.run_id
			WHERE NOT r.backfill AND rc.status = 'succeeded' ORDER BY rc.cluster, rc.finished_at DESC)
		SELECT r.id, r.as_of, r.status, EXISTS (
			SELECT 1 FROM recommender_run_clusters rc JOIN latest l ON l.cluster = rc.cluster
			WHERE rc.run_id = r.id AND rc.status = 'succeeded' AND rc.pricing->>'source' = 'default' AND l.source = 'metrics')
		FROM recommender_runs r WHERE r.backfill ORDER BY r.as_of, r.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (RunInfo, error) {
		var x RunInfo
		err := r.Scan(&x.ID, &x.AsOf, &x.Status, &x.RepricingNeeded)
		x.AsOf = x.AsOf.UTC()
		return x, err
	})
}

// LastMetricsPricing returns the rates of the cluster's latest successful run since since whose
// pricing came from the pricing series.
func (s *Store) LastMetricsPricing(ctx context.Context, cluster string, since time.Time) (recommender.Pricing, bool, error) {
	var b []byte
	err := s.Pool.QueryRow(ctx, `SELECT rc.pricing FROM recommender_run_clusters rc
		WHERE rc.cluster = $1 AND rc.status = 'succeeded' AND rc.finished_at >= $2 AND rc.pricing->>'source' = 'metrics'
		ORDER BY rc.finished_at DESC LIMIT 1`, cluster, since).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return recommender.Pricing{}, false, nil
	}
	if err != nil {
		return recommender.Pricing{}, false, err
	}
	var p recommender.Pricing
	if err := json.Unmarshal(b, &p); err != nil {
		return recommender.Pricing{}, false, err
	}
	// The run stored the adjusted rates; the fallback needs the rates before the hub's settings.
	if p = p.Base(); p.CPUCoreHour <= 0 || p.MemoryGiBHour <= 0 {
		return recommender.Pricing{}, false, nil
	}
	return p, true, nil
}

// LiveInventory returns the number of live workloads of a cluster that came from the inventory
// (not only from agent reports).
func (s *Store) LiveInventory(ctx context.Context, cluster string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM workloads WHERE cluster = $1 AND deleted_at IS NULL AND attributes <> '{}'::jsonb`, cluster).Scan(&n)
	return n, err
}

// FirstRegularRun returns the as-of time of the first successful non-backfill run.
func (s *Store) FirstRegularRun(ctx context.Context) (time.Time, bool, error) {
	var t *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT min(as_of) FROM recommender_runs WHERE NOT backfill AND status IN ('succeeded', 'partial')`).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, false, err
	}
	return t.UTC(), true, nil
}

// DeleteRuns deletes runs with their per-cluster results and their recommendations, and returns
// the number of recommendations deleted.
func (s *Store) DeleteRuns(ctx context.Context, ids []uuid.UUID) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM recommendations WHERE run_id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM recommender_runs WHERE id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// HasSucceededRun reports whether any run succeeded (used to decide on a dev backfill).
func (s *Store) HasSucceededRun(ctx context.Context) (bool, error) {
	var ok bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recommender_runs WHERE status IN ('succeeded', 'partial'))`).Scan(&ok)
	return ok, err
}

// LastRunPerCluster returns the finish time of the last successful non-backfill run per cluster.
func (s *Store) LastRunPerCluster(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.Pool.Query(ctx, `SELECT rc.cluster, max(rc.finished_at) FROM recommender_run_clusters rc
		JOIN recommender_runs r ON r.id = rc.run_id WHERE rc.status = 'succeeded' AND NOT r.backfill GROUP BY rc.cluster`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var c string
		var t time.Time
		if err := rows.Scan(&c, &t); err != nil {
			return nil, err
		}
		out[c] = t.UTC()
	}
	return out, rows.Err()
}

// SavingsSample is the savings total of one cluster from the moment At on.
type SavingsSample struct {
	Cluster string
	// At is when the total became current: the end of a regular run, or the as-of time of a
	// backfill run (which recomputes the past).
	At     time.Time
	CPU    float64
	Memory float64
}

// SavingsSamples returns per-cluster run totals with At <= to, including the last one before
// from for each cluster, ordered by At.
func (s *Store) SavingsSamples(ctx context.Context, clusters []string, from, to time.Time) ([]SavingsSample, error) {
	if clusters == nil {
		clusters = []string{}
	}
	rows, err := s.Pool.Query(ctx, `WITH rc AS (
			SELECT rc.cluster, CASE WHEN r.backfill THEN rc.as_of ELSE rc.finished_at END AS at,
				rc.monthly_savings_cpu_usd AS cpu, rc.monthly_savings_memory_usd AS mem
			FROM recommender_run_clusters rc JOIN recommender_runs r ON r.id = rc.run_id
			WHERE rc.status = 'succeeded' AND (cardinality($3::text[]) = 0 OR rc.cluster = ANY($3::text[])))
		SELECT cluster, at, cpu, mem FROM (
			SELECT cluster, at, cpu, mem FROM rc WHERE at >= $1 AND at <= $2
			UNION ALL
			(SELECT DISTINCT ON (cluster) cluster, at, cpu, mem FROM rc WHERE at < $1 ORDER BY cluster, at DESC)
		) x ORDER BY at, cluster`, from, to, clusters)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SavingsSample, error) {
		var x SavingsSample
		err := r.Scan(&x.Cluster, &x.At, &x.CPU, &x.Memory)
		return x, err
	})
}

// ---- agent reports ----

// UpsertStatuses stores a batch of workload statuses.
func (s *Store) UpsertStatuses(ctx context.Context, cluster string, items []hubapi.WorkloadStatus, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// One statement per item (workload upsert + status upsert), all sent in one batch.
	batch := &pgx.Batch{}
	for _, it := range items {
		settings := statusSettings(it)
		live := []byte("[]")
		if it.Live != nil {
			live, _ = json.Marshal(it.Live)
		}
		observed := now
		if it.ObservedAt != nil {
			observed = *it.ObservedAt
		}
		// Labels: nil = not reported (an older agent), the stored ones stay.
		var labels, nsLabels []byte
		if it.Labels != nil {
			labels, _ = json.Marshal(it.Labels)
		}
		if it.NamespaceLabels != nil {
			nsLabels, _ = json.Marshal(it.NamespaceLabels)
		}
		// A workload the agent reports exists: one PruneAbsent deleted is revived.
		batch.Queue(`WITH w AS (
				INSERT INTO workloads (cluster, namespace, kind, name, first_seen_at, last_seen_at, labels, namespace_labels)
				VALUES ($1, $2, $3, $4, $20, $20, coalesce($21::jsonb, '{}'::jsonb), coalesce($22::jsonb, '{}'::jsonb))
				ON CONFLICT (cluster, namespace, kind, name) DO UPDATE SET
					deleted_at = CASE WHEN workloads.deleted_by = 'agent' THEN NULL ELSE workloads.deleted_at END,
					deleted_by = CASE WHEN workloads.deleted_by = 'agent' THEN NULL ELSE workloads.deleted_by END,
					labels = coalesce($21::jsonb, workloads.labels),
					namespace_labels = coalesce($22::jsonb, workloads.namespace_labels)
				RETURNING id)
			INSERT INTO workload_status (workload_id, recommendation_id, policy, cpu_mode, cpu_mode_source, memory_mode,
				memory_mode_source, strategy, settings, phase, reason, message, live, applied_at, observed_at, next_window, updated_at)
			SELECT w.id, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20 FROM w
			ON CONFLICT (workload_id) DO UPDATE SET recommendation_id = excluded.recommendation_id, policy = excluded.policy,
				cpu_mode = excluded.cpu_mode, cpu_mode_source = excluded.cpu_mode_source, memory_mode = excluded.memory_mode,
				memory_mode_source = excluded.memory_mode_source, strategy = excluded.strategy, settings = excluded.settings,
				phase = excluded.phase, reason = excluded.reason, message = excluded.message, live = excluded.live,
				applied_at = excluded.applied_at, observed_at = excluded.observed_at, next_window = excluded.next_window, updated_at = $20`,
			cluster, it.Workload.Namespace, it.Workload.Kind, it.Workload.Name,
			nullUUID(it.RecommendationID), it.Policy, it.Modes.CPU, nullStr(it.Modes.CPUSource), it.Modes.Memory,
			nullStr(it.Modes.MemorySource), nullStr(it.Strategy), settings, it.Phase, it.Reason, it.Message, live, it.AppliedAt, observed, it.NextWindow, now,
			labels, nsLabels)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return err
	}
	// The reported labels may move workloads to another team.
	if err := s.recomputeReported(ctx, tx, cluster, items); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordPull records a recommendation pull by the cluster's agent at now and returns the two
// pulls before it (nil when unknown). Pulls less than minGap after the last recorded one (pages
// of one pull, retries) are not recorded; recorded is false then.
func (s *Store) RecordPull(ctx context.Context, cluster string, now time.Time, minGap time.Duration) (last, prev *time.Time, recorded bool, err error) {
	err = s.Pool.QueryRow(ctx, `WITH old AS (SELECT agent_pull_at, agent_prev_pull_at FROM clusters WHERE name = $1 FOR UPDATE)
		UPDATE clusters c SET agent_prev_pull_at = old.agent_pull_at, agent_pull_at = $2::timestamptz
		FROM old WHERE c.name = $1 AND (old.agent_pull_at IS NULL OR old.agent_pull_at <= $2::timestamptz - make_interval(secs => $3::float8))
		RETURNING old.agent_pull_at, old.agent_prev_pull_at`, cluster, now, minGap.Seconds()).Scan(&last, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, false, nil
	}
	return last, prev, err == nil, err
}

// PruneAbsent soft-deletes the live workloads of an agent-backed cluster that the agent no longer
// reports. The agent sends a full status batch after every pull; last and prev are the
// two pulls before the current one. A workload is deleted when
//   - it was not reported since prev, so it is missing from the full batches after prev and last,
//   - the full batch after last arrived (some workload of the cluster was reported since last),
//   - it has no pods in the latest metrics (liveSQL) and existed before prev.
//
// It keeps its history, status and audit; a status report or pods in a later run revive it.
// Returns the keys deleted.
func (s *Store) PruneAbsent(ctx context.Context, cluster string, last, prev, now time.Time) ([]hubapi.WorkloadKey, error) {
	rows, err := s.Pool.Query(ctx, `UPDATE workloads w SET deleted_at = $4, deleted_by = 'agent'
		WHERE w.cluster = $1 AND w.deleted_at IS NULL AND w.first_seen_at < $3 AND NOT `+liveSQL+`
		  AND NOT EXISTS (SELECT 1 FROM workload_status ws WHERE ws.workload_id = w.id AND ws.updated_at >= $3)
		  AND EXISTS (SELECT 1 FROM workload_status ws JOIN workloads x ON x.id = ws.workload_id
		              WHERE x.cluster = $1 AND ws.updated_at >= $2)
		RETURNING w.namespace, w.kind, w.name`, cluster, last, prev, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hubapi.WorkloadKey
	for rows.Next() {
		k := hubapi.WorkloadKey{Cluster: cluster}
		if err := rows.Scan(&k.Namespace, &k.Kind, &k.Name); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ReportedSettings returns the settings the agent reported per workload of a cluster.
func (s *Store) ReportedSettings(ctx context.Context, cluster string) (map[hubapi.WorkloadKey]*hubapi.ValueSettings, error) {
	rows, err := s.Pool.Query(ctx, `SELECT w.namespace, w.kind, w.name, ws.settings FROM workload_status ws
		JOIN workloads w ON w.id = ws.workload_id WHERE w.cluster = $1`, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[hubapi.WorkloadKey]*hubapi.ValueSettings{}
	for rows.Next() {
		k := hubapi.WorkloadKey{Cluster: cluster}
		var b []byte
		if err := rows.Scan(&k.Namespace, &k.Kind, &k.Name, &b); err != nil {
			return nil, err
		}
		var vs hubapi.ValueSettings
		if json.Unmarshal(b, &vs) == nil && vs != (hubapi.ValueSettings{}) {
			out[k] = &vs
		}
	}
	return out, rows.Err()
}

// InsertEvents appends events; already stored ids are duplicates. Returns the stored events.
func (s *Store) InsertEvents(ctx context.Context, cluster string, items []hubapi.Event) (stored []hubapi.Event, duplicates int, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, e := range items {
		containers := []byte("[]")
		if e.Containers != nil {
			containers, _ = json.Marshal(e.Containers)
		}
		details := []byte("{}")
		if e.Details != nil {
			details, _ = json.Marshal(e.Details)
		}
		resources := e.Resources
		if resources == nil {
			resources = []string{}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO audit_events (id, cluster, namespace, workload_kind, workload_name, workload_id, type, reason,
			time, recommendation_id, strategy, resources, containers, message, details)
			VALUES ($1, $2, $3, $4, $5, (SELECT id FROM workloads WHERE cluster = $2 AND namespace = $3 AND kind = $4 AND name = $5),
				$6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (id) DO NOTHING`,
			e.ID, cluster, e.Workload.Namespace, e.Workload.Kind, e.Workload.Name, e.Type, e.Reason, e.Time,
			nullUUID(e.RecommendationID), nullStr(e.Strategy), resources, containers, e.Message, details)
		if err != nil {
			return nil, 0, err
		}
		if tag.RowsAffected() == 0 {
			duplicates++
		} else {
			stored = append(stored, e)
		}
	}
	return stored, duplicates, tx.Commit(ctx)
}

// AuditQuery filters audit events.
type AuditQuery struct {
	Cluster, Namespace, Kind, Workload string
	Types                              []string
	From, To                           *time.Time
	Desc                               bool
	Limit                              int
	// After is the keyset cursor (time, id) of the last item of the previous page.
	AfterTime *time.Time
	AfterID   string
}

// QueryAudit returns events and whether more exist.
func (s *Store) QueryAudit(ctx context.Context, q AuditQuery) ([]hubapi.AuditEvent, bool, error) {
	// Configuration changes are served by QueryConfigChanges (GET /v1/audit/config), not here.
	conds := []string{"e.type <> 'config_changed'"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if q.Cluster != "" {
		add("e.cluster = $%d", q.Cluster)
	}
	if q.Namespace != "" {
		add("e.namespace = $%d", q.Namespace)
	}
	if q.Kind != "" {
		add("e.workload_kind = $%d", q.Kind)
	}
	if q.Workload != "" {
		add("e.workload_name = $%d", q.Workload)
	}
	if len(q.Types) > 0 {
		add("e.type = ANY($%d)", q.Types)
	}
	// A team-scoped caller sees the events of its teams' workloads only.
	conds = append(conds, workloadIDsInScope(ctx, "e.workload_id", &args))
	if q.From != nil {
		add("e.time >= $%d", *q.From)
	}
	if q.To != nil {
		add("e.time < $%d", *q.To)
	}
	order := "ASC"
	cmp := ">"
	if q.Desc {
		order, cmp = "DESC", "<"
	}
	if q.AfterTime != nil {
		args = append(args, *q.AfterTime, q.AfterID)
		conds = append(conds, fmt.Sprintf("(e.time, e.id) %s ($%d, $%d::uuid)", cmp, len(args)-1, len(args)))
	}
	args = append(args, q.Limit+1)
	sqlText := `SELECT e.id, e.type, e.time, e.namespace, e.workload_kind, e.workload_name, e.recommendation_id, e.strategy,
		e.resources, e.containers, e.reason, e.message, e.details, e.cluster, e.received_at
		FROM audit_events e WHERE ` + joinAnd(conds) + fmt.Sprintf(` ORDER BY e.time %s, e.id %s LIMIT $%d`, order, order, len(args))
	rows, err := s.Pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []hubapi.AuditEvent{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, e)
	}
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	return out, more, rows.Err()
}

// WorkloadEvents returns events of one workload in [from, to), newest first.
func (s *Store) WorkloadEvents(ctx context.Context, k hubapi.WorkloadKey, from, to time.Time, limit int) ([]hubapi.AuditEvent, error) {
	rows, err := s.Pool.Query(ctx, `SELECT e.id, e.type, e.time, e.namespace, e.workload_kind, e.workload_name, e.recommendation_id, e.strategy,
		e.resources, e.containers, e.reason, e.message, e.details, e.cluster, e.received_at
		FROM audit_events e WHERE e.cluster = $1 AND e.namespace = $2 AND e.workload_kind = $3 AND e.workload_name = $4
		AND e.type <> 'config_changed' AND e.time >= $5 AND e.time < $6 ORDER BY e.time DESC, e.id DESC LIMIT $7`, k.Cluster, k.Namespace, k.Kind, k.Name, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hubapi.AuditEvent{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanEvent(rows pgx.Rows) (hubapi.AuditEvent, error) {
	var (
		e                   hubapi.AuditEvent
		id                  uuid.UUID
		recID               *uuid.UUID
		strategy            *string
		containers, details []byte
	)
	if err := rows.Scan(&id, &e.Type, &e.Time, &e.Workload.Namespace, &e.Workload.Kind, &e.Workload.Name, &recID, &strategy,
		&e.Resources, &containers, &e.Reason, &e.Message, &details, &e.Cluster, &e.ReceivedAt); err != nil {
		return e, err
	}
	e.ID = id.String()
	e.Time, e.ReceivedAt = e.Time.UTC(), e.ReceivedAt.UTC()
	if recID != nil {
		e.RecommendationID = recID.String()
	}
	e.Strategy = deref(strategy)
	if len(e.Resources) == 0 {
		e.Resources = nil
	}
	_ = json.Unmarshal(containers, &e.Containers)
	if len(e.Containers) == 0 {
		e.Containers = nil
	}
	_ = json.Unmarshal(details, &e.Details)
	if len(e.Details) == 0 {
		e.Details = nil
	}
	e.WorkloadID = hubapi.WorkloadKey{Cluster: e.Cluster, Namespace: e.Workload.Namespace, Kind: e.Workload.Kind, Name: e.Workload.Name}.ID()
	return e, nil
}

func joinAnd(c []string) string {
	out := c[0]
	for _, s := range c[1:] {
		out += " AND " + s
	}
	return out
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullUUID(s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &u
}

// IsUniqueViolation reports a unique-constraint error.
func IsUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
