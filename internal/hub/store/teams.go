package store

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/teams"
)

// db is what both the pool and a transaction offer.
type db interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

const teamColumns = `id, display_name, description, namespaces, namespace_labels, clusters, owner_values, version, created_at,
	coalesce(created_by, ''), updated_at`

func scanTeam(r pgx.Row) (hubapi.Team, error) {
	var t hubapi.Team
	var labels []byte
	var updated time.Time
	err := r.Scan(&t.ID, &t.DisplayName, &t.Description, &t.Namespaces, &labels, &t.Clusters, &t.OwnerValues, &t.Version,
		&t.CreatedAt, &t.CreatedBy, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.NamespaceLabels = map[string]string{}
	_ = json.Unmarshal(labels, &t.NamespaceLabels)
	for _, l := range []*[]string{&t.Namespaces, &t.Clusters, &t.OwnerValues} {
		if *l == nil {
			*l = []string{}
		}
	}
	t.CreatedAt, updated = t.CreatedAt.UTC(), updated.UTC()
	t.UpdatedAt = &updated
	if t.DisplayName == "" {
		t.DisplayName = t.ID
	}
	return t, nil
}

// ListTeams returns every team by id, without counts. A team-scoped caller gets only its teams.
func (s *Store) ListTeams(ctx context.Context) ([]hubapi.Team, error) {
	var args []any
	rows, err := s.Pool.Query(ctx, `SELECT `+teamColumns+` FROM teams WHERE `+teams.SQL(ctx, "id", &args)+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hubapi.Team{}
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTeam returns a team visible to the caller.
func (s *Store) GetTeam(ctx context.Context, id string) (hubapi.Team, error) {
	args := []any{id}
	return scanTeam(s.Pool.QueryRow(ctx, `SELECT `+teamColumns+` FROM teams WHERE id = $1 AND `+teams.SQL(ctx, "id", &args), args...))
}

// TeamNames returns the display name of every team by id (not scoped: names only).
func (s *Store) TeamNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, CASE WHEN display_name = '' THEN id ELSE display_name END FROM teams`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func teamArgs(in hubapi.TeamInput) (namespaces, clusters, owners []string, labels []byte) {
	nz := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	l := in.NamespaceLabels
	if l == nil {
		l = map[string]string{}
	}
	labels, _ = json.Marshal(l)
	return nz(in.Namespaces), nz(in.Clusters), nz(in.OwnerValues), labels
}

// CreateTeam creates a team, reassigns the workloads and records c.
func (s *Store) CreateTeam(ctx context.Context, in hubapi.TeamInput, c Change) (hubapi.Team, error) {
	var out hubapi.Team
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		ns, cl, ow, labels := teamArgs(in)
		var err error
		out, err = scanTeam(tx.QueryRow(ctx, `INSERT INTO teams (id, display_name, description, namespaces, namespace_labels, clusters,
				owner_values, created_at, created_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $8) ON CONFLICT (id) DO NOTHING RETURNING `+teamColumns,
			in.ID, in.DisplayName, in.Description, ns, labels, cl, ow, c.Time, nullStr(c.Actor.Name)))
		if errors.Is(err, ErrNotFound) {
			return ErrAlreadyExists
		}
		if err != nil {
			return err
		}
		if err := s.recomputeTeams(ctx, tx, "", nil); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetTeam, ID: out.ID, Name: out.DisplayName}
		c.Version, c.After = out.Version, out
		return recordChange(ctx, tx, c)
	})
	return out, err
}

// UpdateTeam replaces a team's definition at version ifVersion, reassigns the workloads and records c.
func (s *Store) UpdateTeam(ctx context.Context, id string, ifVersion int, in hubapi.TeamInput, c Change) (hubapi.Team, error) {
	var out hubapi.Team
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanTeam(tx.QueryRow(ctx, `SELECT `+teamColumns+` FROM teams WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if cur.Version != ifVersion {
			return ErrVersionConflict
		}
		ns, cl, ow, labels := teamArgs(in)
		out, err = scanTeam(tx.QueryRow(ctx, `UPDATE teams SET display_name = $2, description = $3, namespaces = $4, namespace_labels = $5,
				clusters = $6, owner_values = $7, updated_at = $8, version = version + 1 WHERE id = $1 RETURNING `+teamColumns,
			id, in.DisplayName, in.Description, ns, labels, cl, ow, c.Time))
		if err != nil {
			return err
		}
		if err := s.recomputeTeams(ctx, tx, "", nil); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetTeam, ID: out.ID, Name: out.DisplayName}
		c.Version, c.Before, c.After = out.Version, cur, out
		return recordChange(ctx, tx, c)
	})
	return out, err
}

// DeleteTeam deletes a team at version ifVersion: its workloads are reassigned (Unassigned when no
// other team takes them) and the users' bindings to it are removed. Records c.
func (s *Store) DeleteTeam(ctx context.Context, id string, ifVersion int, c Change) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanTeam(tx.QueryRow(ctx, `SELECT `+teamColumns+` FROM teams WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if cur.Version != ifVersion {
			return ErrVersionConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM teams WHERE id = $1`, id); err != nil {
			return err
		}
		// Bindings to the team go; a team-scoped user's role becomes the highest one left.
		if _, err := tx.Exec(ctx, `UPDATE users u SET team_roles = x.roles,
				role = CASE WHEN u.scope = 'teams' AND x.max_role IS NOT NULL THEN x.max_role ELSE u.role END,
				version = u.version + 1, updated_at = $2
			FROM (SELECT username,
					coalesce((SELECT jsonb_agg(b) FROM jsonb_array_elements(team_roles) b WHERE b->>'team' <> $1), '[]'::jsonb) AS roles,
					(SELECT CASE WHEN bool_or(b->>'role' = 'operator') THEN 'operator' WHEN count(*) > 0 THEN 'viewer' END
						FROM jsonb_array_elements(team_roles) b WHERE b->>'team' <> $1) AS max_role
				FROM users WHERE team_roles @> jsonb_build_array(jsonb_build_object('team', $1::text))) x
			WHERE u.username = x.username`, id, c.Time); err != nil {
			return err
		}
		if err := s.recomputeTeams(ctx, tx, "", nil); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetTeam, ID: cur.ID, Name: cur.DisplayName}
		c.Version, c.Before = cur.Version+1, cur
		return recordChange(ctx, tx, c)
	})
}

// UnknownTeams returns the ids that name no team.
func (s *Store) UnknownTeams(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT x FROM unnest($1::text[]) x WHERE x NOT IN (SELECT id FROM teams) ORDER BY x`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// resolver returns the team resolver of the current definitions.
func (s *Store) resolver(ctx context.Context, q db) (*teams.Resolver, error) {
	rows, err := q.Query(ctx, `SELECT id, namespaces, namespace_labels, clusters, owner_values FROM teams`)
	if err != nil {
		return nil, err
	}
	defs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (teams.Def, error) {
		var d teams.Def
		var labels []byte
		err := r.Scan(&d.ID, &d.Namespaces, &labels, &d.Clusters, &d.OwnerValues)
		_ = json.Unmarshal(labels, &d.NamespaceLabels)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	return teams.NewResolver(defs, s.OwnerLabel), nil
}

// RecomputeTeams reassigns every workload to its team (at startup: the owner label key may have
// changed).
func (s *Store) RecomputeTeams(ctx context.Context) error {
	return s.inTx(ctx, func(tx pgx.Tx) error { return s.recomputeTeams(ctx, tx, "", nil) })
}

// recomputeTeams reassigns the workloads of cluster ("" = all) — only ids when not nil — to the
// team their labels and namespace now give, writing only the rows that change.
func (s *Store) recomputeTeams(ctx context.Context, q db, cluster string, ids []int64) error {
	res, err := s.resolver(ctx, q)
	if err != nil {
		return err
	}
	rows, err := q.Query(ctx, `SELECT id, cluster, namespace, labels, namespace_labels, coalesce(team, '') FROM workloads
		WHERE ($1 = '' OR cluster = $1) AND ($2::bigint[] IS NULL OR id = ANY($2))`, cluster, ids)
	if err != nil {
		return err
	}
	var changedIDs []int64
	var changedTeams []string
	for rows.Next() {
		var id int64
		var w teams.Workload
		var labels, nsLabels []byte
		var cur string
		if err := rows.Scan(&id, &w.Cluster, &w.Namespace, &labels, &nsLabels, &cur); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal(labels, &w.Labels)
		_ = json.Unmarshal(nsLabels, &w.NamespaceLabels)
		if t := res.Resolve(w); t != cur {
			changedIDs = append(changedIDs, id)
			changedTeams = append(changedTeams, t)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(changedIDs) == 0 {
		return nil
	}
	_, err = q.Exec(ctx, `UPDATE workloads w SET team = nullif(x.team, '') FROM unnest($1::bigint[], $2::text[]) AS x(id, team)
		WHERE w.id = x.id`, changedIDs, changedTeams)
	return err
}

// WorkloadTeam returns the team of a workload ("" = Unassigned), deleted ones included; found is
// false for an unknown workload. Not scoped: authorization uses it.
func (s *Store) WorkloadTeam(ctx context.Context, k hubapi.WorkloadKey) (team string, found bool, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT coalesce(team, '') FROM workloads WHERE cluster = $1 AND namespace = $2 AND kind = $3 AND name = $4`,
		k.Cluster, k.Namespace, k.Kind, k.Name).Scan(&team)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return team, err == nil, err
}

// TeamFilterSQL is the condition of an explicit team filter (team ids, hubapi.TeamUnassigned for no
// team) on column col; "true" without a filter.
func TeamFilterSQL(col string, ids []string, args *[]any) string {
	if len(ids) == 0 {
		return "true"
	}
	named := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == hubapi.TeamUnassigned })
	*args = append(*args, named)
	cond := col + " = ANY($" + strconv.Itoa(len(*args)) + "::text[])"
	if len(named) < len(ids) {
		cond = "(" + cond + " OR " + col + " IS NULL)"
	}
	return cond
}

// RecordRunTeams records the estimated savings per team of one cluster at the end of a run (team ""
// = Unassigned).
func (s *Store) RecordRunTeams(ctx context.Context, runID any, cluster string, per map[string]hubapi.Savings) error {
	if len(per) == 0 {
		return nil
	}
	keys := slices.Sorted(maps.Keys(per))
	cpu := make([]float64, len(keys))
	mem := make([]float64, len(keys))
	for i, k := range keys {
		cpu[i], mem[i] = per[k].CPU, per[k].Memory
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO recommender_run_teams (run_id, cluster, team, monthly_savings_cpu_usd, monthly_savings_memory_usd)
		SELECT $1, $2, x.team, x.cpu, x.mem FROM unnest($3::text[], $4::float8[], $5::float8[]) AS x(team, cpu, mem)
		ON CONFLICT DO NOTHING`, runID, cluster, keys, cpu, mem)
	return err
}

// TeamSavingsSample is SavingsSample per cluster and team.
type TeamSavingsSample struct {
	SavingsSample
	Team string // "" = Unassigned
}

// TeamSavingsSamples is SavingsSamples per cluster and team, for the runs that recorded teams.
// teamIDs filters (hubapi.TeamUnassigned = no team); the caller's scope always applies.
func (s *Store) TeamSavingsSamples(ctx context.Context, clusters, teamIDs []string, from, to time.Time) ([]TeamSavingsSample, error) {
	if clusters == nil {
		clusters = []string{}
	}
	args := []any{from, to, clusters}
	cond := TeamFilterSQL("nullif(rt.team, '')", teamIDs, &args) + " AND " + teams.SQL(ctx, "nullif(rt.team, '')", &args)
	rows, err := s.Pool.Query(ctx, `WITH rc AS (
			SELECT rc.cluster, rt.team, CASE WHEN r.backfill THEN rc.as_of ELSE rc.finished_at END AS at,
				rt.monthly_savings_cpu_usd AS cpu, rt.monthly_savings_memory_usd AS mem
			FROM recommender_run_teams rt
			JOIN recommender_run_clusters rc ON rc.run_id = rt.run_id AND rc.cluster = rt.cluster
			JOIN recommender_runs r ON r.id = rc.run_id
			WHERE rc.status = 'succeeded' AND (cardinality($3::text[]) = 0 OR rc.cluster = ANY($3::text[])) AND `+cond+`)
		SELECT cluster, team, at, cpu, mem FROM (
			SELECT cluster, team, at, cpu, mem FROM rc WHERE at >= $1 AND at <= $2
			UNION ALL
			(SELECT DISTINCT ON (cluster, team) cluster, team, at, cpu, mem FROM rc WHERE at < $1 ORDER BY cluster, team, at DESC)
		) x ORDER BY at, cluster, team`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TeamSavingsSample, error) {
		var x TeamSavingsSample
		err := r.Scan(&x.Cluster, &x.Team, &x.At, &x.CPU, &x.Memory)
		return x, err
	})
}

// workloadIDsInScope is the condition "column holds the id of a workload the caller may see";
// "true" when not restricted.
func workloadIDsInScope(ctx context.Context, col string, args *[]any) string {
	cond := teams.SQL(ctx, "team", args)
	if cond == "true" {
		return cond
	}
	return col + " IN (SELECT id FROM workloads WHERE " + cond + ")"
}

// recomputeReported reassigns the workloads of a status batch that reported labels.
func (s *Store) recomputeReported(ctx context.Context, q db, cluster string, items []hubapi.WorkloadStatus) error {
	var keys []string
	for _, it := range items {
		if it.Labels != nil || it.NamespaceLabels != nil {
			keys = append(keys, it.Workload.Namespace+"/"+it.Workload.Kind+"/"+it.Workload.Name)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT id FROM workloads WHERE cluster = $1 AND namespace || '/' || kind || '/' || name = ANY($2)`, cluster, keys)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(ids) == 0 {
		return err
	}
	return s.recomputeTeams(ctx, q, cluster, ids)
}
