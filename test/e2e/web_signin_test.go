//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// TestWebSignIn (smoke tier): signing in through the web UI's nginx proxy gives a real session. The
// proxy must swap the browser's kerf_session cookie for hub auth; if it does not, every request
// still carries the read-only `ui` token and the user stays a viewer after a "successful" sign-in.
// Without the cookie the proxy keeps sending the `ui` token (anonymous viewer).
func TestWebSignIn(t *testing.T) {
	t.Parallel()
	web := strings.TrimRight(getenv("E2E_WEB_URL", "http://localhost:19090"), "/")
	client := func() *http.Client { return &http.Client{Timeout: time.Minute} }
	me := func(c *http.Client) hubapi.Me {
		t.Helper()
		resp, err := c.Get(web + "/v1/me")
		must(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s/v1/me: %s: %s", web, resp.Status, bytes.TrimSpace(body))
		}
		var m hubapi.Me
		must(t, json.Unmarshal(body, &m))
		return m
	}

	// Anonymous: the proxy adds the read-only ui token.
	if m := me(client()); m.AuthMethod != hubapi.AuthMethodToken || m.Role != hubapi.UserRoleViewer {
		t.Errorf("without a cookie: authMethod %q, role %q; want %q, %q", m.AuthMethod, m.Role, hubapi.AuthMethodToken, hubapi.UserRoleViewer)
	}

	// Sign in through the web proxy, like the UI's login form.
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

	got := me(c)
	if got.AuthMethod != hubapi.AuthMethodSession || got.Role != user || got.User == nil || got.User.Username != user {
		t.Errorf("after sign in through the web proxy: authMethod %q, role %q, user %+v; want %q, %q, %q "+
			"(the proxy does not forward the session cookie)", got.AuthMethod, got.Role, got.User, hubapi.AuthMethodSession, user, user)
	}

	// Sign out ends the session: back to the anonymous viewer.
	req, err := http.NewRequest(http.MethodPost, web+"/v1/auth/logout", nil)
	must(t, err)
	req.Header.Set(hubapi.CSRFHeader, got.CSRFToken)
	if lr, err := c.Do(req); err == nil {
		lr.Body.Close()
	}
}
