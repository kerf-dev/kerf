package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// hubToken reads the pricing token from its file on every call (a rotated Secret needs no restart).
// A missing or empty file is an error naming where the token should come from, logged at error level
// at most once per logEvery; readiness fails with the same error.
type hubToken struct {
	path     string
	source   string // e.g. "Secret kerf/hub-local-tokens key pricing"
	log      *slog.Logger
	logEvery time.Duration
	now      func() time.Time

	mu      sync.Mutex
	lastLog time.Time
}

func newHubToken(path, source string, log *slog.Logger) *hubToken {
	if source == "" {
		source = "file " + path
	}
	return &hubToken{path: path, source: source, log: log, logEvery: 10 * time.Second, now: time.Now}
}

// Token returns the token, or an error starting with "hub token missing: ".
func (h *hubToken) Token() (string, error) {
	b, err := os.ReadFile(h.path)
	tok := strings.TrimSpace(string(b))
	switch {
	case errors.Is(err, os.ErrNotExist) || (err == nil && tok == ""):
		err = fmt.Errorf("hub token missing: %s", h.source)
	case err != nil:
		err = fmt.Errorf("hub token unreadable (%s): %w", h.source, err)
	default:
		return tok, nil
	}
	h.logOnce(err)
	return "", err
}

func (h *hubToken) logOnce(err error) {
	h.mu.Lock()
	now := h.now()
	due := h.lastLog.IsZero() || now.Sub(h.lastLog) >= h.logEvery
	if due {
		h.lastLog = now
	}
	h.mu.Unlock()
	if due {
		h.log.Error(err.Error())
	}
}

// bearerTransport adds the pricing token to every request (node metrics read through the hub).
type bearerTransport struct {
	next  http.RoundTripper
	token func() (string, error)
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := b.token()
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	next := b.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(r)
}

// hubMetricsPath is the hub's query proxy for its own metrics datasource (hub.config.prometheusURL).
const hubMetricsPath = "/v1/internal/datasources/local"

// vmSource returns the node metrics URL and the setting it comes from: --vm-url when set, else the hub's
// own metrics datasource through the hub (with --hub-url).
func vmSource(vmURL, setting, hubURL string) (url, source string, viaHub bool, err error) {
	if vmURL != "" {
		if setting == "" {
			setting = "--vm-url"
		}
		return vmURL, setting, false, nil
	}
	if hubURL == "" {
		return "", "", false, errors.New("--vm-url is required without --hub-url")
	}
	return strings.TrimRight(hubURL, "/") + hubMetricsPath, "hub.config.prometheusURL, read through the hub (pricing.vmURL is empty)", true, nil
}
