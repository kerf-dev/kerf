// Package auth holds the building blocks of the hub's local users and browser sessions (D-023):
// bcrypt password hashing, random session/CSRF/API tokens, login rate limiting and lockout, and
// the session cookie. It has no database or HTTP routing code; internal/hub wires it.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Password rules (Password schema): at least 8 characters, at most 72 bytes (bcrypt's limit).
const (
	MinPasswordLen   = 8
	MaxPasswordBytes = 72
	// DefaultCost is the bcrypt cost of new hashes (at least 12).
	DefaultCost = 12
)

// ErrWeakPassword is returned for passwords that break the rules.
var ErrWeakPassword = errors.New("password must have at least 8 characters and at most 72 bytes")

// ValidatePassword checks the password rules.
func ValidatePassword(pw string) error {
	if len([]rune(pw)) < MinPasswordLen || len(pw) > MaxPasswordBytes {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns the bcrypt hash of pw with the given cost (DefaultCost when < 12, so a
// misconfiguration never weakens it).
func HashPassword(pw string, cost int) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	if cost < DefaultCost {
		cost = DefaultCost
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), cost)
	return string(b), err
}

// CheckPassword reports whether pw matches the bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// BurnPasswordCheck spends the time of one password check against a fixed hash, so a login for
// an unknown user takes as long as one with a wrong password.
func BurnPasswordCheck(pw string) {
	dummyOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), DefaultCost)
	})
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
}

// RandomToken returns prefix + n random bytes in base64url (no padding).
func RandomToken(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// TemporaryPassword returns a random password for resets (20 characters, base64url).
func TemporaryPassword() (string, error) {
	return RandomToken("", 15)
}

// SHA256 returns the SHA-256 of s (how session and API tokens are stored).
func SHA256(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// TokenEqual compares two secrets in constant time; empty never matches.
func TokenEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---- session cookie ----

// CookieName is the session cookie (hubapi.SessionCookie).
const CookieName = "kerf_session"

// SessionCookie returns the session cookie: HttpOnly, SameSite=Strict, Path=/, Secure unless the
// hub serves plain HTTP in development.
func SessionCookie(value string, expires time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name: CookieName, Value: value, Path: "/", Expires: expires.UTC(),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}
}

// ClearCookie returns a cookie that removes the session cookie.
func ClearCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0).UTC(),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}
}

// ClientIP returns the caller's address for rate limiting: the last X-Forwarded-For entry or else
// the connection's remote host. The last entry is the one the proxy right in front of the hub added
// (the web nginx overwrites the header with the client it saw, an ALB appends it); earlier entries
// come from the client and are not trusted.
func ClientIP(r *http.Request) string {
	if xffs := r.Header.Values("X-Forwarded-For"); len(xffs) > 0 {
		parts := strings.Split(xffs[len(xffs)-1], ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- rate limiting and lockout ----

// Limiter allows at most Max events per key within Window (sliding window, in memory).
type Limiter struct {
	Max    int
	Window time.Duration

	mu   sync.Mutex
	hits map[string][]time.Time
	ops  int
}

// Allow records an event for key at now if allowed. Denied events are not recorded; retryAfter
// is when the oldest event in the window expires.
func (l *Limiter) Allow(key string, now time.Time) (ok bool, retryAfter time.Duration) {
	if l.Max <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	l.ops++
	if l.ops%1024 == 0 {
		l.gc(now)
	}
	h := prune(l.hits[key], now.Add(-l.Window))
	if len(h) >= l.Max {
		l.hits[key] = h
		return false, h[0].Add(l.Window).Sub(now)
	}
	l.hits[key] = append(h, now)
	return true, 0
}

// Reset forgets key (e.g. after a successful login).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

func (l *Limiter) gc(now time.Time) {
	for k, h := range l.hits {
		if h = prune(h, now.Add(-l.Window)); len(h) == 0 {
			delete(l.hits, k)
		} else {
			l.hits[k] = h
		}
	}
}

func prune(h []time.Time, after time.Time) []time.Time {
	i := 0
	for i < len(h) && !h[i].After(after) {
		i++
	}
	return h[i:]
}

// Lockout locks an account after MaxFailures consecutive failed logins, for Duration.
type Lockout struct {
	MaxFailures int
	Duration    time.Duration
}

// LockedUntil returns when an account with failures consecutive failures is locked until, or nil.
func (l Lockout) LockedUntil(failures int, now time.Time) *time.Time {
	if l.MaxFailures <= 0 || failures < l.MaxFailures {
		return nil
	}
	t := now.Add(l.Duration)
	return &t
}

// RetryAfterSeconds formats a Retry-After value (whole seconds, at least 1).
func RetryAfterSeconds(d time.Duration) string {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return fmt.Sprint(s)
}
