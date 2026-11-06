//go:build e2e

package e2e

// Admin-area client (WO-24): the scenarios drive the hub API like the web UI does, as a logged-in
// user with the session cookie and the CSRF token (docs/contracts/admin.md, D-023).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1alpha1 "github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Dev users seeded by deploy/charts/hub/values-dev.yaml (E2E_<USER>_PASSWORD overrides).
var devPasswords = map[string]string{
	hubapi.UserRoleViewer:   "ro-dev-viewer-pass",
	hubapi.UserRoleOperator: "ro-dev-operator-pass",
	hubapi.UserRoleAdmin:    "ro-dev-admin-pass",
}

// session is a logged-in hub user.
type session struct {
	User string
	CSRF string
	// Me is the login answer.
	Me hubapi.Me
	c  *http.Client
}

var (
	sessionsMu sync.Mutex
	sessions   = map[string]*session{}
)

// as returns the suite-wide session of a dev user, logging in on first use. Sessions are shared by
// all scenarios: the hub rate-limits logins (10/min per user) and the suite runs in parallel.
func as(t *testing.T, user string) *session {
	t.Helper()
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	if s := sessions[user]; s != nil {
		return s
	}
	s, err := loginExpect(user, getenv("E2E_"+strings.ToUpper(user)+"_PASSWORD", devPasswords[user]), user)
	if err != nil {
		t.Fatalf("log in as %s: %v", user, err)
	}
	sessions[user] = s
	return s
}

func login(user, password string) (*session, error) {
	jar, _ := cookiejar.New(nil)
	s := &session{User: user, c: &http.Client{Jar: jar, Timeout: time.Minute}}
	b, _ := json.Marshal(hubapi.LoginRequest{Username: user, Password: password})
	resp, err := s.c.Post(env.Hub.Base+"/v1/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, body)
	}
	var me hubapi.Me
	if err := json.Unmarshal(body, &me); err != nil {
		return nil, err
	}
	if me.CSRFToken == "" || me.AuthMethod != hubapi.AuthMethodSession {
		return nil, fmt.Errorf("unexpected login answer: %s", body)
	}
	s.CSRF, s.Me = me.CSRFToken, me
	return s, nil
}

// loginExpect logs in and requires the session's role (the dev users are named after their role).
func loginExpect(user, password, role string) (*session, error) {
	s, err := login(user, password)
	if err != nil {
		return nil, err
	}
	if s.Me.Role != role {
		return nil, fmt.Errorf("log in as %s: role %q, want %q", user, s.Me.Role, role)
	}
	return s, nil
}

// sessionOf returns a user's session if one was opened (for cleanups, which must not fail the test
// by logging in).
func sessionOf(user string) *session {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	return sessions[user]
}

// logoutAll ends the suite's sessions (TestMain).
func logoutAll() {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	for u, s := range sessions {
		_, _ = s.raw(context.Background(), http.MethodPost, "/v1/auth/logout", nil, nil)
		delete(sessions, u)
	}
}

// reply is a hub answer.
type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

// Problem is the hub's problem+json code ("" if the body is none).
func (r reply) Problem() string {
	var p hubapi.Problem
	_ = json.Unmarshal(r.Body, &p)
	return p.Code
}

func (r reply) String() string {
	return fmt.Sprintf("HTTP %d: %s", r.Status, bytes.TrimSpace(r.Body))
}

// hdr are extra request headers; "X-CSRF-Token": "" leaves the CSRF token out.
type hdr map[string]string

func (s *session) raw(ctx context.Context, method, path string, body any, h hdr) (reply, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return reply{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, env.Hub.Base+path, rd)
	if err != nil {
		return reply{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set(hubapi.CSRFHeader, s.CSRF)
	}
	for k, v := range h {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := s.c.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// do sends a request and fails the test on a transport error.
func (s *session) do(t *testing.T, method, path string, body any, h hdr) reply {
	t.Helper()
	r, err := s.raw(ctx(t), method, path, body, h)
	if err != nil {
		t.Fatalf("%s %s as %s: %v", method, path, s.User, err)
	}
	return r
}

// call sends a request, requires the status and decodes the answer into out (if not nil).
func (s *session) call(t *testing.T, method, path string, body any, h hdr, want int, out any) reply {
	t.Helper()
	r := s.do(t, method, path, body, h)
	if r.Status != want {
		t.Fatalf("%s %s as %s: want HTTP %d, got %s", method, path, s.User, want, r)
	}
	if out != nil {
		if err := json.Unmarshal(r.Body, out); err != nil {
			t.Fatalf("%s %s: decode: %v: %s", method, path, err, r.Body)
		}
	}
	return r
}

// get decodes a GET answer (200).
func (s *session) get(t *testing.T, path string, out any) {
	t.Helper()
	s.call(t, http.MethodGet, path, nil, nil, http.StatusOK, out)
}

func ifMatch(version int) hdr { return hdr{"If-Match": strconv.Quote(strconv.Itoa(version))} }

func reason(t *testing.T, what string) string { return "e2e " + t.Name() + ": " + what }

// ---- policies ----

// hubPolicy is a hub policy of type `policy` for the namespace on the clusters.
func hubPolicy(t *testing.T, name, ns string, clusters []string, spec v1alpha1.RightsizingPolicySpec) hubapi.PolicyInput {
	spec.Selector.Namespaces = []string{ns}
	if spec.Priority == 0 {
		spec.Priority = 1000
	}
	return hubapi.PolicyInput{Type: v1alpha1.HubPolicyTypePolicy, Name: name, Clusters: clusters, Spec: spec, Reason: reason(t, "create "+name)}
}

// createHubPolicy creates the policy as admin and deletes it (and waits for the hub to accept the
// delete) when the test ends, unless the test deleted it itself.
func createHubPolicy(t *testing.T, in hubapi.PolicyInput) hubapi.AdminPolicy {
	t.Helper()
	var p hubapi.AdminPolicy
	retryNotReady(t, "create policy "+in.Name, func() reply {
		return as(t, hubapi.UserRoleAdmin).do(t, http.MethodPost, "/v1/admin/policies", in, nil)
	}, http.StatusCreated, &p)
	t.Logf("hub policy %s (%s) version %d", p.Name, p.ID, p.Version)
	t.Cleanup(func() {
		if env.Keep {
			return
		}
		if err := deleteHubPolicyQuiet(p.ID, "e2e cleanup"); err != nil {
			t.Logf("cleanup: delete hub policy %s: %v", p.Name, err)
		}
	})
	return p
}

// deleteHubPolicyQuiet deletes a hub policy at its current version; a deleted or missing policy is fine.
func deleteHubPolicyQuiet(id, why string) error {
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := sessionOf(hubapi.UserRoleAdmin)
	if s == nil {
		return fmt.Errorf("no admin session")
	}
	r, err := s.raw(c, http.MethodGet, "/v1/admin/policies/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return err
	}
	if r.Status == http.StatusNotFound {
		return nil
	}
	var p hubapi.AdminPolicy
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return fmt.Errorf("%s", r)
	}
	if p.DeletedAt != nil {
		return nil
	}
	r, err = s.raw(c, http.MethodDelete, "/v1/admin/policies/"+url.PathEscape(id), hubapi.ChangeReason{Reason: why},
		hdr{"If-Match": strconv.Quote(strconv.Itoa(p.Version))})
	if err != nil {
		return err
	}
	if r.Status/100 != 2 && r.Status != http.StatusNotFound {
		return fmt.Errorf("%s", r)
	}
	return nil
}

// retryNotReady repeats a gated request while the hub answers 409 cluster_not_automation_ready
// (a spoke can drop out of AutomationReady for a minute, e.g. while another track redeploys its
// agent), then requires want.
func retryNotReady(t *testing.T, what string, send func() reply, want int, out any) reply {
	t.Helper()
	var r reply
	eventually(t, 3*time.Minute, 10*time.Second, what, func() (bool, string) {
		r = send()
		return !(r.Status == http.StatusConflict && r.Problem() == hubapi.CodeClusterNotAutomationReady), r.String()
	})
	if r.Status != want {
		t.Fatalf("%s: want HTTP %d, got %s", what, want, r)
	}
	if out != nil {
		must(t, json.Unmarshal(r.Body, out))
	}
	return r
}

// configChanges returns config-audit rows (newest first) matching the query.
func configChanges(t *testing.T, q url.Values) []hubapi.ConfigChange {
	t.Helper()
	q.Set("limit", "200")
	var out hubapi.ConfigChangeList
	as(t, hubapi.UserRoleViewer).get(t, "/v1/audit/config?"+q.Encode(), &out)
	return out.Items
}

// waitConfigChange waits for a config-audit row with the action matching q and returns it.
func waitConfigChange(t *testing.T, q url.Values, action string, match func(hubapi.ConfigChange) bool) hubapi.ConfigChange {
	t.Helper()
	var found hubapi.ConfigChange
	q.Set("action", action)
	eventually(t, 2*time.Minute, 3*time.Second, "config change "+action+" "+q.Encode(), func() (bool, string) {
		items := configChanges(t, q)
		for _, c := range items {
			if match == nil || match(c) {
				found = c
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d rows", len(items))
	})
	return found
}

// ---- workloads ----

func workloadID(cl cluster, ns, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s", cl.Name, ns, deployment, name)
}

func workloadPath(cl cluster, ns, name string) string {
	return "/v1/workloads/" + url.PathEscape(workloadID(cl, ns, name))
}

// approve approves the workload's latest recommendation as user. A recommender run between reading
// the recommendation and approving it makes it stale (409): read again and retry.
func approve(t *testing.T, user string, cl cluster, ns, name string) hubapi.Approval {
	t.Helper()
	var a hubapi.Approval
	eventually(t, 3*time.Minute, 5*time.Second, "approval of "+ns+"/"+name, func() (bool, string) {
		var d hubapi.WorkloadDetail
		as(t, user).get(t, workloadPath(cl, ns, name), &d)
		if d.Recommendation == nil {
			return false, "no recommendation"
		}
		r := as(t, user).do(t, http.MethodPost, workloadPath(cl, ns, name)+"/approve",
			hubapi.ApprovalRequest{RecommendationID: d.Recommendation.ID, Reason: reason(t, "approve once")}, nil)
		switch {
		case r.Status == http.StatusCreated:
			must(t, json.Unmarshal(r.Body, &a))
			return true, ""
		case r.Status == http.StatusConflict && (r.Problem() == hubapi.CodeRecommendationStale || r.Problem() == hubapi.CodeClusterNotAutomationReady):
			return false, r.String()
		default:
			t.Fatalf("approve %s/%s: %s", ns, name, r)
			return false, ""
		}
	})
	t.Logf("approved recommendation %s of %s/%s as %s (approval %s)", a.RecommendationID, ns, name, user, a.ID)
	return a
}

// approveApplied approves the latest recommendation and waits for the agent's applied/ApprovedOnce
// event for it (the event names the approved recommendation). A recommender run between the
// approval and the agent's next pull supersedes the approval (only the latest recommendation can
// be approved): then it approves the new one, up to 3 times. The applied event of a superseded
// approval may still arrive (the agent had applied it, WO-29); it is not the one waited for. The
// suite's own recompute requests are held off meanwhile.
func approveApplied(t *testing.T, user string, cl cluster, ns, name string) (hubapi.Approval, auditEvent) {
	t.Helper()
	release := env.Hub.holdRecompute(t, cl.Name)
	defer release()
	for attempt := 1; ; attempt++ {
		a := approve(t, user, cl, ns, name)
		var ev auditEvent
		var state string
		eventually(t, 3*time.Minute, 5*time.Second, "approval "+a.ID+" applied or superseded", func() (bool, string) {
			evs, err := env.Hub.audit(ctx(t), cl.Name, ns, deployment, name, "applied")
			if err != nil {
				return false, err.Error()
			}
			for _, e := range evs {
				if e.Reason == v1alpha1.ReasonApprovedOnce && e.RecommendationID == a.RecommendationID {
					ev = e
					return true, ""
				}
			}
			var d hubapi.WorkloadDetail
			as(t, viewer).get(t, workloadPath(cl, ns, name), &d)
			if d.Approval != nil && d.Approval.ID == a.ID {
				state = d.Approval.State
			}
			return state == hubapi.ApprovalSuperseded || state == hubapi.ApprovalExpired, fmt.Sprintf("approval %s, %d applied events", state, len(evs))
		})
		if ev.ID != "" {
			return a, ev
		}
		if attempt == 3 {
			t.Fatalf("approval of %s/%s %s three times in a row", ns, name, state)
		}
		t.Logf("approval %s was %s before the agent applied it (a recommender run in between); approving again", a.ID, state)
	}
}

// ---- automation ----

// setPause pauses or resumes a cluster's automation as user and returns the answer.
func setPause(t *testing.T, user, clusterName string, paused bool, h hdr) reply {
	t.Helper()
	what := "resume"
	if paused {
		what = "pause"
	}
	return as(t, user).do(t, http.MethodPut, "/v1/clusters/"+url.PathEscape(clusterName)+"/automation",
		hubapi.AutomationUpdate{Paused: paused, Reason: reason(t, what)}, h)
}

// ---- clusters and readiness ----

// registerCluster registers a cluster without an agent token as admin and deletes it at the end.
func registerCluster(t *testing.T, in hubapi.ClusterCreate) hubapi.ClusterCreated {
	t.Helper()
	no := false
	in.CreateAgentToken = &no
	if in.Environment == "" {
		in.Environment = hubapi.EnvironmentNonProd
	}
	in.Reason = reason(t, "register "+in.Name)
	var out hubapi.ClusterCreated
	as(t, hubapi.UserRoleAdmin).call(t, http.MethodPost, "/v1/admin/clusters", in, nil, http.StatusCreated, &out)
	t.Logf("registered cluster %s", in.Name)
	t.Cleanup(func() {
		if env.Keep {
			return
		}
		if err := deleteClusterQuiet(in.Name, "e2e cleanup"); err != nil {
			t.Errorf("cleanup: delete cluster %s: %v", in.Name, err)
		}
	})
	return out
}

func deleteClusterQuiet(name, why string) error {
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := sessionOf(hubapi.UserRoleAdmin)
	if s == nil {
		return fmt.Errorf("no admin session")
	}
	r, err := s.raw(c, http.MethodGet, "/v1/admin/clusters/"+url.PathEscape(name), nil, nil)
	if err != nil {
		return err
	}
	if r.Status == http.StatusNotFound {
		return nil
	}
	var ca hubapi.ClusterAdmin
	if err := json.Unmarshal(r.Body, &ca); err != nil {
		return fmt.Errorf("%s", r)
	}
	if ca.DeletedAt != nil {
		return nil
	}
	r, err = s.raw(c, http.MethodDelete, "/v1/admin/clusters/"+url.PathEscape(name), hubapi.ChangeReason{Reason: why},
		hdr{"If-Match": strconv.Quote(strconv.Itoa(ca.Version))})
	if err != nil {
		return err
	}
	if r.Status/100 != 2 && r.Status != http.StatusNotFound {
		return fmt.Errorf("%s", r)
	}
	return nil
}

func readiness(t *testing.T, clusterName string) hubapi.ClusterReadiness {
	t.Helper()
	var r hubapi.ClusterReadiness
	as(t, hubapi.UserRoleViewer).get(t, "/v1/clusters/"+url.PathEscape(clusterName)+"/readiness", &r)
	return r
}

func recheck(t *testing.T, clusterName string) hubapi.RecheckAccepted {
	t.Helper()
	var a hubapi.RecheckAccepted
	as(t, hubapi.UserRoleOperator).call(t, http.MethodPost, "/v1/clusters/"+url.PathEscape(clusterName)+"/readiness/recheck",
		nil, nil, http.StatusAccepted, &a)
	return a
}

func check(r hubapi.ClusterReadiness, id string) *hubapi.ReadinessCheck {
	for i := range r.Checks {
		if r.Checks[i].ID == id {
			return &r.Checks[i]
		}
	}
	return nil
}

// agentChecksSince reports whether every agent-sourced check was reported at or after t0.
func agentChecksSince(r hubapi.ClusterReadiness, t0 time.Time) (bool, string) {
	n := 0
	for _, c := range r.Checks {
		if c.Source != "agent" {
			continue
		}
		n++
		if c.CheckedAt.Before(t0.Add(-2 * time.Second)) {
			return false, fmt.Sprintf("agent check %s from %s, recheck at %s", c.ID, c.CheckedAt.Format(time.TimeOnly), t0.Format(time.TimeOnly))
		}
	}
	return n > 0, fmt.Sprintf("%d agent checks", n)
}

// waitReadiness rechecks the cluster and waits until the agent answered the recheck and cond holds.
func waitReadiness(t *testing.T, clusterName string, timeout time.Duration, what string, cond func(hubapi.ClusterReadiness) (bool, string)) hubapi.ClusterReadiness {
	t.Helper()
	acc := recheck(t, clusterName)
	var r hubapi.ClusterReadiness
	last := time.Now()
	eventually(t, timeout, 5*time.Second, clusterName+" readiness: "+what, func() (bool, string) {
		r = readiness(t, clusterName)
		if ok, why := agentChecksSince(r, acc.RequestedAt); !ok {
			if time.Since(last) > 3*time.Minute { // the hub drops an unanswered recheck after 15 min
				acc, last = recheck(t, clusterName), time.Now()
			}
			return false, "state " + r.State + ": " + why
		}
		ok, why := cond(r)
		return ok, fmt.Sprintf("state %s blockedBy %v: %s", r.State, r.BlockedBy, why)
	})
	return r
}

func stateIs(want string) func(hubapi.ClusterReadiness) (bool, string) {
	return func(r hubapi.ClusterReadiness) (bool, string) { return r.State == want, "want " + want }
}

// ---- spoke-2 agent ----

// spoke2 serialises the scenarios that disturb the ro-spoke-2 agent or need it undisturbed while
// they apply: the hub outage (TestHubUnreachable), the pause (TestAdminPause), the RBAC removal
// (TestReadinessRBAC), the KSM scale-down and the spoke-2 readiness check. Without it a pause would
// block another scenario's apply, a missing pods/resize permission would fail an in-place resize,
// and an outage would hide a recheck.
var spoke2 sync.Mutex

// lockSpoke2 takes the spoke-2 lock until unlock is called or the test ends.
func lockSpoke2(t *testing.T) (unlock func()) {
	t.Helper()
	start := time.Now()
	spoke2.Lock()
	t.Logf("spoke-2 lock taken after %s", time.Since(start).Round(time.Second))
	var once sync.Once
	unlock = func() { once.Do(spoke2.Unlock) }
	t.Cleanup(unlock)
	return unlock
}

// sweepHub removes hub objects a killed run left behind: policies and clusters named e2e-*, and
// pauses set by the suite (reason "e2e …"). Runs before the scenarios (setup) and from
// `make e2e-clean`; never while scenarios run, since they own such objects.
func sweepHub(logf func(string, ...any)) error {
	s, err := loginExpect(hubapi.UserRoleAdmin, getenv("E2E_ADMIN_PASSWORD", devPasswords[hubapi.UserRoleAdmin]), hubapi.UserRoleAdmin)
	if err != nil {
		return fmt.Errorf("log in as admin: %w", err)
	}
	sessionsMu.Lock()
	sessions[hubapi.UserRoleAdmin] = s
	sessionsMu.Unlock()
	c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	getJSON := func(path string, out any) error {
		r, err := s.raw(c, http.MethodGet, path, nil, nil)
		if err != nil {
			return err
		}
		if r.Status != http.StatusOK {
			return fmt.Errorf("GET %s: %s", path, r)
		}
		return json.Unmarshal(r.Body, out)
	}
	var policies hubapi.AdminPolicyList
	if err := getJSON("/v1/admin/policies", &policies); err != nil {
		return err
	}
	for _, p := range policies.Items {
		if strings.HasPrefix(p.Name, "e2e-") && p.DeletedAt == nil {
			if err := deleteHubPolicyQuiet(p.ID, "e2e sweep: left by an interrupted run"); err != nil {
				return fmt.Errorf("delete policy %s: %w", p.Name, err)
			}
			logf("sweep: deleted hub policy %s", p.Name)
		}
	}
	var clusters hubapi.ClusterList
	if err := getJSON("/v1/clusters", &clusters); err != nil {
		return err
	}
	for _, cl := range clusters.Items {
		if strings.HasPrefix(cl.Name, "e2e-") {
			if err := deleteClusterQuiet(cl.Name, "e2e sweep: left by an interrupted run"); err != nil {
				return fmt.Errorf("delete cluster %s: %w", cl.Name, err)
			}
			logf("sweep: deleted cluster %s", cl.Name)
		}
	}
	var teams hubapi.TeamList
	if err := getJSON("/v1/admin/teams", &teams); err != nil {
		return err
	}
	for _, tm := range teams.Items {
		if strings.HasPrefix(tm.ID, "e2e-") {
			deleteTeamQuiet(tm.ID, "e2e sweep: left by an interrupted run")
			logf("sweep: deleted team %s", tm.ID)
		}
	}
	var auto hubapi.FleetAutomation
	if err := getJSON("/v1/admin/automation", &auto); err != nil {
		return err
	}
	scopes := map[string]hubapi.Automation{}
	if auto.Fleet.Paused {
		scopes[""] = auto.Fleet
	}
	for _, a := range auto.Clusters {
		if a.Automation.Paused {
			scopes[a.Cluster] = a.Automation
		}
	}
	for scope, a := range scopes {
		if !strings.HasPrefix(a.Reason, "e2e ") {
			continue
		}
		path := "/v1/admin/automation"
		if scope != "" {
			path = "/v1/clusters/" + url.PathEscape(scope) + "/automation"
		}
		r, err := s.raw(c, http.MethodPut, path, hubapi.AutomationUpdate{Paused: false, Reason: "e2e sweep: resume after an interrupted run"}, nil)
		if err != nil || r.Status != http.StatusOK {
			return fmt.Errorf("resume %s: %v %s", path, err, r)
		}
		logf("sweep: resumed %s (paused by %q)", path, a.Reason)
	}
	return nil
}

// TestHubSweep only sweeps (make e2e-clean sets E2E_SWEEP=1); the sweep itself runs in setup.
func TestHubSweep(t *testing.T) {
	if os.Getenv("E2E_SWEEP") == "" {
		t.Skip("make e2e-clean only")
	}
}
