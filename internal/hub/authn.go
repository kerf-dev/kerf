package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/kerf-dev/kerf/internal/hub/auth"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// access is who may call a route: bearer tokens of the listed roles (an admin token always) and,
// when session is set, browser sessions of users with at least that role (D-023).
type access struct {
	tokens  []string
	session string
}

var (
	accessAgent    = access{tokens: []string{hubapi.RoleAgent}}
	accessAudit    = access{tokens: []string{hubapi.RoleAgent, hubapi.RoleUI}, session: hubapi.UserRoleViewer}
	accessViewer   = access{tokens: []string{hubapi.RoleUI}, session: hubapi.UserRoleViewer}
	accessOperator = access{tokens: []string{hubapi.RoleAdmin}, session: hubapi.UserRoleOperator}
	accessAdmin    = access{tokens: []string{hubapi.RoleAdmin}, session: hubapi.UserRoleAdmin}
	accessSession  = access{session: hubapi.UserRoleViewer}
	// accessPricing: the pricing service's token (D-026) or an admin token.
	accessPricing = access{tokens: []string{hubapi.RolePricing}}
)

// Principal is the authenticated caller.
type Principal struct {
	Method  string // hubapi.AuthMethodSession or hubapi.AuthMethodToken
	Role    string // user role (viewer/operator/admin); "" for agent tokens
	Token   store.Token
	Session *store.Session
}

// Actor returns the audit actor of the caller.
func (p Principal) Actor() hubapi.Actor {
	if p.Session != nil {
		return hubapi.Actor{Type: hubapi.ActorUser, Name: p.Session.User.Username, Role: p.Session.User.Role}
	}
	return hubapi.Actor{Type: hubapi.ActorToken, Name: "token:" + strconv.FormatInt(p.Token.ID, 10), Role: p.Role}
}

// IsAdmin reports whether the caller has admin rights.
func (p Principal) IsAdmin() bool { return p.Role == hubapi.UserRoleAdmin }

type principalKey struct{}

// PrincipalFrom returns the caller of a request.
func PrincipalFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// authenticate checks the session cookie (it wins over a bearer token), CSRF on session writes,
// then the bearer token and its role. Agent tokens are additionally restricted to their cluster
// for /v1/clusters/{cluster}/... routes; unknown clusters are 404.
func (s *Server) authenticate(a access, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Principal
		if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" && a.session != "" {
			sess, err := s.Store.LookupSession(r.Context(), auth.SHA256(c.Value), s.now())
			switch {
			case err == nil:
				p = Principal{Method: hubapi.AuthMethodSession, Role: sess.User.Role, Session: &sess}
			case !errors.Is(err, store.ErrNotFound):
				s.internal(w, r, err)
				return
			}
		}
		if p.Session != nil {
			if !safeMethod(r.Method) && !auth.TokenEqual(r.Header.Get(hubapi.CSRFHeader), p.Session.CSRFToken) {
				writeProblem(w, r, http.StatusForbidden, hubapi.CodeCSRFInvalid, "Missing or wrong "+hubapi.CSRFHeader+" header.")
				return
			}
			if !hubapi.UserRoleAtLeast(p.Role, a.session) {
				writeProblem(w, r, http.StatusForbidden, "role_forbidden", "Role "+p.Role+" may not use this endpoint (needs "+a.session+").")
				return
			}
		} else {
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || strings.TrimSpace(raw) == "" || len(a.tokens) == 0 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="kerf"`)
				msg := "Missing bearer token."
				if len(a.tokens) == 0 {
					msg = "Log in first: this endpoint needs a session."
				}
				writeProblem(w, r, http.StatusUnauthorized, "unauthorized", msg)
				return
			}
			tok, err := s.lookupToken(r.Context(), strings.TrimSpace(raw))
			if errors.Is(err, store.ErrNotFound) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="kerf", error="invalid_token"`)
				writeProblem(w, r, http.StatusUnauthorized, "invalid_token", "Unknown, expired or revoked token.")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if tok.Role != hubapi.RoleAdmin && !hubapi.Contains(a.tokens, tok.Role) {
				writeProblem(w, r, http.StatusForbidden, "role_forbidden", "Token role "+tok.Role+" may not use this endpoint.")
				return
			}
			p = Principal{Method: hubapi.AuthMethodToken, Role: hubapi.TokenUserRole(tok.Role), Token: tok}
		}
		if cluster := r.PathValue("cluster"); cluster != "" {
			if p.Token.Role == hubapi.RoleAgent && p.Token.Cluster != cluster {
				writeProblem(w, r, http.StatusForbidden, "cluster_forbidden", "Token is not allowed to access cluster "+cluster+".")
				return
			}
			exists, err := s.Store.ClusterExists(r.Context(), cluster)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if !exists {
				writeProblem(w, r, http.StatusNotFound, "cluster_not_found", "Unknown cluster "+cluster+".")
				return
			}
		}
		if p.Token.Role == hubapi.RoleAgent {
			s.heartbeat(r.Context(), p.Token.Cluster, "")
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, p.Token)
		ctx = context.WithValue(ctx, principalKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) lookupToken(ctx context.Context, raw string) (store.Token, error) {
	key := string(store.HashToken(raw))
	now := s.now()
	cacheOK := s.tokenCacheOK()
	s.mu.Lock()
	if e, ok := s.tokens[key]; ok && cacheOK && now.Before(e.expires) {
		s.mu.Unlock()
		return e.tok, nil
	}
	s.mu.Unlock()
	tok, err := s.Store.LookupToken(ctx, raw)
	if err != nil {
		return tok, err
	}
	if err := s.Store.TouchToken(ctx, tok.ID, now); err != nil {
		s.Log.WarnContext(ctx, "token last-used", "err", err)
	}
	if !cacheOK {
		return tok, nil
	}
	s.mu.Lock()
	if s.tokens == nil {
		s.tokens = map[string]tokenEntry{}
	}
	s.tokens[key] = tokenEntry{tok: tok, expires: now.Add(s.Cfg.TokenCacheTTL.D())}
	s.mu.Unlock()
	return tok, nil
}

// forgetTokens drops the token cache after a revocation, here and (notification) on the other
// hub replicas.
func (s *Server) forgetTokens(ctx context.Context) {
	s.dropTokenCache()
	if s.Coord != nil {
		if err := s.Store.Notify(ctx, store.NotifyTokens); err != nil {
			s.Log.WarnContext(ctx, "notify token revocation", "err", err)
		}
	}
}

func (s *Server) dropTokenCache() {
	s.mu.Lock()
	s.tokens = nil
	s.mu.Unlock()
}

// tokenCacheOK: with several replicas the cache is used only while revocation notifications
// reach this replica.
func (s *Server) tokenCacheOK() bool { return s.Coord == nil || s.Coord.Listening() }

// ---- login, logout, me ----

func (s *Server) loginLimiters() (*auth.Limiter, *auth.Limiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ipLimiter == nil {
		n := s.Cfg.Auth.loginRateLimit()
		s.ipLimiter = &auth.Limiter{Max: 3 * n, Window: time.Minute}
		s.userLimiter = &auth.Limiter{Max: n, Window: time.Minute}
	}
	return s.ipLimiter, s.userLimiter
}

// Sign-in answers: the same for a wrong password, an unknown, disabled or locked user, and every
// rate limit.
const (
	msgInvalidCredentials = "Wrong username or password."
	msgTooManySignIns     = "Too many sign-in attempts. Try again later."
)

func tooMany(w http.ResponseWriter, r *http.Request, code string, retry time.Duration, detail string) {
	w.Header().Set("Retry-After", auth.RetryAfterSeconds(retry))
	writeProblem(w, r, http.StatusTooManyRequests, code, detail)
}

func (s *Server) me(sess *store.Session, csrf bool) hubapi.Me {
	m := hubapi.Me{AuthMethod: hubapi.AuthMethodSession, Role: sess.User.Role}
	u := sess.User.User
	m.User = &u
	if csrf {
		m.CSRFToken = sess.CSRFToken
	}
	exp := sess.ExpiresAt
	m.SessionExpiresAt = &exp
	return m
}

// login serves POST /v1/auth/login.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req hubapi.LoginRequest
	if !decodeBody(w, r, &req) {
		return
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	now := s.now()
	ipL, userL := s.loginLimiters()
	// One message for every rate limit: it says nothing about whether the user exists.
	if ok, retry := ipL.Allow(auth.ClientIP(r), now); !ok {
		tooMany(w, r, hubapi.CodeRateLimited, retry, msgTooManySignIns)
		return
	}
	if ok, retry := userL.Allow(username, now); !ok {
		tooMany(w, r, hubapi.CodeRateLimited, retry, msgTooManySignIns)
		return
	}
	invalid := func() {
		writeProblem(w, r, http.StatusUnauthorized, hubapi.CodeInvalidCredentials, msgInvalidCredentials)
	}
	u, err := s.Store.GetUser(r.Context(), username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internal(w, r, err)
		return
	}
	// Unknown, deleted, disabled and locked users get exactly the answer of a wrong password, after
	// the same bcrypt work, so a response never tells whether a username exists.
	locked := err == nil && u.LockedUntil != nil && now.Before(*u.LockedUntil)
	if err != nil || u.DeletedAt != nil || u.Disabled || locked {
		auth.BurnPasswordCheck(req.Password)
		if locked {
			s.Log.InfoContext(r.Context(), "login refused: account locked", "user", username, "until", *u.LockedUntil)
		}
		invalid()
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		ac := s.Cfg.Auth
		locked, err := s.Store.LoginFailed(r.Context(), username, ac.maxFailedLogins(), now.Add(ac.lockoutDuration()))
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if locked != nil && now.Before(*locked) {
			s.Log.WarnContext(r.Context(), "account locked", "user", username, "until", *locked)
		}
		invalid()
		return
	}
	token, err := auth.RandomToken("", 32)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	csrf, err := auth.RandomToken("", 24)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	exp := now.Add(s.Cfg.Auth.sessionTTL()).UTC()
	if err := s.Store.CreateSession(r.Context(), auth.SHA256(token), username, csrf, exp, r.UserAgent(), auth.ClientIP(r), now); err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.Store.LoginSucceeded(r.Context(), username, now); err != nil {
		s.internal(w, r, err)
		return
	}
	userL.Reset(username)
	t := now.UTC()
	u.LastLoginAt, u.LockedUntil = &t, nil
	http.SetCookie(w, auth.SessionCookie(token, exp, s.Cfg.Auth.cookieSecure()))
	s.Log.InfoContext(r.Context(), "login", "user", username, "role", u.Role)
	writeJSON(w, s.me(&store.Session{CSRFToken: csrf, ExpiresAt: exp, User: u}, true))
}

// logout serves POST /v1/auth/logout. It always clears the cookie, also when the session already
// expired or was revoked, so a browser never keeps a dead cookie. A live session is revoked and
// needs its CSRF token (no forced sign-out from another page). It returns no data.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" {
		sess, err := s.Store.LookupSession(r.Context(), auth.SHA256(c.Value), s.now())
		switch {
		case err == nil:
			if !auth.TokenEqual(r.Header.Get(hubapi.CSRFHeader), sess.CSRFToken) {
				writeProblem(w, r, http.StatusForbidden, hubapi.CodeCSRFInvalid, "Missing or wrong "+hubapi.CSRFHeader+" header.")
				return
			}
			if err := s.Store.RevokeSession(r.Context(), sess.IDHash, s.now()); err != nil {
				s.internal(w, r, err)
				return
			}
			s.Log.InfoContext(r.Context(), "logout", "user", sess.User.Username)
		case !errors.Is(err, store.ErrNotFound):
			s.internal(w, r, err)
			return
		}
	}
	http.SetCookie(w, auth.ClearCookie(s.Cfg.Auth.cookieSecure()))
	w.WriteHeader(http.StatusNoContent)
}

// getMe serves GET /v1/me.
func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	if p.Session != nil {
		writeJSON(w, s.me(p.Session, true))
		return
	}
	writeJSON(w, hubapi.Me{AuthMethod: hubapi.AuthMethodToken, Role: p.Role, TokenID: strconv.FormatInt(p.Token.ID, 10)})
}

// changeOwnPassword serves POST /v1/me/password.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	var req hubapi.PasswordChange
	if !decodeBody(w, r, &req) {
		return
	}
	now := s.now()
	_, userL := s.loginLimiters()
	if ok, retry := userL.Allow(p.Session.User.Username, now); !ok {
		tooMany(w, r, hubapi.CodeRateLimited, retry, "Too many attempts.")
		return
	}
	if !auth.CheckPassword(p.Session.User.PasswordHash, req.CurrentPassword) {
		writeProblem(w, r, http.StatusUnauthorized, hubapi.CodeInvalidCredentials, "Wrong current password.")
		return
	}
	if req.NewPassword == req.CurrentPassword {
		badRequest(w, r, "invalid_password", "The new password must differ from the current one.")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword, s.Cfg.Auth.BcryptCost)
	if err != nil {
		badRequest(w, r, "invalid_password", err.Error())
		return
	}
	if err := s.Store.SetPassword(r.Context(), p.Session.User.Username, hash, false, p.Session.IDHash, now, nil); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- bootstrap and dev users ----

// BootstrapUsers creates the initial admin on a database without users (D-023) and the dev seed
// users when missing. Passwords are hashed with cfg.Auth.BcryptCost.
func BootstrapUsers(ctx context.Context, st *store.Store, cfg Config, log interface {
	InfoContext(context.Context, string, ...any)
}) error {
	now := time.Now()
	ba := cfg.Auth.BootstrapAdmin
	pw := os.Getenv("HUB_BOOTSTRAP_ADMIN_PASSWORD")
	if pw == "" && ba.PasswordFile != "" {
		b, err := os.ReadFile(ba.PasswordFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		pw = strings.TrimSpace(string(b))
	}
	if pw != "" {
		n, err := st.CountUsers(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			name := ba.Username
			if name == "" {
				name = "admin"
			}
			hash, err := auth.HashPassword(pw, cfg.Auth.BcryptCost)
			if err != nil {
				return fmt.Errorf("bootstrap admin: %w", err)
			}
			if _, err := st.EnsureUser(ctx, name, "Administrator", hubapi.UserRoleAdmin, hash, false, now); err != nil {
				return err
			}
			log.InfoContext(ctx, "bootstrap admin created", "user", name)
		}
	}
	users := cfg.DevSeed.Users
	if cfg.DevSeed.Enabled && cfg.DevSeed.UsersFile != "" {
		b, err := os.ReadFile(cfg.DevSeed.UsersFile)
		if err != nil {
			return fmt.Errorf("devSeed.usersFile: %w", err)
		}
		var more []DevUser
		if err := yaml.UnmarshalStrict(b, &more); err != nil {
			return fmt.Errorf("devSeed.usersFile: %w", err)
		}
		users = append(slices.Clone(users), more...)
	}
	for _, u := range users {
		if !cfg.DevSeed.Enabled {
			break
		}
		if !hubapi.Contains(hubapi.UserRoles, u.Role) {
			return fmt.Errorf("devSeed.users: %s: invalid role %q", u.Username, u.Role)
		}
		if _, err := st.GetUser(ctx, u.Username); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		hash, err := auth.HashPassword(u.Password, cfg.Auth.BcryptCost)
		if err != nil {
			return fmt.Errorf("devSeed.users: %s: %w", u.Username, err)
		}
		created, err := st.EnsureUser(ctx, u.Username, u.DisplayName, u.Role, hash, false, now)
		if err != nil {
			return err
		}
		if created {
			log.InfoContext(ctx, "dev user created", "user", u.Username, "role", u.Role)
		}
	}
	return nil
}
