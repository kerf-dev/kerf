// Package samltest is a SAML 2.0 identity provider for tests. It mints real, signed responses to the
// hub's authentication requests, and broken ones on demand (wrong audience or issuer, expired,
// unsigned, tampered, wrapped).
package samltest

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
)

// User is someone the IdP signs in.
type User struct {
	NameID string
	// NameIDFormat (default persistent).
	NameIDFormat string
	Email        string
	Name         string
	Groups       []string
}

// Signing says what the IdP signs.
type Signing int

const (
	SignAssertion Signing = iota // the assertion only (the default)
	SignResponse                 // the response only
	SignBoth
	SignNone
)

// Options break a response on purpose; the zero value is a valid one.
type Options struct {
	Audience     string // "" = the request's issuer (the SP's entity ID)
	Issuer       string // "" = the IdP's entity ID
	InResponseTo string // "" = the request's ID
	// Skew moves the IdP's clock: IssueInstant, NotBefore and NotOnOrAfter are now+Skew based.
	Skew time.Duration
	// Lifetime of the assertion (Conditions NotOnOrAfter = issue + Lifetime; default 5m).
	Lifetime            time.Duration
	SessionNotOnOrAfter *time.Time
	Sign                Signing
	// OtherKey signs with a key the metadata does not list.
	OtherKey bool
	// Tamper changes the user's groups after signing.
	Tamper bool
	// Wrap is an XML signature wrapping attack: "sibling" puts an unsigned assertion for Wrapper before
	// the signed one; "nested" gives the signed assertion's ID and signature to one for Wrapper and
	// hides the signed one inside it.
	Wrap    string
	Wrapper User
}

// IdP is the test identity provider.
type IdP struct {
	EntityID string
	SSOURL   string
	// Now is the IdP's clock (default time.Now).
	Now func() time.Time

	key, otherKey   *rsa.PrivateKey
	cert, otherCert *x509.Certificate

	mu    sync.Mutex
	users map[string]User
	srv   *httptest.Server
}

// New returns an IdP with a fresh RSA key and certificate.
func New(t testing.TB) *IdP {
	t.Helper()
	idp := &IdP{EntityID: "https://idp.example.com/saml/metadata", SSOURL: "https://idp.example.com/saml/sso",
		users: map[string]User{}}
	idp.key, idp.cert = keyPair(t)
	idp.otherKey, idp.otherCert = keyPair(t)
	return idp
}

func keyPair(t testing.TB) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "idp.example.com"},
		NotBefore: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

func (p *IdP) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// AddUser adds or replaces a user.
func (p *IdP) AddUser(u User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[u.NameID] = u
}

// SetGroups changes a user's groups at the IdP.
func (p *IdP) SetGroups(nameID string, groups ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.users[nameID]
	u.Groups = groups
	p.users[nameID] = u
}

// Metadata is the IdP's metadata XML.
func (p *IdP) Metadata() []byte {
	md := saml.EntityDescriptor{EntityID: p.EntityID, IDPSSODescriptors: []saml.IDPSSODescriptor{{
		SSODescriptor: saml.SSODescriptor{RoleDescriptor: saml.RoleDescriptor{
			ProtocolSupportEnumeration: "urn:oasis:names:tc:SAML:2.0:protocol",
			KeyDescriptors: []saml.KeyDescriptor{{Use: "signing", KeyInfo: saml.KeyInfo{X509Data: saml.X509Data{
				X509Certificates: []saml.X509Certificate{{Data: base64.StdEncoding.EncodeToString(p.cert.Raw)}}}}}},
		}},
		SingleSignOnServices: []saml.Endpoint{{Binding: saml.HTTPRedirectBinding, Location: p.SSOURL}},
	}}}
	b, err := xml.Marshal(md)
	if err != nil {
		panic(err)
	}
	return b
}

// MetadataFile writes the metadata to a file in t's temporary directory and returns its path.
func (p *IdP) MetadataFile(t testing.TB) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "idp-metadata.xml")
	if err := os.WriteFile(f, p.Metadata(), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// MetadataURL serves the metadata over HTTP (closed with t) and returns its URL.
func (p *IdP) MetadataURL(t testing.TB) string {
	t.Helper()
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		_, _ = w.Write(p.Metadata())
	}))
	t.Cleanup(p.srv.Close)
	return p.srv.URL + "/metadata"
}

// Request is an authentication request as the IdP received it.
type Request struct {
	ID, Issuer, ACSURL, RelayState string
	Signed                         bool
}

// ParseRedirect decodes the HTTP-Redirect binding URL the hub sent the browser to.
func (p *IdP) ParseRedirect(redirect string) (Request, error) {
	u, err := url.Parse(redirect)
	if err != nil {
		return Request{}, err
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != p.SSOURL {
		return Request{}, fmt.Errorf("redirect to %s, want the IdP's %s", got, p.SSOURL)
	}
	q := u.Query()
	raw, err := base64.StdEncoding.DecodeString(q.Get("SAMLRequest"))
	if err != nil {
		return Request{}, fmt.Errorf("SAMLRequest: %w", err)
	}
	x, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(raw)), 1<<20))
	if err != nil {
		return Request{}, fmt.Errorf("SAMLRequest inflate: %w", err)
	}
	var req saml.AuthnRequest
	if err := xml.Unmarshal(x, &req); err != nil {
		return Request{}, fmt.Errorf("SAMLRequest: %w", err)
	}
	out := Request{ID: req.ID, ACSURL: req.AssertionConsumerServiceURL, RelayState: q.Get("RelayState"), Signed: q.Get("Signature") != ""}
	if req.Issuer != nil {
		out.Issuer = req.Issuer.Value
	}
	return out, nil
}

// Respond answers the request in redirect for the user nameID: the form the browser posts to the
// ACS (SAMLResponse, RelayState) and the ACS URL.
func (p *IdP) Respond(redirect, nameID string, o Options) (url.Values, string, error) {
	req, err := p.ParseRedirect(redirect)
	if err != nil {
		return nil, "", err
	}
	p.mu.Lock()
	u, ok := p.users[nameID]
	p.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("unknown user %q", nameID)
	}
	xmlResp, err := p.Response(req, u, o)
	if err != nil {
		return nil, "", err
	}
	return url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString(xmlResp)}, "RelayState": {req.RelayState}}, req.ACSURL, nil
}

func id() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "_" + hex.EncodeToString(b)
}

func (p *IdP) assertion(req Request, u User, o Options, now time.Time) *saml.Assertion {
	issuer := p.EntityID
	if o.Issuer != "" {
		issuer = o.Issuer
	}
	audience := req.Issuer
	if o.Audience != "" {
		audience = o.Audience
	}
	inResponseTo := req.ID
	if o.InResponseTo != "" {
		inResponseTo = o.InResponseTo
	}
	life := o.Lifetime
	if life == 0 {
		life = 5 * time.Minute
	}
	format := u.NameIDFormat
	if format == "" {
		format = string(saml.PersistentNameIDFormat)
	}
	var attrs []saml.Attribute
	add := func(name string, vals ...string) {
		if len(vals) == 0 || len(vals) == 1 && vals[0] == "" {
			return
		}
		a := saml.Attribute{Name: name, NameFormat: "urn:oasis:names:tc:SAML:2.0:attrname-format:basic"}
		for _, v := range vals {
			a.Values = append(a.Values, saml.AttributeValue{Type: "xs:string", Value: v})
		}
		attrs = append(attrs, a)
	}
	add("email", u.Email)
	add("displayName", u.Name)
	add("groups", u.Groups...)
	return &saml.Assertion{
		ID: id(), IssueInstant: now, Version: "2.0",
		Issuer: saml.Issuer{Format: "urn:oasis:names:tc:SAML:2.0:nameid-format:entity", Value: issuer},
		Subject: &saml.Subject{
			NameID: &saml.NameID{Format: format, Value: u.NameID},
			SubjectConfirmations: []saml.SubjectConfirmation{{Method: "urn:oasis:names:tc:SAML:2.0:cm:bearer",
				SubjectConfirmationData: &saml.SubjectConfirmationData{InResponseTo: inResponseTo, NotOnOrAfter: now.Add(life), Recipient: req.ACSURL}}},
		},
		Conditions: &saml.Conditions{NotBefore: now.Add(-30 * time.Second), NotOnOrAfter: now.Add(life),
			AudienceRestrictions: []saml.AudienceRestriction{{Audience: saml.Audience{Value: audience}}}},
		AuthnStatements: []saml.AuthnStatement{{AuthnInstant: now, SessionIndex: id(), SessionNotOnOrAfter: o.SessionNotOnOrAfter,
			AuthnContext: saml.AuthnContext{AuthnContextClassRef: &saml.AuthnContextClassRef{
				Value: "urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport"}}}},
		AttributeStatements: []saml.AttributeStatement{{Attributes: attrs}},
	}
}

func (p *IdP) signer(o Options) *dsig.SigningContext {
	key, cert := p.key, p.cert
	if o.OtherKey {
		key, cert = p.otherKey, p.otherCert
	}
	ctx := dsig.NewDefaultSigningContext(dsig.TLSCertKeyStore(tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert}))
	ctx.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	if err := ctx.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		panic(err)
	}
	return ctx
}

// signedAssertion returns the assertion element, signed when sign.
func (p *IdP) signedAssertion(a *saml.Assertion, o Options, sign bool) (*etree.Element, error) {
	if !sign {
		return a.Element(), nil
	}
	el, err := p.signer(o).SignEnveloped(a.Element())
	if err != nil {
		return nil, err
	}
	// Put the signature where the schema wants it (after the Issuer).
	a.Signature = el.ChildElements()[len(el.ChildElements())-1]
	return a.Element(), nil
}

// Response is the response XML to req for u.
func (p *IdP) Response(req Request, u User, o Options) ([]byte, error) {
	now := p.now().Add(o.Skew).UTC().Truncate(time.Second)
	a := p.assertion(req, u, o, now)
	signA := o.Sign == SignAssertion || o.Sign == SignBoth
	signR := o.Sign == SignResponse || o.Sign == SignBoth
	ael, err := p.signedAssertion(a, o, signA)
	if err != nil {
		return nil, err
	}
	switch o.Wrap {
	case "":
	case "sibling":
		evil, err := p.signedAssertion(p.assertion(req, o.Wrapper, o, now), o, false)
		if err != nil {
			return nil, err
		}
		ael = wrapSibling(evil, ael)
	case "nested":
		ev := p.assertion(req, o.Wrapper, o, now)
		ev.ID, ev.Signature = a.ID, a.Signature
		evil := ev.Element()
		adv := evil.CreateElement("saml:Advice")
		adv.AddChild(ael)
		ael = evil
	default:
		return nil, fmt.Errorf("unknown wrap %q", o.Wrap)
	}
	issuer := p.EntityID
	if o.Issuer != "" {
		issuer = o.Issuer
	}
	inResponseTo := req.ID
	if o.InResponseTo != "" {
		inResponseTo = o.InResponseTo
	}
	resp := &saml.Response{ID: id(), InResponseTo: inResponseTo, Version: "2.0", IssueInstant: now, Destination: req.ACSURL,
		Issuer: &saml.Issuer{Format: "urn:oasis:names:tc:SAML:2.0:nameid-format:entity", Value: issuer},
		Status: saml.Status{StatusCode: saml.StatusCode{Value: saml.StatusSuccess}}}
	rel := resp.Element()
	addAssertions(rel, ael)
	if signR {
		signed, err := p.signer(o).SignEnveloped(rel)
		if err != nil {
			return nil, err
		}
		resp.Signature = signed.ChildElements()[len(signed.ChildElements())-1]
		rel = resp.Element()
		addAssertions(rel, ael)
	}
	if o.Tamper {
		for _, v := range rel.FindElements("//AttributeValue") {
			v.SetText("platform-admins")
		}
	}
	doc := etree.NewDocument()
	doc.SetRoot(rel)
	return doc.WriteToBytes()
}

// wrapSibling is a placeholder element holding two assertions: addAssertions unpacks it.
func wrapSibling(evil, signed *etree.Element) *etree.Element {
	w := etree.NewElement("wrap")
	w.AddChild(evil)
	w.AddChild(signed)
	return w
}

func addAssertions(resp, ael *etree.Element) {
	if ael.Tag == "wrap" {
		for _, c := range ael.ChildElements() {
			resp.AddChild(c.Copy())
		}
		return
	}
	resp.AddChild(ael.Copy())
}
