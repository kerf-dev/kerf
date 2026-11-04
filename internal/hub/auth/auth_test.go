package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestValidatePassword(t *testing.T) {
	for _, tc := range []struct {
		pw string
		ok bool
	}{
		{"short", false},
		{"1234567", false},
		{"12345678", true},
		{"äöüäöüäö", true}, // 8 runes, 16 bytes
		{strings.Repeat("a", 72), true},
		{strings.Repeat("a", 73), false},
	} {
		if err := ValidatePassword(tc.pw); (err == nil) != tc.ok {
			t.Errorf("ValidatePassword(%q) = %v, want ok=%v", tc.pw, err, tc.ok)
		}
	}
}

func TestHashAndCheck(t *testing.T) {
	h, err := HashPassword("correct horse", 4) // raised to DefaultCost
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := bcrypt.Cost([]byte(h)); c != DefaultCost {
		t.Errorf("cost %d, want %d", c, DefaultCost)
	}
	if !CheckPassword(h, "correct horse") || CheckPassword(h, "wrong horse") || CheckPassword("garbage", "x") {
		t.Error("CheckPassword")
	}
	if _, err := HashPassword("short", DefaultCost); err == nil {
		t.Error("weak password hashed")
	}
	BurnPasswordCheck("anything") // no panic, same work
}

func TestTokens(t *testing.T) {
	a, err := RandomToken("ro-", 32)
	if err != nil || !strings.HasPrefix(a, "ro-") || len(a) != 3+43 {
		t.Fatalf("RandomToken = %q, %v", a, err)
	}
	b, _ := RandomToken("ro-", 32)
	if a == b {
		t.Error("tokens repeat")
	}
	p, err := TemporaryPassword()
	if err != nil || ValidatePassword(p) != nil {
		t.Errorf("TemporaryPassword = %q, %v", p, err)
	}
	if len(SHA256("x")) != 32 {
		t.Error("SHA256 length")
	}
	if !TokenEqual("abc", "abc") || TokenEqual("abc", "abd") || TokenEqual("", "") {
		t.Error("TokenEqual")
	}
}

func TestCookies(t *testing.T) {
	exp := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)
	c := SessionCookie("v", exp, true)
	if c.Name != "ro_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || !c.Expires.Equal(exp) {
		t.Errorf("SessionCookie = %+v", c)
	}
	s := c.String()
	for _, want := range []string{"HttpOnly", "Secure", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(s, want) {
			t.Errorf("cookie %q lacks %s", s, want)
		}
	}
	cl := ClearCookie(false)
	if cl.MaxAge >= 0 || cl.Secure || cl.Value != "" {
		t.Errorf("ClearCookie = %+v", cl)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	if got := ClientIP(r); got != "10.0.0.1" {
		t.Errorf("remote: %q", got)
	}
	r.Header.Set("X-Real-IP", "10.0.0.2")
	if got := ClientIP(r); got != "10.0.0.2" {
		t.Errorf("x-real-ip: %q", got)
	}
	r.Header.Set("X-Forwarded-For", " 192.168.1.5 , 10.0.0.3")
	if got := ClientIP(r); got != "192.168.1.5" {
		t.Errorf("xff: %q", got)
	}
	r = httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "pipe"
	if got := ClientIP(r); got != "pipe" {
		t.Errorf("no port: %q", got)
	}
}

func TestLimiter(t *testing.T) {
	l := &Limiter{Max: 3, Window: time.Minute}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k", t0.Add(time.Duration(i)*time.Second)); !ok {
			t.Fatalf("hit %d denied", i)
		}
	}
	ok, retry := l.Allow("k", t0.Add(10*time.Second))
	if ok || retry != 50*time.Second {
		t.Errorf("4th: ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allow("other", t0); !ok {
		t.Error("keys are independent")
	}
	if ok, _ := l.Allow("k", t0.Add(61*time.Second)); !ok {
		t.Error("window slides")
	}
	l.Reset("k")
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k", t0.Add(62*time.Second)); !ok {
			t.Error("after reset")
		}
	}
	// gc drops idle keys.
	for i := 0; i < 1100; i++ {
		l.Allow("burst", t0.Add(time.Hour))
	}
	l.mu.Lock()
	_, kept := l.hits["other"]
	l.mu.Unlock()
	if kept {
		t.Error("idle key not collected")
	}
	if ok, _ := (&Limiter{}).Allow("x", t0); !ok {
		t.Error("zero limiter must allow")
	}
}

func TestLockout(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := Lockout{MaxFailures: 5, Duration: 15 * time.Minute}
	if l.LockedUntil(4, now) != nil {
		t.Error("locked too early")
	}
	if u := l.LockedUntil(5, now); u == nil || !u.Equal(now.Add(15*time.Minute)) {
		t.Errorf("LockedUntil(5) = %v", u)
	}
	if (Lockout{}).LockedUntil(100, now) != nil {
		t.Error("disabled lockout locked")
	}
	if RetryAfterSeconds(1500*time.Millisecond) != "2" || RetryAfterSeconds(0) != "1" {
		t.Error("RetryAfterSeconds")
	}
}
