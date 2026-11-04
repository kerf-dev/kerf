package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	rd "github.com/tomer1983/resource-optimizer/api/v1alpha1/readiness"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// DB is the part of pgxpool.Pool the store uses.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store keeps readiness results, state and history (tables cluster_readiness, readiness_checks,
// readiness_history, migration 0010) and records state transitions in the config audit.
type Store struct {
	DB DB
	// Gauges, when set, are updated on every Apply.
	Gauges *Gauges
}

// SystemActor is the actor of readiness transitions in the config audit.
var SystemActor = hubapi.Actor{Type: hubapi.ActorSystem, Name: "readiness"}

// Update is a set of new results for one cluster.
type Update struct {
	Checks []hubapi.ReadinessCheck
	// ReplaceIDs: stored results with these ids that are not in Checks are removed (a producer's
	// full set, e.g. every agent check, or every metrics check).
	ReplaceIDs []string
	// Capabilities replaces the agent capabilities when not nil.
	Capabilities []hubapi.AgentCapability
	At           time.Time
}

// AgentCheckIDs are the catalog checks the agent produces.
func AgentCheckIDs() []string {
	var out []string
	for _, c := range rd.Catalog {
		if c.Source == rd.SourceAgent {
			out = append(out, c.ID)
		}
	}
	return out
}

// Applied is the outcome of Apply.
type Applied struct {
	Evaluation
	Prev    rd.State
	Changes []Change
	Checks  []hubapi.ReadinessCheck
}

// Transitioned reports whether the state changed.
func (a Applied) Transitioned() bool { return a.Prev != a.State }

func checkColumns() string {
	return `check_id, source, category, severity, status, message, remediation, coalesce(remediation_type, ''), docs_url, details, checked_at`
}

func scanCheck(row pgx.Row) (hubapi.ReadinessCheck, error) {
	var c hubapi.ReadinessCheck
	var details []byte
	if err := row.Scan(&c.ID, &c.Source, &c.Category, &c.Severity, &c.Status, &c.Message, &c.Remediation, &c.RemediationType, &c.DocsURL, &details, &c.CheckedAt); err != nil {
		return c, err
	}
	if len(details) > 2 {
		_ = json.Unmarshal(details, &c.Details)
	}
	if cat, ok := rd.Lookup(c.ID); ok {
		c.Gates = string(cat.Gates)
	}
	c.CheckedAt = c.CheckedAt.UTC()
	return c, nil
}

func loadChecks(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, cluster string) ([]hubapi.ReadinessCheck, error) {
	rows, err := q.Query(ctx, `SELECT `+checkColumns()+` FROM readiness_checks WHERE cluster = $1`, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hubapi.ReadinessCheck
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return Normalize(out), rows.Err()
}

// Apply stores new results of a cluster, recomputes its state, records the history (status
// changes and transitions) and a ReadinessChanged config audit entry on a transition, all in one
// transaction. Agent results newer than a pending re-check clear it.
func (s *Store) Apply(ctx context.Context, cluster string, u Update) (Applied, error) {
	if u.At.IsZero() {
		u.At = time.Now()
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Applied{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `INSERT INTO cluster_readiness (cluster, state_since) VALUES ($1, $2) ON CONFLICT (cluster) DO NOTHING`, cluster, u.At); err != nil {
		return Applied{}, err
	}
	var prev string
	var recheck *time.Time
	if err := tx.QueryRow(ctx, `SELECT state, recheck_requested_at FROM cluster_readiness WHERE cluster = $1 FOR UPDATE`, cluster).Scan(&prev, &recheck); err != nil {
		return Applied{}, err
	}
	old, err := loadChecks(ctx, tx, cluster)
	if err != nil {
		return Applied{}, err
	}

	incoming := Normalize(u.Checks)
	merged := map[string]hubapi.ReadinessCheck{}
	for _, c := range old {
		merged[c.ID] = c
	}
	inNew := map[string]bool{}
	for _, c := range incoming {
		inNew[c.ID] = true
		merged[c.ID] = c
	}
	var removed []string
	for _, id := range u.ReplaceIDs {
		if _, ok := merged[id]; ok && !inNew[id] {
			delete(merged, id)
			removed = append(removed, id)
		}
	}
	all := make([]hubapi.ReadinessCheck, 0, len(merged))
	for _, c := range merged {
		all = append(all, c)
	}
	all = Normalize(all)

	if len(removed) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM readiness_checks WHERE cluster = $1 AND check_id = ANY($2)`, cluster, removed); err != nil {
			return Applied{}, err
		}
	}
	agentNewest := time.Time{}
	for _, c := range incoming {
		details := []byte("{}")
		if len(c.Details) > 0 {
			details, _ = json.Marshal(c.Details)
		}
		var remType any
		if c.RemediationType != "" {
			remType = c.RemediationType
		}
		if _, err := tx.Exec(ctx, `INSERT INTO readiness_checks (cluster, check_id, source, category, severity, status, message, remediation, remediation_type, docs_url, details, checked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (cluster, check_id) DO UPDATE SET source = EXCLUDED.source, category = EXCLUDED.category, severity = EXCLUDED.severity,
				status = EXCLUDED.status, message = EXCLUDED.message, remediation = EXCLUDED.remediation, remediation_type = EXCLUDED.remediation_type,
				docs_url = EXCLUDED.docs_url, details = EXCLUDED.details, checked_at = EXCLUDED.checked_at`,
			cluster, c.ID, c.Source, c.Category, c.Severity, c.Status, c.Message, c.Remediation, remType, c.DocsURL, details, c.CheckedAt); err != nil {
			return Applied{}, fmt.Errorf("store check %s: %w", c.ID, err)
		}
		if c.Source == string(rd.SourceAgent) && c.CheckedAt.After(agentNewest) {
			agentNewest = c.CheckedAt
		}
	}

	e := Evaluate(all)
	prevState := rd.State(prev)
	changes := Diff(prevState, old, incoming, e.State, u.At)
	for _, id := range removed {
		for _, c := range old {
			if c.ID == id {
				changes = append(changes, Change{Time: u.At, Kind: "check", CheckID: id, FromStatus: c.Status, ToStatus: string(rd.StatusUnknown), Message: "no longer reported"})
			}
		}
	}
	for _, ch := range changes {
		if _, err := tx.Exec(ctx, `INSERT INTO readiness_history (cluster, time, kind, from_state, to_state, check_id, from_status, to_status, message)
			VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, ''), nullif($7, ''), nullif($8, ''), $9)`,
			cluster, ch.Time, ch.Kind, ch.FromState, ch.ToState, ch.CheckID, ch.FromStatus, ch.ToStatus, ch.Message); err != nil {
			return Applied{}, fmt.Errorf("readiness history: %w", err)
		}
	}

	set := `checked_at = $2`
	args := []any{cluster, u.At}
	if e.State != prevState {
		set += `, state = $3, state_since = $2`
		args = append(args, string(e.State))
	}
	if u.Capabilities != nil {
		b, err := json.Marshal(u.Capabilities)
		if err != nil {
			return Applied{}, err
		}
		args = append(args, b)
		set += `, capabilities = $` + strconv.Itoa(len(args))
	}
	if recheck != nil && !agentNewest.IsZero() && !agentNewest.Before(*recheck) {
		set += `, recheck_requested_at = NULL`
	}
	if _, err := tx.Exec(ctx, `UPDATE cluster_readiness SET `+set+` WHERE cluster = $1`, args...); err != nil {
		return Applied{}, err
	}

	if e.State != prevState {
		before, _ := json.Marshal(map[string]any{"state": prevState})
		blocked := append([]string{}, e.BlockedBy...)
		after, _ := json.Marshal(map[string]any{"state": e.State, "blockedBy": blocked})
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events (id, cluster, type, reason, time, message, actor, target_type, target_id, target_name, before, after)
			VALUES ($1, $2, 'config_changed', $3, $4, $5, $6, $7, $2, $2, $8, $9)`,
			uuid.New(), cluster, hubapi.ActionReadinessChanged, u.At, TransitionMessage(prevState, e.State, all), SystemActor, hubapi.TargetReadiness, before, after); err != nil {
			return Applied{}, fmt.Errorf("readiness audit: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Applied{}, err
	}
	if s.Gauges != nil {
		s.Gauges.Set(cluster, e.State, all)
	}
	return Applied{Evaluation: e, Prev: prevState, Changes: changes, Checks: all}, nil
}

// Get returns a cluster's readiness (state Registered and no checks when nothing was stored).
func (s *Store) Get(ctx context.Context, cluster string) (hubapi.ClusterReadiness, error) {
	out := hubapi.ClusterReadiness{Cluster: cluster, State: string(rd.StateRegistered), Checks: []hubapi.ReadinessCheck{}}
	var since time.Time
	var checked, recheck *time.Time
	var caps []byte
	err := s.DB.QueryRow(ctx, `SELECT state_since, checked_at, recheck_requested_at, capabilities FROM cluster_readiness WHERE cluster = $1`, cluster).
		Scan(&since, &checked, &recheck, &caps)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	checks, err := loadChecks(ctx, s.DB, cluster)
	if err != nil {
		return out, err
	}
	e := Evaluate(checks)
	out.State, out.NextState, out.BlockedBy, out.Summary = string(e.State), string(e.NextState), e.BlockedBy, e.Summary
	if checks != nil {
		out.Checks = checks
	}
	if !since.IsZero() {
		since = since.UTC()
		out.StateSince = &since
	}
	out.CheckedAt, out.RecheckRequestedAt = utc(checked), utc(recheck)
	if len(caps) > 2 {
		_ = json.Unmarshal(caps, &out.Capabilities)
	}
	return out, nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// Summaries returns the readiness summary of every cluster with stored readiness.
func (s *Store) Summaries(ctx context.Context) (map[string]hubapi.ReadinessSummary, error) {
	rows, err := s.DB.Query(ctx, `SELECT r.cluster, r.checked_at, c.check_id, c.status
		FROM cluster_readiness r LEFT JOIN readiness_checks c ON c.cluster = r.cluster`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checks := map[string][]hubapi.ReadinessCheck{}
	checked := map[string]*time.Time{}
	for rows.Next() {
		var cluster string
		var at *time.Time
		var id, status *string
		if err := rows.Scan(&cluster, &at, &id, &status); err != nil {
			return nil, err
		}
		checked[cluster] = utc(at)
		if id != nil {
			checks[cluster] = append(checks[cluster], hubapi.ReadinessCheck{ID: *id, Status: *status})
		} else if _, ok := checks[cluster]; !ok {
			checks[cluster] = nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]hubapi.ReadinessSummary{}
	for cluster, cs := range checks {
		out[cluster] = Summary(Evaluate(Normalize(cs)), checked[cluster])
	}
	return out, nil
}

// History returns history entries of a cluster, newest first. cont is the continue token of a
// previous page.
func (s *Store) History(ctx context.Context, cluster string, from, to time.Time, limit int, cont string) (hubapi.ReadinessHistory, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	before := int64(1<<62 - 1)
	if cont != "" {
		v, err := strconv.ParseInt(cont, 10, 64)
		if err != nil {
			return hubapi.ReadinessHistory{}, fmt.Errorf("invalid continue token")
		}
		before = v
	}
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	rows, err := s.DB.Query(ctx, `SELECT id, time, kind, coalesce(from_state, ''), coalesce(to_state, ''), coalesce(check_id, ''),
		coalesce(from_status, ''), coalesce(to_status, ''), message
		FROM readiness_history WHERE cluster = $1 AND time >= $2 AND time <= $3 AND id < $4 ORDER BY time DESC, id DESC LIMIT $5`,
		cluster, from, to, before, limit+1)
	if err != nil {
		return hubapi.ReadinessHistory{}, err
	}
	defer rows.Close()
	out := hubapi.ReadinessHistory{Items: []hubapi.ReadinessHistoryEntry{}}
	var lastID int64
	for rows.Next() {
		var id int64
		var e hubapi.ReadinessHistoryEntry
		if err := rows.Scan(&id, &e.Time, &e.Kind, &e.FromState, &e.ToState, &e.CheckID, &e.FromStatus, &e.ToStatus, &e.Message); err != nil {
			return out, err
		}
		if len(out.Items) == limit {
			out.Continue = strconv.FormatInt(lastID, 10)
			break
		}
		e.Time = e.Time.UTC()
		out.Items = append(out.Items, e)
		lastID = id
	}
	return out, rows.Err()
}

// RequestRecheck records a re-check request (sent to the agent as ClusterConfig.flags.recheck).
func (s *Store) RequestRecheck(ctx context.Context, cluster string, at time.Time) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO cluster_readiness (cluster, state_since, recheck_requested_at) VALUES ($1, $2, $2)
		ON CONFLICT (cluster) DO UPDATE SET recheck_requested_at = EXCLUDED.recheck_requested_at`, cluster, at)
	return err
}

// RecheckRequestedAt returns the pending re-check of a cluster, if any.
func (s *Store) RecheckRequestedAt(ctx context.Context, cluster string) (*time.Time, error) {
	var t *time.Time
	err := s.DB.QueryRow(ctx, `SELECT recheck_requested_at FROM cluster_readiness WHERE cluster = $1`, cluster).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return utc(t), err
}

// States returns the readiness state of every cluster with stored readiness (auto-patch gating).
func (s *Store) States(ctx context.Context) (map[string]rd.State, error) {
	rows, err := s.DB.Query(ctx, `SELECT cluster, state FROM cluster_readiness`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]rd.State{}
	for rows.Next() {
		var c, st string
		if err := rows.Scan(&c, &st); err != nil {
			return nil, err
		}
		out[c] = rd.State(st)
	}
	return out, rows.Err()
}

// Refresh sets the gauges of every cluster from the store (hub start-up).
func (s *Store) Refresh(ctx context.Context, clusters []string) error {
	if s.Gauges == nil {
		return nil
	}
	for _, c := range clusters {
		r, err := s.Get(ctx, c)
		if err != nil {
			return err
		}
		s.Gauges.Set(c, rd.State(r.State), r.Checks)
	}
	return nil
}
