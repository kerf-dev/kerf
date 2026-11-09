package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func hash32(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// TestSAMLReplayCache: an assertion is accepted once while it is valid; the record is shared by every
// store (replica) on the database.
func TestSAMLReplayCache(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	other, err := Open(ctx, st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	id := hash32("assertion-1")
	if err := st.ConsumeSAMLAssertion(ctx, id, now.Add(5*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := other.ConsumeSAMLAssertion(ctx, id, now.Add(5*time.Minute), now.Add(time.Minute)); !errors.Is(err, ErrReplayed) {
		t.Errorf("replay on another replica: %v, want ErrReplayed", err)
	}
	if err := st.ConsumeSAMLAssertion(ctx, hash32("assertion-2"), now.Add(5*time.Minute), now); err != nil {
		t.Errorf("another assertion: %v", err)
	}
	// After it expired the record may be reused (the assertion itself is refused by then).
	if err := st.ConsumeSAMLAssertion(ctx, id, now.Add(20*time.Minute), now.Add(6*time.Minute)); err != nil {
		t.Errorf("after expiry: %v", err)
	}
}

func TestSAMLLogins(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 1, 0, time.UTC)
	state := hash32("relay-state")
	if err := st.SaveSAMLLogin(ctx, state, "id-req-1", "/clusters", now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Not answered yet: nothing to take.
	if _, err := st.TakeSAMLLogin(ctx, state, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("take before the response: %v", err)
	}
	l, err := st.PendingSAMLLogin(ctx, state, now)
	if err != nil || l.RequestID != "id-req-1" || l.Next != "/clusters" {
		t.Fatalf("pending: %+v %v", l, err)
	}
	ident := []byte(`{"subject":"u-1","username":"a@example.com"}`)
	if err := st.CompleteSAMLLogin(ctx, state, ident, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteSAMLLogin(ctx, state, ident, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("second response: %v", err)
	}
	if _, err := st.PendingSAMLLogin(ctx, state, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("pending after the response: %v", err)
	}
	l, err = st.TakeSAMLLogin(ctx, state, now)
	if err != nil || l.Next != "/clusters" || !bytes.Contains(l.Identity, []byte(`"u-1"`)) {
		t.Fatalf("take: %+v %v", l, err)
	}
	if _, err := st.TakeSAMLLogin(ctx, state, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("second take: %v", err)
	}
	// Expired sign-ins are neither pending nor taken.
	late := hash32("late")
	if err := st.SaveSAMLLogin(ctx, late, "id-req-2", "/", now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PendingSAMLLogin(ctx, late, now.Add(11*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired pending: %v", err)
	}
}

// TestIdPIdentitiesByProtocol: an OIDC and a SAML identity with the same issuer and subject are two
// identities; neither takes over the other's live user.
func TestIdPIdentitiesByProtocol(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	in := IdPUser{Issuer: "https://idp.example.com", Subject: "u-1", Username: "a@example.com", Email: "a@example.com",
		Role: hubapi.UserRoleViewer, Scope: hubapi.ScopeFleet, Provider: "Example"}
	u, err := st.UpsertIdPUser(ctx, in, now)
	if err != nil || u.Source != hubapi.UserSourceOIDC || u.CreatedBy != "oidc" {
		t.Fatalf("oidc user: %+v %v", u, err)
	}
	saml := in
	saml.Source = hubapi.UserSourceSAML
	if _, err := st.UpsertIdPUser(ctx, saml, now); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("saml identity on the live oidc user: %v, want ErrUsernameTaken", err)
	}
	saml.Username, saml.Email = "a2@example.com", "a2@example.com"
	u, err = st.UpsertIdPUser(ctx, saml, now)
	if err != nil || u.Source != hubapi.UserSourceSAML || u.CreatedBy != "saml" {
		t.Fatalf("saml user: %+v %v", u, err)
	}
	// The OIDC refresh only touches the OIDC identity.
	if _, err := st.SetOIDCRole(ctx, in.Issuer, in.Subject, IdPUser{Role: hubapi.UserRoleAdmin, Scope: hubapi.ScopeFleet}, nil, "Example", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetUser(ctx, "a2@example.com"); got.Role != hubapi.UserRoleViewer {
		t.Errorf("saml user role changed by an OIDC refresh: %s", got.Role)
	}
	if got, _ := st.GetUser(ctx, "a@example.com"); got.Role != hubapi.UserRoleAdmin {
		t.Errorf("oidc user role: %s", got.Role)
	}
	// Revoking by identity finds the SAML user only.
	if name, err := st.RevokeIdentitySessions(ctx, hubapi.UserSourceSAML, in.Issuer, in.Subject, now); err != nil || name != "a2@example.com" {
		t.Errorf("revoke: %q %v", name, err)
	}
}
