package hubclient

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// MissingTokenError is returned by a RequiredToken source when the token file is missing or empty.
type MissingTokenError struct {
	// Source names where the token should come from, e.g. "Secret kerf/kerf-agent-token key token".
	Source string
}

func (e *MissingTokenError) Error() string { return "hub token missing: " + e.Source }

// RequiredToken is a token that must be present. Its Token method returns a *MissingTokenError
// (and logs it at error level, at most once per logEvery) while src yields an empty token; Check
// is a readiness check with the same error, so the pod is not Ready without its token.
type RequiredToken struct {
	src      TokenSource
	source   string
	log      *slog.Logger
	logEvery time.Duration
	now      func() time.Time

	mu      sync.Mutex
	lastLog time.Time
}

// NewRequiredToken wraps src. source names where the token comes from (empty: "the token file").
func NewRequiredToken(src TokenSource, source string, log *slog.Logger) *RequiredToken {
	if source == "" {
		source = "the token file"
	}
	if log == nil {
		log = slog.Default()
	}
	return &RequiredToken{src: src, source: source, log: log, logEvery: 10 * time.Second, now: time.Now}
}

// Token implements TokenSource.
func (r *RequiredToken) Token() (string, error) {
	tok, err := r.src()
	if err == nil && tok == "" {
		err = &MissingTokenError{Source: r.source}
	}
	if err != nil {
		r.logOnce(err)
		return "", err
	}
	return tok, nil
}

func (r *RequiredToken) logOnce(err error) {
	r.mu.Lock()
	now := r.now()
	due := r.lastLog.IsZero() || now.Sub(r.lastLog) >= r.logEvery
	if due {
		r.lastLog = now
	}
	r.mu.Unlock()
	if !due {
		return
	}
	var missing *MissingTokenError
	if errors.As(err, &missing) {
		r.log.Error(err.Error())
		return
	}
	r.log.Error("hub token unreadable", "source", r.source, "err", err)
}

// Check is a readiness check (controller-runtime healthz.Checker): an error while the token is missing.
func (r *RequiredToken) Check(_ *http.Request) error {
	_, err := r.Token()
	return err
}
