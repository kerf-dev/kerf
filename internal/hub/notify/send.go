package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sender posts rendered messages. It never follows redirects and never puts the destination URL
// into an error: the URL of a Slack or Teams webhook is a credential.
type Sender struct {
	// Client defaults to a client with a 15 s timeout.
	Client *http.Client
	// UserAgent defaults to "kerf-hub".
	UserAgent string
}

// Result is the outcome of one attempt.
type Result struct {
	OK         bool
	HTTPStatus int
	Latency    time.Duration
	// Err says what went wrong, without the URL ("" when OK).
	Err string
	// Retryable: network errors, 5xx, 408 and 429. Other failures (a revoked webhook answers 404,
	// a bad payload 400) don't get better by waiting.
	Retryable bool
	// RetryAfter is the destination's Retry-After on a 429 or 503 (0 = none).
	RetryAfter time.Duration
}

func (s *Sender) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Send posts req to rawURL.
func (s *Sender) Send(ctx context.Context, rawURL string, req Request) Result {
	start := time.Now()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(req.Body))
	if err != nil {
		return Result{Err: scrub(err, rawURL)}
	}
	ua := s.UserAgent
	if ua == "" {
		ua = "kerf-hub"
	}
	hr.Header.Set("User-Agent", ua)
	for k, v := range req.Headers {
		hr.Header.Set(k, v)
	}
	resp, err := s.client().Do(hr)
	if err != nil {
		return Result{Latency: time.Since(start), Err: scrub(err, rawURL), Retryable: ctx.Err() == nil}
	}
	defer resp.Body.Close()
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	r := Result{HTTPStatus: resp.StatusCode, Latency: time.Since(start)}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.OK = true
		return r
	}
	r.Err = fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		r.Err += " (redirects are not followed; check the URL)"
	}
	if body := Redact(string(excerpt), 200); body != "" && !strings.HasPrefix(strings.TrimSpace(body), "<") {
		r.Err += ": " + body
	}
	r.Retryable = resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests
	if ra := resp.Header.Get("Retry-After"); ra != "" && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
		if n, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && n > 0 {
			r.RetryAfter = min(time.Duration(n)*time.Second, 10*time.Minute)
		}
	}
	return r
}

// scrub turns a transport error into text without the request URL.
func scrub(err error, rawURL string) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	if rawURL != "" {
		msg = strings.ReplaceAll(msg, rawURL, "[url]")
		if u, perr := url.Parse(rawURL); perr == nil {
			if p := u.EscapedPath(); len(p) > 1 {
				msg = strings.ReplaceAll(msg, p, "/[path]")
			}
			if u.RawQuery != "" {
				msg = strings.ReplaceAll(msg, u.RawQuery, "[query]")
			}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "timed out"
	}
	return Redact(msg, 300)
}

// ValidateURL checks a channel URL: absolute, https (http only when allowHTTP), a host, no
// credentials in it and no fragment.
func ValidateURL(raw string, allowHTTP bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return errors.New("url must be an absolute https URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowHTTP:
	case u.Scheme == "http":
		return errors.New("url must use https")
	default:
		return errors.New("url must be an absolute https URL")
	}
	if u.User != nil {
		return errors.New("url must not contain credentials")
	}
	if u.Fragment != "" {
		return errors.New("url must not have a fragment")
	}
	return nil
}

// MaskURL returns the URL reduced to scheme and host: the path and query hold the secret.
func MaskURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "…"
	}
	return u.Scheme + "://" + u.Host + "/…"
}
