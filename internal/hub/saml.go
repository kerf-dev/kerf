package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// Sign-in with SAML 2.0 (docs/auth.md). The browser goes to /v1/auth/saml/login, which sends it to
// the IdP with an AuthnRequest (HTTP-Redirect binding) and a random RelayState, also kept in the
// kerf_saml cookie. The IdP posts its response to the assertion consumer service /v1/auth/saml/acs
// (HTTP-POST binding). That cross-site POST carries no SameSite=Lax cookie, so the ACS only verifies
// the response for the pending request of its RelayState and stores the identity; it then redirects
// (303) to /v1/auth/saml/finish, a top-level GET that does carry kerf_saml. Finish takes the sign-in
// of that cookie, once, and starts a normal session: a response only signs in the browser that
// started the sign-in.

const (
	samlCookie     = "kerf_saml"
	samlCookiePath = "/v1/auth/saml"
	samlLoginTTL   = 10 * time.Minute
	// samlMaxForm bounds the posted response (large group lists included).
	samlMaxForm = 1 << 20
)

// Error codes of the sign-in page (/login?error=...).
const (
	samlErrUnavailable = "saml_unavailable" // the IdP's metadata cannot be read
	samlErrFailed      = "saml_failed"      // the IdP refused, or its response is invalid
	samlErrExpired     = "saml_expired"     // unknown, used or expired RelayState; another browser
	samlErrDenied      = "saml_denied"      // no mapped group (and no default role), or a disabled user
	samlErrConflict    = "saml_conflict"    // the username belongs to another user
)

// samlMetadata serves GET /v1/auth/saml/metadata (public): the SP metadata to import at the IdP.
func (s *Server) samlMetadata(w http.ResponseWriter, r *http.Request) {
	if s.SAML == nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Sign-in with SAML is not configured.")
		return
	}
	md, err := s.SAML.SPMetadata()
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.Header().Set("Content-Disposition", `inline; filename="kerf-saml-metadata.xml"`)
	_, _ = w.Write(md)
}

// samlLogin serves GET /v1/auth/saml/login?next=<path> (public): redirects to the IdP.
func (s *Server) samlLogin(w http.ResponseWriter, r *http.Request) {
	if s.SAML == nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Sign-in with SAML is not configured.")
		return
	}
	next := safeNextPath(r.URL.Query().Get("next"))
	ipL, _ := s.loginLimiters()
	if ok, retry := ipL.Allow(auth.ClientIP(r), s.now()); !ok {
		tooMany(w, r, retry, msgTooManySignIns)
		return
	}
	// 30 random bytes: 40 characters, within the 80 bytes the bindings allow for RelayState.
	state, err := auth.RandomToken("", 30)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	target, requestID, err := s.SAML.AuthnRequestURL(r.Context(), state)
	if err != nil {
		s.Log.WarnContext(r.Context(), "saml sign-in unavailable", "err", err)
		s.samlFail(w, r, samlErrUnavailable, next)
		return
	}
	now := s.now()
	if err := s.Store.SaveSAMLLogin(r.Context(), auth.SHA256(state), requestID, next, now, now.Add(samlLoginTTL)); err != nil {
		s.internal(w, r, err)
		return
	}
	// SameSite=Lax: finish is a top-level navigation that follows the IdP's cross-site POST.
	http.SetCookie(w, &http.Cookie{Name: samlCookie, Value: state, Path: samlCookiePath, MaxAge: int(samlLoginTTL / time.Second),
		HttpOnly: true, Secure: s.Cfg.Auth.cookieSecure(), SameSite: http.SameSiteLaxMode})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// samlACS serves POST /v1/auth/saml/acs (public): the IdP's response (HTTP-POST binding). It verifies
// the response against the pending request of its RelayState, stores the identity and redirects to
// finish. It changes no user and starts no session.
func (s *Server) samlACS(w http.ResponseWriter, r *http.Request) {
	if s.SAML == nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Sign-in with SAML is not configured.")
		return
	}
	ctx := r.Context()
	ipL, _ := s.loginLimiters()
	if ok, retry := ipL.Allow(auth.ClientIP(r), s.now()); !ok {
		tooMany(w, r, retry, msgTooManySignIns)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, samlMaxForm)
	if err := r.ParseForm(); err != nil {
		s.Log.InfoContext(ctx, "saml response unreadable", "err", err)
		s.samlFail(w, r, samlErrFailed, "/")
		return
	}
	state := r.PostForm.Get("RelayState")
	if state == "" || len(state) > 200 {
		// IdP-initiated sign-ins carry no RelayState of ours: they are refused.
		s.Log.InfoContext(ctx, "saml response without the RelayState of a sign-in started here")
		s.samlFail(w, r, samlErrExpired, "/")
		return
	}
	now := s.now()
	login, err := s.Store.PendingSAMLLogin(ctx, auth.SHA256(state), now)
	if errors.Is(err, store.ErrNotFound) {
		s.Log.InfoContext(ctx, "saml response for an unknown, used or expired sign-in")
		s.samlFail(w, r, samlErrExpired, "/")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, err := s.SAML.ParseResponse(ctx, r, login.RequestID, func(ctx context.Context, idHash []byte, expires time.Time) error {
		err := s.Store.ConsumeSAMLAssertion(ctx, idHash, expires, s.now())
		if errors.Is(err, store.ErrReplayed) {
			return auth.ErrReplayed
		}
		return err
	})
	switch {
	case errors.Is(err, auth.ErrSAMLUnavailable):
		s.Log.WarnContext(ctx, "saml sign-in unavailable", "err", err)
		s.samlFail(w, r, samlErrUnavailable, login.Next)
		return
	case err != nil:
		s.Log.WarnContext(ctx, "saml sign-in failed", "err", truncate(err.Error(), 400), "replayed", errors.Is(err, auth.ErrReplayed))
		s.samlFail(w, r, samlErrFailed, login.Next)
		return
	}
	b, err := json.Marshal(id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.Store.CompleteSAMLLogin(ctx, auth.SHA256(state), b, now); errors.Is(err, store.ErrNotFound) {
		s.samlFail(w, r, samlErrExpired, login.Next)
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, samlCookiePath+"/finish", http.StatusSeeOther)
}

// samlFinish serves GET /v1/auth/saml/finish (public): needs the kerf_saml cookie of the browser that
// started the sign-in; maps the groups to a role, creates or updates the user and starts a session.
func (s *Server) samlFinish(w http.ResponseWriter, r *http.Request) {
	if s.SAML == nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "Sign-in with SAML is not configured.")
		return
	}
	ctx := r.Context()
	c, err := r.Cookie(samlCookie)
	if err != nil || c.Value == "" {
		s.Log.InfoContext(ctx, "saml sign-in without the state cookie of this browser")
		s.samlFail(w, r, samlErrExpired, "/")
		return
	}
	now := s.now()
	login, err := s.Store.TakeSAMLLogin(ctx, auth.SHA256(c.Value), now)
	if errors.Is(err, store.ErrNotFound) {
		s.Log.InfoContext(ctx, "saml sign-in: no answered sign-in for this browser")
		s.samlFail(w, r, samlErrExpired, "/")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var id auth.SAMLIdentity
	if err := json.Unmarshal(login.Identity, &id); err != nil {
		s.internal(w, r, err)
		return
	}
	provider := s.SAML.Cfg.DisplayName()
	access, ok := s.SAML.Cfg.MapAccess(id.Groups)
	if !ok {
		// The IdP no longer gives this user a role: its other sessions end too.
		user, err := s.Store.RevokeIdentitySessions(ctx, hubapi.UserSourceSAML, id.Issuer, id.Subject, now)
		if err != nil {
			s.Log.WarnContext(ctx, "revoke sessions", "err", err)
		}
		s.Log.InfoContext(ctx, "saml sign-in denied: no mapped group", "user", id.Username, "subject", id.Subject,
			"groups", len(id.Groups), "sessionsRevoked", user != "")
		s.samlFail(w, r, samlErrDenied, login.Next)
		return
	}
	iu := oidcAccess(access)
	iu.Source, iu.Issuer, iu.Subject, iu.Username, iu.Email = hubapi.UserSourceSAML, id.Issuer, id.Subject, id.Username, id.Email
	iu.DisplayName, iu.Groups, iu.Provider = id.Name, id.Groups, provider
	u, err := s.Store.UpsertIdPUser(ctx, iu, now)
	if errors.Is(err, store.ErrUsernameTaken) {
		s.Log.WarnContext(ctx, "saml sign-in refused: the username belongs to another user", "user", id.Username, "subject", id.Subject)
		s.samlFail(w, r, samlErrConflict, login.Next)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if u.Disabled {
		s.Log.InfoContext(ctx, "saml sign-in denied: user disabled", "user", u.Username)
		s.samlFail(w, r, samlErrDenied, login.Next)
		return
	}
	token, err1 := auth.RandomToken("", 32)
	csrf, err2 := auth.RandomToken("", 24)
	if err := errors.Join(err1, err2); err != nil {
		s.internal(w, r, err)
		return
	}
	// The session lasts sessionTTL, or less when the IdP limits it (SessionNotOnOrAfter). The groups are
	// read again at the next sign-in.
	exp := now.Add(s.Cfg.Auth.sessionTTL()).UTC()
	if t := id.SessionNotOnOrAfter; t != nil && t.Before(exp) {
		exp = t.UTC()
	}
	if !exp.After(now) {
		s.samlFail(w, r, samlErrExpired, login.Next)
		return
	}
	if err := s.Store.CreateSAMLSession(ctx, auth.SHA256(token), u.Username, csrf, exp, r.UserAgent(), auth.ClientIP(r),
		id.Issuer, id.Subject, now); err != nil {
		s.internal(w, r, err)
		return
	}
	http.SetCookie(w, auth.SessionCookie(token, exp, s.Cfg.Auth.cookieSecure()))
	s.clearSAMLCookie(w)
	s.Log.InfoContext(ctx, "login", "user", u.Username, "role", u.Role, "method", "saml", "subject", id.Subject)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, login.Next, http.StatusFound)
}

// samlFail sends the browser back to the sign-in page with an error code (303: also after the
// IdP's POST).
func (s *Server) samlFail(w http.ResponseWriter, r *http.Request, code, next string) {
	s.clearSAMLCookie(w)
	v := url.Values{"error": {code}}
	if next != "/" {
		v.Set("next", next)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/login?"+v.Encode(), http.StatusSeeOther)
}

func (s *Server) clearSAMLCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: samlCookie, Value: "", Path: samlCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: s.Cfg.Auth.cookieSecure(), SameSite: http.SameSiteLaxMode})
}

// endSAMLSession ends a SAML session when SAML sign-in is no longer configured. Otherwise the session
// keeps the role it was given at sign-in until it expires (a SAML IdP has no refresh).
func (s *Server) endSAMLSession(ctx context.Context, sess *store.Session) (ended bool) {
	if s.SAML != nil {
		return false
	}
	s.Log.InfoContext(ctx, "saml session ended", "user", sess.User.Username, "reason", "sign-in with SAML is no longer configured")
	if err := s.Store.RevokeSession(ctx, sess.IDHash, s.now()); err != nil {
		s.Log.WarnContext(ctx, "revoke session", "err", err)
	}
	return true
}
