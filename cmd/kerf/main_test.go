package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kerf-dev/kerf/internal/report"
)

// emptyProm answers every query with an empty result and records the request headers.
func emptyProm(t *testing.T) (*httptest.Server, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		typ := "vector"
		if strings.HasSuffix(r.URL.Path, "query_range") {
			typ = "matrix"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"` + typ + `","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []http.Header { mu.Lock(); defer mu.Unlock(); return append([]http.Header(nil), seen...) }
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = run(context.Background(), args, &o, &e)
	return code, o.String(), e.String()
}

func TestVersion(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "1.2.3"
	if code, out, _ := runCLI("version"); code != 0 || out != "kerf 1.2.3\n" {
		t.Errorf("version: code %d out %q", code, out)
	}
	version = ""
	if code, out, _ := runCLI("version"); code != 0 || !strings.HasPrefix(out, "kerf ") || strings.TrimSpace(out) == "kerf" {
		t.Errorf("version without a build version: code %d out %q", code, out)
	}
}

func TestUsageAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		code     int
		inStderr string
		inStdout string
	}{
		{"no command", nil, 2, "Usage:", ""},
		{"help", []string{"help"}, 0, "", "kerf report"},
		{"report help", []string{"help", "report"}, 0, "--prometheus-url", ""},
		{"unknown command", []string{"apply"}, 2, `unknown command "apply"`, ""},
		{"missing url", []string{"report"}, 1, "--prometheus-url is required", ""},
		{"bad format", []string{"report", "--prometheus-url", "http://x", "--format", "xml"}, 2, "--format", ""},
		{"bad cluster label", []string{"report", "--prometheus-url", "http://x", "--cluster-label", "prod"}, 1, "--cluster-label", ""},
		{"bad profile", []string{"report", "--prometheus-url", "http://x", "--profile", "fast"}, 1, "--profile", ""},
		{"stray argument", []string{"report", "--prometheus-url", "http://x", "extra"}, 2, "unexpected argument", ""},
		{"unreachable", []string{"report", "--prometheus-url", "http://127.0.0.1:1", "--quiet"}, 1, "read usage from the metrics source", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runCLI(tc.args...)
			if code != tc.code || !strings.Contains(errOut, tc.inStderr) || !strings.Contains(out, tc.inStdout) {
				t.Errorf("code %d (want %d)\nstdout: %s\nstderr: %s", code, tc.code, out, errOut)
			}
		})
	}
}

func TestReportFormatsAndPointer(t *testing.T) {
	srv, _ := emptyProm(t)
	for _, format := range []string{"table", "markdown", "json", "csv"} {
		t.Run(format, func(t *testing.T) {
			code, out, errOut := runCLI("report", "--prometheus-url", srv.URL, "--cluster-label", "cluster=prod-eu-1", "--format", format)
			if code != 0 {
				t.Fatalf("code %d: %s", code, errOut)
			}
			inOut := strings.Contains(out, report.InstallURL)
			switch format {
			case "csv": // data only: the pointer goes to stderr
				if inOut || !strings.Contains(errOut, report.InstallURL) {
					t.Errorf("csv: pointer on stdout=%v, stderr=%q", inOut, errOut)
				}
			default:
				if !inOut {
					t.Errorf("%s output lacks the pointer to the full install:\n%s", format, out)
				}
			}
			if !strings.Contains(errOut, "Reading 14 days") && format != "csv" {
				t.Errorf("no progress message: %q", errOut)
			}
		})
	}
	t.Run("quiet", func(t *testing.T) {
		if _, _, errOut := runCLI("report", "--prometheus-url", srv.URL, "--quiet"); errOut != "" {
			t.Errorf("stderr = %q", errOut)
		}
	})
	t.Run("url from the environment", func(t *testing.T) {
		t.Setenv("KERF_PROMETHEUS_URL", srv.URL)
		if code, _, errOut := runCLI("report", "--quiet"); code != 0 {
			t.Errorf("code %d: %s", code, errOut)
		}
	})
}

func TestReportAuthFlags(t *testing.T) {
	srv, seen := emptyProm(t)
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	passFile := filepath.Join(dir, "pass")
	if err := os.WriteFile(passFile, []byte("file-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	type tc struct {
		name  string
		env   map[string]string
		args  []string
		check func(h http.Header) bool
	}
	for _, c := range []tc{
		{"token file", nil, []string{"--token-file", tokenFile}, func(h http.Header) bool { return h.Get("Authorization") == "Bearer file-token" }},
		{"token from the environment", map[string]string{"KERF_PROMETHEUS_TOKEN": "env-token"}, nil, func(h http.Header) bool { return h.Get("Authorization") == "Bearer env-token" }},
		{"basic auth", nil, []string{"--username", "kerf", "--password-file", passFile}, func(h http.Header) bool {
			r := &http.Request{Header: h}
			u, p, ok := r.BasicAuth()
			return ok && u == "kerf" && p == "file-pass"
		}},
		{"tenant headers", nil, []string{"--header", "X-Scope-OrgID: t1", "--header", "X-Other: 2"}, func(h http.Header) bool {
			return h.Get("X-Scope-OrgID") == "t1" && h.Get("X-Other") == "2"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			before := len(seen())
			args := append([]string{"report", "--prometheus-url", srv.URL, "--quiet"}, c.args...)
			if code, _, errOut := runCLI(args...); code != 0 {
				t.Fatalf("code %d: %s", code, errOut)
			}
			hs := seen()[before:]
			if len(hs) == 0 {
				t.Fatal("no requests")
			}
			for _, h := range hs {
				if !c.check(h) {
					t.Fatalf("headers = %v", h)
				}
			}
		})
	}
	t.Run("missing token file", func(t *testing.T) {
		code, _, errOut := runCLI("report", "--prometheus-url", srv.URL, "--token-file", filepath.Join(dir, "nope"))
		if code != 1 || !strings.Contains(errOut, "nope") {
			t.Errorf("code %d: %s", code, errOut)
		}
	})
}
