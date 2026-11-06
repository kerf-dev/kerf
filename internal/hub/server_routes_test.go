package hub

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// publicRoutes are the only /v1 endpoints a caller without credentials may reach. Adding one is a
// security decision: change this list on purpose, never to make the test pass.
var publicRoutes = []string{
	"POST /v1/auth/login",  // sign-in itself
	"POST /v1/auth/logout", // clears the cookie; revokes only a live session with its CSRF token
	// Sign-in with the identity provider: what the sign-in page offers, the redirect to the provider
	// and its callback (which needs the state cookie of the browser that started it).
	"GET /v1/auth/providers",
	"GET /v1/auth/oidc/login",
	"GET /v1/auth/oidc/callback",
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// TestRouteInventory fails when a /v1 route answers a request without credentials, unless it is
// listed in publicRoutes. The probes and /metrics live outside /v1.
func TestRouteInventory(t *testing.T) {
	s := &Server{Cfg: DefaultConfig(), Metrics: NewMetrics(), Log: discardLog()}
	h := s.Handler()

	var public []string
	for _, rt := range s.apiRoutes() {
		method, path, ok := strings.Cut(rt.pattern, " ")
		if !ok || !strings.HasPrefix(path, "/v1/") {
			t.Errorf("route %q: want \"METHOD /v1/...\"", rt.pattern)
			continue
		}
		if rt.access == nil {
			public = append(public, rt.pattern)
			continue
		}
		if len(rt.access.tokens) == 0 && rt.access.session == "" {
			t.Errorf("%s: access allows nobody", rt.pattern)
		}
		// Concrete values for the path parameters (a workload id is one %2F-encoded segment).
		path = pathParam.ReplaceAllStringFunc(path, func(p string) string {
			if p == "{id}" {
				return "c1%2Fns%2FDeployment%2Fapp"
			}
			return "x"
		})
		for _, cred := range []struct{ name, header, value string }{
			{"no credentials", "", ""},
			{"empty bearer", "Authorization", "Bearer "},
			{"basic auth", "Authorization", "Basic YWRtaW46YWRtaW4="},
			{"empty session cookie", "Cookie", "kerf_session="},
		} {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			if cred.header != "" {
				req.Header.Set(cred.header, cred.value)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s with %s: status %d, want 401", rt.pattern, cred.name, w.Code)
			}
		}
	}
	slices.Sort(public)
	want := slices.Sorted(slices.Values(publicRoutes))
	if !slices.Equal(public, want) {
		t.Errorf("public /v1 routes = %q, want %q (publicRoutes)", public, want)
	}

	// Outside /v1: only the probes and metrics answer without credentials.
	for _, p := range []string{"/healthz", "/metrics"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", p, w.Code)
		}
	}
	// An unknown /v1 path is a 404 problem, never data.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/not-a-route", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown route: status %d, want 404", w.Code)
	}
}

// TestRoutesOnlyFromInventory fails when server.go registers a /v1 route on the mux directly,
// bypassing apiRoutes (and with it authentication and the inventory test).
func TestRoutesOnlyFromInventory(t *testing.T) {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true // the apiRoutes loop: rt.pattern
		}
		pattern, _ := strconv.Unquote(lit.Value)
		_, path, _ := strings.Cut(pattern, " ")
		if path == "" {
			path = pattern
		}
		if strings.HasPrefix(path, "/v1") {
			t.Errorf("%s: %s registers %q on the mux directly; add it to apiRoutes", fs.Position(call.Pos()), sel.Sel.Name, pattern)
		}
		return true
	})
}
