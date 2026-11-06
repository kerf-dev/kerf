package hub

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// Sign-in with OpenID Connect (docs/auth.md): authorization code flow with PKCE. The browser goes
// to /v1/auth/oidc/login, the provider, then /v1/auth/oidc/callback, which starts a normal session.
// The state, nonce and PKCE verifier live in oidc_logins (one use, 10 minutes); the browser holds the
// state in the kerf_oidc cookie, so a callback only completes in the browser that started it.

const (
	oidcCookie   = "kerf_oidc"
	oidcLoginTTL = 10 * time.Minute
	// oidcLease: how long one request may take to refresh a session before another may try.
	oidcLease = 30 * time.Second
)

// Error codes of the sign-in page (/login?error=...).
const (
	oidcErrUnavailable = "oidc_unavailable" // provider unreachable or misconfigured
	oidcErrFailed      = "oidc_failed"      // the provider or the token exchange failed
	oidcErrExpired     = "oidc_expired"     // unknown, used or expired state; another browser
	oidcErrDenied      = "oidc_denied"      // no mapped group (and no default role), or a disabled user
	oidcErrConflict    = "oidc_conflict"    // the username belongs to a local user
)

// authProviders serves GET /v1/auth/providers (public): what the sign-in page offers.
func (s *Server) authProviders(w http.ResponseWriter, _ *http.Request) {
	p := hubapi.AuthProviders{Local: s.Cfg.Auth.localEnabled()}
	if s.OIDC != nil {
		p.OIDC = &hubapi.OIDCProvider{Name: s.OIDC.Cfg.DisplayName(), LoginURL: "/v1/auth/oidc/login"}
	}
	writeJSON(w, p)
}

// oidcLogin serves GET /v1/auth/oidc/login?next=<path> (public): redirects to the provider.
func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Sign-in with an identity provider is not configured.")
		return
	}
	next := safeNextPath(r.URL.Query().Get("next"))
	ipL, _ := s.loginLimiters()
	if ok, retry := ipL.Allow(auth.ClientIP(r), s.now()); !ok {
		tooMany(w, r, retry, msgTooManySignIns)
		return
	}
	state, err1 := auth.RandomToken("", 32)
	nonce, err2 := auth.RandomToken("", 24)
	if err := errors.Join(err1, err2); err != nil {
		s.internal(w, r, err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	target, err := s.OIDC.AuthURL(r.Context(), state, nonce, verifier)
	if err != nil {
		s.Log.WarnContext(r.Context(), "oidc sign-in unavailable", "err", err)
		s.oidcFail(w, r, oidcErrUnavailable, next)
		return
	}
	sealed, err := s.OIDC.Box.Seal(auth.SHA256(state), verifier)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	now := s.now()
	if err := s.Store.SaveOIDCLogin(r.Context(), auth.SHA256(state), store.OIDCLogin{Nonce: nonce, CodeVerifier: sealed, Next: next},
		now, now.Add(oidcLoginTTL)); err != nil {
		s.internal(w, r, err)
		return
	}
	// SameSite=Lax: the callback is a top-level navigation from the provider's site.
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: state, Path: "/v1/auth/oidc", MaxAge: int(oidcLoginTTL / time.Second),
		HttpOnly: true, Secure: s.Cfg.Auth.cookieSecure(), SameSite: http.SameSiteLaxMode})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// oidcCallback serves GET /v1/auth/oidc/callback (public): checks the state, exchanges the code,
// maps the groups to a role, creates or updates the user and starts a session.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Sign-in with an identity provider is not configured.")
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	state := q.Get("state")
	if e := q.Get("error"); e != "" {
		s.Log.WarnContext(ctx, "oidc sign-in refused by the provider", "error", e, "description", truncate(q.Get("error_description"), 200))
		s.oidcFail(w, r, oidcErrFailed, "/")
		return
	}
	c, err := r.Cookie(oidcCookie)
	if state == "" || err != nil || !auth.TokenEqual(c.Value, state) {
		s.Log.InfoContext(ctx, "oidc callback without the state cookie of this browser")
		s.oidcFail(w, r, oidcErrExpired, "/")
		return
	}
	now := s.now()
	login, err := s.Store.TakeOIDCLogin(ctx, auth.SHA256(state), now)
	if errors.Is(err, store.ErrNotFound) {
		s.oidcFail(w, r, oidcErrExpired, "/")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	verifier, err := s.OIDC.Box.Open(auth.SHA256(state), login.CodeVerifier)
	if err != nil {
		s.oidcFail(w, r, oidcErrExpired, login.Next)
		return
	}
	id, toks, err := s.OIDC.Exchange(ctx, q.Get("code"), verifier, login.Nonce)
	if err != nil {
		s.Log.WarnContext(ctx, "oidc sign-in failed", "err", err)
		s.oidcFail(w, r, oidcErrFailed, login.Next)
		return
	}
	role, ok := s.OIDC.Cfg.MapRole(id.Groups)
	if !ok {
		s.Log.InfoContext(ctx, "oidc sign-in denied: no mapped group", "user", id.Username, "subject", id.Subject, "groups", len(id.Groups))
		s.oidcFail(w, r, oidcErrDenied, login.Next)
		return
	}
	u, err := s.Store.UpsertOIDCUser(ctx, store.OIDCUser{Issuer: id.Issuer, Subject: id.Subject, Username: id.Username, Email: id.Email,
		DisplayName: id.Name, Groups: id.Groups, Role: role, Provider: s.OIDC.Cfg.DisplayName()}, now)
	if errors.Is(err, store.ErrUsernameTaken) {
		s.Log.WarnContext(ctx, "oidc sign-in refused: the username belongs to another user", "user", id.Username, "subject", id.Subject)
		s.oidcFail(w, r, oidcErrConflict, login.Next)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if u.Disabled {
		s.Log.InfoContext(ctx, "oidc sign-in denied: user disabled", "user", u.Username)
		s.oidcFail(w, r, oidcErrDenied, login.Next)
		return
	}
	token, err1 := auth.RandomToken("", 32)
	csrf, err2 := auth.RandomToken("", 24)
	if err := errors.Join(err1, err2); err != nil {
		s.internal(w, r, err)
		return
	}
	exp := now.Add(s.Cfg.Auth.sessionTTL()).UTC()
	var sealed []byte
	if toks.Refresh != "" {
		if sealed, err = s.OIDC.Box.Seal(auth.SHA256(token), toks.Refresh); err != nil {
			s.internal(w, r, err)
			return
		}
	} else if limit := now.Add(s.OIDC.Cfg.Refresh()).UTC(); limit.Before(exp) {
		// No refresh token (offline_access not granted): the role cannot be re-read, so the session
		// lasts one refresh interval and the next sign-in reads the groups again.
		exp = limit
	}
	if err := s.Store.CreateOIDCSession(ctx, auth.SHA256(token), u.Username, csrf, exp, r.UserAgent(), auth.ClientIP(r),
		id.Issuer, id.Subject, sealed, now); err != nil {
		s.internal(w, r, err)
		return
	}
	http.SetCookie(w, auth.SessionCookie(token, exp, s.Cfg.Auth.cookieSecure()))
	s.clearOIDCCookie(w)
	s.Log.InfoContext(ctx, "login", "user", u.Username, "role", u.Role, "method", "oidc", "subject", id.Subject, "refresh", toks.Refresh != "")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, login.Next, http.StatusFound)
}

// oidcFail sends the browser back to the sign-in page with an error code.
func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, code, next string) {
	s.clearOIDCCookie(w)
	v := url.Values{"error": {code}}
	if next != "/" {
		v.Set("next", next)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/login?"+v.Encode(), http.StatusFound)
}

func (s *Server) clearOIDCCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/v1/auth/oidc", MaxAge: -1,
		HttpOnly: true, Secure: s.Cfg.Auth.cookieSecure(), SameSite: http.SameSiteLaxMode})
}

// refreshOIDC re-reads the role of an IdP session from the provider once per refresh interval. It
// reports whether the session must end: the provider refused the refresh token, the user lost every
// mapped group, or OIDC is no longer configured. A provider that is down keeps the session (with the
// role it had) until the next try.
func (s *Server) refreshOIDC(ctx context.Context, sess *store.Session) (ended bool) {
	now := s.now()
	end := func(reason string, args ...any) bool {
		s.Log.InfoContext(ctx, "oidc session ended", append([]any{"user", sess.User.Username, "reason", reason}, args...)...)
		if err := s.Store.RevokeSession(ctx, sess.IDHash, now); err != nil {
			s.Log.WarnContext(ctx, "revoke session", "err", err)
		}
		return true
	}
	if s.OIDC == nil {
		return end("sign-in with an identity provider is no longer configured")
	}
	if sess.OIDCRefreshedAt != nil && now.Sub(*sess.OIDCRefreshedAt) < s.OIDC.Cfg.Refresh() {
		return false
	}
	if sess.OIDCRefreshToken == nil {
		return false // such sessions last one refresh interval (oidcCallback)
	}
	sealed, ok, err := s.Store.LeaseOIDCRefresh(ctx, sess.IDHash, now, oidcLease)
	if err != nil || !ok {
		if err != nil {
			s.Log.WarnContext(ctx, "oidc refresh lease", "err", err)
		}
		return false
	}
	refresh, err := s.OIDC.Box.Open(sess.IDHash, sealed)
	if err != nil {
		return end("refresh token unreadable (client secret changed?)")
	}
	id, toks, err := s.OIDC.Refresh(ctx, refresh)
	switch {
	case errors.Is(err, auth.ErrSessionEnded):
		return end("the provider refused the refresh token")
	case err != nil:
		s.Log.WarnContext(ctx, "oidc refresh failed; keeping the session for now", "user", sess.User.Username, "err", err)
		return false
	case id.Subject != sess.OIDCSubject:
		return end("the provider returned another subject")
	}
	role, ok := s.OIDC.Cfg.MapRole(id.Groups)
	if !ok {
		return end("no mapped group", "groups", len(id.Groups))
	}
	u, err := s.Store.SetOIDCRole(ctx, sess.OIDCIssuer, sess.OIDCSubject, role, id.Groups, s.OIDC.Cfg.DisplayName(), now)
	if errors.Is(err, store.ErrNotFound) {
		return end("identity removed")
	}
	if err != nil {
		s.Log.WarnContext(ctx, "oidc role update", "err", err)
		return false
	}
	if u.Disabled || u.DeletedAt != nil {
		return end("user disabled or deleted")
	}
	if u.Role != sess.User.Role {
		s.Log.InfoContext(ctx, "oidc role changed", "user", u.Username, "from", sess.User.Role, "to", u.Role)
	}
	sess.User = u
	if next, err := s.OIDC.Box.Seal(sess.IDHash, toks.Refresh); err != nil {
		s.Log.WarnContext(ctx, "seal refresh token", "err", err)
	} else if err := s.Store.OIDCRefreshed(ctx, sess.IDHash, next, now); err != nil {
		s.Log.WarnContext(ctx, "store refresh token", "err", err)
	}
	return false
}

// safeNextPath keeps a same-site path to return to after sign-in; anything else is "/".
func safeNextPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	for _, c := range next {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "/login" || strings.HasPrefix(u.Path, "/login/") {
		return "/"
	}
	return next
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
