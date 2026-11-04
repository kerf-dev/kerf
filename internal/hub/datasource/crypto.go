package datasource

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeySize is the size of a secrets encryption key (AES-256).
const KeySize = 32

// blobVersion prefixes every ciphertext: version byte, 4-byte key id, 12-byte nonce, sealed data.
const blobVersion = 1

// Cipher encrypts datasource secrets at rest with AES-256-GCM. The first key encrypts; every key
// decrypts, so a key is rotated by putting the new one first and keeping the old one until every
// datasource was saved again. The datasource id is the associated data: a ciphertext copied to
// another row does not decrypt.
type Cipher struct {
	keys []cipherKey
}

type cipherKey struct {
	id   [4]byte
	aead cipher.AEAD
}

// ErrNoKey is returned when a ciphertext was made with a key the Cipher does not have.
var ErrNoKey = errors.New("datasource secret encrypted with an unknown key")

// NewCipher returns a Cipher; keys[0] encrypts. Each key has KeySize bytes.
func NewCipher(keys ...[]byte) (*Cipher, error) {
	if len(keys) == 0 {
		return nil, errors.New("no secrets encryption key")
	}
	c := &Cipher{}
	for i, k := range keys {
		if len(k) != KeySize {
			return nil, fmt.Errorf("secrets key %d: want %d bytes, got %d", i, KeySize, len(k))
		}
		block, err := aes.NewCipher(k)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(k)
		var id [4]byte
		copy(id[:], sum[:4])
		c.keys = append(c.keys, cipherKey{id: id, aead: aead})
	}
	return c, nil
}

// ParseKey decodes a key written as base64 (standard or URL, padded or not) or hex.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, dec := range []func(string) ([]byte, error){
		hex.DecodeString,
		base64.StdEncoding.DecodeString, base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString, base64.RawURLEncoding.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("secrets key: want %d bytes as base64 or hex (e.g. openssl rand -base64 32)", KeySize)
}

// LoadCipher reads keys from a file (a mounted Secret key): one key per line, the first encrypts.
func LoadCipher(path string) (*Cipher, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secrets key: %w", err)
	}
	var keys [][]byte
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, err := ParseKey(line)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return NewCipher(keys...)
}

// Encrypt seals plain for the datasource id.
func (c *Cipher) Encrypt(id string, plain []byte) ([]byte, error) {
	k := c.keys[0]
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{blobVersion}, k.id[:]...)
	out = append(out, nonce...)
	return k.aead.Seal(out, nonce, plain, []byte(id)), nil
}

// Decrypt opens a blob sealed for the datasource id.
func (c *Cipher) Decrypt(id string, blob []byte) ([]byte, error) {
	if len(blob) < 5 || blob[0] != blobVersion {
		return nil, errors.New("datasource secret: unknown format")
	}
	for _, k := range c.keys {
		if !bytes.Equal(k.id[:], blob[1:5]) {
			continue
		}
		ns := k.aead.NonceSize()
		if len(blob) < 5+ns {
			return nil, errors.New("datasource secret: truncated")
		}
		plain, err := k.aead.Open(nil, blob[5:5+ns], blob[5+ns:], []byte(id))
		if err != nil {
			return nil, fmt.Errorf("datasource secret: %w", err)
		}
		return plain, nil
	}
	return nil, ErrNoKey
}

// Rotated reports whether blob was sealed with a key other than the encrypting one.
func (c *Cipher) Rotated(blob []byte) bool {
	return len(blob) >= 5 && !bytes.Equal(c.keys[0].id[:], blob[1:5])
}

// Secrets is the secret material of a datasource; it is only ever stored encrypted.
type Secrets struct {
	Token     string `json:"token,omitempty"`
	Password  string `json:"password,omitempty"`
	ClientKey string `json:"clientKey,omitempty"`
}

// Empty reports whether no secret is set.
func (s Secrets) Empty() bool { return s == Secrets{} }

// Seal encrypts s for the datasource id; nil when s is empty.
func (c *Cipher) Seal(id string, s Secrets) ([]byte, error) {
	if s.Empty() {
		return nil, nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return c.Encrypt(id, b)
}

// Open decrypts the secrets of the datasource id; nil blob = no secrets.
func (c *Cipher) Open(id string, blob []byte) (Secrets, error) {
	if len(blob) == 0 {
		return Secrets{}, nil
	}
	b, err := c.Decrypt(id, blob)
	if err != nil {
		return Secrets{}, err
	}
	var s Secrets
	if err := json.Unmarshal(b, &s); err != nil {
		return Secrets{}, fmt.Errorf("datasource secret: %w", err)
	}
	return s, nil
}
