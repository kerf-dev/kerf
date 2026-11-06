package hub

import (
	"cmp"
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
	"github.com/kerf-dev/kerf/internal/hub/teams"
)

// access is who may call a route: bearer tokens of the listed roles (an admin token always) and,
// when session is set, browser sessions of users with at least that role (D-023).
//
// teams marks the routes a team-scoped user may call (docs/teams.md): their data is filtered by the
// caller's scope in the store, and on /v1/workloads/{id}/... routes the caller needs the role on
// the workload's team. Every other route needs the role fleet-wide.
type access struct {
	tokens  []string
	session string
	teams   bool
}

var (
	accessAgent    = access{tokens: []string{hubapi.RoleAgent}}
	accessAudit    = access{tokens: []string{hubapi.RoleAgent, hubapi.RoleUI}, session: hubapi.UserRoleViewer, teams: true}
	accessViewer   = access{tokens: []string{hubapi.RoleUI}, session: hubapi.UserRoleViewer}
	accessOperator = access{tokens: []string{hubapi.RoleAdmin}, session: hubapi.UserRoleOperator}
	accessAdmin    = access{tokens: []string{hubapi.RoleAdmin}, session: hubapi.UserRoleAdmin}
	accessSession  = access{session: hubapi.UserRoleViewer, teams: true}
	// accessTeamViewer and accessTeamOperator: as accessViewer and accessOperator, also for
	// team-scoped users (filtered data; the role on the workload's team).
	accessTeamViewer   = access{tokens: []string{hubapi.RoleUI}, session: hubapi.UserRoleViewer, teams: true}
	accessTeamOperator = access{tokens: []string{hubapi.RoleAdmin}, session: hubapi.UserRoleOperator, teams: true}
	// accessPricing: the pricing service's token (D-026) or an admin token.
	accessPricing = access{tokens: []string{hubapi.RolePricing}}
)

// Principal is the authenticated caller.
type Principal struct {
	Method  string // hubapi.AuthMethodSession or hubapi.AuthMethodToken
	Role    string // user role (viewer/operator/admin); "" for agent tokens
	Token   store.Token
	Session *store.Session
	// Scope is what the caller may see and do (fleet-wide for tokens).
	Scope teams.Scope
	// usedTeams is the scope used by this request: the team whose binding allowed the action.
	usedTeams []string
}

// Actor returns the audit actor of the caller. An IdP user carries its subject and e-mail.
func (p Principal) Actor() hubapi.Actor {
	if p.Session != nil {
		a := hubapi.Actor{Type: hubapi.ActorUser, Name: p.Session.User.Username, Role: p.Session.User.Role}
		if p.Session.IsOIDC() {
			a.Subject, a.Email = p.Session.OIDCSubject, p.Session.User.Email
		}
		a.Teams = p.usedTeams
		return a
	}
	return hubapi.Actor{Type: hubapi.ActorToken, Name: "token:" + strconv.FormatInt(p.Token.ID, 10), Role: p.Role}
}

// sessionScope returns the scope of a signed-in user.
func sessionScope(u store.UserRow) teams.Scope {
	bs := make([]teams.Binding, 0, len(u.TeamRoles))
	for _, b := range u.TeamRoles {
		bs = append(bs, teams.Binding{Team: b.Team, Role: b.Role})
	}
	return teams.UserScope(u.Role, u.Scope == hubapi.ScopeTeams, bs)
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
			case err == nil && sess.IsOIDC() && s.refreshOIDC(r.Context(), &sess):
				// The provider ended the session: treated as no session (401).
			case err == nil:
				sc := sessionScope(sess.User)
				p = Principal{Method: hubapi.AuthMethodSession, Role: sc.MaxRole(), Session: &sess, Scope: sc}
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
			if !a.teams && !hubapi.UserRoleAtLeast(p.Scope.FleetRole, a.session) {
				writeProblem(w, r, http.StatusForbidden, hubapi.CodeScopeForbidden,
					"This endpoint covers the whole fleet; your roles are on teams "+strings.Join(p.Scope.Teams(), ", ")+" (needs "+a.session+" fleet-wide).")
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
			// Tokens are fleet-wide (agent tokens are restricted to their cluster below).
			p.Scope = teams.Fleet(cmp.Or(p.Role, hubapi.UserRoleViewer))
		}
		if id := r.PathValue("id"); id != "" && a.session != "" && !hubapi.UserRoleAtLeast(p.Scope.FleetRole, a.session) {
			// A workload route the fleet role does not cover: the caller needs the route's role on
			// the workload's team.
			if !s.authorizeWorkload(w, r, &p, a.session, id) {
				return
			}
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
		// Every read of the store is filtered by the caller's scope.
		ctx = teams.WithScope(ctx, p.Scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authorizeWorkload checks the caller's role on the team of workload id. A workload the caller
// cannot see is not found for reads and forbidden for writes; one it sees without the role is
// forbidden. A team binding that grants a write is recorded as the scope used (Actor.Teams). An
// unknown or malformed id is left to the handler.
func (s *Server) authorizeWorkload(w http.ResponseWriter, r *http.Request, p *Principal, need, id string) bool {
	key, err := hubapi.ParseWorkloadID(id)
	if err != nil {
		return true
	}
	team, found, err := s.Store.WorkloadTeam(r.Context(), key)
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if !found {
		return true
	}
	if !p.Scope.CanSee(team) && safeMethod(r.Method) {
		writeProblem(w, r, http.StatusNotFound, "workload_not_found", "Unknown workload "+key.ID()+".")
		return false
	}
	ok, byTeam := p.Scope.Grants(team, need)
	if !ok {
		writeProblem(w, r, http.StatusForbidden, hubapi.CodeScopeForbidden, "Your role on the team of workload "+key.ID()+" does not allow this (needs "+need+").")
		return false
	}
	if byTeam {
		p.usedTeams = []string{team}
	}
	return true
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

func tooMany(w http.ResponseWriter, r *http.Request, retry time.Duration, detail string) {
	w.Header().Set("Retry-After", auth.RetryAfterSeconds(retry))
	writeProblem(w, r, http.StatusTooManyRequests, hubapi.CodeRateLimited, detail)
}

func (s *Server) me(sess *store.Session, csrf bool) hubapi.Me {
	m := hubapi.Me{AuthMethod: hubapi.AuthMethodSession, Role: sessionScope(sess.User).MaxRole()}
	u := sess.User.User
	m.User = &u
	m.Scope, m.TeamRoles = cmp.Or(u.Scope, hubapi.ScopeFleet), u.TeamRoles
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
		tooMany(w, r, retry, msgTooManySignIns)
		return
	}
	if ok, retry := userL.Allow(username, now); !ok {
		tooMany(w, r, retry, msgTooManySignIns)
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
	// IdP users have no password; with local sign-in off only the break-glass admin has one.
	noPassword := err == nil && (u.Source == hubapi.UserSourceOIDC || !s.Cfg.Auth.passwordAllowed(username))
	if err != nil || u.DeletedAt != nil || u.Disabled || locked || noPassword {
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
	writeJSON(w, hubapi.Me{AuthMethod: hubapi.AuthMethodToken, Role: p.Role, TokenID: strconv.FormatInt(p.Token.ID, 10), Scope: hubapi.ScopeFleet})
}

// changeOwnPassword serves POST /v1/me/password.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	var req hubapi.PasswordChange
	if !decodeBody(w, r, &req) {
		return
	}
	if p.Session.User.Source == hubapi.UserSourceOIDC {
		writeProblem(w, r, http.StatusConflict, hubapi.CodeIdPManaged, "You sign in with the identity provider: there is no Kerf password to change.")
		return
	}
	now := s.now()
	_, userL := s.loginLimiters()
	if ok, retry := userL.Allow(p.Session.User.Username, now); !ok {
		tooMany(w, r, retry, "Too many attempts.")
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
