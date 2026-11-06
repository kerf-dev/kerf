package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// ErrUsernameTaken: an IdP user would get the name of a local user or of another identity.
var ErrUsernameTaken = errors.New("username taken by another user")

// OIDCUser is an identity-provider user as the hub records it at sign-in or refresh.
type OIDCUser struct {
	Issuer, Subject string
	Username, Email string
	DisplayName     string
	Groups          []string
	Role            string
	// Scope and TeamRoles come from the group mappings with teams (hubapi.User).
	Scope     string
	TeamRoles []hubapi.TeamRole
	Provider  string // shown in audit reasons
}

// sameAccess reports whether a user already has the role and bindings.
func sameAccess(u UserRow, role, scope string, roles []hubapi.TeamRole) bool {
	if scope == "" {
		scope = hubapi.ScopeFleet
	}
	return u.Role == role && u.Scope == scope && slices.Equal(u.TeamRoles, roles)
}

// systemOIDC is the actor of the user changes the provider drives (first sign-in, role from groups).
var systemOIDC = hubapi.Actor{Type: hubapi.ActorSystem, Name: "oidc"}

// UpsertOIDCUser creates the user of an identity at its first sign-in, or updates its role, e-mail and
// display name. Creation and role changes are recorded in the audit log. A deleted user is restored
// (the provider is the source of truth); a disabled one stays disabled (the caller refuses it).
func (s *Store) UpsertOIDCUser(ctx context.Context, in OIDCUser, now time.Time) (UserRow, error) {
	var out UserRow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var username string
		err := tx.QueryRow(ctx, `SELECT username FROM user_identities WHERE issuer = $1 AND subject = $2 FOR UPDATE`,
			in.Issuer, in.Subject).Scan(&username)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			cur, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1 FOR UPDATE`, in.Username))
			switch {
			case err == nil && (cur.Source != hubapi.UserSourceOIDC || cur.DeletedAt == nil):
				return ErrUsernameTaken
			case err == nil: // a deleted IdP user of another identity: replaced, like CreateUser
				if _, err := tx.Exec(ctx, `DELETE FROM user_identities WHERE username = $1`, in.Username); err != nil {
					return err
				}
			case !errors.Is(err, ErrNotFound):
				return err
			}
			scope, roles := userBindings(in.Scope, in.TeamRoles)
			out, err = scanUser(tx.QueryRow(ctx, `INSERT INTO users (username, display_name, role, password_hash, must_change_password,
					password_changed_at, created_at, created_by, updated_at, last_login_at, source, email, scope, team_roles)
				VALUES ($1, $2, $3, '', false, $4, $4, 'oidc', $4, $4, 'oidc', $5, $6, $7)
				ON CONFLICT (username) DO UPDATE SET display_name = excluded.display_name, role = excluded.role,
					scope = excluded.scope, team_roles = excluded.team_roles,
					created_at = excluded.created_at, created_by = excluded.created_by, updated_at = excluded.updated_at,
					last_login_at = excluded.last_login_at, email = excluded.email, disabled = false, deleted_at = NULL,
					version = users.version + 1
				RETURNING `+userColumns, in.Username, in.DisplayName, in.Role, now, in.Email, scope, roles))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_identities (issuer, subject, username, email, groups, created_at, last_login_at)
				VALUES ($1, $2, $3, $4, $5, $6, $6)`, in.Issuer, in.Subject, in.Username, in.Email, nonNil(in.Groups), now); err != nil {
				return err
			}
			return recordChange(ctx, tx, Change{Actor: systemOIDC, Action: hubapi.ActionUserCreated, Time: now,
				Reason:  "first sign-in with " + in.Provider,
				Target:  hubapi.ConfigTarget{Type: hubapi.TargetUser, ID: out.Username, Name: out.Username},
				Version: out.Version, After: out.User})
		case err != nil:
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE user_identities SET email = $3, groups = $4, last_login_at = $5 WHERE issuer = $1 AND subject = $2`,
			in.Issuer, in.Subject, in.Email, nonNil(in.Groups), now); err != nil {
			return err
		}
		cur, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1 FOR UPDATE`, username))
		if err != nil {
			return err
		}
		name := in.DisplayName
		if name == "" {
			name = cur.DisplayName
		}
		access := !sameAccess(cur, in.Role, in.Scope, in.TeamRoles)
		changed := access || cur.DisplayName != name || cur.Email != in.Email || cur.DeletedAt != nil
		scope, roles := userBindings(in.Scope, in.TeamRoles)
		out, err = scanUser(tx.QueryRow(ctx, `UPDATE users SET role = $2, display_name = $3, email = $4, last_login_at = $5,
				deleted_at = NULL, updated_at = CASE WHEN $6 THEN $5 ELSE updated_at END,
				version = CASE WHEN $6 THEN version + 1 ELSE version END, scope = $7, team_roles = $8
			WHERE username = $1 RETURNING `+userColumns, username, in.Role, name, in.Email, now, changed, scope, roles))
		if err != nil || !changed {
			return err
		}
		reason := "sign-in with " + in.Provider
		if access {
			reason = "role from the groups at " + in.Provider
		}
		return recordChange(ctx, tx, Change{Actor: systemOIDC, Action: hubapi.ActionUserUpdated, Time: now, Reason: reason,
			Target:  hubapi.ConfigTarget{Type: hubapi.TargetUser, ID: out.Username, Name: out.Username},
			Version: out.Version, Before: cur.User, After: out.User})
	})
	return out, err
}

// SetOIDCRole applies the role and team bindings re-read at a session refresh (no last_login_at
// change). It records the change when they differ.
func (s *Store) SetOIDCRole(ctx context.Context, issuer, subject string, in OIDCUser, groups []string, provider string, now time.Time) (UserRow, error) {
	var out UserRow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var username string
		if err := tx.QueryRow(ctx, `UPDATE user_identities SET groups = $3 WHERE issuer = $1 AND subject = $2 RETURNING username`,
			issuer, subject, nonNil(groups)).Scan(&username); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		cur, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1 FOR UPDATE`, username))
		if err != nil || sameAccess(cur, in.Role, in.Scope, in.TeamRoles) {
			out = cur
			return err
		}
		scope, roles := userBindings(in.Scope, in.TeamRoles)
		out, err = scanUser(tx.QueryRow(ctx, `UPDATE users SET role = $2, updated_at = $3, version = version + 1, scope = $4, team_roles = $5
			WHERE username = $1 RETURNING `+userColumns, username, in.Role, now, scope, roles))
		if err != nil {
			return err
		}
		return recordChange(ctx, tx, Change{Actor: systemOIDC, Action: hubapi.ActionUserUpdated, Time: now,
			Reason:  "role from the groups at " + provider,
			Target:  hubapi.ConfigTarget{Type: hubapi.TargetUser, ID: out.Username, Name: out.Username},
			Version: out.Version, Before: cur.User, After: out.User})
	})
	return out, err
}

// CreateOIDCSession stores a session of an IdP user with its sealed refresh token (nil: none).
func (s *Store) CreateOIDCSession(ctx context.Context, idHash []byte, username, csrf string, expires time.Time, userAgent, remote string,
	issuer, subject string, refresh []byte, now time.Time) error {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions (id_sha256, username, csrf_token, created_at, expires_at, last_seen_at, user_agent, remote_addr,
			oidc_issuer, oidc_subject, oidc_refresh_token, oidc_refreshed_at)
		VALUES ($1, $2, $3, $4, $5, $4, $6, $7, $8, $9, $10, $4)`, idHash, username, csrf, now, expires, userAgent, remote, issuer, subject, refresh)
	return err
}

// LeaseOIDCRefresh lets one request refresh a session: it takes a lease until now+lease when no other
// is held and returns the sealed refresh token. ok is false when another request holds the lease.
func (s *Store) LeaseOIDCRefresh(ctx context.Context, idHash []byte, now time.Time, lease time.Duration) (refresh []byte, ok bool, err error) {
	err = s.Pool.QueryRow(ctx, `UPDATE sessions SET oidc_refresh_lease = $3
		WHERE id_sha256 = $1 AND revoked_at IS NULL AND (oidc_refresh_lease IS NULL OR oidc_refresh_lease < $2)
		RETURNING oidc_refresh_token`, idHash, now, now.Add(lease)).Scan(&refresh)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return refresh, err == nil, err
}

// OIDCRefreshed stores the new sealed refresh token and ends the lease.
func (s *Store) OIDCRefreshed(ctx context.Context, idHash, refresh []byte, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET oidc_refresh_token = $2, oidc_refreshed_at = $3, oidc_refresh_lease = NULL
		WHERE id_sha256 = $1`, idHash, refresh, now)
	return err
}

// ---- sign-ins in progress ----

// OIDCLogin is a sign-in between the redirect to the provider and its callback.
type OIDCLogin struct {
	Nonce        string
	CodeVerifier []byte // sealed
	Next         string
}

// SaveOIDCLogin stores a sign-in in progress (and drops expired ones now and then).
func (s *Store) SaveOIDCLogin(ctx context.Context, stateHash []byte, l OIDCLogin, now, expires time.Time) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO oidc_logins (state_sha256, nonce, code_verifier, next_path, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, stateHash, l.Nonce, l.CodeVerifier, l.Next, now, expires)
	if err == nil && now.Second()%10 == 0 {
		_, _ = s.Pool.Exec(ctx, `DELETE FROM oidc_logins WHERE expires_at < $1`, now)
	}
	return err
}

// TakeOIDCLogin returns and deletes the sign-in of a state: each state is used once. ErrNotFound when
// unknown or expired.
func (s *Store) TakeOIDCLogin(ctx context.Context, stateHash []byte, now time.Time) (OIDCLogin, error) {
	var l OIDCLogin
	var exp time.Time
	err := s.Pool.QueryRow(ctx, `DELETE FROM oidc_logins WHERE state_sha256 = $1 RETURNING nonce, code_verifier, next_path, expires_at`,
		stateHash).Scan(&l.Nonce, &l.CodeVerifier, &l.Next, &exp)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !now.Before(exp) {
		return OIDCLogin{}, ErrNotFound
	}
	return l, err
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return slices.Clone(v)
}
