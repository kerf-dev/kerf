package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/auth/samltest"
)

// memReplay is an in-memory replay cache.
type memReplay struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (m *memReplay) consume(_ context.Context, h []byte, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]time.Time{}
	}
	if _, ok := m.seen[string(h)]; ok {
		return ErrReplayed
	}
	m.seen[string(h)] = exp
	return nil
}

const testACS = "https://kerf.example.com/v1/auth/saml/acs"

func newTestSAML(t *testing.T, idp *samltest.IdP, mod func(*SAMLConfig)) *SAML {
	t.Helper()
	cfg := SAMLConfig{Enabled: true, Name: "Example IdP", ACSURL: testACS, IdPMetadataFile: idp.MetadataFile(t),
		RoleMappings: []RoleMapping{{Group: "platform-admins", Role: "admin"}, {Group: "engineering", Role: "viewer"}}}
	if mod != nil {
		mod(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := NewSAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// post is the browser posting the IdP's form to the ACS.
func post(t *testing.T, acs string, form url.Values) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/saml/acs", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if acs != testACS {
		t.Fatalf("the IdP posts to %s, want %s", acs, testACS)
	}
	return r
}

func TestSAMLParseResponse(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	alice := samltest.User{NameID: "u-0001", Email: "Alice@Example.com", Name: "Alice Example", Groups: []string{"platform-admins", "sales"}}
	mallory := samltest.User{NameID: "u-0666", Email: "mallory@example.com", Groups: []string{"platform-admins"}}
	tr := false
	for _, tc := range []struct {
		name    string
		user    samltest.User
		opts    samltest.Options
		cfg     func(*SAMLConfig)
		wantErr string // "" = accepted
		check   func(*testing.T, SAMLIdentity)
	}{
		{name: "valid signed assertion", user: alice, check: func(t *testing.T, id SAMLIdentity) {
			if id.Subject != "u-0001" || id.Username != "alice@example.com" || id.Email != "Alice@Example.com" || id.Name != "Alice Example" ||
				!slices.Equal(id.Groups, []string{"platform-admins", "sales"}) || id.Issuer != "https://idp.example.com/saml/metadata" {
				t.Errorf("identity %+v", id)
			}
		}},
		{name: "assertion and response signed", user: alice, opts: samltest.Options{Sign: samltest.SignBoth}},
		{name: "clock skew within the allowance", user: alice, opts: samltest.Options{Skew: 2 * time.Minute}},
		{name: "wrong audience", user: alice, opts: samltest.Options{Audience: "https://other.example.com/saml"}, wantErr: "AudienceRestriction"},
		{name: "wrong issuer", user: alice, opts: samltest.Options{Issuer: "https://evil.example.com/saml"}, wantErr: "Issuer"},
		{name: "expired", user: alice, opts: samltest.Options{Skew: -10 * time.Minute}, wantErr: "expired"},
		{name: "not yet valid", user: alice, opts: samltest.Options{Skew: 10 * time.Minute}, wantErr: "future"},
		{name: "unsigned", user: alice, opts: samltest.Options{Sign: samltest.SignNone}, wantErr: "signature"},
		{name: "only the response signed: refused by default", user: alice, opts: samltest.Options{Sign: samltest.SignResponse},
			wantErr: "assertion is not signed"},
		{name: "only the response signed: accepted when allowed", user: alice, opts: samltest.Options{Sign: samltest.SignResponse},
			cfg: func(c *SAMLConfig) { c.RequireSignedAssertions = &tr }},
		{name: "unsigned even when signed responses suffice", user: alice, opts: samltest.Options{Sign: samltest.SignNone},
			cfg: func(c *SAMLConfig) { c.RequireSignedAssertions = &tr }, wantErr: "signature"},
		{name: "signed by a key not in the metadata", user: alice, opts: samltest.Options{OtherKey: true}, wantErr: "signature"},
		{name: "tampered after signing", user: alice, opts: samltest.Options{Tamper: true}, wantErr: "signature"},
		{name: "wrapped: unsigned sibling assertion", user: alice, opts: samltest.Options{Wrap: "sibling", Wrapper: mallory}, wantErr: "Assertion"},
		{name: "wrapped: signed assertion hidden in another", user: alice, opts: samltest.Options{Wrap: "nested", Wrapper: mallory}, wantErr: "signature"},
		{name: "answers another request", user: alice, opts: samltest.Options{InResponseTo: "id-other"}, wantErr: "InResponseTo"},
		{name: "no groups", user: samltest.User{NameID: "u-0002", Email: "bob@example.com"}, check: func(t *testing.T, id SAMLIdentity) {
			if id.Groups == nil || len(id.Groups) != 0 {
				t.Errorf("groups %#v, want empty", id.Groups)
			}
			if _, ok := (SAMLConfig{RoleMappings: []RoleMapping{{Group: "platform-admins", Role: "admin"}}}).MapAccess(id.Groups); ok {
				t.Error("a user without groups gets access")
			}
		}},
		{name: "transient NameID", user: samltest.User{NameID: "t-1", NameIDFormat: "urn:oasis:names:tc:SAML:2.0:nameid-format:transient", Email: "c@example.com"},
			wantErr: "transient"},
		{name: "e-mail NameID without an email attribute", user: samltest.User{NameID: "Dana@Example.com",
			NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"}, check: func(t *testing.T, id SAMLIdentity) {
			if id.Username != "dana@example.com" || id.Email != "Dana@Example.com" {
				t.Errorf("identity %+v", id)
			}
		}},
		{name: "username from the NameID", user: samltest.User{NameID: "erin", Email: "erin@example.com"},
			cfg: func(c *SAMLConfig) { c.UsernameAttribute = "NameID" }, check: func(t *testing.T, id SAMLIdentity) {
				if id.Username != "erin" {
					t.Errorf("username %q", id.Username)
				}
			}},
		{name: "no username", user: samltest.User{NameID: "u-0003"}, wantErr: "not a valid Kerf username"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idp := samltest.New(t)
			idp.Now = func() time.Time { return now }
			idp.AddUser(tc.user)
			s := newTestSAML(t, idp, tc.cfg)
			s.Now = func() time.Time { return now }
			target, reqID, err := s.AuthnRequestURL(context.Background(), "relay-state-1")
			if err != nil {
				t.Fatal(err)
			}
			form, acs, err := idp.Respond(target, tc.user.NameID, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if form.Get("RelayState") != "relay-state-1" {
				t.Fatalf("relay state %q", form.Get("RelayState"))
			}
			rc := &memReplay{}
			id, err := s.ParseResponse(context.Background(), post(t, acs, form), reqID, rc.consume)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("accepted %+v, want an error with %q", id, tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q, want %q", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if tc.check != nil {
				tc.check(t, id)
			}
			// The same response again: refused by the replay cache.
			if _, err := s.ParseResponse(context.Background(), post(t, acs, form), reqID, rc.consume); !errors.Is(err, ErrReplayed) {
				t.Errorf("replayed response: %v, want ErrReplayed", err)
			}
		})
	}
}

// TestSAMLOnlyPostBinding: an artifact (HTTP-Artifact binding) or an empty form is refused before
// anything is fetched or parsed.
func TestSAMLOnlyPostBinding(t *testing.T) {
	s := newTestSAML(t, samltest.New(t), nil)
	for name, form := range map[string]url.Values{
		"artifact": {"SAMLart": {"AAQAAMFbLinlXaCM+FIxiDwGOLAy2T71gbpO7ZhNzAgEANlB90ECfpNEVLg="}, "RelayState": {"rs"}},
		"empty":    {"RelayState": {"rs"}},
	} {
		_, err := s.ParseResponse(context.Background(), post(t, testACS, form), "id-1", (&memReplay{}).consume)
		if err == nil || !strings.Contains(err.Error(), "HTTP-POST") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSAMLRequestAndMetadata(t *testing.T) {
	idp := samltest.New(t)
	s := newTestSAML(t, idp, nil)
	target, reqID, err := s.AuthnRequestURL(context.Background(), "rs")
	if err != nil {
		t.Fatal(err)
	}
	req, err := idp.ParseRedirect(target)
	if err != nil {
		t.Fatal(err)
	}
	if req.ID != reqID || req.ACSURL != testACS || req.Issuer != "https://kerf.example.com/v1/auth/saml/metadata" || req.RelayState != "rs" || req.Signed {
		t.Errorf("request %+v (id %s)", req, reqID)
	}
	md, err := s.SPMetadata()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`entityID="https://kerf.example.com/v1/auth/saml/metadata"`, `Location="` + testACS + `"`,
		`urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST`, `WantAssertionsSigned="true"`} {
		if !strings.Contains(string(md), want) {
			t.Errorf("SP metadata lacks %s:\n%s", want, md)
		}
	}
	if strings.Contains(string(md), "HTTP-Artifact") {
		t.Error("SP metadata offers the artifact binding")
	}
}

func TestSAMLMetadataURL(t *testing.T) {
	idp := samltest.New(t)
	u := idp.MetadataURL(t)
	s := newTestSAML(t, idp, func(c *SAMLConfig) { c.IdPMetadataFile, c.IdPMetadataURL, c.AllowHTTPMetadata = "", u, true })
	if _, _, err := s.AuthnRequestURL(context.Background(), "rs"); err != nil {
		t.Fatal(err)
	}
	// The IdP is down: a sign-in fails with ErrSAMLUnavailable, and the hub keeps metadata it read before.
	down := newTestSAML(t, idp, func(c *SAMLConfig) {
		c.IdPMetadataFile, c.IdPMetadataURL, c.AllowHTTPMetadata = "", "http://127.0.0.1:1/md", true
	})
	if _, _, err := down.AuthnRequestURL(context.Background(), "rs"); !errors.Is(err, ErrSAMLUnavailable) {
		t.Errorf("IdP down: %v", err)
	}
}

func TestSAMLConfigValidate(t *testing.T) {
	ok := SAMLConfig{Enabled: true, ACSURL: testACS, IdPMetadataURL: "https://idp.example.com/md", DefaultRole: "viewer"}
	for _, tc := range []struct {
		name string
		mod  func(*SAMLConfig)
		want string
	}{
		{"valid", func(*SAMLConfig) {}, ""},
		{"disabled is never checked", func(c *SAMLConfig) { *c = SAMLConfig{} }, ""},
		{"acs path", func(c *SAMLConfig) { c.ACSURL = "https://kerf.example.com/saml" }, "acsURL"},
		{"no metadata", func(c *SAMLConfig) { c.IdPMetadataURL = "" }, "idpMetadataURL or idpMetadataFile"},
		{"both metadata sources", func(c *SAMLConfig) { c.IdPMetadataFile = "/x" }, "exclusive"},
		{"http metadata", func(c *SAMLConfig) { c.IdPMetadataURL = "http://idp.example.com/md" }, "use https"},
		{"http metadata allowed", func(c *SAMLConfig) { c.IdPMetadataURL, c.AllowHTTPMetadata = "http://idp.example.com/md", true }, ""},
		{"key without certificate", func(c *SAMLConfig) { c.SPKeyFile = "/k" }, "go together"},
		{"transient", func(c *SAMLConfig) { c.NameIDFormat = "transient" }, "nameIDFormat"},
		{"no mapping", func(c *SAMLConfig) { c.DefaultRole = "" }, "nobody can sign in"},
		{"admin on teams", func(c *SAMLConfig) {
			c.RoleMappings = []RoleMapping{{Group: "g", Role: "admin", Teams: []string{"pay"}}}
		}, "fleet-wide"},
		{"skew", func(c *SAMLConfig) { c.ClockSkew = "1h" }, "clockSkew"},
		{"refresh", func(c *SAMLConfig) { c.MetadataRefreshInterval = "10s" }, "metadataRefreshInterval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.mod(&c)
			err := c.Validate()
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("Validate() = %v, want %q", err, tc.want)
			}
		})
	}
}

func writeKeyPair(t *testing.T, dir, name string) (keyFile, certFile string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "kerf-saml"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile, certFile = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".crt")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return keyFile, certFile
}

// TestSAMLSPKey: with an SP key the requests are signed and the metadata carries the certificate.
func TestSAMLSPKey(t *testing.T) {
	idp := samltest.New(t)
	dir := t.TempDir()
	key, cert := writeKeyPair(t, dir, "sp")
	s := newTestSAML(t, idp, func(c *SAMLConfig) { c.SPKeyFile, c.SPCertFile = key, cert })
	target, _, err := s.AuthnRequestURL(context.Background(), "rs")
	if err != nil {
		t.Fatal(err)
	}
	req, err := idp.ParseRedirect(target)
	if err != nil || !req.Signed || !strings.Contains(target, "SigAlg=") {
		t.Errorf("request %+v (%v): want a signed HTTP-Redirect request", req, err)
	}
	md, err := s.SPMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), `AuthnRequestsSigned="true"`) || !strings.Contains(string(md), "X509Certificate") {
		t.Errorf("SP metadata without the signing certificate:\n%s", md)
	}
	// A certificate of another key is refused at start.
	_, otherCert := writeKeyPair(t, dir, "other")
	cfg := s.Cfg
	cfg.SPCertFile = otherCert
	if _, err := NewSAML(cfg); err == nil || !strings.Contains(err.Error(), "not the certificate") {
		t.Errorf("mismatched key and certificate: %v", err)
	}
}
