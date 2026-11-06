package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Roles of local and IdP users, lowest first (hubapi.UserRoles; repeated here to keep this package free
// of the API types).
var roleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

// OIDCConfig configures sign-in with an OpenID Connect provider (authorization code flow with PKCE).
type OIDCConfig struct {
	Enabled bool `json:"enabled"`
	// Name is shown on the sign-in page: "Sign in with <Name>" (default "SSO").
	Name   string `json:"name"`
	Issuer string `json:"issuer"`
	// ClientID and the client secret, read from ClientSecretFile (a mounted Secret) or env
	// HUB_OIDC_CLIENT_SECRET.
	ClientID         string `json:"clientID"`
	ClientSecretFile string `json:"clientSecretFile"`
	// RedirectURL is this hub's callback as the browser reaches it, registered with the provider:
	// https://<ui host>/v1/auth/oidc/callback.
	RedirectURL string `json:"redirectURL"`
	// Scopes requested (default openid, profile, email, offline_access). offline_access gives the refresh
	// token that lets the hub re-read the groups; add "groups" for Dex and Keycloak-like providers.
	Scopes []string `json:"scopes"`
	// AuthParams are extra authorization request parameters (e.g. Google: access_type=offline, prompt=consent).
	AuthParams map[string]string `json:"authParams"`
	// GroupsClaim holds the user's groups in the ID token or userinfo (default "groups").
	GroupsClaim string `json:"groupsClaim"`
	// UsernameClaim names the Kerf user (default "email"; lowercased, must be a valid username).
	UsernameClaim string `json:"usernameClaim"`
	// RoleMappings map groups to roles; the highest matching role wins.
	RoleMappings []RoleMapping `json:"roleMappings"`
	// DefaultRole is given when no group matches; empty denies the sign-in.
	DefaultRole string `json:"defaultRole"`
	// RefreshInterval: how often a session re-reads the user's groups from the provider (default 5m).
	RefreshInterval string `json:"refreshInterval"`
}

// RoleMapping gives members of Group the Role.
type RoleMapping struct {
	Group string `json:"group"`
	Role  string `json:"role"`
}

// DefaultOIDCScopes are requested when Scopes is empty.
var DefaultOIDCScopes = []string{oidc.ScopeOpenID, "profile", "email", oidc.ScopeOfflineAccess}

// Validate checks an enabled configuration.
func (c OIDCConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if c.Issuer == "" {
		errs = append(errs, errors.New("auth.oidc.issuer is required"))
	} else if u, err := url.Parse(c.Issuer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("auth.oidc.issuer %q is not an http(s) URL", c.Issuer))
	}
	if c.ClientID == "" {
		errs = append(errs, errors.New("auth.oidc.clientID is required"))
	}
	if u, err := url.Parse(c.RedirectURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		!strings.HasSuffix(u.Path, "/v1/auth/oidc/callback") {
		errs = append(errs, fmt.Errorf("auth.oidc.redirectURL %q must be the absolute URL of /v1/auth/oidc/callback", c.RedirectURL))
	}
	for _, m := range c.RoleMappings {
		if m.Group == "" || roleRank[m.Role] == 0 {
			errs = append(errs, fmt.Errorf("auth.oidc.roleMappings: {group: %q, role: %q}: want a group and viewer, operator or admin", m.Group, m.Role))
		}
	}
	if c.DefaultRole != "" && roleRank[c.DefaultRole] == 0 {
		errs = append(errs, fmt.Errorf("auth.oidc.defaultRole %q: want viewer, operator, admin or empty (deny)", c.DefaultRole))
	}
	if len(c.RoleMappings) == 0 && c.DefaultRole == "" {
		errs = append(errs, errors.New("auth.oidc: set roleMappings or defaultRole, or nobody can sign in"))
	}
	if c.RefreshInterval != "" {
		if d, err := time.ParseDuration(c.RefreshInterval); err != nil || d < time.Minute {
			errs = append(errs, fmt.Errorf("auth.oidc.refreshInterval %q: want a duration of at least 1m", c.RefreshInterval))
		}
	}
	return errors.Join(errs...)
}

// Refresh returns RefreshInterval (default 5m).
func (c OIDCConfig) Refresh() time.Duration {
	if d, err := time.ParseDuration(c.RefreshInterval); err == nil && d >= time.Minute {
		return d
	}
	return 5 * time.Minute
}

// DisplayName returns Name or "SSO".
func (c OIDCConfig) DisplayName() string {
	if c.Name == "" {
		return "SSO"
	}
	return c.Name
}

// MapRole returns the highest role of the mapped groups, else DefaultRole; ok is false when the user
// gets no role (deny).
func (c OIDCConfig) MapRole(groups []string) (role string, ok bool) {
	for _, m := range c.RoleMappings {
		if slices.Contains(groups, m.Group) && roleRank[m.Role] > roleRank[role] {
			role = m.Role
		}
	}
	if role == "" {
		role = c.DefaultRole
	}
	return role, role != ""
}

// ---- secrets at rest ----

// Box encrypts the refresh tokens and PKCE verifiers the hub keeps (AES-256-GCM). The key is derived
// from the client secret, so every replica has it and a database dump alone opens nothing; a new
// client secret makes old refresh tokens unreadable (those sessions end at their next refresh).
type Box struct{ aead cipher.AEAD }

// NewBox derives the key from secret.
func NewBox(secret string) (*Box, error) {
	if secret == "" {
		return nil, errors.New("empty client secret")
	}
	key, err := hkdf.Key(sha256.New, []byte(secret), nil, "kerf oidc token encryption v1", 32)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Box{aead: g}, nil
}

// Seal encrypts plain; ad binds it to its row (e.g. the session id hash).
func (b *Box) Seal(ad []byte, plain string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plain), ad), nil
}

// Open decrypts what Seal made with the same ad.
func (b *Box) Open(ad, blob []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(blob) < n {
		return "", errors.New("sealed value too short")
	}
	p, err := b.aead.Open(nil, blob[:n], blob[n:], ad)
	return string(p), err
}

// ---- provider ----

// Identity is who the provider says signed in.
type Identity struct {
	Issuer   string
	Subject  string
	Username string // from UsernameClaim, lowercased
	Email    string
	Name     string
	Groups   []string
}

// Tokens of a sign-in or refresh. Never logged.
type Tokens struct {
	Refresh string // empty: the provider gave none (no offline_access)
}

// ErrSessionEnded is returned by Refresh when the provider no longer accepts the refresh token
// (revoked, expired, user removed): the session must end.
var ErrSessionEnded = errors.New("the provider ended the session")

// OIDC is the client of one provider. Discovery happens on first use and is retried, so the hub
// starts even when the provider is down.
type OIDC struct {
	Cfg    OIDCConfig
	Box    *Box
	Client *http.Client // nil: http.DefaultClient (NewOIDC sets a client with timeouts)

	secret string
	mu     sync.Mutex
	prov   *oidc.Provider
	last   time.Time
	err    error
}

// NewOIDC reads the client secret (file, else env HUB_OIDC_CLIENT_SECRET) and returns the client.
func NewOIDC(cfg OIDCConfig) (*OIDC, error) {
	secret := os.Getenv("HUB_OIDC_CLIENT_SECRET")
	if cfg.ClientSecretFile != "" {
		b, err := os.ReadFile(cfg.ClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("auth.oidc.clientSecretFile: %w", err)
		}
		secret = strings.TrimSpace(string(b))
	}
	if secret == "" {
		return nil, errors.New("auth.oidc: the client secret is empty (clientSecretFile or HUB_OIDC_CLIENT_SECRET)")
	}
	box, err := NewBox(secret)
	if err != nil {
		return nil, err
	}
	return &OIDC{Cfg: cfg, Box: box, secret: secret, Client: idpClient()}, nil
}

// idpClient bounds every call to the provider: a refresh runs inside a user's request, so a slow
// or unreachable provider must fail fast (the session keeps its role until the next try). Idle
// connections are not kept long: proxies and load balancers drop them silently.
func idpClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.IdleConnTimeout = 30 * time.Second
	tr.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{Timeout: 15 * time.Second, Transport: tr}
}

func (o *OIDC) ctx(ctx context.Context) context.Context {
	if o.Client != nil {
		return oidc.ClientContext(ctx, o.Client)
	}
	return ctx
}

// provider discovers the provider once; after a failure it retries at most every 10 s.
func (o *OIDC) provider(ctx context.Context) (*oidc.Provider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.prov != nil {
		return o.prov, nil
	}
	if o.err != nil && time.Since(o.last) < 10*time.Second {
		return nil, o.err
	}
	dctx, cancel := context.WithTimeout(o.ctx(ctx), 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, o.Cfg.Issuer)
	o.last = time.Now()
	if err != nil {
		o.err = fmt.Errorf("oidc discovery of %s: %w", o.Cfg.Issuer, err)
		return nil, o.err
	}
	o.prov, o.err = p, nil
	return p, nil
}

func (o *OIDC) oauth(p *oidc.Provider) *oauth2.Config {
	scopes := o.Cfg.Scopes
	if len(scopes) == 0 {
		scopes = DefaultOIDCScopes
	}
	return &oauth2.Config{ClientID: o.Cfg.ClientID, ClientSecret: o.secret, Endpoint: p.Endpoint(),
		RedirectURL: o.Cfg.RedirectURL, Scopes: scopes}
}

// AuthURL returns the provider's authorization URL for a new sign-in.
func (o *OIDC) AuthURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	p, err := o.provider(ctx)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	for k, v := range o.Cfg.AuthParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	return o.oauth(p).AuthCodeURL(state, opts...), nil
}

// Exchange trades the code for tokens and verifies the ID token and its nonce.
func (o *OIDC) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, Tokens, error) {
	p, err := o.provider(ctx)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	tok, err := o.oauth(p).Exchange(o.ctx(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, Tokens{}, fmt.Errorf("code exchange: %w", safeErr(err))
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, Tokens{}, errors.New("the provider returned no ID token")
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: o.Cfg.ClientID}).Verify(o.ctx(ctx), raw)
	if err != nil {
		return Identity{}, Tokens{}, fmt.Errorf("ID token: %w", err)
	}
	if !TokenEqual(idt.Nonce, nonce) {
		return Identity{}, Tokens{}, errors.New("ID token: wrong nonce")
	}
	id, err := o.identity(ctx, p, idt, tok)
	return id, Tokens{Refresh: tok.RefreshToken}, err
}

// Refresh re-reads the identity (groups) with a refresh token. ErrSessionEnded: the provider refused
// the token. The returned refresh token replaces the old one (providers rotate them).
func (o *OIDC) Refresh(ctx context.Context, refresh string) (Identity, Tokens, error) {
	p, err := o.provider(ctx)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	tok, err := o.oauth(p).TokenSource(o.ctx(ctx), &oauth2.Token{RefreshToken: refresh, Expiry: time.Unix(1, 0)}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || re.ErrorCode == "unauthorized_client" || re.Response != nil && re.Response.StatusCode == http.StatusUnauthorized) {
			return Identity{}, Tokens{}, ErrSessionEnded
		}
		return Identity{}, Tokens{}, fmt.Errorf("refresh: %w", safeErr(err))
	}
	var idt *oidc.IDToken
	if raw, _ := tok.Extra("id_token").(string); raw != "" {
		if idt, err = p.Verifier(&oidc.Config{ClientID: o.Cfg.ClientID}).Verify(o.ctx(ctx), raw); err != nil {
			return Identity{}, Tokens{}, fmt.Errorf("refreshed ID token: %w", err)
		}
	}
	id, err := o.identity(ctx, p, idt, tok)
	if tok.RefreshToken == "" {
		tok.RefreshToken = refresh
	}
	return id, Tokens{Refresh: tok.RefreshToken}, err
}

// identity reads the claims of the ID token, completed by userinfo when the ID token lacks the
// groups or the username (or when a refresh returned no ID token).
func (o *OIDC) identity(ctx context.Context, p *oidc.Provider, idt *oidc.IDToken, tok *oauth2.Token) (Identity, error) {
	claims := map[string]any{}
	var id Identity
	if idt != nil {
		if err := idt.Claims(&claims); err != nil {
			return id, fmt.Errorf("ID token claims: %w", err)
		}
		id.Issuer, id.Subject = idt.Issuer, idt.Subject
	}
	if idt == nil || claims[o.groupsClaim()] == nil || claims[o.usernameClaim()] == nil {
		ui, err := p.UserInfo(o.ctx(ctx), oauth2.StaticTokenSource(tok))
		switch {
		case err == nil:
			more := map[string]any{}
			if err := ui.Claims(&more); err != nil {
				return id, fmt.Errorf("userinfo claims: %w", err)
			}
			if id.Subject != "" && ui.Subject != id.Subject {
				return id, errors.New("userinfo subject differs from the ID token")
			}
			for k, v := range more {
				if _, ok := claims[k]; !ok {
					claims[k] = v
				}
			}
			if id.Subject == "" {
				id.Issuer, id.Subject = o.Cfg.Issuer, ui.Subject
			}
		case idt == nil:
			return id, fmt.Errorf("userinfo: %w", safeErr(err))
		}
	}
	id.Email, _ = claims["email"].(string)
	id.Name, _ = claims["name"].(string)
	user, _ := claims[o.usernameClaim()].(string)
	if user == "" {
		user, _ = claims["preferred_username"].(string)
	}
	id.Username = strings.ToLower(strings.TrimSpace(user))
	id.Groups = stringList(claims[o.groupsClaim()])
	if id.Subject == "" {
		return id, errors.New("the provider returned no subject")
	}
	if !IdPUsernameRE.MatchString(id.Username) {
		return id, fmt.Errorf("claim %s %q is not a valid Kerf username", o.usernameClaim(), id.Username)
	}
	return id, nil
}

// IdPUsernameRE is the username rule of IdP users (their e-mail address, lowercased).
var IdPUsernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._@+-]{1,126}$`)

func (o *OIDC) groupsClaim() string {
	if o.Cfg.GroupsClaim == "" {
		return "groups"
	}
	return o.Cfg.GroupsClaim
}

func (o *OIDC) usernameClaim() string {
	if o.Cfg.UsernameClaim == "" {
		return "email"
	}
	return o.Cfg.UsernameClaim
}

// stringList accepts a list of strings or one string (some providers send one group as a string).
func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// safeErr drops the response body of token endpoint errors (it is logged, and must never carry a
// token): only the OAuth error code and the status stay.
func safeErr(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		return fmt.Errorf("token endpoint: status %d, error %q", status, re.ErrorCode)
	}
	return err
}
