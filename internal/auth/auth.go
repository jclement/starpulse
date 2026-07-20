// Package auth implements admin password checking (plaintext or bcrypt)
// and signed web session tokens.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// CheckPassword verifies a presented password against the configured value,
// which is either a bcrypt hash (starts with "$2") or plaintext.
func CheckPassword(configured, presented string) bool {
	if configured == "" || presented == "" {
		return false
	}
	if strings.HasPrefix(configured, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(configured), []byte(presented)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(presented)) == 1
}

// Hash bcrypt-hashes a password for use in config files.
func Hash(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// Sessions issues and validates HMAC-signed expiring tokens.
type Sessions struct {
	secret []byte
}

// NewSessions creates a session signer from a hex secret, generating a new
// secret when the provided one is empty. The (possibly new) secret is
// returned so the caller can persist it.
func NewSessions(hexSecret string) (*Sessions, string) {
	if raw, err := hex.DecodeString(hexSecret); err == nil && len(raw) >= 32 {
		return &Sessions{secret: raw}, hexSecret
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	return &Sessions{secret: raw}, hex.EncodeToString(raw)
}

// Token returns a signed token valid for ttl.
func (s *Sessions) Token(ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := strconv.FormatInt(exp, 10)
	return payload + "." + s.sign(payload)
}

// Valid reports whether a token is authentic and unexpired.
func (s *Sessions) Valid(token string) bool {
	payload, mac, found := strings.Cut(token, ".")
	if !found {
		return false
	}
	if !hmac.Equal([]byte(s.sign(payload)), []byte(mac)) {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func (s *Sessions) sign(payload string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// RandomPassword returns a URL-safe random password.
func RandomPassword() string {
	raw := make([]byte, 18)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Fingerprint formats a SHA-256 digest as lowercase hex.
func Fingerprint(sum [32]byte) string {
	return fmt.Sprintf("%x", sum[:])
}
