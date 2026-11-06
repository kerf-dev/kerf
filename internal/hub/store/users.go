package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// UserRow is a local user with its credentials and login bookkeeping (D-023).
type UserRow struct {
	hubapi.User
	PasswordHash string
	FailedLogins int
}

const userColumns = `username, display_name, role, password_hash, disabled, must_change_password, failed_logins, locked_until,
	last_login_at, created_at, coalesce(created_by, ''), updated_at, version, deleted_at, source, coalesce(email, ''), scope, team_roles`

func scanUser(r pgx.Row) (UserRow, error) {
	var u UserRow
	var updated time.Time
	var roles []byte
	err := r.Scan(&u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.Disabled, &u.MustChangePassword, &u.FailedLogins,
		&u.LockedUntil, &u.LastLoginAt, &u.CreatedAt, &u.CreatedBy, &updated, &u.Version, &u.DeletedAt, &u.Source, &u.Email,
		&u.Scope, &roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	u.TeamRoles = parseTeamRoles(roles)
	u.CreatedAt = u.CreatedAt.UTC()
	updated = updated.UTC()
	u.UpdatedAt = &updated
	u.LockedUntil, u.LastLoginAt, u.DeletedAt = utc(u.LockedUntil), utc(u.LastLoginAt), utc(u.DeletedAt)
	return u, err
}

// GetUser returns a user, deleted ones included.
func (s *Store) GetUser(ctx context.Context, username string) (UserRow, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username))
}

// ListUsers lists users by name.
func (s *Store) ListUsers(ctx context.Context, includeDeleted bool) ([]hubapi.User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+userColumns+` FROM users WHERE $1 OR deleted_at IS NULL ORDER BY username`, includeDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hubapi.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u.User)
	}
	return out, rows.Err()
}

// CountUsers returns the number of users, deleted ones included.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// EnsureUser creates a user if no user (live or deleted) has the name (bootstrap and dev seed;
// not audited). It reports whether it created one.
func (s *Store) EnsureUser(ctx context.Context, username, displayName, role, hash string, mustChange bool, now time.Time) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO users (username, display_name, role, password_hash, must_change_password,
			password_changed_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6, $6) ON CONFLICT (username) DO NOTHING`, username, displayName, role, hash, mustChange, now)
	return tag.RowsAffected() == 1, err
}

// CreateUser creates a user and records c (its After is set to the new user). A deleted user of
// the same name is replaced: usernames stay unique in the audit trail per lifetime.
func (s *Store) CreateUser(ctx context.Context, u hubapi.UserCreate, hash string, mustChange bool, c Change) (hubapi.User, error) {
	var out UserRow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		scope, roles := userBindings(u.Scope, u.TeamRoles)
		out, err = scanUser(tx.QueryRow(ctx, `INSERT INTO users (username, display_name, role, password_hash, must_change_password,
				password_changed_at, created_at, created_by, updated_at, scope, team_roles)
			VALUES ($1, $2, $3, $4, $5, $6, $6, $7, $6, $8, $9)
			ON CONFLICT (username) DO UPDATE SET display_name = excluded.display_name, role = excluded.role,
				scope = excluded.scope, team_roles = excluded.team_roles,
				password_hash = excluded.password_hash, must_change_password = excluded.must_change_password,
				password_changed_at = excluded.password_changed_at, created_at = excluded.created_at, created_by = excluded.created_by,
				updated_at = excluded.updated_at, disabled = false, failed_logins = 0, locked_until = NULL, last_login_at = NULL,
				deleted_at = NULL, source = 'local', email = NULL, version = users.version + 1
			WHERE users.deleted_at IS NOT NULL
			RETURNING `+userColumns, u.Username, u.DisplayName, u.Role, hash, mustChange, c.Time, c.Actor.Name, scope, roles))
		if errors.Is(err, ErrNotFound) {
			return ErrAlreadyExists
		}
		if err != nil {
			return err
		}
		// A deleted IdP user of the same name keeps no link to the new local user.
		if _, err := tx.Exec(ctx, `DELETE FROM user_identities WHERE username = $1`, out.Username); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetUser, ID: out.Username, Name: out.Username}
		c.Version, c.After = out.Version, out.User
		return recordChange(ctx, tx, c)
	})
	return out.User, err
}

// lockUser selects a live user for update.
func lockUser(ctx context.Context, tx pgx.Tx, username string) (UserRow, error) {
	return scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1 AND deleted_at IS NULL FOR UPDATE`, username))
}

// otherAdmins counts enabled live admins other than username (locking them).
func otherAdmins(ctx context.Context, tx pgx.Tx, username string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT username FROM users WHERE role = 'admin' AND NOT disabled AND deleted_at IS NULL AND username <> $1 FOR UPDATE`, username)
	if err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	return n, rows.Err()
}

// UpdateUser applies upd to a live user at version ifVersion and records c.
func (s *Store) UpdateUser(ctx context.Context, username string, ifVersion int, upd hubapi.UserUpdate, c Change) (hubapi.User, error) {
	var out UserRow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockUser(ctx, tx, username)
		if err != nil {
			return err
		}
		if cur.Version != ifVersion {
			return ErrVersionConflict
		}
		next := cur
		if upd.DisplayName != nil {
			next.DisplayName = *upd.DisplayName
		}
		if upd.Role != nil {
			next.Role = *upd.Role
		}
		if upd.Disabled != nil {
			next.Disabled = *upd.Disabled
		}
		if upd.Scope != nil {
			next.Scope = *upd.Scope
		}
		if upd.TeamRoles != nil {
			next.TeamRoles = *upd.TeamRoles
		}
		if next.Scope == hubapi.ScopeTeams {
			// The role of a team-scoped user is the highest of its team roles.
			next.Role = maxTeamRole(next.TeamRoles, next.Role)
		}
		if cur.Role == hubapi.UserRoleAdmin && !cur.Disabled && (next.Role != hubapi.UserRoleAdmin || next.Disabled) {
			n, err := otherAdmins(ctx, tx, username)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrLastAdmin
			}
		}
		scope, roles := userBindings(next.Scope, next.TeamRoles)
		out, err = scanUser(tx.QueryRow(ctx, `UPDATE users SET display_name = $2, role = $3, disabled = $4, updated_at = $5,
				scope = $6, team_roles = $7, version = version + 1 WHERE username = $1 RETURNING `+userColumns,
			username, next.DisplayName, next.Role, next.Disabled, c.Time, scope, roles))
		if err != nil {
			return err
		}
		if next.Disabled && !cur.Disabled {
			if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE username = $1 AND revoked_at IS NULL`, username, c.Time); err != nil {
				return err
			}
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetUser, ID: username, Name: username}
		c.Version, c.Before, c.After = out.Version, cur.User, out.User
		return recordChange(ctx, tx, c)
	})
	return out.User, err
}

// DeleteUser soft-deletes a live user at version ifVersion, revokes its sessions and records c.
func (s *Store) DeleteUser(ctx context.Context, username string, ifVersion int, c Change) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockUser(ctx, tx, username)
		if err != nil {
			return err
		}
		if cur.Version != ifVersion {
			return ErrVersionConflict
		}
		if cur.Role == hubapi.UserRoleAdmin && !cur.Disabled {
			n, err := otherAdmins(ctx, tx, username)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrLastAdmin
			}
		}
		out, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET deleted_at = $2, updated_at = $2, version = version + 1
			WHERE username = $1 RETURNING `+userColumns, username, c.Time))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE username = $1 AND revoked_at IS NULL`, username, c.Time); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetUser, ID: username, Name: username}
		c.Version, c.Before, c.After = out.Version, cur.User, out.User
		return recordChange(ctx, tx, c)
	})
}

// SetPassword replaces a live user's password, clears the lockout and revokes the user's sessions
// except keepSession (nil: all). A non-nil c is recorded (admin resets; own changes are not).
func (s *Store) SetPassword(ctx context.Context, username, hash string, mustChange bool, keepSession []byte, now time.Time, c *Change) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockUser(ctx, tx, username)
		if err != nil {
			return err
		}
		out, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET password_hash = $2, must_change_password = $3, password_changed_at = $4,
				failed_logins = 0, locked_until = NULL, updated_at = $4, version = version + 1
			WHERE username = $1 RETURNING `+userColumns, username, hash, mustChange, now))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE username = $1 AND revoked_at IS NULL
			AND ($3::bytea IS NULL OR id_sha256 <> $3)`, username, now, keepSession); err != nil {
			return err
		}
		if c == nil {
			return nil
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetUser, ID: username, Name: username}
		c.Version, c.Before, c.After = out.Version, cur.User, out.User
		return recordChange(ctx, tx, *c)
	})
}

// LoginFailed counts a failed login of a live user and locks it until lockUntil once failures
// reach maxFailures (0 = never). It returns the lock time, if any.
func (s *Store) LoginFailed(ctx context.Context, username string, maxFailures int, lockUntil time.Time) (*time.Time, error) {
	var locked *time.Time
	err := s.Pool.QueryRow(ctx, `UPDATE users SET failed_logins = failed_logins + 1,
			locked_until = CASE WHEN $2 > 0 AND failed_logins + 1 >= $2 THEN $3 ELSE locked_until END
		WHERE username = $1 AND deleted_at IS NULL RETURNING locked_until`, username, maxFailures, lockUntil).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return utc(locked), err
}

// LoginSucceeded resets the failure counter and records the login time.
func (s *Store) LoginSucceeded(ctx context.Context, username string, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET failed_logins = 0, locked_until = NULL, last_login_at = $2 WHERE username = $1`, username, now)
	return err
}

// ---- sessions ----

// Session is a valid browser session and its user.
type Session struct {
	IDHash    []byte
	CSRFToken string
	ExpiresAt time.Time
	User      UserRow
	// OIDC sessions: the provider identity, the sealed refresh token (nil: none) and when the role
	// was last re-read from the provider.
	OIDCIssuer, OIDCSubject string
	OIDCRefreshToken        []byte
	OIDCRefreshedAt         *time.Time
}

// IsOIDC reports whether the session came from the identity provider.
func (s Session) IsOIDC() bool { return s.OIDCSubject != "" }

// CreateSession stores a session (only the SHA-256 of its token).
func (s *Store) CreateSession(ctx context.Context, idHash []byte, username, csrf string, expires time.Time, userAgent, remote string, now time.Time) error {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions (id_sha256, username, csrf_token, created_at, expires_at, last_seen_at, user_agent, remote_addr)
		VALUES ($1, $2, $3, $4, $5, $4, $6, $7)`, idHash, username, csrf, now, expires, userAgent, remote)
	return err
}

// LookupSession returns the valid session with the hash: not revoked, not expired, of a live,
// enabled user. It refreshes last_seen_at at most once a minute.
func (s *Store) LookupSession(ctx context.Context, idHash []byte, now time.Time) (Session, error) {
	var sess Session
	var lastSeen time.Time
	row := s.Pool.QueryRow(ctx, `SELECT s.csrf_token, s.expires_at, s.last_seen_at, u.username, u.display_name, u.role, u.password_hash, u.disabled, u.must_change_password,
			u.failed_logins, u.locked_until, u.last_login_at, u.created_at, coalesce(u.created_by, ''), u.updated_at, u.version, u.deleted_at,
			u.source, coalesce(u.email, ''), coalesce(s.oidc_issuer, ''), coalesce(s.oidc_subject, ''), s.oidc_refresh_token, s.oidc_refreshed_at,
			u.scope, u.team_roles
		FROM sessions s JOIN users u ON u.username = s.username
		WHERE s.id_sha256 = $1 AND s.revoked_at IS NULL AND s.expires_at > $2 AND u.deleted_at IS NULL AND NOT u.disabled`, idHash, now)
	var u UserRow
	var updated time.Time
	var roles []byte
	err := row.Scan(&sess.CSRFToken, &sess.ExpiresAt, &lastSeen, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.Disabled,
		&u.MustChangePassword, &u.FailedLogins, &u.LockedUntil, &u.LastLoginAt, &u.CreatedAt, &u.CreatedBy, &updated, &u.Version, &u.DeletedAt,
		&u.Source, &u.Email, &sess.OIDCIssuer, &sess.OIDCSubject, &sess.OIDCRefreshToken, &sess.OIDCRefreshedAt, &u.Scope, &roles)
	u.TeamRoles = parseTeamRoles(roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return sess, ErrNotFound
	}
	if err != nil {
		return sess, err
	}
	updated = updated.UTC()
	u.CreatedAt, u.UpdatedAt = u.CreatedAt.UTC(), &updated
	u.LockedUntil, u.LastLoginAt = utc(u.LockedUntil), utc(u.LastLoginAt)
	sess.IDHash, sess.ExpiresAt, sess.User = idHash, sess.ExpiresAt.UTC(), u
	if now.Sub(lastSeen) >= time.Minute {
		_, _ = s.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id_sha256 = $1`, idHash, now)
	}
	return sess, nil
}

// RevokeSession ends a session.
func (s *Store) RevokeSession(ctx context.Context, idHash []byte, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE id_sha256 = $1 AND revoked_at IS NULL`, idHash, now)
	return err
}

// PruneSessions deletes sessions that expired or were revoked before cutoff.
func (s *Store) PruneSessions(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1 OR revoked_at < $1`, cutoff)
	return tag.RowsAffected(), err
}

// ---- API tokens ----

const tokenColumns = `id, role, coalesce(cluster, ''), description, created_at, coalesce(created_by, ''), expires_at, last_used_at, revoked_at`

func scanToken(r pgx.Row) (hubapi.TokenInfo, error) {
	var t hubapi.TokenInfo
	var id int64
	err := r.Scan(&id, &t.Role, &t.Cluster, &t.Description, &t.CreatedAt, &t.CreatedBy, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	t.ID = strconv.FormatInt(id, 10)
	t.CreatedAt = t.CreatedAt.UTC()
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = utc(t.ExpiresAt), utc(t.LastUsedAt), utc(t.RevokedAt)
	return t, err
}

// ListTokens lists API tokens, newest first.
func (s *Store) ListTokens(ctx context.Context, cluster string, includeRevoked bool) ([]hubapi.TokenInfo, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+tokenColumns+` FROM api_tokens
		WHERE ($1 = '' OR cluster = $1) AND ($2 OR revoked_at IS NULL) ORDER BY created_at DESC, id DESC`, cluster, includeRevoked)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hubapi.TokenInfo{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func createToken(ctx context.Context, tx pgx.Tx, hash []byte, t hubapi.TokenCreate, by string, now time.Time) (hubapi.TokenInfo, error) {
	return scanToken(tx.QueryRow(ctx, `INSERT INTO api_tokens (token_sha256, role, cluster, description, created_at, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+tokenColumns, hash, t.Role, nullStr(t.Cluster), t.Description, now, t.ExpiresAt, nullStr(by)))
}

// CreateToken stores a token (its hash) and records c.
func (s *Store) CreateToken(ctx context.Context, hash []byte, t hubapi.TokenCreate, c Change) (hubapi.TokenInfo, error) {
	var out hubapi.TokenInfo
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, err = createToken(ctx, tx, hash, t, c.Actor.Name, c.Time); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetToken, ID: out.ID, Name: out.Description, Cluster: out.Cluster}
		c.After = out
		return recordChange(ctx, tx, c)
	})
	return out, err
}

// RevokeToken revokes a token and records c; an already revoked token is returned unchanged.
func (s *Store) RevokeToken(ctx context.Context, id int64, c Change) (hubapi.TokenInfo, error) {
	var out hubapi.TokenInfo
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanToken(tx.QueryRow(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if cur.RevokedAt != nil {
			out = cur
			return nil
		}
		if out, err = scanToken(tx.QueryRow(ctx, `UPDATE api_tokens SET revoked_at = $2, revoked_by = $3 WHERE id = $1 RETURNING `+tokenColumns,
			id, c.Time, nullStr(c.Actor.Name))); err != nil {
			return err
		}
		c.Target = hubapi.ConfigTarget{Type: hubapi.TargetToken, ID: out.ID, Name: out.Description, Cluster: out.Cluster}
		c.Before, c.After = cur, out
		return recordChange(ctx, tx, c)
	})
	return out, err
}

// TouchToken records a token's use, at most once a minute.
func (s *Store) TouchToken(ctx context.Context, id int64, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = $2::timestamptz WHERE id = $1 AND (last_used_at IS NULL OR last_used_at <= $2::timestamptz - interval '1 minute')`, id, now)
	return err
}

// parseTeamRoles reads users.team_roles; nil when empty (omitted in the API).
func parseTeamRoles(b []byte) []hubapi.TeamRole {
	var out []hubapi.TeamRole
	if json.Unmarshal(b, &out) != nil || len(out) == 0 {
		return nil
	}
	return out
}

// userBindings returns the stored scope and team_roles of a user.
func userBindings(scope string, roles []hubapi.TeamRole) (string, []byte) {
	if scope == "" {
		scope = hubapi.ScopeFleet
	}
	if roles == nil {
		roles = []hubapi.TeamRole{}
	}
	b, _ := json.Marshal(roles)
	return scope, b
}

// maxTeamRole is the highest role of roles, or def when there are none.
func maxTeamRole(roles []hubapi.TeamRole, def string) string {
	if len(roles) == 0 {
		return def
	}
	r := hubapi.UserRoleViewer
	for _, tr := range roles {
		if tr.Role == hubapi.UserRoleOperator {
			r = hubapi.UserRoleOperator
		}
	}
	return r
}
