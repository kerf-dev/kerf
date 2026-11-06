package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func rel(tag string, flags ...string) Release {
	r := Release{TagName: tag, HTMLURL: "https://github.com/kerf-dev/kerf/releases/tag/" + tag}
	for _, f := range flags {
		switch f {
		case "draft":
			r.Draft = true
		case "pre":
			r.Prerelease = true
		}
	}
	return r
}

func TestSelect(t *testing.T) {
	all := []Release{
		rel("v0.1.0"), rel("v0.2.0"), rel("v0.2.1"), rel("v0.3.0-rc.1"), rel("v0.3.0-rc.2"),
		rel("v0.4.0", "draft"), rel("nightly"), rel("v0.2.2", "pre"), rel("v1.0.0-beta.1"),
	}
	tests := []struct {
		name     string
		releases []Release
		current  string
		want     string // "" = none
	}{
		{"stable picks the highest stable", all, "0.1.0", "v0.2.1"},
		{"a leading v on current", all, "v0.2.1", "v0.2.1"},
		{"dev build: stable only", all, "dev-abc123", "v0.2.1"},
		{"pre-release of the same minor sees newer pre-releases", all, "0.3.0-rc.1", "v0.3.0-rc.2"},
		{"pre-release of another minor: stable only", all, "0.4.0-alpha.1", "v0.2.1"},
		{"pre-release of 1.0 sees 1.0 betas", all, "1.0.0-alpha.1", "v1.0.0-beta.1"},
		{"GitHub prerelease flag counts as pre-release", []Release{rel("v0.2.1"), rel("v0.2.2", "pre")}, "0.2.1", "v0.2.1"},
		{"flagged pre-release allowed for a pre-release of its minor", []Release{rel("v0.2.1"), rel("v0.2.2", "pre")}, "0.2.2-rc.1", "v0.2.2"},
		{"drafts never count", []Release{rel("v9.0.0", "draft"), rel("v0.1.0")}, "0.1.0", "v0.1.0"},
		{"stable release wins over its own rc", []Release{rel("v0.3.0-rc.2"), rel("v0.3.0")}, "0.3.0-rc.1", "v0.3.0"},
		{"no releases", nil, "0.1.0", ""},
		{"only pre-releases for a stable hub", []Release{rel("v0.1.0-rc.1")}, "0.1.0", ""},
		{"tags without v", []Release{rel("0.5.0"), rel("v0.4.0")}, "0.1.0", "0.5.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Select(tt.releases, tt.current)
			if tt.want == "" {
				if ok {
					t.Fatalf("want none, got %s", got.TagName)
				}
				return
			}
			if !ok || got.TagName != tt.want {
				t.Fatalf("got %q (ok %v), want %q", got.TagName, ok, tt.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.1.0", "0.2.0", -1, true},
		{"v0.2.0", "0.2.0", 0, true},
		{"0.3.0", "0.3.0-rc.1", 1, true},
		{"dev", "0.1.0", 0, false},
		{"0.1.0", "", 0, false},
	}
	for _, tt := range tests {
		got, ok := Compare(tt.a, tt.b)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d, %v", tt.a, tt.b, got, ok, tt.want, tt.ok)
		}
	}
	if !IsRelease("v1.2.3") || !IsRelease("0.3.0-rc.1") || IsRelease("dev") || IsRelease("dev-abc") {
		t.Error("IsRelease")
	}
}

func TestFetch(t *testing.T) {
	const body = `[{"tag_name":"v0.2.0","name":"Kerf 0.2.0","html_url":"https://github.com/kerf-dev/kerf/releases/tag/v0.2.0","draft":false,"prerelease":false,"published_at":"2026-11-03T10:00:00Z","body":"notes"}]`
	tests := []struct {
		name    string
		etag    string
		handler http.HandlerFunc
		check   func(t *testing.T, f Fetched, err error)
	}{
		{
			name: "ok",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `W/"abc"`)
				_, _ = w.Write([]byte(body))
			},
			check: func(t *testing.T, f Fetched, err error) {
				if err != nil || len(f.Releases) != 1 || f.ETag != `W/"abc"` || f.Releases[0].PublishedAt == nil || f.Releases[0].Version() != "0.2.0" {
					t.Fatalf("got %+v, %v", f, err)
				}
			},
		},
		{
			name: "not modified",
			etag: `W/"abc"`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") != `W/"abc"` {
					t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
				}
				w.WriteHeader(http.StatusNotModified)
			},
			check: func(t *testing.T, f Fetched, err error) {
				if err != nil || !f.NotModified || f.ETag != `W/"abc"` {
					t.Fatalf("got %+v, %v", f, err)
				}
			},
		},
		{
			name: "rate limited",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", "1793700000")
				http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			},
			check: func(t *testing.T, _ Fetched, err error) {
				if err == nil || !strings.Contains(err.Error(), "rate limit") || !strings.Contains(err.Error(), "resets at 2026-") {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:    "server error",
			handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusBadGateway) },
			check: func(t *testing.T, _ Fetched, err error) {
				if err == nil || err.Error() != "releases API: HTTP 502: boom" {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:    "not JSON",
			handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
			check: func(t *testing.T, _ Fetched, err error) {
				if err == nil || !strings.Contains(err.Error(), "not a JSON list") {
					t.Fatalf("err = %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Nothing about the installation: a generic User-Agent, no credentials, no cookies.
				if ua := r.Header.Get("User-Agent"); ua != "kerf-hub/0.1.0" {
					t.Errorf("User-Agent = %q", ua)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("credentials sent")
				}
				if r.URL.Query().Get("per_page") != "50" || len(r.URL.Query()) != 1 {
					t.Errorf("query = %q", r.URL.RawQuery)
				}
				tt.handler(w, r)
			}))
			defer srv.Close()
			c := &Client{URL: srv.URL + "/repos/kerf-dev/kerf/releases", UserAgent: "kerf-hub/0.1.0"}
			f, err := c.Fetch(context.Background(), tt.etag)
			tt.check(t, f, err)
		})
	}
}

func TestFetchUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := (&Client{URL: url}).Fetch(context.Background(), "")
	if err == nil {
		t.Fatal("want an error for a closed server")
	}
}
