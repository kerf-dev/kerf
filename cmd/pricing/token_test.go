package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHubToken(t *testing.T) {
	const source = "Secret kerf/hub-local-tokens key pricing"
	for _, tc := range []struct {
		name    string
		content *string
		want    string
	}{
		{name: "present", content: strp("tok\n"), want: "tok"},
		{name: "Secret absent", content: nil},
		{name: "empty", content: strp("\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if tc.content != nil {
				if err := os.WriteFile(path, []byte(*tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			h := newHubToken(path, source, slog.New(slog.NewTextHandler(&buf, nil)))
			now := time.Unix(0, 0)
			h.now = func() time.Time { return now }
			tok, err := h.Token()
			_, _ = h.Token() // within logEvery: not logged again
			if tc.want != "" {
				if err != nil || tok != tc.want || buf.Len() != 0 {
					t.Fatalf("Token() = %q, %v (log %q)", tok, err, buf.String())
				}
				return
			}
			if err == nil || err.Error() != "hub token missing: "+source {
				t.Fatalf("Token() error = %v", err)
			}
			if n := strings.Count(buf.String(), `level=ERROR msg="hub token missing: `+source+`"`); n != 1 {
				t.Errorf("logged %d times, want 1: %s", n, buf.String())
			}
		})
	}
}

func TestVMSource(t *testing.T) {
	for _, tc := range []struct {
		name, vmURL, setting, hubURL string
		wantURL, wantSource          string
		wantHub, wantErr             bool
	}{
		{name: "explicit", vmURL: "http://vm:8428", setting: "pricing.vmURL", hubURL: "http://hub:8080", wantURL: "http://vm:8428", wantSource: "pricing.vmURL"},
		{name: "explicit flag", vmURL: "http://vm:8428", wantURL: "http://vm:8428", wantSource: "--vm-url"},
		{name: "through the hub", hubURL: "http://hub:8080/", wantURL: "http://hub:8080/v1/internal/datasources/local", wantSource: "hub.config.prometheusURL", wantHub: true},
		{name: "neither", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, src, viaHub, err := vmSource(tc.vmURL, tc.setting, tc.hubURL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr {
				return
			}
			if u != tc.wantURL || !strings.HasPrefix(src, tc.wantSource) || viaHub != tc.wantHub {
				t.Errorf("vmSource = %q, %q, %v", u, src, viaHub)
			}
		})
	}
}

func TestBearerTransport(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Get("Authorization") }))
	defer srv.Close()
	c := &http.Client{Transport: bearerTransport{token: func() (string, error) { return "tok", nil }}}
	res, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
}

func strp(s string) *string { return &s }
