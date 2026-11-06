package auth_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/auth/oidctest"
)

func TestMapRole(t *testing.T) {
	cfg := auth.OIDCConfig{RoleMappings: []auth.RoleMapping{
		{Group: "platform-admins", Role: "admin"}, {Group: "platform", Role: "operator"}, {Group: "engineering", Role: "viewer"},
	}}
	withDefault := cfg
	withDefault.DefaultRole = "viewer"
	tests := []struct {
		name   string
		cfg    auth.OIDCConfig
		groups []string
		want   string
		ok     bool
	}{
		{"highest role wins", cfg, []string{"engineering", "platform-admins", "platform"}, "admin", true},
		{"operator", cfg, []string{"platform", "other"}, "operator", true},
		{"viewer", cfg, []string{"engineering"}, "viewer", true},
		{"no match denies", cfg, []string{"sales"}, "", false},
		{"no groups denies", cfg, nil, "", false},
		{"case matters", cfg, []string{"Platform-Admins"}, "", false},
		{"default role", withDefault, []string{"sales"}, "viewer", true},
		{"mapping beats default", withDefault, []string{"platform"}, "operator", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.cfg.MapRole(tt.groups)
			if got != tt.want || ok != tt.ok {
				t.Errorf("MapRole(%v) = %q, %v; want %q, %v", tt.groups, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestOIDCConfigValidate(t *testing.T) {
	good := auth.OIDCConfig{Enabled: true, Issuer: "https://idp.example", ClientID: "kerf",
		RedirectURL: "https://kerf.example/v1/auth/oidc/callback", DefaultRole: "viewer"}
	tests := []struct {
		name string
		mod  func(*auth.OIDCConfig)
		err  string
	}{
		{"valid", func(*auth.OIDCConfig) {}, ""},
		{"disabled is not checked", func(c *auth.OIDCConfig) { *c = auth.OIDCConfig{} }, ""},
		{"issuer", func(c *auth.OIDCConfig) { c.Issuer = "" }, "issuer is required"},
		{"issuer scheme", func(c *auth.OIDCConfig) { c.Issuer = "ftp://x" }, "not an http(s) URL"},
		{"client", func(c *auth.OIDCConfig) { c.ClientID = "" }, "clientID is required"},
		{"redirect", func(c *auth.OIDCConfig) { c.RedirectURL = "https://kerf.example/callback" }, "redirectURL"},
		{"bad mapping", func(c *auth.OIDCConfig) { c.RoleMappings = []auth.RoleMapping{{Group: "x", Role: "root"}} }, "roleMappings"},
		{"bad default", func(c *auth.OIDCConfig) { c.DefaultRole = "owner" }, "defaultRole"},
		{"nobody", func(c *auth.OIDCConfig) { c.DefaultRole = "" }, "nobody can sign in"},
		{"refresh", func(c *auth.OIDCConfig) { c.RefreshInterval = "10s" }, "refreshInterval"},
		{"connect address", func(c *auth.OIDCConfig) { c.ConnectAddress = "dex.dex.svc" }, "connectAddress"},
		{"connect address ok", func(c *auth.OIDCConfig) { c.ConnectAddress = "dex.dex.svc:5556" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := good
			tt.mod(&c)
			err := c.Validate()
			if (err == nil) != (tt.err == "") || err != nil && !strings.Contains(err.Error(), tt.err) {
				t.Errorf("Validate() = %v, want %q", err, tt.err)
			}
		})
	}
}

func TestBox(t *testing.T) {
	b, err := auth.NewBox("secret-1")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal([]byte("row-1"), "refresh-token-value")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "refresh-token-value") {
		t.Fatal("sealed value contains the plain text")
	}
	if got, err := b.Open([]byte("row-1"), sealed); err != nil || got != "refresh-token-value" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := b.Open([]byte("row-2"), sealed); err == nil {
		t.Error("opened with another row's associated data")
	}
	other, _ := auth.NewBox("secret-2")
	if _, err := other.Open([]byte("row-1"), sealed); err == nil {
		t.Error("opened with a key from another client secret")
	}
	if _, err := auth.NewBox(""); err == nil {
		t.Error("NewBox accepted an empty secret")
	}
}

func newClient(t *testing.T, p *oidctest.Provider, mod func(*auth.OIDCConfig)) *auth.OIDC {
	t.Helper()
	t.Setenv("HUB_OIDC_CLIENT_SECRET", p.ClientSecret)
	cfg := auth.OIDCConfig{Enabled: true, Name: "Test IdP", Issuer: p.Issuer, ClientID: p.ClientID,
		RedirectURL: "https://kerf.example/v1/auth/oidc/callback", DefaultRole: "viewer"}
	if mod != nil {
		mod(&cfg)
	}
	o, err := auth.NewOIDC(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// signIn runs the flow up to the code exchange and returns the identity and tokens.
func signIn(t *testing.T, o *auth.OIDC, p *oidctest.Provider, subject string) (auth.Identity, auth.Tokens, error) {
	t.Helper()
	ctx := context.Background()
	verifier := oauth2.GenerateVerifier()
	authURL, err := o.AuthURL(ctx, "state-1", "nonce-1", verifier)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := p.Authorize(authURL, subject)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(cb)
	return o.Exchange(ctx, u.Query().Get("code"), verifier, "nonce-1")
}

func TestOIDCExchangeAndRefresh(t *testing.T) {
	p := oidctest.New(t)
	p.AddUser(oidctest.User{Subject: "u-1", Email: "Alice@Example.com", Name: "Alice", Groups: []string{"platform", "engineering"}})
	o := newClient(t, p, func(c *auth.OIDCConfig) { c.AuthParams = map[string]string{"prompt": "consent"} })
	ctx := context.Background()

	id, toks, err := signIn(t, o, p, "u-1")
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "u-1" || id.Issuer != p.Issuer || id.Username != "alice@example.com" || id.Name != "Alice" ||
		strings.Join(id.Groups, ",") != "platform,engineering" || toks.Refresh == "" {
		t.Fatalf("identity %+v, refresh %v", id, toks.Refresh != "")
	}
	q := p.LastAuthParams()
	if q.Get("prompt") != "consent" || !strings.Contains(q.Get("scope"), "offline_access") || !strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("authorization request %v", q)
	}

	// The groups change at the provider: the refresh sees them, and the refresh token rotates.
	p.SetGroups("u-1", "engineering")
	id2, toks2, err := o.Refresh(ctx, toks.Refresh)
	if err != nil || strings.Join(id2.Groups, ",") != "engineering" || toks2.Refresh == toks.Refresh {
		t.Fatalf("refresh: %+v %v (rotated %v)", id2, err, toks2.Refresh != toks.Refresh)
	}
	// The old (rotated) refresh token is refused: the session ends.
	if _, _, err := o.Refresh(ctx, toks.Refresh); !errors.Is(err, auth.ErrSessionEnded) {
		t.Errorf("reused refresh token: %v, want ErrSessionEnded", err)
	}
	p.RevokeRefreshTokens()
	if _, _, err := o.Refresh(ctx, toks2.Refresh); !errors.Is(err, auth.ErrSessionEnded) {
		t.Errorf("revoked refresh token: %v, want ErrSessionEnded", err)
	}
}

func TestOIDCRefreshWithoutIDToken(t *testing.T) {
	p := oidctest.New(t)
	p.OmitIDTokenOnRefresh = true
	p.AddUser(oidctest.User{Subject: "u-2", Email: "bob@example.com", Groups: []string{"a"}})
	o := newClient(t, p, nil)
	_, toks, err := signIn(t, o, p, "u-2")
	if err != nil {
		t.Fatal(err)
	}
	p.SetGroups("u-2", "b")
	id, _, err := o.Refresh(context.Background(), toks.Refresh)
	if err != nil || id.Subject != "u-2" || strings.Join(id.Groups, ",") != "b" {
		t.Fatalf("refresh via userinfo: %+v %v", id, err)
	}
}

func TestOIDCExchangeChecks(t *testing.T) {
	p := oidctest.New(t)
	p.AddUser(oidctest.User{Subject: "u-3", Email: "carol@example.com"})
	p.AddUser(oidctest.User{Subject: "u-4", Email: "not an email!"})
	o := newClient(t, p, nil)
	ctx := context.Background()

	// Wrong nonce.
	verifier := oauth2.GenerateVerifier()
	authURL, _ := o.AuthURL(ctx, "s", "nonce-a", verifier)
	cb, _ := p.Authorize(authURL, "u-3")
	u, _ := url.Parse(cb)
	if _, _, err := o.Exchange(ctx, u.Query().Get("code"), verifier, "nonce-b"); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Errorf("wrong nonce: %v", err)
	}
	// Wrong PKCE verifier: the provider refuses the code; the error carries no token.
	authURL, _ = o.AuthURL(ctx, "s", "n", verifier)
	cb, _ = p.Authorize(authURL, "u-3")
	u, _ = url.Parse(cb)
	_, _, err := o.Exchange(ctx, u.Query().Get("code"), oauth2.GenerateVerifier(), "n")
	if err == nil || !strings.Contains(err.Error(), `"invalid_grant"`) {
		t.Errorf("wrong verifier: %v", err)
	}
	for _, tok := range p.Issued() {
		if err != nil && strings.Contains(err.Error(), tok) {
			t.Errorf("error %q contains a token", err)
		}
	}
	// A username claim that is not a valid username.
	if _, _, err := signIn(t, o, p, "u-4"); err == nil || !strings.Contains(err.Error(), "not a valid Kerf username") {
		t.Errorf("bad username: %v", err)
	}
}

func TestOIDCProviderDown(t *testing.T) {
	t.Setenv("HUB_OIDC_CLIENT_SECRET", "x")
	o, err := auth.NewOIDC(auth.OIDCConfig{Enabled: true, Issuer: "http://127.0.0.1:1", ClientID: "kerf",
		RedirectURL: "https://kerf.example/v1/auth/oidc/callback", DefaultRole: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.AuthURL(context.Background(), "s", "n", "v"); err == nil || !strings.Contains(err.Error(), "discovery") {
		t.Errorf("AuthURL with the provider down: %v", err)
	}
}

// TestOIDCConnectAddress: the issuer is an address the hub cannot reach (as localhost in a pod); with
// connectAddress the hub reaches the provider elsewhere while the issuer and its URLs stay the same.
func TestOIDCConnectAddress(t *testing.T) {
	p := oidctest.New(t)
	p.AddUser(oidctest.User{Subject: "u-5", Email: "eve@example.com", Groups: []string{"g"}})
	real := strings.TrimPrefix(p.Issuer, "http://")
	// The fake provider answers for whatever issuer it is configured with; pretend it is published at
	// an unreachable address.
	p.Issuer = "http://kerf-unreachable.invalid:15556"
	o := newClient(t, p, func(c *auth.OIDCConfig) { c.ConnectAddress = real })
	id, toks, err := signIn(t, o, p, "u-5")
	if err != nil || id.Issuer != p.Issuer || toks.Refresh == "" {
		t.Fatalf("sign-in through connectAddress: %+v %v", id, err)
	}
	if _, _, err := o.Refresh(context.Background(), toks.Refresh); err != nil {
		t.Errorf("refresh through connectAddress: %v", err)
	}
}

func TestMapAccess(t *testing.T) {
	cfg := auth.OIDCConfig{RoleMappings: []auth.RoleMapping{
		{Group: "engineering", Role: "viewer"},
		{Group: "payments-devs", Role: "operator", Teams: []string{"payments"}},
		{Group: "payments-readers", Role: "viewer", Teams: []string{"payments", "ledger"}},
	}, DefaultRole: "viewer"}
	tests := []struct {
		name   string
		groups []string
		fleet  string
		teams  map[string]string
		ok     bool
	}{
		{"fleet viewer and team operator", []string{"engineering", "payments-devs"}, "viewer", map[string]string{"payments": "operator"}, true},
		{"team only: no fleet role, no default", []string{"payments-devs"}, "", map[string]string{"payments": "operator"}, true},
		{"highest per team", []string{"payments-devs", "payments-readers"}, "", map[string]string{"payments": "operator", "ledger": "viewer"}, true},
		{"no match: default role", []string{"sales"}, "viewer", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, ok := cfg.MapAccess(tt.groups)
			if a.FleetRole != tt.fleet || ok != tt.ok || len(a.TeamRoles) != len(tt.teams) {
				t.Fatalf("MapAccess(%v) = %+v, %v", tt.groups, a, ok)
			}
			for k, v := range tt.teams {
				if a.TeamRoles[k] != v {
					t.Errorf("team %s: %q, want %q", k, a.TeamRoles[k], v)
				}
			}
		})
	}
	noDefault := cfg
	noDefault.DefaultRole = ""
	if _, ok := noDefault.MapAccess([]string{"sales"}); ok {
		t.Error("no mapped group and no default: allowed")
	}
	bad := auth.OIDCConfig{Enabled: true, Issuer: "https://idp.example", ClientID: "kerf",
		RedirectURL: "https://kerf.example/v1/auth/oidc/callback",
		RoleMappings: []auth.RoleMapping{{Group: "a", Role: "admin", Teams: []string{"payments"}}, {Group: "b", Role: "viewer", Teams: []string{"Bad"}}}}
	err := bad.Validate()
	if err == nil || !strings.Contains(err.Error(), "admin is fleet-wide") || !strings.Contains(err.Error(), `invalid team id "Bad"`) {
		t.Errorf("Validate: %v", err)
	}
}
