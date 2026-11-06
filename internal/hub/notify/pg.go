package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Errors of the repository; the hub maps them to problem codes.
var (
	ErrNotFound        = errors.New("not found")
	ErrVersionConflict = errors.New("version conflict")
	ErrNameTaken       = errors.New("name already in use")
)

// DB is the part of pgxpool.Pool the repository uses.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PG is the Postgres repository (tables of db/migrations/0015_notifications.sql). Ids are UUID
// strings.
type PG struct{ DB DB }

// Channel is a channel as stored: the URL and the signing secret are sealed in SecretEnc.
type Channel struct {
	ID            string
	Name          string
	Type          string
	Source        string
	Enabled       bool
	SecretEnc     []byte
	URLHint       string
	HasSecret     bool
	Version       int
	CreatedAt     time.Time
	CreatedBy     *hubapi.Actor
	UpdatedAt     time.Time
	UpdatedBy     *hubapi.Actor
	LastStatus    string
	LastAttemptAt *time.Time
	LastError     string
	LastSuccessAt *time.Time
	Pending       int
}

// Rule is a stored rule.
type Rule struct {
	ID         string
	Name       string
	Enabled    bool
	EventTypes []string
	Clusters   []string
	Namespaces []string
	ChannelIDs []string
	Version    int
	CreatedAt  time.Time
	CreatedBy  *hubapi.Actor
	UpdatedAt  time.Time
	UpdatedBy  *hubapi.Actor
}

// DigestSettings is the stored digest row.
type DigestSettings struct {
	Enabled    bool
	Weekday    int
	TimeOfDay  string
	Timezone   string
	ChannelIDs []string
	Version    int
	UpdatedAt  time.Time
	UpdatedBy  *hubapi.Actor
	LastPeriod *time.Time
}

// Delivery is a queued, sent or failed message to one channel.
type Delivery struct {
	ID            string
	ChannelID     string
	ChannelName   string
	Kind          string
	EventID       string
	EventType     string
	DedupeKey     string
	Summary       string
	Payload       Message
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	HTTPStatus    int
	CreatedAt     time.Time
	LastAttemptAt *time.Time
	SentAt        *time.Time
}

func actorOf(b []byte) *hubapi.Actor {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var a hubapi.Actor
	if json.Unmarshal(b, &a) != nil {
		return nil
	}
	return &a
}

func actorJSON(a hubapi.Actor) []byte {
	b, _ := json.Marshal(a)
	return b
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// ---- channels ----

const channelColumns = `c.id::text, c.name, c.type, c.source, c.enabled, c.secret_enc, c.url_hint, c.has_secret, c.version, c.created_at, c.created_by,
	c.updated_at, c.updated_by, coalesce(c.last_status, ''), c.last_attempt_at, c.last_error, c.last_success_at,
	(SELECT count(*) FROM notification_deliveries d WHERE d.channel_id = c.id AND d.status = 'pending')`

func scanChannel(row pgx.Row) (Channel, error) {
	var c Channel
	var cb, ub []byte
	err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Source, &c.Enabled, &c.SecretEnc, &c.URLHint, &c.HasSecret, &c.Version, &c.CreatedAt, &cb,
		&c.UpdatedAt, &ub, &c.LastStatus, &c.LastAttemptAt, &c.LastError, &c.LastSuccessAt, &c.Pending)
	c.CreatedBy, c.UpdatedBy = actorOf(cb), actorOf(ub)
	return c, err
}

// ListChannels returns the live channels by name.
func (p *PG) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := p.DB.Query(ctx, `SELECT `+channelColumns+` FROM notification_channels c WHERE c.deleted_at IS NULL ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannel returns a live channel.
func (p *PG) GetChannel(ctx context.Context, id string) (Channel, error) {
	c, err := scanChannel(p.DB.QueryRow(ctx, `SELECT `+channelColumns+` FROM notification_channels c WHERE c.id = $1::uuid AND c.deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return c, err
}

// InsertChannel stores a new channel (version 1).
func (p *PG) InsertChannel(ctx context.Context, c Channel, by hubapi.Actor) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO notification_channels (id, name, type, source, enabled, secret_enc, url_hint, has_secret, version, created_by, updated_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, 1, $9, $9)`,
		c.ID, c.Name, c.Type, c.Source, c.Enabled, c.SecretEnc, c.URLHint, c.HasSecret, actorJSON(by))
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

// UpdateChannel replaces the editable fields of a channel if its stored version is version.
func (p *PG) UpdateChannel(ctx context.Context, c Channel, version int, by hubapi.Actor) error {
	tag, err := p.DB.Exec(ctx, `UPDATE notification_channels SET name = $3, type = $4, enabled = $5, secret_enc = $6, url_hint = $7, has_secret = $8,
			version = version + 1, updated_at = now(), updated_by = $9
		WHERE id = $1::uuid AND deleted_at IS NULL AND version = $2`,
		c.ID, version, c.Name, c.Type, c.Enabled, c.SecretEnc, c.URLHint, c.HasSecret, actorJSON(by))
	if isUnique(err) {
		return ErrNameTaken
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return p.missingOrConflict(ctx, "notification_channels", c.ID)
	}
	return nil
}

func (p *PG) missingOrConflict(ctx context.Context, table, id string) error {
	var n int
	if err := p.DB.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1::uuid AND deleted_at IS NULL`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return ErrVersionConflict
}

// DeleteChannel soft-deletes a channel if its stored version is version and drops its queue.
func (p *PG) DeleteChannel(ctx context.Context, id string, version int, by hubapi.Actor) error {
	tag, err := p.DB.Exec(ctx, `UPDATE notification_channels SET deleted_at = now(), updated_at = now(), updated_by = $3, version = version + 1
		WHERE id = $1::uuid AND deleted_at IS NULL AND version = $2`, id, version, actorJSON(by))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return p.missingOrConflict(ctx, "notification_channels", id)
	}
	_, err = p.DB.Exec(ctx, `DELETE FROM notification_deliveries WHERE channel_id = $1::uuid AND status = 'pending'`, id)
	return err
}

// ChannelUsers returns the names of the rules and of the digest that send to the channel.
func (p *PG) ChannelUsers(ctx context.Context, id string) ([]string, error) {
	rows, err := p.DB.Query(ctx, `SELECT 'rule ' || name FROM notification_rules WHERE deleted_at IS NULL AND $1::uuid = ANY(channel_ids)
		UNION ALL SELECT 'the weekly digest' FROM notification_digest WHERE enabled AND $1::uuid = ANY(channel_ids)`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecordResult stores the outcome of a delivery attempt on the channel (for the channel list and the
// kerf_notification_last_success_timestamp_seconds gauge). status is sent, failed or retrying.
func (p *PG) RecordResult(ctx context.Context, id, status string, at time.Time, errText string) error {
	_, err := p.DB.Exec(ctx, `UPDATE notification_channels SET last_status = $2, last_attempt_at = $3, last_error = $4,
			last_success_at = CASE WHEN $2 = 'sent' THEN $3 ELSE last_success_at END
		WHERE id = $1::uuid`, id, status, at, errText)
	return err
}

// SyncConfigChannels makes the channels with source config equal to want (matched by id): it
// upserts them and soft-deletes the ones no longer listed. Their sealed secret is replaced, so a
// rotated Secret takes effect at the next start.
func (p *PG) SyncConfigChannels(ctx context.Context, want []Channel) error {
	ids := make([]string, 0, len(want))
	for _, c := range want {
		ids = append(ids, c.ID)
		sys, _ := json.Marshal(hubapi.Actor{Type: "system", Name: "chart values"})
		_, err := p.DB.Exec(ctx, `INSERT INTO notification_channels (id, name, type, source, enabled, secret_enc, url_hint, has_secret, version, created_by, updated_by)
			VALUES ($1::uuid, $2, $3, 'config', $4, $5, $6, $7, 1, $8, $8)
			ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, type = EXCLUDED.type, enabled = EXCLUDED.enabled, secret_enc = EXCLUDED.secret_enc,
				url_hint = EXCLUDED.url_hint, has_secret = EXCLUDED.has_secret, deleted_at = NULL, updated_at = now(), updated_by = EXCLUDED.updated_by,
				version = notification_channels.version + 1`,
			c.ID, c.Name, c.Type, c.Enabled, c.SecretEnc, c.URLHint, c.HasSecret, sys)
		if isUnique(err) {
			return fmt.Errorf("channel %q: %w (an API channel has this name)", c.Name, ErrNameTaken)
		}
		if err != nil {
			return err
		}
	}
	_, err := p.DB.Exec(ctx, `UPDATE notification_channels SET deleted_at = now() WHERE source = 'config' AND deleted_at IS NULL AND NOT (id = ANY($1::uuid[]))`, ids)
	return err
}

// ---- rules ----

const ruleColumns = `id::text, name, enabled, event_types, clusters, namespaces, channel_ids::text[], version, created_at, created_by, updated_at, updated_by`

func scanRule(row pgx.Row) (Rule, error) {
	var r Rule
	var cb, ub []byte
	err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.EventTypes, &r.Clusters, &r.Namespaces, &r.ChannelIDs, &r.Version, &r.CreatedAt, &cb, &r.UpdatedAt, &ub)
	r.CreatedBy, r.UpdatedBy = actorOf(cb), actorOf(ub)
	return r, err
}

// ListRules returns the live rules by name.
func (p *PG) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := p.DB.Query(ctx, `SELECT `+ruleColumns+` FROM notification_rules WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRule returns a live rule.
func (p *PG) GetRule(ctx context.Context, id string) (Rule, error) {
	r, err := scanRule(p.DB.QueryRow(ctx, `SELECT `+ruleColumns+` FROM notification_rules WHERE id = $1::uuid AND deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	return r, err
}

// InsertRule stores a new rule (version 1).
func (p *PG) InsertRule(ctx context.Context, r Rule, by hubapi.Actor) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO notification_rules (id, name, enabled, event_types, clusters, namespaces, channel_ids, version, created_by, updated_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid[], 1, $8, $8)`,
		r.ID, r.Name, r.Enabled, r.EventTypes, nz(r.Clusters), nz(r.Namespaces), r.ChannelIDs, actorJSON(by))
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// UpdateRule replaces a rule if its stored version is version.
func (p *PG) UpdateRule(ctx context.Context, r Rule, version int, by hubapi.Actor) error {
	tag, err := p.DB.Exec(ctx, `UPDATE notification_rules SET name = $3, enabled = $4, event_types = $5, clusters = $6, namespaces = $7, channel_ids = $8::uuid[],
			version = version + 1, updated_at = now(), updated_by = $9
		WHERE id = $1::uuid AND deleted_at IS NULL AND version = $2`,
		r.ID, version, r.Name, r.Enabled, r.EventTypes, nz(r.Clusters), nz(r.Namespaces), r.ChannelIDs, actorJSON(by))
	if isUnique(err) {
		return ErrNameTaken
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return p.missingOrConflict(ctx, "notification_rules", r.ID)
	}
	return nil
}

// DeleteRule soft-deletes a rule if its stored version is version.
func (p *PG) DeleteRule(ctx context.Context, id string, version int, by hubapi.Actor) error {
	tag, err := p.DB.Exec(ctx, `UPDATE notification_rules SET deleted_at = now(), updated_at = now(), updated_by = $3, version = version + 1
		WHERE id = $1::uuid AND deleted_at IS NULL AND version = $2`, id, version, actorJSON(by))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return p.missingOrConflict(ctx, "notification_rules", id)
	}
	return nil
}

// ---- digest ----

// GetDigest returns the digest settings.
func (p *PG) GetDigest(ctx context.Context) (DigestSettings, error) {
	var d DigestSettings
	var ub []byte
	err := p.DB.QueryRow(ctx, `SELECT enabled, weekday, time_of_day, timezone, channel_ids::text[], version, updated_at, updated_by, last_period
		FROM notification_digest WHERE id = 1`).Scan(&d.Enabled, &d.Weekday, &d.TimeOfDay, &d.Timezone, &d.ChannelIDs, &d.Version, &d.UpdatedAt, &ub, &d.LastPeriod)
	d.UpdatedBy = actorOf(ub)
	return d, err
}

// UpdateDigest stores the settings if the stored version is version.
func (p *PG) UpdateDigest(ctx context.Context, d DigestSettings, version int, by hubapi.Actor) error {
	tag, err := p.DB.Exec(ctx, `UPDATE notification_digest SET enabled = $2, weekday = $3, time_of_day = $4, timezone = $5, channel_ids = $6::uuid[],
			version = version + 1, updated_at = now(), updated_by = $7
		WHERE id = 1 AND version = $1`, version, d.Enabled, d.Weekday, d.TimeOfDay, d.Timezone, nz(d.ChannelIDs), actorJSON(by))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

// ClaimDigestPeriod records that the digest scheduled for period is being queued; it reports false
// when that period (or a later one) was already queued, on this or another replica.
func (p *PG) ClaimDigestPeriod(ctx context.Context, period time.Time) (bool, error) {
	tag, err := p.DB.Exec(ctx, `UPDATE notification_digest SET last_period = $1 WHERE id = 1 AND (last_period IS NULL OR last_period < $1)`, period)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ---- deliveries ----

// Enqueue queues d. It reports false when nothing was queued: the channel already has a delivery
// for the event, or one with the same dedupe key younger than dedupeWithin (0 = no window).
func (p *PG) Enqueue(ctx context.Context, d Delivery, dedupeWithin time.Duration) (bool, error) {
	payload, err := json.Marshal(d.Payload)
	if err != nil {
		return false, err
	}
	var eventID *string
	if d.EventID != "" {
		eventID = &d.EventID
	}
	tag, err := p.DB.Exec(ctx, `INSERT INTO notification_deliveries (id, channel_id, kind, event_id, event_type, dedupe_key, summary, payload, status, next_attempt_at, created_at)
		SELECT $1::uuid, $2::uuid, $3::text, $4::uuid, $5::text, $6::text, $7::text, $8::jsonb, 'pending', $9::timestamptz, $9::timestamptz
		WHERE NOT EXISTS (SELECT 1 FROM notification_deliveries WHERE $6::text <> '' AND $10::float8 > 0 AND channel_id = $2::uuid AND dedupe_key = $6
				AND created_at > $9::timestamptz - make_interval(secs => $10::float8))
		ON CONFLICT DO NOTHING`,
		d.ID, d.ChannelID, d.Kind, eventID, d.EventType, d.DedupeKey, d.Summary, payload, d.CreatedAt, dedupeWithin.Seconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

const deliveryColumns = `d.id::text, d.channel_id::text, c.name, d.kind, coalesce(d.event_id::text, ''), d.event_type, d.dedupe_key, d.summary, d.payload, d.status,
	d.attempts, d.next_attempt_at, d.last_error, coalesce(d.last_http_status, 0), d.created_at, d.last_attempt_at, d.sent_at`

func scanDelivery(row pgx.Row) (Delivery, error) {
	var d Delivery
	var payload []byte
	err := row.Scan(&d.ID, &d.ChannelID, &d.ChannelName, &d.Kind, &d.EventID, &d.EventType, &d.DedupeKey, &d.Summary, &payload, &d.Status,
		&d.Attempts, &d.NextAttemptAt, &d.LastError, &d.HTTPStatus, &d.CreatedAt, &d.LastAttemptAt, &d.SentAt)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(payload, &d.Payload); err != nil {
		return d, fmt.Errorf("delivery %s payload: %w", d.ID, err)
	}
	return d, nil
}

// Claim returns up to limit deliveries due at now and leases them for lease: a second leader that
// overlaps for a moment does not send the same message.
func (p *PG) Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Delivery, error) {
	rows, err := p.DB.Query(ctx, `WITH due AS (
			SELECT id FROM notification_deliveries WHERE status = 'pending' AND next_attempt_at <= $1
			ORDER BY next_attempt_at, created_at LIMIT $2 FOR UPDATE SKIP LOCKED),
		leased AS (
			UPDATE notification_deliveries d SET next_attempt_at = $1::timestamptz + make_interval(secs => $3) FROM due WHERE d.id = due.id RETURNING d.*)
		SELECT `+deliveryColumns+` FROM leased d JOIN notification_channels c ON c.id = d.channel_id ORDER BY d.created_at`,
		now, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Settle stores the outcome of an attempt: status pending (retry at next), sent or failed.
func (p *PG) Settle(ctx context.Context, id, status string, attempts int, at, next time.Time, errText string, httpStatus int) error {
	var sentAt *time.Time
	if status == hubapi.DeliverySent {
		sentAt = &at
	}
	var hs *int
	if httpStatus != 0 {
		hs = &httpStatus
	}
	_, err := p.DB.Exec(ctx, `UPDATE notification_deliveries SET status = $2, attempts = $3, last_attempt_at = $4, next_attempt_at = $5, last_error = $6,
			last_http_status = $7, sent_at = $8 WHERE id = $1::uuid`, id, status, attempts, at, next, errText, hs, sentAt)
	return err
}

// DeliveryFilter filters ListDeliveries.
type DeliveryFilter struct {
	ChannelID string
	Status    string
	Limit     int
	Offset    int
}

// ListDeliveries returns deliveries, newest first, and whether more exist.
func (p *PG) ListDeliveries(ctx context.Context, f DeliveryFilter) ([]Delivery, bool, error) {
	rows, err := p.DB.Query(ctx, `SELECT `+deliveryColumns+` FROM notification_deliveries d JOIN notification_channels c ON c.id = d.channel_id
		WHERE ($1 = '' OR d.channel_id = $1::uuid) AND ($2 = '' OR d.status = $2)
		ORDER BY d.created_at DESC, d.id DESC LIMIT $3 OFFSET $4`, f.ChannelID, f.Status, f.Limit+1, f.Offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	return out, more, nil
}

// PruneDeliveries deletes finished deliveries created before t.
func (p *PG) PruneDeliveries(ctx context.Context, t time.Time) (int64, error) {
	tag, err := p.DB.Exec(ctx, `DELETE FROM notification_deliveries WHERE status <> 'pending' AND created_at < $1`, t)
	return tag.RowsAffected(), err
}

// ---- events ----

// Cursor returns where the leader stopped reading the audit log and the floor below which it never
// reads; ok is false before the first run.
func (p *PG) Cursor(ctx context.Context) (pos, floor time.Time, ok bool, err error) {
	err = p.DB.QueryRow(ctx, `SELECT received_at, floor_at FROM notification_cursor WHERE id = 1`).Scan(&pos, &floor)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, time.Time{}, false, nil
	}
	return pos, floor, err == nil, err
}

// SetCursor stores the read position.
func (p *PG) SetCursor(ctx context.Context, t time.Time) error {
	_, err := p.DB.Exec(ctx, `UPDATE notification_cursor SET received_at = $1 WHERE id = 1`, t)
	return err
}

// ResetCursor moves both the position and the floor to the database's current time: nothing stored
// before it is ever sent. (The log's received_at is the database's clock, so is the cursor.)
func (p *PG) ResetCursor(ctx context.Context) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO notification_cursor (id, received_at, floor_at) VALUES (1, now(), now())
		ON CONFLICT (id) DO UPDATE SET received_at = EXCLUDED.received_at, floor_at = EXCLUDED.floor_at`)
	return err
}

// Event is an audit row that can become a notification.
type Event struct {
	ID         string
	Type       string // a hubapi.NotificationEventType
	Reason     string
	Time       time.Time
	ReceivedAt time.Time
	Cluster    string
	Namespace  string
	Kind       string
	Name       string
	Strategy   string
	Resources  []string
	Containers []hubapi.ContainerChange
	Message    string
	Details    map[string]string
	Actor      string
	Scope      string
	FromState  string
	ToState    string
}

// EventsSince returns up to limit notifiable audit rows stored at or after since, oldest first.
// Callers pass an overlap (a row can commit after a later one) and rely on Enqueue to ignore
// events it already queued.
func (p *PG) EventsSince(ctx context.Context, since time.Time, limit int) ([]Event, error) {
	rows, err := p.DB.Query(ctx, `SELECT id::text, type, reason, time, received_at, coalesce(cluster, ''), coalesce(namespace, ''), coalesce(workload_kind, ''),
			coalesce(workload_name, ''), coalesce(strategy, ''), resources, containers, message, details,
			coalesce(actor->>'name', ''), coalesce(target_type, ''), coalesce(target_id, ''), before, after
		FROM audit_events
		WHERE received_at >= $1 AND (type IN ('applied', 'rolled_back', 'failed', 'blocked')
			OR (type = 'config_changed' AND (target_type = 'automation' OR (target_type = 'readiness' AND reason = 'ReadinessChanged'))))
		ORDER BY received_at, id LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var typ, targetType, targetID string
		var containers, details, before, after []byte
		if err := rows.Scan(&e.ID, &typ, &e.Reason, &e.Time, &e.ReceivedAt, &e.Cluster, &e.Namespace, &e.Kind, &e.Name, &e.Strategy, &e.Resources,
			&containers, &e.Message, &details, &e.Actor, &targetType, &targetID, &before, &after); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(containers, &e.Containers)
		_ = json.Unmarshal(details, &e.Details)
		if typ == hubapi.EventConfigChanged {
			e.Type = configEventType(&e, targetType, targetID, before, after)
			if e.Type == "" {
				continue // a readiness change that isn't a drop, or an unrelated action
			}
		} else {
			e.Type = typ
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// configEventType maps a config_changed row to a notification event type ("" = none) and fills the
// fields of the event that come from before/after.
func configEventType(e *Event, targetType, targetID string, before, after []byte) string {
	switch targetType {
	case hubapi.TargetAutomation:
		e.Scope = "cluster"
		if targetID == "*" {
			e.Scope, e.Cluster = "fleet", ""
		} else {
			e.Cluster = targetID
		}
		switch e.Reason {
		case hubapi.ActionAutomationPaused:
			return hubapi.NotifyAutomationPaused
		case hubapi.ActionAutomationResumed:
			return hubapi.NotifyAutomationResumed
		}
	case hubapi.TargetReadiness:
		e.Cluster = targetID
		var b, a struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(before, &b)
		_ = json.Unmarshal(after, &a)
		e.FromState, e.ToState = b.State, a.State
		from, to := readiness.State(b.State), readiness.State(a.State)
		if from != to && from.AtLeast(to) && to.AtLeast(readiness.StateRegistered) {
			return hubapi.NotifyReadinessDegraded
		}
	}
	return ""
}

// ChangeCounts counts applied, rolled back and failed events received in [from, to).
func (p *PG) ChangeCounts(ctx context.Context, from, to time.Time) (c ChangeCounts, err error) {
	err = p.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE type = 'applied'), count(*) FILTER (WHERE type = 'rolled_back'), count(*) FILTER (WHERE type = 'failed')
		FROM audit_events WHERE received_at >= $1 AND received_at < $2 AND type IN ('applied', 'rolled_back', 'failed')`, from, to).Scan(&c.Applied, &c.RolledBack, &c.Failed)
	return c, err
}
