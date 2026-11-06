// Package oidctest is a minimal OpenID Connect provider for tests: discovery, JWKS, the token
// endpoint (authorization code with PKCE, rotating refresh tokens) and userinfo. Users and their
// groups can be changed while a test runs; Authorize plays the browser's visit to the provider.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// User is a user of the provider.
type User struct {
	Subject, Email, Name string
	Groups               []string
}

// Provider is the fake provider; Issuer is its URL.
type Provider struct {
	Issuer                 string
	ClientID, ClientSecret string
	RedirectURL            string
	OmitRefreshToken       bool // no offline access
	OmitIDTokenOnRefresh   bool // refresh answers carry no ID token (userinfo is used)
	srv                    *httptest.Server
	key                    *rsa.PrivateKey
	mu                     sync.Mutex
	users                  map[string]*User
	codes                  map[string]grant
	refresh                map[string]string // refresh token -> subject
	issued                 []string          // every token handed out (leak checks)
	exchanges, refreshes   int
	lastAuthParams         url.Values
}

type grant struct {
	subject, nonce, challenge, redirect string
}

// New starts a provider with client "kerf" / "kerf-secret"; it stops with the test.
func New(t testing.TB) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{ClientID: "kerf", ClientSecret: "kerf-secret", key: key,
		users: map[string]*User{}, codes: map[string]grant{}, refresh: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /keys", p.keys)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /userinfo", p.userinfo)
	p.srv = httptest.NewServer(mux)
	p.Issuer = p.srv.URL
	t.Cleanup(p.srv.Close)
	return p
}

// AddUser adds or replaces a user.
func (p *Provider) AddUser(u User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[u.Subject] = &u
}

// SetGroups changes a user's groups (seen at the next refresh).
func (p *Provider) SetGroups(subject string, groups ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[subject].Groups = groups
}

// RevokeRefreshTokens makes every refresh token invalid (invalid_grant).
func (p *Provider) RevokeRefreshTokens() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.refresh)
}

// Issued returns every token the provider handed out (codes, ID, access and refresh tokens).
func (p *Provider) Issued() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.issued...)
}

// Counts returns how many code exchanges and refreshes were made.
func (p *Provider) Counts() (exchanges, refreshes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exchanges, p.refreshes
}

// LastAuthParams returns the query of the last authorization request.
func (p *Provider) LastAuthParams() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAuthParams
}

// Authorize plays the browser at the provider: it checks the authorization URL the hub redirected
// to and returns the callback URL (code and state) for subject.
func (p *Provider) Authorize(authURL, subject string) (string, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastAuthParams = q
	switch {
	case !strings.HasPrefix(authURL, p.Issuer+"/auth?"):
		return "", fmt.Errorf("not this provider's authorization endpoint: %s", authURL)
	case q.Get("response_type") != "code", q.Get("client_id") != p.ClientID:
		return "", fmt.Errorf("bad response_type or client_id: %v", q)
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		return "", fmt.Errorf("no PKCE S256 challenge: %v", q)
	case q.Get("state") == "" || q.Get("nonce") == "":
		return "", fmt.Errorf("no state or nonce: %v", q)
	case p.RedirectURL != "" && q.Get("redirect_uri") != p.RedirectURL:
		return "", fmt.Errorf("redirect_uri %q, want %q", q.Get("redirect_uri"), p.RedirectURL)
	}
	if _, ok := p.users[subject]; !ok {
		return "", fmt.Errorf("unknown subject %q", subject)
	}
	code := p.newToken("code")
	p.codes[code] = grant{subject: subject, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	cb, _ := url.Parse(q.Get("redirect_uri"))
	cq := url.Values{"code": {code}, "state": {q.Get("state")}}
	cb.RawQuery = cq.Encode()
	return cb.String(), nil
}

func (p *Provider) newToken(kind string) string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	t := kind + "-" + base64.RawURLEncoding.EncodeToString(b)
	p.issued = append(p.issued, t)
	return t
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer": p.Issuer, "authorization_endpoint": p.Issuer + "/auth", "token_endpoint": p.Issuer + "/token",
		"jwks_uri": p.Issuer + "/keys", "userinfo_endpoint": p.Issuer + "/userinfo",
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"},
	})
}

func (p *Provider) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var subject, nonce string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		g, ok := p.codes[r.PostForm.Get("code")]
		delete(p.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || r.PostForm.Get("redirect_uri") != g.redirect {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		subject, nonce = g.subject, g.nonce
		p.exchanges++
	case "refresh_token":
		s, ok := p.refresh[r.PostForm.Get("refresh_token")]
		delete(p.refresh, r.PostForm.Get("refresh_token")) // rotation: each refresh token is used once
		if _, live := p.users[s]; !ok || !live {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		subject = s
		p.refreshes++
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	u := p.users[subject]
	now := time.Now()
	claims := map[string]any{"iss": p.Issuer, "sub": u.Subject, "aud": p.ClientID, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"email": u.Email, "name": u.Name, "groups": u.Groups}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	access := p.newToken("access")
	out := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600}
	if !p.OmitIDTokenOnRefresh || nonce != "" {
		idt, err := p.sign(claims)
		if err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error")
			return
		}
		p.issued = append(p.issued, idt)
		out["id_token"] = idt
	}
	if !p.OmitRefreshToken {
		rt := p.newToken("refresh")
		p.refresh[rt] = subject
		out["refresh_token"] = rt
	}
	p.refresh["by-access:"+access] = subject
	writeJSON(w, http.StatusOK, out)
}

func (p *Provider) userinfo(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.refresh["by-access:"+tok]
	u := p.users[s]
	if !ok || u == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sub": u.Subject, "email": u.Email, "name": u.Name, "groups": u.Groups})
}

func (p *Provider) sign(claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(claims)
	obj, err := signer.Sign(b)
	if err != nil {
		return "", err
	}
	return obj.CompactSerialize()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
