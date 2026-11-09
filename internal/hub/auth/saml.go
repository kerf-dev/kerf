package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/jonboulle/clockwork"
	dsig "github.com/russellhaering/goxmldsig"
)

// SAMLConfig configures sign-in with a SAML 2.0 identity provider (SP-initiated, HTTP-Redirect
// binding to the IdP, HTTP-POST binding back to the assertion consumer service).
type SAMLConfig struct {
	Enabled bool `json:"enabled"`
	// Name is shown on the sign-in page: "Sign in with <Name>" (default "SSO").
	Name string `json:"name"`
	// ACSURL is this hub's assertion consumer service as the browser reaches it (the web UI's host),
	// registered with the IdP: https://<ui host>/v1/auth/saml/acs.
	ACSURL string `json:"acsURL"`
	// EntityID is Kerf's entity ID at the IdP (the audience of the assertions). Default: the SP
	// metadata URL, https://<ui host>/v1/auth/saml/metadata.
	EntityID string `json:"entityID"`
	// The IdP's metadata: IdPMetadataURL (fetched, refreshed every MetadataRefreshInterval) or
	// IdPMetadataFile (a mounted Secret, re-read as often). IdPEntityID picks the IdP out of an
	// aggregate and must match the metadata's entity ID when set.
	IdPMetadataURL  string `json:"idpMetadataURL"`
	IdPMetadataFile string `json:"idpMetadataFile"`
	IdPEntityID     string `json:"idpEntityID"`
	// AllowHTTPMetadata accepts an http:// IdPMetadataURL (development, or a cluster-internal
	// address you trust): whoever can change the metadata in transit chooses the signing certificate.
	AllowHTTPMetadata bool `json:"allowHTTPMetadata"`
	// MetadataRefreshInterval (default 24h, at least 1m).
	MetadataRefreshInterval string `json:"metadataRefreshInterval"`
	// SPKeyFile and SPCertFile (PEM, a mounted Secret) are optional: with them the hub signs its
	// authentication requests, publishes the certificate in its metadata and decrypts encrypted
	// assertions.
	SPKeyFile  string `json:"spKeyFile"`
	SPCertFile string `json:"spCertFile"`
	// NameIDFormat requested: unspecified (default: the IdP's setting), persistent or emailAddress.
	// The NameID must be stable: it is the user's identity at the IdP. Transient is refused.
	NameIDFormat string `json:"nameIDFormat"`
	// UsernameAttribute names the Kerf user (default "email"; lowercased, must be a valid username).
	// "NameID" uses the NameID. When the attribute is missing and the NameID is an e-mail address
	// (format emailAddress), the NameID is used.
	UsernameAttribute string `json:"usernameAttribute"`
	// EmailAttribute (default "email"), NameAttribute (display name, default "displayName") and
	// GroupsAttribute (default "groups"). Attributes match by Name or FriendlyName.
	EmailAttribute  string `json:"emailAttribute"`
	NameAttribute   string `json:"nameAttribute"`
	GroupsAttribute string `json:"groupsAttribute"`
	// RoleMappings and DefaultRole: as for OIDC (the highest matching role wins; teams give team
	// roles; an empty DefaultRole denies users without a mapped group).
	RoleMappings []RoleMapping `json:"roleMappings"`
	DefaultRole  string        `json:"defaultRole"`
	// RequireSignedAssertions (default true): the assertion itself must carry a valid signature. false
	// also accepts an unsigned assertion inside a signed response. An unsigned response with an
	// unsigned assertion is always refused.
	RequireSignedAssertions *bool `json:"requireSignedAssertions"`
	// ClockSkew allowed between the IdP's clock and the hub's for NotBefore, NotOnOrAfter and the
	// issue instant (default 3m, at most 10m).
	ClockSkew string `json:"clockSkew"`
}

const (
	samlACSPath      = "/v1/auth/saml/acs"
	samlMetadataPath = "/v1/auth/saml/metadata"
	samlUseNameID    = "NameID"
	// samlMaxMetadata bounds a fetched or mounted IdP metadata document.
	samlMaxMetadata = 2 << 20
)

var samlNameIDFormats = map[string]saml.NameIDFormat{
	"":             saml.UnspecifiedNameIDFormat,
	"unspecified":  saml.UnspecifiedNameIDFormat,
	"persistent":   saml.PersistentNameIDFormat,
	"emailAddress": saml.EmailAddressNameIDFormat,
}

// Validate checks an enabled configuration.
func (c SAMLConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if u, err := url.Parse(c.ACSURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		!strings.HasSuffix(u.Path, samlACSPath) || u.RawQuery != "" || u.Fragment != "" {
		errs = append(errs, fmt.Errorf("auth.saml.acsURL %q must be the absolute URL of %s", c.ACSURL, samlACSPath))
	}
	switch {
	case c.IdPMetadataURL == "" && c.IdPMetadataFile == "":
		errs = append(errs, errors.New("auth.saml: set idpMetadataURL or idpMetadataFile"))
	case c.IdPMetadataURL != "" && c.IdPMetadataFile != "":
		errs = append(errs, errors.New("auth.saml: idpMetadataURL and idpMetadataFile are exclusive"))
	case c.IdPMetadataURL != "":
		u, err := url.Parse(c.IdPMetadataURL)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
			errs = append(errs, fmt.Errorf("auth.saml.idpMetadataURL %q is not an http(s) URL", c.IdPMetadataURL))
		case u.Scheme == "http" && !c.AllowHTTPMetadata:
			errs = append(errs, fmt.Errorf("auth.saml.idpMetadataURL %q: use https (or set allowHTTPMetadata for a trusted network)", c.IdPMetadataURL))
		}
	}
	if (c.SPKeyFile == "") != (c.SPCertFile == "") {
		errs = append(errs, errors.New("auth.saml: spKeyFile and spCertFile go together"))
	}
	if _, ok := samlNameIDFormats[c.NameIDFormat]; !ok {
		errs = append(errs, fmt.Errorf("auth.saml.nameIDFormat %q: want unspecified, persistent or emailAddress", c.NameIDFormat))
	}
	errs = append(errs, validateRoleMappings("auth.saml", c.RoleMappings, c.DefaultRole)...)
	if c.MetadataRefreshInterval != "" {
		if d, err := time.ParseDuration(c.MetadataRefreshInterval); err != nil || d < time.Minute {
			errs = append(errs, fmt.Errorf("auth.saml.metadataRefreshInterval %q: want a duration of at least 1m", c.MetadataRefreshInterval))
		}
	}
	if c.ClockSkew != "" {
		if d, err := time.ParseDuration(c.ClockSkew); err != nil || d < 0 || d > 10*time.Minute {
			errs = append(errs, fmt.Errorf("auth.saml.clockSkew %q: want a duration from 0s to 10m", c.ClockSkew))
		}
	}
	return errors.Join(errs...)
}

// DisplayName returns Name or "SSO".
func (c SAMLConfig) DisplayName() string {
	if c.Name == "" {
		return "SSO"
	}
	return c.Name
}

// MapAccess maps the IdP's groups like OIDCConfig.MapAccess.
func (c SAMLConfig) MapAccess(groups []string) (Access, bool) {
	return mapAccess(c.RoleMappings, c.DefaultRole, groups)
}

func (c SAMLConfig) refresh() time.Duration {
	if d, err := time.ParseDuration(c.MetadataRefreshInterval); err == nil && d >= time.Minute {
		return d
	}
	return 24 * time.Hour
}

func (c SAMLConfig) clockSkew() time.Duration {
	if d, err := time.ParseDuration(c.ClockSkew); err == nil && d >= 0 && d <= 10*time.Minute {
		return d
	}
	return 3 * time.Minute
}

func (c SAMLConfig) requireSignedAssertions() bool {
	return c.RequireSignedAssertions == nil || *c.RequireSignedAssertions
}

func (c SAMLConfig) attr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// MetadataURL is the SP metadata URL next to the ACS URL.
func (c SAMLConfig) MetadataURL() string {
	return strings.TrimSuffix(c.ACSURL, samlACSPath) + samlMetadataPath
}

// SPEntityID returns EntityID or the metadata URL.
func (c SAMLConfig) SPEntityID() string {
	if c.EntityID != "" {
		return c.EntityID
	}
	return c.MetadataURL()
}

// ---- service provider ----

// SAMLIdentity is who the IdP says signed in, from a verified assertion.
type SAMLIdentity struct {
	Issuer   string   `json:"issuer"`  // the IdP's entity ID
	Subject  string   `json:"subject"` // the NameID
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Groups   []string `json:"groups"`
	// SessionNotOnOrAfter: the IdP's limit on the session (AuthnStatement), if any.
	SessionNotOnOrAfter *time.Time `json:"sessionNotOnOrAfter,omitempty"`
}

// ReplayCache records the hash of an accepted assertion ID until expires and fails for one already
// recorded, with ErrReplayed (shared by every hub replica).
type ReplayCache func(ctx context.Context, idHash []byte, expires time.Time) error

// ErrReplayed: the assertion was accepted before.
var ErrReplayed = saml.ErrAssertionReplayed

// ErrSAMLUnavailable: the IdP's metadata could not be read.
var ErrSAMLUnavailable = errors.New("saml identity provider metadata unavailable")

// SAML is the service provider of one IdP. The IdP's metadata is read on first use and refreshed; a
// failed read is retried, so the hub starts while the IdP is down.
type SAML struct {
	Cfg SAMLConfig
	// Now is the clock (nil: time.Now); Client fetches the metadata (nil: a client with timeouts).
	Now    func() time.Time
	Client *http.Client

	key  crypto.Signer
	cert *x509.Certificate
	acs  url.URL
	meta url.URL

	mu      sync.Mutex
	md      *saml.EntityDescriptor
	mdAt    time.Time // last successful read
	tryAt   time.Time // last attempt
	lastErr error
}

// NewSAML reads the SP key and certificate and returns the service provider. A misconfigured clock
// skew is the library's global setting: it is set here, once, before the hub serves.
func NewSAML(cfg SAMLConfig) (*SAML, error) {
	s := &SAML{Cfg: cfg, Client: &http.Client{Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Scheme != "https" && !cfg.AllowHTTPMetadata {
				return errors.New("metadata redirect refused")
			}
			return nil
		}}}
	acs, err := url.Parse(cfg.ACSURL)
	if err != nil {
		return nil, fmt.Errorf("auth.saml.acsURL: %w", err)
	}
	meta, err := url.Parse(cfg.MetadataURL())
	if err != nil {
		return nil, fmt.Errorf("auth.saml.acsURL: %w", err)
	}
	s.acs, s.meta = *acs, *meta
	if cfg.SPKeyFile != "" {
		if s.key, s.cert, err = loadKeyPair(cfg.SPKeyFile, cfg.SPCertFile); err != nil {
			return nil, err
		}
	}
	if d := cfg.clockSkew(); d != saml.MaxClockSkew {
		saml.MaxClockSkew = d
	}
	return s, nil
}

func loadKeyPair(keyFile, certFile string) (crypto.Signer, *x509.Certificate, error) {
	kb, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("auth.saml.spKeyFile: %w", err)
	}
	cb, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, fmt.Errorf("auth.saml.spCertFile: %w", err)
	}
	kp, _ := pem.Decode(kb)
	if kp == nil {
		return nil, nil, errors.New("auth.saml.spKeyFile: no PEM block")
	}
	var key any
	if key, err = x509.ParsePKCS8PrivateKey(kp.Bytes); err != nil {
		if key, err = x509.ParsePKCS1PrivateKey(kp.Bytes); err != nil {
			if key, err = x509.ParseECPrivateKey(kp.Bytes); err != nil {
				return nil, nil, errors.New("auth.saml.spKeyFile: not a PKCS#8, PKCS#1 or EC private key")
			}
		}
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("auth.saml.spKeyFile: unsupported key type")
	}
	switch signer.(type) {
	case *rsa.PrivateKey, *ecdsa.PrivateKey:
	default:
		return nil, nil, errors.New("auth.saml.spKeyFile: want an RSA or ECDSA key")
	}
	cp, _ := pem.Decode(cb)
	if cp == nil {
		return nil, nil, errors.New("auth.saml.spCertFile: no PEM block")
	}
	cert, err := x509.ParseCertificate(cp.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("auth.saml.spCertFile: %w", err)
	}
	type equaler interface{ Equal(crypto.PublicKey) bool }
	if pub, ok := signer.Public().(equaler); !ok || !pub.Equal(cert.PublicKey) {
		return nil, nil, errors.New("auth.saml: spCertFile is not the certificate of spKeyFile")
	}
	return signer, cert, nil
}

func (s *SAML) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// idpMetadata returns the IdP's metadata, reading it when missing or older than the refresh
// interval. A failed refresh keeps the metadata read before; without any it is retried at most every
// 10 s.
func (s *SAML) idpMetadata(ctx context.Context) (*saml.EntityDescriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.md != nil && now.Sub(s.mdAt) < s.Cfg.refresh() {
		return s.md, nil
	}
	if now.Sub(s.tryAt) < 10*time.Second && !s.tryAt.IsZero() {
		if s.md != nil {
			return s.md, nil
		}
		return nil, s.lastErr
	}
	s.tryAt = now
	md, err := s.readMetadata(ctx)
	if err != nil {
		s.lastErr = fmt.Errorf("%w: %w", ErrSAMLUnavailable, err)
		if s.md != nil {
			return s.md, nil // keep the last good metadata; the caller logs nothing new
		}
		return nil, s.lastErr
	}
	s.md, s.mdAt, s.lastErr = md, now, nil
	return md, nil
}

// MetadataError is the last error reading the IdP's metadata (nil after a successful read).
func (s *SAML) MetadataError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *SAML) readMetadata(ctx context.Context) (*saml.EntityDescriptor, error) {
	var data []byte
	if s.Cfg.IdPMetadataFile != "" {
		f, err := os.Open(s.Cfg.IdPMetadataFile)
		if err != nil {
			return nil, fmt.Errorf("auth.saml.idpMetadataFile: %w", err)
		}
		defer f.Close()
		if data, err = io.ReadAll(io.LimitReader(f, samlMaxMetadata+1)); err != nil {
			return nil, fmt.Errorf("auth.saml.idpMetadataFile: %w", err)
		}
	} else {
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, s.Cfg.IdPMetadataURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/samlmetadata+xml, application/xml, text/xml")
		resp, err := s.Client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", s.Cfg.IdPMetadataURL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: status %d", s.Cfg.IdPMetadataURL, resp.StatusCode)
		}
		if data, err = io.ReadAll(io.LimitReader(resp.Body, samlMaxMetadata+1)); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", s.Cfg.IdPMetadataURL, err)
		}
	}
	if len(data) > samlMaxMetadata {
		return nil, fmt.Errorf("the IdP metadata is larger than %d bytes", samlMaxMetadata)
	}
	var md *saml.EntityDescriptor
	var err error
	if s.Cfg.IdPEntityID != "" {
		md, err = samlsp.ParseMetadataByEntityID(data, s.Cfg.IdPEntityID)
	} else {
		md, err = samlsp.ParseMetadata(data)
	}
	if err != nil {
		return nil, fmt.Errorf("the IdP metadata: %w", err)
	}
	if len(md.IDPSSODescriptors) == 0 {
		return nil, errors.New("the IdP metadata has no IDPSSODescriptor")
	}
	if s.Cfg.IdPEntityID != "" && md.EntityID != s.Cfg.IdPEntityID {
		return nil, fmt.Errorf("the IdP metadata is for entity %q, not idpEntityID %q", md.EntityID, s.Cfg.IdPEntityID)
	}
	return md, nil
}

// sp is the library's service provider for one request.
func (s *SAML) sp(md *saml.EntityDescriptor, replay saml.AssertionReplayCache) *saml.ServiceProvider {
	sp := &saml.ServiceProvider{
		EntityID:                         s.Cfg.SPEntityID(),
		MetadataURL:                      s.meta,
		AcsURL:                           s.acs,
		IDPMetadata:                      md,
		AuthnNameIDFormat:                samlNameIDFormats[s.Cfg.NameIDFormat],
		SendUnspecifiedNameIDFormat:      true,
		AssertionConsumerServiceBindings: []string{saml.HTTPPostBinding},
		Clock:                            dsig.NewFakeClock(funcClock{Clock: clockwork.NewRealClock(), now: s.now}),
		ReplayCache:                      replay,
		RejectSHA1Signatures:             false,
		MetadataValidDuration:            7 * 24 * time.Hour,
	}
	if s.key != nil {
		sp.Key, sp.Certificate = s.key, s.cert
		if _, ok := s.key.(*ecdsa.PrivateKey); ok {
			sp.SignatureMethod = dsig.ECDSASHA256SignatureMethod
		} else {
			sp.SignatureMethod = dsig.RSASHA256SignatureMethod
		}
	}
	return sp
}

// SPMetadata returns the SP metadata to import at the IdP.
func (s *SAML) SPMetadata() ([]byte, error) {
	b, err := xml.MarshalIndent(s.sp(nil, nil).Metadata(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}

// AuthnRequestURL returns where to send the browser (the IdP's HTTP-Redirect single sign-on
// service) and the ID of the request, which the response must answer.
func (s *SAML) AuthnRequestURL(ctx context.Context, relayState string) (target, requestID string, err error) {
	md, err := s.idpMetadata(ctx)
	if err != nil {
		return "", "", err
	}
	sp := s.sp(md, nil)
	loc := sp.GetSSOBindingLocation(saml.HTTPRedirectBinding)
	if loc == "" {
		return "", "", fmt.Errorf("%w: the IdP has no HTTP-Redirect single sign-on service", ErrSAMLUnavailable)
	}
	req, err := sp.MakeAuthenticationRequest(loc, saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", "", err
	}
	u, err := req.Redirect(relayState, sp)
	if err != nil {
		return "", "", err
	}
	return u.String(), req.ID, nil
}

// ParseResponse verifies the IdP's response posted to the assertion consumer service (r's form must
// be parsed) against the request requestID and returns the identity. The library checks the XML
// signature (only what the signature covers is read), the issuer, the audience, the destination,
// InResponseTo, the times (NotBefore, NotOnOrAfter, IssueInstant, with the clock skew) and records
// the assertion in replay. Errors carry the reason for the logs, never the response or a value
// from it.
func (s *SAML) ParseResponse(ctx context.Context, r *http.Request, requestID string, replay ReplayCache) (SAMLIdentity, error) {
	// Only the HTTP-POST binding: an artifact would make the hub call the IdP's artifact resolution
	// service on behalf of whoever posted it.
	if r.Form.Get("SAMLart") != "" || r.Form.Get("SAMLResponse") == "" {
		return SAMLIdentity{}, errors.New("invalid response: want a SAMLResponse posted with the HTTP-POST binding")
	}
	md, err := s.idpMetadata(ctx)
	if err != nil {
		return SAMLIdentity{}, err
	}
	sp := s.sp(md, replayAdapter{ctx: ctx, issuer: md.EntityID, f: replay})
	a, err := sp.ParseResponse(r, []string{requestID})
	if err != nil {
		var ire *saml.InvalidResponseError
		if errors.As(err, &ire) && ire.PrivateErr != nil {
			return SAMLIdentity{}, fmt.Errorf("invalid response: %w", ire.PrivateErr)
		}
		return SAMLIdentity{}, fmt.Errorf("invalid response: %w", err)
	}
	if s.Cfg.requireSignedAssertions() && a.Signature == nil {
		return SAMLIdentity{}, errors.New("invalid response: the assertion is not signed (only the response is): sign assertions at the IdP, or set requireSignedAssertions: false")
	}
	return s.identity(a)
}

// identity reads the user from a verified assertion.
func (s *SAML) identity(a *saml.Assertion) (SAMLIdentity, error) {
	id := SAMLIdentity{Issuer: a.Issuer.Value}
	var nameIDFormat string
	if a.Subject != nil && a.Subject.NameID != nil {
		id.Subject, nameIDFormat = strings.TrimSpace(a.Subject.NameID.Value), a.Subject.NameID.Format
	}
	if id.Subject == "" {
		return id, errors.New("the assertion has no NameID")
	}
	if nameIDFormat == string(saml.TransientNameIDFormat) {
		return id, errors.New("the NameID is transient: Kerf needs a stable one (persistent, emailAddress or the username)")
	}
	values := func(name string) []string {
		var out []string
		for _, st := range a.AttributeStatements {
			for _, at := range st.Attributes {
				if at.Name != name && at.FriendlyName != name {
					continue
				}
				for _, v := range at.Values {
					if v.Nil {
						continue
					}
					if t := strings.TrimSpace(v.Value); t != "" {
						out = append(out, t)
					}
				}
			}
		}
		return out
	}
	first := func(name string) string {
		if v := values(name); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	nameIDIsEmail := nameIDFormat == string(saml.EmailAddressNameIDFormat)
	id.Email = first(s.Cfg.attr(s.Cfg.EmailAttribute, "email"))
	if id.Email == "" && nameIDIsEmail {
		id.Email = id.Subject
	}
	id.Name = first(s.Cfg.attr(s.Cfg.NameAttribute, "displayName"))
	user := ""
	if ua := s.Cfg.attr(s.Cfg.UsernameAttribute, "email"); ua == samlUseNameID {
		user = id.Subject
	} else if user = first(ua); user == "" && nameIDIsEmail {
		user = id.Subject
	}
	id.Username = strings.ToLower(strings.TrimSpace(user))
	id.Groups = values(s.Cfg.attr(s.Cfg.GroupsAttribute, "groups"))
	if id.Groups == nil {
		id.Groups = []string{}
	}
	for _, st := range a.AuthnStatements {
		if t := st.SessionNotOnOrAfter; t != nil && !t.IsZero() && (id.SessionNotOnOrAfter == nil || t.Before(*id.SessionNotOnOrAfter)) {
			v := t.UTC()
			id.SessionNotOnOrAfter = &v
		}
	}
	if !IdPUsernameRE.MatchString(id.Username) {
		return id, fmt.Errorf("attribute %s %q is not a valid Kerf username", s.Cfg.attr(s.Cfg.UsernameAttribute, "email"), id.Username)
	}
	return id, nil
}

// replayAdapter is the library's replay cache over the hub's store: the key is the SHA-256 of the
// IdP's entity ID and the assertion ID.
type replayAdapter struct {
	ctx    context.Context
	issuer string
	f      ReplayCache
}

func (r replayAdapter) Consume(id string, expires time.Time) error {
	if r.f == nil {
		return errors.New("no replay cache")
	}
	h := sha256.Sum256(bytes.Join([][]byte{[]byte("saml"), []byte(r.issuer), []byte(id)}, []byte{0}))
	return r.f(r.ctx, h[:], expires)
}

// funcClock is a clockwork clock whose time is now (the hub's clock, so tests can move it).
type funcClock struct {
	clockwork.Clock
	now func() time.Time
}

func (c funcClock) Now() time.Time                  { return c.now() }
func (c funcClock) Since(t time.Time) time.Duration { return c.now().Sub(t) }
func (c funcClock) Until(t time.Time) time.Duration { return t.Sub(c.now()) }
