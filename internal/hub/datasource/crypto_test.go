package datasource

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func key(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCipherRoundTrip(t *testing.T) {
	c, err := NewCipher(key(t))
	if err != nil {
		t.Fatal(err)
	}
	s := Secrets{Token: "tok-123", Password: "pw", ClientKey: "-----BEGIN KEY-----"}
	blob, err := c.Seal("ds-1", s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("tok-123")) {
		t.Fatal("ciphertext contains the plain token")
	}
	got, err := c.Open("ds-1", blob)
	if err != nil || got != s {
		t.Fatalf("Open = %+v, %v", got, err)
	}
	// bound to the id
	if _, err := c.Open("ds-2", blob); err == nil {
		t.Error("ciphertext opened for another datasource id")
	}
	// tampering
	blob[len(blob)-1] ^= 1
	if _, err := c.Open("ds-1", blob); err == nil {
		t.Error("tampered ciphertext opened")
	}
	// empty
	if b, _ := c.Seal("x", Secrets{}); b != nil {
		t.Error("empty secrets sealed")
	}
	if s, err := c.Open("x", nil); err != nil || !s.Empty() {
		t.Error("nil blob")
	}
	if _, err := c.Open("x", []byte{9, 9}); err == nil {
		t.Error("bad format opened")
	}
}

func TestCipherRotation(t *testing.T) {
	oldK, newK := key(t), key(t)
	old, _ := NewCipher(oldK)
	blob, _ := old.Seal("ds", Secrets{Token: "t"})
	rotated, err := NewCipher(newK, oldK)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := rotated.Open("ds", blob); err != nil || s.Token != "t" {
		t.Fatalf("old blob after rotation: %v", err)
	}
	if !rotated.Rotated(blob) {
		t.Error("blob of the old key not reported as rotated")
	}
	nb, _ := rotated.Seal("ds", Secrets{Token: "t"})
	if rotated.Rotated(nb) {
		t.Error("new blob reported as rotated")
	}
	onlyNew, _ := NewCipher(newK)
	if _, err := onlyNew.Open("ds", blob); !errors.Is(err, ErrNoKey) {
		t.Errorf("err = %v, want ErrNoKey", err)
	}
}

func TestParseKeyAndLoad(t *testing.T) {
	k := key(t)
	for _, s := range []string{base64.StdEncoding.EncodeToString(k), base64.RawURLEncoding.EncodeToString(k), hex.EncodeToString(k), " " + base64.StdEncoding.EncodeToString(k) + "\n"} {
		got, err := ParseKey(s)
		if err != nil || !bytes.Equal(got, k) {
			t.Errorf("ParseKey(%q) = %v", s, err)
		}
	}
	if _, err := ParseKey("short"); err == nil {
		t.Error("short key accepted")
	}
	if _, err := NewCipher(); err == nil {
		t.Error("no key accepted")
	}
	if _, err := NewCipher([]byte("x")); err == nil {
		t.Error("1-byte key accepted")
	}
	k2 := key(t)
	p := filepath.Join(t.TempDir(), "key")
	content := "# primary\n" + base64.StdEncoding.EncodeToString(k2) + "\n\n" + hex.EncodeToString(k) + "\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCipher(p)
	if err != nil || len(c.keys) != 2 {
		t.Fatalf("LoadCipher = %v", err)
	}
	if err := os.WriteFile(p, []byte("nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCipher(p); err == nil || !strings.Contains(err.Error(), "base64 or hex") {
		t.Errorf("bad key file: %v", err)
	}
	if _, err := LoadCipher(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file")
	}
}
