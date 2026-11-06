//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func webURL() string { return strings.TrimRight(getenv("E2E_WEB_URL", "http://localhost:19090"), "/") }

// TestWebSignIn (smoke tier): sign-in is required through the web UI's nginx proxy. Without a
// session cookie the proxy sends no credentials, so the hub answers 401 and no data. Signing in
// through the proxy gives a real session with the user's role (the proxy must forward the
// kerf_session cookie), and signing out ends it and clears the cookie.
func TestWebSignIn(t *testing.T) {
	t.Parallel()
	web := webURL()
	client := func() *http.Client { return &http.Client{Timeout: time.Minute} }
	get := func(c *http.Client, path string, hdr ...string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, web+path, nil)
		must(t, err)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := c.Do(req)
		must(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}

	// Anonymous: 401 and no data on every read the UI makes, also when the browser sends its own
	// Authorization header (the proxy never forwards it) or a stale cookie.
	for _, tc := range []struct {
		path string
		hdr  []string
	}{
		{"/v1/me", nil},
		{"/v1/workloads", nil},
		{"/v1/summary", nil},
		{"/v1/clusters", nil},
		{"/v1/audit", nil},
		{"/v1/admin/settings", nil},
		{"/v1/workloads", []string{"Authorization", "Bearer " + getenv("E2E_UI_TOKEN", "ro-dev-ui-97446cd3a095035d6290abc5")}},
		{"/v1/workloads", []string{"Cookie", "kerf_session=stale-or-forged"}},
	} {
		code, body := get(client(), tc.path, tc.hdr...)
		if code != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s %v through the proxy: %d, want 401: %s", tc.path, tc.hdr, code, bytes.TrimSpace(body))
		}
		if bytes.Contains(body, []byte(`"items"`)) || bytes.Contains(body, []byte(`"clusters"`)) {
			t.Errorf("anonymous GET %s returned data: %s", tc.path, body)
		}
	}

	// Sign in through the web proxy, like the UI's sign-in form.
	user := hubapi.UserRoleOperator
	jar, err := cookiejar.New(nil)
	must(t, err)
	c := client()
	c.Jar = jar
	b, _ := json.Marshal(hubapi.LoginRequest{Username: user, Password: getenv("E2E_"+strings.ToUpper(user)+"_PASSWORD", devPasswords[user])})
	resp, err := c.Post(web+"/v1/auth/login", "application/json", bytes.NewReader(b))
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in as %s through %s: %s: %s", user, web, resp.Status, bytes.TrimSpace(body))
	}
	var set bool
	for _, ck := range resp.Cookies() {
		set = set || ck.Name == "kerf_session"
	}
	if !set {
		t.Fatalf("login did not set the kerf_session cookie (Set-Cookie: %v)", resp.Header.Values("Set-Cookie"))
	}

	code, body := get(c, "/v1/me")
	var got hubapi.Me
	if code != http.StatusOK || json.Unmarshal(body, &got) != nil {
		t.Fatalf("GET /v1/me after sign in: %d: %s", code, bytes.TrimSpace(body))
	}
	if got.AuthMethod != hubapi.AuthMethodSession || got.Role != user || got.User == nil || got.User.Username != user {
		t.Errorf("after sign in through the web proxy: authMethod %q, role %q, user %+v; want %q, %q, %q "+
			"(the proxy does not forward the session cookie)", got.AuthMethod, got.Role, got.User, hubapi.AuthMethodSession, user, user)
	}
	if code, body := get(c, "/v1/workloads?limit=1"); code != http.StatusOK {
		t.Errorf("GET /v1/workloads with a session: %d: %s", code, bytes.TrimSpace(body))
	}

	// A wrong password and an unknown user get the same answer.
	wrong := func(u string) (int, string) {
		b, _ := json.Marshal(hubapi.LoginRequest{Username: u, Password: "not-the-password"})
		r, err := client().Post(web+"/v1/auth/login", "application/json", bytes.NewReader(b))
		must(t, err)
		defer r.Body.Close()
		var p hubapi.Problem
		_ = json.NewDecoder(r.Body).Decode(&p)
		return r.StatusCode, p.Code + ": " + p.Detail
	}
	if c1, m1 := wrong(user); c1 != http.StatusUnauthorized {
		t.Errorf("wrong password: %d %s, want 401", c1, m1)
	} else if c2, m2 := wrong("e2e-no-such-user"); c2 != c1 || m2 != m1 {
		t.Errorf("unknown user: %d %q; wrong password: %d %q (must not differ)", c2, m2, c1, m1)
	}

	// Sign out revokes the session and clears the cookie.
	req, err := http.NewRequest(http.MethodPost, web+"/v1/auth/logout", nil)
	must(t, err)
	req.Header.Set(hubapi.CSRFHeader, got.CSRFToken)
	lr, err := c.Do(req)
	must(t, err)
	lr.Body.Close()
	if lr.StatusCode != http.StatusNoContent {
		t.Fatalf("sign out: %s", lr.Status)
	}
	if code, _ := get(c, "/v1/me"); code != http.StatusUnauthorized {
		t.Errorf("GET /v1/me after sign out: %d, want 401", code)
	}
}

// TestWebSignInBrowser runs the browser test of sign-in through the web proxy (web/e2e-live, with
// Playwright): an anonymous visit sees only the sign-in page and gets no data, signing in returns to
// the page first asked for, a next= to another origin is ignored, an ended session goes back to
// sign-in, and sign-out. Needs Node with `npm ci` done in web/ and Chromium for Playwright
// (`npx playwright install chromium`); E2E_BROWSER=0 skips it.
func TestWebSignInBrowser(t *testing.T) {
	if os.Getenv("E2E_BROWSER") == "0" {
		t.Skip("E2E_BROWSER=0")
	}
	t.Parallel()
	dir, err := filepath.Abs(filepath.Join("..", "..", "web"))
	must(t, err)
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@playwright", "test")); err != nil {
		t.Fatalf("web/node_modules has no @playwright/test: run `npm ci` in web/ (or set E2E_BROWSER=0): %v", err)
	}
	cmd := exec.CommandContext(t.Context(), "npx", "playwright", "test", "-c", "playwright.live.config.ts", "--reporter=line")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "E2E_WEB_URL="+webURL())
	for _, u := range []string{hubapi.UserRoleViewer, hubapi.UserRoleOperator, hubapi.UserRoleAdmin} {
		k := "E2E_" + strings.ToUpper(u) + "_PASSWORD"
		cmd.Env = append(cmd.Env, k+"="+getenv(k, devPasswords[u]))
	}
	out, err := cmd.CombinedOutput()
	t.Logf("playwright (web/e2e-live) against %s:\n%s", webURL(), out)
	if err != nil {
		t.Fatalf("browser sign-in test failed: %v", err)
	}
}
