package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// ---- cluster administration (D-022, D-025 fields stored only) ----

// ClusterRow is a cluster with its admin metadata, agent sync state and metrics binding.
type ClusterRow struct {
	Name, Description, Environment, Owner, PolicySource, Provider, Region string
	CreatedAt                                                             time.Time
	UpdatedAt                                                             *time.Time
	UpdatedBy                                                             *hubapi.Actor
	DeletedAt                                                             *time.Time
	Version                                                               int
	LastSeenAt                                                            *time.Time
	AgentVersion                                                          string
	ConfigPulledAt                                                        *time.Time
	AppliedConfigVersion                                                  string
	PolicyInventory                                                       *[]hubapi.PolicyInventoryItem
	PolicyInventoryAt                                                     *time.Time
	Binding                                                               *hubapi.MetricsBinding
	ReadinessState                                                        string
	ReadinessCheckedAt                                                    *time.Time
}

const clusterColumns = `c.name, c.description, coalesce(c.environment, ''), c.owner, c.policy_source, c.provider, c.region, c.created_at,
	c.updated_at, c.updated_by, c.deleted_at, c.version, c.last_seen_at, coalesce(c.agent_version, ''), c.config_pulled_at,
	coalesce(c.applied_config_version, ''), c.policy_inventory, c.policy_inventory_at,
	b.datasource_id, b.cluster_label, b.cluster_label_value, b.extra_matchers, b.job_names,
	coalesce(r.state, ''), r.checked_at`

const clusterFrom = ` FROM clusters c LEFT JOIN cluster_metrics_bindings b ON b.cluster = c.name LEFT JOIN cluster_readiness r ON r.cluster = c.name`

func scanCluster(row pgx.Row) (ClusterRow, error) {
	var (
		c                     ClusterRow
		by, inv, jobs         []byte
		dsID, label, labelVal *string
		matchers              []string
	)
	err := row.Scan(&c.Name, &c.Description, &c.Environment, &c.Owner, &c.PolicySource, &c.Provider, &c.Region, &c.CreatedAt,
		&c.UpdatedAt, &by, &c.DeletedAt, &c.Version, &c.LastSeenAt, &c.AgentVersion, &c.ConfigPulledAt, &c.AppliedConfigVersion, &inv,
		&c.PolicyInventoryAt, &dsID, &label, &labelVal, &matchers, &jobs, &c.ReadinessState, &c.ReadinessCheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt, c.DeletedAt, c.LastSeenAt, c.ConfigPulledAt = utc(c.UpdatedAt), utc(c.DeletedAt), utc(c.LastSeenAt), utc(c.ConfigPulledAt)
	c.PolicyInventoryAt, c.ReadinessCheckedAt = utc(c.PolicyInventoryAt), utc(c.ReadinessCheckedAt)
	if len(by) > 0 {
		var a hubapi.Actor
		if json.Unmarshal(by, &a) == nil {
			c.UpdatedBy = &a
		}
	}
	if len(inv) > 0 {
		var items []hubapi.PolicyInventoryItem
		if json.Unmarshal(inv, &items) == nil {
			if items == nil {
				items = []hubapi.PolicyInventoryItem{}
			}
			c.PolicyInventory = &items
		}
	}
	if dsID != nil {
		b := hubapi.MetricsBinding{DatasourceID: *dsID, ClusterLabel: deref(label), ClusterLabelValue: deref(labelVal), ExtraMatchers: matchers}
		var j hubapi.MetricsJobNames
		if len(jobs) > 0 && json.Unmarshal(jobs, &j) == nil && (len(j.CAdvisor) > 0 || len(j.KubeStateMetrics) > 0) {
			b.JobNames = &j
		}
		c.Binding = &b
	}
	return c, nil
}

// ClusterRows lists clusters by name; deleted ones only with includeDeleted.
func (s *Store) ClusterRows(ctx context.Context, includeDeleted bool) ([]ClusterRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+clusterColumns+clusterFrom+` WHERE $1 OR c.deleted_at IS NULL ORDER BY c.name`, includeDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClusterRow
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCluster returns a cluster, deleted ones included.
func (s *Store) GetCluster(ctx context.Context, name string) (ClusterRow, error) {
	return scanCluster(s.Pool.QueryRow(ctx, `SELECT `+clusterColumns+clusterFrom+` WHERE c.name = $1`, name))
}

func lockCluster(ctx context.Context, tx pgx.Tx, name string) (ClusterRow, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM clusters WHERE name = $1 FOR UPDATE`, name); err != nil {
		return ClusterRow{}, err
	}
	return scanCluster(tx.QueryRow(ctx, `SELECT `+clusterColumns+clusterFrom+` WHERE c.name = $1`, name))
}

// ensureDatasource makes sure a binding's datasource exists: 'local' is created from localURL
// when missing (WO-25 upserts it at start-up); another id must exist and be live.
func ensureDatasource(ctx context.Context, tx pgx.Tx, id, localURL string) error {
	if id == hubapi.LocalDatasourceID {
		_, err := tx.Exec(ctx, `INSERT INTO datasources (id, name, type, url, built_in) VALUES ('local', 'local', 'victoriametrics', $1, true)
			ON CONFLICT (id) DO NOTHING`, localURL)
		return err
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM datasources WHERE id = $1 AND deleted_at IS NULL)`, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrUnknownDatasource
	}
	return nil
}

// ErrUnknownDatasource: a metrics binding names a datasource that does not exist.
var ErrUnknownDatasource = errors.New("unknown datasource")

func upsertBinding(ctx context.Context, tx pgx.Tx, cluster string, b hubapi.MetricsBinding, localURL string, c Change) error {
	if err := ensureDatasource(ctx, tx, b.DatasourceID, localURL); err != nil {
		return err
	}
	jobs := []byte("{}")
	if b.JobNames != nil {
		jobs, _ = json.Marshal(b.JobNames)
	}
	matchers := b.ExtraMatchers
	if matchers == nil {
		matchers = []string{}
	}
	actor, _ := json.Marshal(c.Actor)
	_, err := tx.Exec(ctx, `INSERT INTO cluster_metrics_bindings (cluster, datasource_id, cluster_label, cluster_label_value, extra_matchers,
			job_names, updated_at, updated_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (cluster) DO UPDATE SET datasource_id = excluded.datasource_id, cluster_label = excluded.cluster_label,
			cluster_label_value = excluded.cluster_label_value, extra_matchers = excluded.extra_matchers, job_names = excluded.job_names,
			updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		cluster, b.DatasourceID, b.ClusterLabel, nullStr(b.ClusterLabelValue), matchers, jobs, c.Time, actor)
	return err
}

// ClusterCreated is what CreateCluster returns.
type ClusterCreated struct {
	Cluster ClusterRow
	Token   *hubapi.TokenInfo
}

// CreateCluster registers a cluster (or revives a deleted one), optionally with an agent token
// (its hash), and records c. A live cluster of the name gives ErrAlreadyExists.
func (s *Store) CreateCluster(ctx context.Context, in hubapi.ClusterCreate, tokenHash []byte, localURL string, c Change) (ClusterCreated, error) {
	var out ClusterCreated
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		actor, _ := json.Marshal(c.Actor)
		tag, err := tx.Exec(ctx, `INSERT INTO clusters (name, description, environment, owner, policy_source, provider, region, created_at,
				updated_at, updated_by, version) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9, 1)
			ON CONFLICT (name) DO UPDATE SET description = excluded.description, environment = excluded.environment, owner = excluded.owner,
				policy_source = excluded.policy_source, provider = excluded.provider, region = excluded.region, updated_at = excluded.updated_at,
				updated_by = excluded.updated_by, deleted_at = NULL, version = clusters.version + 1
			WHERE clusters.deleted_at IS NOT NULL`,
			in.Name, in.Description, nullStr(in.Environment), in.Owner, in.PolicySource, in.Provider, in.Region, c.Time, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrAlreadyExists
		}
		if in.Metrics != nil {
			if err := upsertBinding(ctx, tx, in.Name, *in.Metrics, localURL, c); err != nil {
				return err
			}
		}
		if tokenHash != nil {
			t, err := createToken(ctx, tx, tokenHash, hubapi.TokenCreate{Role: hubapi.RoleAgent, Cluster: in.Name, Description: "agent " + in.Name}, c.Actor.Name, c.Time)
			if err != nil {
				return err
			}
			out.Token = &t
		}
		if out.Cluster, err = scanCluster(tx.QueryRow(ctx, `SELECT `+clusterColumns+clusterFrom+` WHERE c.name = $1`, in.Name)); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetCluster, ID: in.Name, Name: in.Name, Cluster: in.Name}
		c.Version, c.After = out.Cluster.Version, clusterAudit(out.Cluster)
		return recordChange(ctx, tx, c)
	})
	return out, err
}

// clusterAudit is the before/after of a cluster in the audit log.
func clusterAudit(c ClusterRow) map[string]any {
	m := map[string]any{"name": c.Name, "description": c.Description, "environment": c.Environment, "owner": c.Owner,
		"policySource": c.PolicySource, "provider": c.Provider, "region": c.Region, "version": c.Version}
	if c.Binding != nil {
		m["metrics"] = c.Binding
	}
	if c.DeletedAt != nil {
		m["deletedAt"] = c.DeletedAt
	}
	return m
}

// UpdateCluster applies upd to a live cluster at version ifVersion and records c.
func (s *Store) UpdateCluster(ctx context.Context, name string, ifVersion int, upd hubapi.ClusterUpdate, localURL string, c Change) (ClusterRow, error) {
	var out ClusterRow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockCluster(ctx, tx, name)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil {
			return ErrNotFound
		}
		if cur.Version != ifVersion {
			return ErrVersionConflict
		}
		next := cur
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&next.Description, upd.Description)
		set(&next.Environment, upd.Environment)
		set(&next.Owner, upd.Owner)
		set(&next.PolicySource, upd.PolicySource)
		set(&next.Provider, upd.Provider)
		set(&next.Region, upd.Region)
		actor, _ := json.Marshal(c.Actor)
		if _, err := tx.Exec(ctx, `UPDATE clusters SET description = $2, environment = $3, owner = $4, policy_source = $5, provider = $6,
				region = $7, updated_at = $8, updated_by = $9, version = version + 1 WHERE name = $1`,
			name, next.Description, nullStr(next.Environment), next.Owner, next.PolicySource, next.Provider, next.Region, c.Time, actor); err != nil {
			return err
		}
		if upd.Metrics != nil {
			if err := upsertBinding(ctx, tx, name, *upd.Metrics, localURL, c); err != nil {
				return err
			}
		}
		if out, err = scanCluster(tx.QueryRow(ctx, `SELECT `+clusterColumns+clusterFrom+` WHERE c.name = $1`, name)); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetCluster, ID: name, Name: name, Cluster: name}
		c.Version, c.Before, c.After = out.Version, clusterAudit(cur), clusterAudit(out)
		return recordChange(ctx, tx, c)
	})
	return out, err
}

// DeleteCluster soft-deletes a live cluster at version ifVersion, revokes its tokens and records c.
// It returns the ids of the revoked tokens.
func (s *Store) DeleteCluster(ctx context.Context, name string, ifVersion int, c Change) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockCluster(ctx, tx, name)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil {
			return ErrNotFound
		}
		if cur.Version != ifVersion {
			return ErrVersionConflict
		}
		actor, _ := json.Marshal(c.Actor)
		if _, err := tx.Exec(ctx, `UPDATE clusters SET deleted_at = $2, updated_at = $2, updated_by = $3, version = version + 1 WHERE name = $1`,
			name, c.Time, actor); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = $2, revoked_by = $3 WHERE cluster = $1 AND revoked_at IS NULL`,
			name, c.Time, nullStr(c.Actor.Name)); err != nil {
			return err
		}
		out, err := scanCluster(tx.QueryRow(ctx, `SELECT `+clusterColumns+clusterFrom+` WHERE c.name = $1`, name))
		if err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetCluster, ID: name, Name: name, Cluster: name}
		c.Version, c.Before, c.After = out.Version, clusterAudit(cur), clusterAudit(out)
		return recordChange(ctx, tx, c)
	})
}

// RecordConfigPull records an agent's config pull.
func (s *Store) RecordConfigPull(ctx context.Context, cluster string, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE clusters SET config_pulled_at = $2 WHERE name = $1`, cluster, now)
	return err
}

// RecordAgentConfig stores the config version an agent materialised and, when inventory is not
// nil, its full policy inventory.
func (s *Store) RecordAgentConfig(ctx context.Context, cluster, configVersion string, inventory []hubapi.PolicyInventoryItem, now time.Time) error {
	var inv []byte
	if inventory != nil {
		inv, _ = json.Marshal(inventory)
	}
	_, err := s.Pool.Exec(ctx, `UPDATE clusters SET applied_config_version = coalesce(nullif($2, ''), applied_config_version),
			policy_inventory = coalesce($3, policy_inventory), policy_inventory_at = CASE WHEN $3::jsonb IS NULL THEN policy_inventory_at ELSE $4 END
		WHERE name = $1`, cluster, configVersion, inv, now)
	return err
}

// ---- automation (pause) ----

// AutomationRow is the pause state of a scope (cluster name or hubapi.FleetScope).
type AutomationRow struct {
	Scope string
	hubapi.Automation
}

func scanAutomation(r pgx.Row) (AutomationRow, error) {
	var a AutomationRow
	var by []byte
	var since time.Time
	err := r.Scan(&a.Scope, &a.Paused, &a.Reason, &by, &since, &a.ResumeAt, &a.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	since = since.UTC()
	a.Since, a.ResumeAt = &since, utc(a.ResumeAt)
	if len(by) > 0 {
		var act hubapi.Actor
		if json.Unmarshal(by, &act) == nil {
			a.By = &act
		}
	}
	return a, nil
}

const automationColumns = `scope, paused, reason, changed_by, changed_at, resume_at, version`

// Automations returns the stored pause states by scope (a missing scope is not paused, version 0).
func (s *Store) Automations(ctx context.Context) (map[string]hubapi.Automation, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+automationColumns+` FROM cluster_automation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]hubapi.Automation{}
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out[a.Scope] = a.Automation
	}
	return out, rows.Err()
}

// GetAutomation returns a scope's pause state (not paused, version 0 when never set).
func (s *Store) GetAutomation(ctx context.Context, scope string) (hubapi.Automation, error) {
	a, err := scanAutomation(s.Pool.QueryRow(ctx, `SELECT `+automationColumns+` FROM cluster_automation WHERE scope = $1`, scope))
	if errors.Is(err, ErrNotFound) {
		return hubapi.Automation{}, nil
	}
	return a.Automation, err
}

// SetAutomation pauses or resumes a scope (ifVersion nil: unconditional) and records c.
func (s *Store) SetAutomation(ctx context.Context, scope string, ifVersion *int, upd hubapi.AutomationUpdate, c Change) (hubapi.Automation, error) {
	var out AutomationRow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		// Serialise writers of the scope, including the first one.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cluster_automation:' || $1))`, scope); err != nil {
			return err
		}
		cur, err := scanAutomation(tx.QueryRow(ctx, `SELECT `+automationColumns+` FROM cluster_automation WHERE scope = $1`, scope))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if ifVersion != nil && *ifVersion != cur.Version {
			return ErrVersionConflict
		}
		resume := upd.ResumeAt
		if !upd.Paused {
			resume = nil
		}
		actor, _ := json.Marshal(c.Actor)
		if out, err = scanAutomation(tx.QueryRow(ctx, `INSERT INTO cluster_automation (scope, paused, reason, changed_by, changed_at, resume_at, version)
			VALUES ($1, $2, $3, $4, $5, $6, 1)
			ON CONFLICT (scope) DO UPDATE SET paused = excluded.paused, reason = excluded.reason, changed_by = excluded.changed_by,
				changed_at = excluded.changed_at, resume_at = excluded.resume_at, version = cluster_automation.version + 1
			RETURNING `+automationColumns, scope, upd.Paused, upd.Reason, actor, c.Time, resume)); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetAutomation, ID: scope, Name: scope}
		if scope != hubapi.FleetScope {
			c.Target.Cluster = scope
		}
		c.Version, c.After = out.Version, out.Automation
		if cur.Version > 0 {
			c.Before = cur.Automation
		}
		return recordChange(ctx, tx, c)
	})
	return out.Automation, err
}

// ExpirePauses resumes every scope whose resumeAt has passed, recording AutomationResumed by the
// system actor for each. It returns the resumed scopes.
func (s *Store) ExpirePauses(ctx context.Context, now time.Time) ([]string, error) {
	var scopes []string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+automationColumns+` FROM cluster_automation WHERE paused AND resume_at IS NOT NULL AND resume_at <= $1
			FOR UPDATE SKIP LOCKED`, now)
		if err != nil {
			return err
		}
		var due []AutomationRow
		for rows.Next() {
			a, err := scanAutomation(rows)
			if err != nil {
				rows.Close()
				return err
			}
			due = append(due, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		system := hubapi.Actor{Type: hubapi.ActorSystem, Name: "system"}
		actor, _ := json.Marshal(system)
		for _, cur := range due {
			out, err := scanAutomation(tx.QueryRow(ctx, `UPDATE cluster_automation SET paused = false, reason = 'resumeAt reached', changed_by = $2,
					changed_at = $3, resume_at = NULL, version = version + 1 WHERE scope = $1 RETURNING `+automationColumns, cur.Scope, actor, now))
			if err != nil {
				return err
			}
			c := Change{Actor: system, Action: hubapi.ActionAutomationResumed, Reason: "resumeAt reached", Time: now,
				Target: hubapi.ConfigTarget{Type: hubapi.TargetAutomation, ID: cur.Scope, Name: cur.Scope}, Version: out.Version,
				Before: cur.Automation, After: out.Automation}
			if cur.Scope != hubapi.FleetScope {
				c.Target.Cluster = cur.Scope
			}
			if err := recordChange(ctx, tx, c); err != nil {
				return err
			}
			scopes = append(scopes, cur.Scope)
		}
		return nil
	})
	return scopes, err
}

// ---- approve once (D-024) ----

const approvalColumns = `id, cluster, namespace, kind, name, recommendation_id, approved_by, approved_at, expires_at, reason,
	consumed_at, consumed_event_id, superseded_at`

func scanApproval(r pgx.Row, now time.Time) (hubapi.Approval, hubapi.WorkloadKey, error) {
	var (
		a             hubapi.Approval
		k             hubapi.WorkloadKey
		id, rec       uuid.UUID
		by            []byte
		consumedEvent *uuid.UUID
		superseded    *time.Time
	)
	err := r.Scan(&id, &k.Cluster, &k.Namespace, &k.Kind, &k.Name, &rec, &by, &a.ApprovedAt, &a.ExpiresAt, &a.Reason, &a.ConsumedAt,
		&consumedEvent, &superseded)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, k, ErrNotFound
	}
	if err != nil {
		return a, k, err
	}
	a.ID, a.RecommendationID, a.WorkloadID = id.String(), rec.String(), k.ID()
	a.ApprovedAt, a.ExpiresAt, a.ConsumedAt = a.ApprovedAt.UTC(), a.ExpiresAt.UTC(), utc(a.ConsumedAt)
	if consumedEvent != nil {
		a.ConsumedEventID = consumedEvent.String()
	}
	_ = json.Unmarshal(by, &a.ApprovedBy)
	switch {
	case a.ConsumedAt != nil:
		a.State = hubapi.ApprovalConsumed
	case superseded != nil:
		a.State = hubapi.ApprovalSuperseded
	case !now.Before(a.ExpiresAt):
		a.State = hubapi.ApprovalExpired
	default:
		a.State = hubapi.ApprovalPending
	}
	return a, k, nil
}

// CreateApproval supersedes the workload's open approval, stores a new one and records c.
func (s *Store) CreateApproval(ctx context.Context, k hubapi.WorkloadKey, recID string, expires time.Time, by hubapi.Actor, reason string, c Change) (hubapi.Approval, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return hubapi.Approval{}, err
	}
	rec, err := uuid.Parse(recID)
	if err != nil {
		return hubapi.Approval{}, err
	}
	var out hubapi.Approval
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE workload_approvals SET superseded_at = $5 WHERE cluster = $1 AND namespace = $2 AND kind = $3 AND name = $4
			AND consumed_at IS NULL AND superseded_at IS NULL`, k.Cluster, k.Namespace, k.Kind, k.Name, c.Time); err != nil {
			return err
		}
		actor, _ := json.Marshal(by)
		if out, _, err = scanApproval(tx.QueryRow(ctx, `INSERT INTO workload_approvals (id, cluster, namespace, kind, name, workload_id,
				recommendation_id, approved_by, approved_at, expires_at, reason)
			VALUES ($1, $2, $3, $4, $5, (SELECT id FROM workloads WHERE cluster = $2 AND namespace = $3 AND kind = $4 AND name = $5),
				$6, $7, $8, $9, $10) RETURNING `+approvalColumns, id, k.Cluster, k.Namespace, k.Kind, k.Name, rec, actor, c.Time, expires, reason), c.Time); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetApproval, ID: out.ID, Name: k.Name, Cluster: k.Cluster, WorkloadID: k.ID()}
		c.Workload, c.After = &k, out
		return recordChange(ctx, tx, c)
	})
	return out, err
}

// LatestApproval returns the newest approval of a workload.
func (s *Store) LatestApproval(ctx context.Context, k hubapi.WorkloadKey, now time.Time) (*hubapi.Approval, error) {
	a, _, err := scanApproval(s.Pool.QueryRow(ctx, `SELECT `+approvalColumns+` FROM workload_approvals
		WHERE cluster = $1 AND namespace = $2 AND kind = $3 AND name = $4 ORDER BY approved_at DESC, id DESC LIMIT 1`,
		k.Cluster, k.Namespace, k.Kind, k.Name), now)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// PendingApprovals returns a cluster's open, unexpired approvals whose recommendation is still
// the workload's latest (what the agent may apply).
func (s *Store) PendingApprovals(ctx context.Context, cluster string, now time.Time) ([]hubapi.ConfigApproval, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+approvalColumns+` FROM workload_approvals a
		WHERE a.cluster = $1 AND a.consumed_at IS NULL AND a.superseded_at IS NULL AND a.expires_at > $2
		AND a.recommendation_id = (SELECT r.id FROM workloads w JOIN recommendations r ON r.workload_id = w.id
			WHERE w.cluster = a.cluster AND w.namespace = a.namespace AND w.kind = a.kind AND w.name = a.name
			ORDER BY r.generated_at DESC, r.id DESC LIMIT 1)
		ORDER BY a.approved_at, a.id`, cluster, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hubapi.ConfigApproval{}
	for rows.Next() {
		a, k, err := scanApproval(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, hubapi.ConfigApproval{ID: a.ID, Workload: k.Ref(), RecommendationID: a.RecommendationID, ExpiresAt: a.ExpiresAt,
			ApprovedBy: a.ApprovedBy.Name, ApprovedAt: a.ApprovedAt})
	}
	return out, rows.Err()
}

// ConsumeApproval marks an open approval of the cluster consumed by an agent event.
func (s *Store) ConsumeApproval(ctx context.Context, cluster, approvalID, eventID string, at time.Time) (bool, error) {
	id, perr := uuid.Parse(approvalID)
	if perr != nil {
		return false, nil //nolint:nilerr // an unknown id consumes nothing
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE workload_approvals SET consumed_at = $3, consumed_event_id = $4
		WHERE id = $1 AND cluster = $2 AND consumed_at IS NULL`, id, cluster, at, nullUUID(eventID))
	return tag.RowsAffected() == 1, err
}

// SupersedeStaleApprovals marks open approvals of a cluster superseded when their workload has a
// newer recommendation.
func (s *Store) SupersedeStaleApprovals(ctx context.Context, cluster string, now time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE workload_approvals a SET superseded_at = $2
		WHERE a.cluster = $1 AND a.consumed_at IS NULL AND a.superseded_at IS NULL
		AND a.recommendation_id IS DISTINCT FROM (SELECT r.id FROM workloads w JOIN recommendations r ON r.workload_id = w.id
			WHERE w.cluster = a.cluster AND w.namespace = a.namespace AND w.kind = a.kind AND w.name = a.name
			ORDER BY r.generated_at DESC, r.id DESC LIMIT 1)
		AND EXISTS (SELECT 1 FROM workloads w JOIN recommendations r ON r.workload_id = w.id
			WHERE w.cluster = a.cluster AND w.namespace = a.namespace AND w.kind = a.kind AND w.name = a.name)`, cluster, now)
	return tag.RowsAffected(), err
}
