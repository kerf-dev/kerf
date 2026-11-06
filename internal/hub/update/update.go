// Package update finds the latest Kerf release for the hub's update check. It reads the
// public GitHub releases API of kerf-dev/kerf with a plain unauthenticated GET; nothing about the
// installation is sent besides a generic User-Agent kerf-hub/<version>.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/version"
)

// DefaultURL is the releases API of the public repository.
const DefaultURL = "https://api.github.com/repos/kerf-dev/kerf/releases"

// maxBody bounds the releases response.
const maxBody = 4 << 20

// Release is one GitHub release (the fields the check uses).
type Release struct {
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"`
}

// Version returns the release's version without a leading v ("" when the tag is not semver).
func (r Release) Version() string {
	v := strings.TrimPrefix(r.TagName, "v")
	if _, err := version.ParseSemantic(v); err != nil {
		return ""
	}
	return v
}

// IsRelease reports whether v (a leading v allowed) is a semver release version, e.g. 0.2.0 or
// 0.3.0-rc.1; dev builds (dev, dev-abc123, main-<sha>) are not.
func IsRelease(v string) bool {
	_, err := version.ParseSemantic(strings.TrimPrefix(v, "v"))
	return err == nil
}

// Compare compares two release versions (-1, 0, 1); ok is false when either is not semver.
func Compare(a, b string) (int, bool) {
	va, err := version.ParseSemantic(strings.TrimPrefix(a, "v"))
	if err != nil {
		return 0, false
	}
	vb, err := version.ParseSemantic(strings.TrimPrefix(b, "v"))
	if err != nil {
		return 0, false
	}
	switch {
	case va.LessThan(vb):
		return -1, true
	case vb.LessThan(va):
		return 1, true
	}
	return 0, true
}

// Select returns the latest release for a hub running current: drafts and non-semver tags are
// skipped; a pre-release (a -suffix tag or GitHub's prerelease flag) counts only when current is
// itself a pre-release of the same major.minor. ok is false when none qualifies.
func Select(releases []Release, current string) (Release, bool) {
	cur, _ := version.ParseSemantic(strings.TrimPrefix(current, "v"))
	var best Release
	var bestV *version.Version
	for _, r := range releases {
		if r.Draft {
			continue
		}
		v, err := version.ParseSemantic(strings.TrimPrefix(r.TagName, "v"))
		if err != nil {
			continue
		}
		if v.PreRelease() != "" || r.Prerelease {
			if cur == nil || cur.PreRelease() == "" || cur.Major() != v.Major() || cur.Minor() != v.Minor() {
				continue
			}
		}
		if bestV == nil || bestV.LessThan(v) {
			best, bestV = r, v
		}
	}
	return best, bestV != nil
}

// Client fetches releases.
type Client struct {
	// URL is the releases endpoint (DefaultURL).
	URL string
	// UserAgent is sent as is (kerf-hub/<version>).
	UserAgent string
	// HTTP is the client; nil = a client with a 20 s timeout and the proxy from the environment
	// (HTTPS_PROXY, NO_PROXY).
	HTTP *http.Client
	// Now is the clock (tests).
	Now func() time.Time
}

// Fetched is the result of a fetch.
type Fetched struct {
	// NotModified: the server answered 304 to the ETag; Releases is empty.
	NotModified bool
	Releases    []Release
	ETag        string
}

// Fetch lists the releases (one page of 50, newest first). etag is the ETag of the last answer
// ("" = none): GitHub answers 304 without counting the request against the rate limit.
func (c *Client) Fetch(ctx context.Context, etag string) (Fetched, error) {
	u := c.URL
	if u == "" {
		u = DefaultURL
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+sep+"per_page=50", nil)
	if err != nil {
		return Fetched{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	hc := c.HTTP
	if hc == nil {
		hc = DefaultHTTPClient()
	}
	res, err := hc.Do(req)
	if err != nil {
		return Fetched{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return Fetched{NotModified: true, ETag: etag}, nil
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return Fetched{}, fmt.Errorf("read releases: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return Fetched{}, c.statusError(res, body)
	}
	if len(body) > maxBody {
		return Fetched{}, errors.New("releases response larger than 4 MiB")
	}
	var rs []Release
	if err := json.Unmarshal(body, &rs); err != nil {
		return Fetched{}, fmt.Errorf("releases response is not a JSON list of releases: %w", err)
	}
	return Fetched{Releases: rs, ETag: res.Header.Get("ETag")}, nil
}

func (c *Client) statusError(res *http.Response, body []byte) error {
	if (res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests) &&
		(res.Header.Get("X-RateLimit-Remaining") == "0" || res.StatusCode == http.StatusTooManyRequests) {
		msg := "GitHub API rate limit exceeded (60 unauthenticated requests per hour per address)"
		if reset, err := strconv.ParseInt(res.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
			msg += "; it resets at " + time.Unix(reset, 0).UTC().Format(time.RFC3339)
		} else if ra := res.Header.Get("Retry-After"); ra != "" {
			msg += "; retry after " + ra + " s"
		}
		return errors.New(msg)
	}
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 200 {
		snippet = snippet[:200] + "…"
	}
	if snippet == "" {
		return fmt.Errorf("releases API: HTTP %d", res.StatusCode)
	}
	return fmt.Errorf("releases API: HTTP %d: %s", res.StatusCode, snippet)
}

// DefaultHTTPClient is the check's HTTP client: 20 s timeout, proxy from the environment.
func DefaultHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyFromEnvironment
	return &http.Client{Timeout: 20 * time.Second, Transport: t}
}
