package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- SAML sign-ins in progress ----

// SAMLLogin is a sign-in between the redirect to the IdP and the start of the session.
type SAMLLogin struct {
	RequestID string // the AuthnRequest ID the response must answer
	Next      string
	// Identity is the verified identity (JSON) once the assertion consumer service accepted the
	// IdP's response; nil before.
	Identity []byte
}

// SaveSAMLLogin stores a sign-in in progress (and drops expired ones now and then).
func (s *Store) SaveSAMLLogin(ctx context.Context, stateHash []byte, requestID, next string, now, expires time.Time) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO saml_logins (state_sha256, request_id, next_path, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, stateHash, requestID, next, now, expires)
	if err == nil && now.Second()%10 == 0 {
		_, _ = s.Pool.Exec(ctx, `DELETE FROM saml_logins WHERE expires_at < $1`, now)
		_, _ = s.Pool.Exec(ctx, `DELETE FROM saml_assertions WHERE expires_at < $1`, now)
	}
	return err
}

// PendingSAMLLogin returns the sign-in of a RelayState that still waits for the IdP's response.
// ErrNotFound when unknown, expired or already answered.
func (s *Store) PendingSAMLLogin(ctx context.Context, stateHash []byte, now time.Time) (SAMLLogin, error) {
	var l SAMLLogin
	err := s.Pool.QueryRow(ctx, `SELECT request_id, next_path FROM saml_logins
		WHERE state_sha256 = $1 AND identity IS NULL AND expires_at > $2`, stateHash, now).Scan(&l.RequestID, &l.Next)
	if errors.Is(err, pgx.ErrNoRows) {
		return SAMLLogin{}, ErrNotFound
	}
	return l, err
}

// CompleteSAMLLogin stores the verified identity of a pending sign-in, once. ErrNotFound when the
// sign-in is unknown, expired or already answered.
func (s *Store) CompleteSAMLLogin(ctx context.Context, stateHash, identity []byte, now time.Time) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE saml_logins SET identity = $2
		WHERE state_sha256 = $1 AND identity IS NULL AND expires_at > $3`, stateHash, identity, now)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// TakeSAMLLogin returns and deletes an answered sign-in: each is used once. ErrNotFound when unknown,
// expired or not answered yet (a pending one is left alone).
func (s *Store) TakeSAMLLogin(ctx context.Context, stateHash []byte, now time.Time) (SAMLLogin, error) {
	var l SAMLLogin
	err := s.Pool.QueryRow(ctx, `DELETE FROM saml_logins WHERE state_sha256 = $1 AND identity IS NOT NULL AND expires_at > $2
		RETURNING request_id, next_path, identity`, stateHash, now).Scan(&l.RequestID, &l.Next, &l.Identity)
	if errors.Is(err, pgx.ErrNoRows) {
		return SAMLLogin{}, ErrNotFound
	}
	return l, err
}

// ---- replay cache ----

// ErrReplayed: the assertion was already accepted once.
var ErrReplayed = errors.New("assertion already used")

// ConsumeSAMLAssertion records an accepted assertion (the SHA-256 of its ID) until expires. It returns
// ErrReplayed when the assertion was already recorded and has not expired: every hub replica shares
// the record. An expired record is replaced.
func (s *Store) ConsumeSAMLAssertion(ctx context.Context, idHash []byte, expires, now time.Time) error {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO saml_assertions (id_sha256, expires_at) VALUES ($1, $2)
		ON CONFLICT (id_sha256) DO UPDATE SET expires_at = excluded.expires_at WHERE saml_assertions.expires_at <= $3`,
		idHash, expires, now)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrReplayed
	}
	return err
}

// ---- sessions ----

// CreateSAMLSession stores a session of a SAML user with its IdP identity.
func (s *Store) CreateSAMLSession(ctx context.Context, idHash []byte, username, csrf string, expires time.Time, userAgent, remote string,
	issuer, subject string, now time.Time) error {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions (id_sha256, username, csrf_token, created_at, expires_at, last_seen_at, user_agent, remote_addr,
			saml_issuer, saml_subject)
		VALUES ($1, $2, $3, $4, $5, $4, $6, $7, $8, $9)`, idHash, username, csrf, now, expires, userAgent, remote, issuer, subject)
	return err
}

// RevokeIdentitySessions ends the live sessions of the user an IdP identity signs in as (the IdP no
// longer gives it a role). It returns the username, or "" when the identity is unknown.
func (s *Store) RevokeIdentitySessions(ctx context.Context, protocol, issuer, subject string, now time.Time) (string, error) {
	var username string
	err := s.Pool.QueryRow(ctx, `SELECT username FROM user_identities WHERE protocol = $1 AND issuer = $2 AND subject = $3`,
		protocol, issuer, subject).Scan(&username)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE username = $1 AND revoked_at IS NULL`, username, now)
	return username, err
}
