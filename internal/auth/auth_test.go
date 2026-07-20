package auth

import (
	"testing"
	"time"
)

func TestCheckPasswordPlaintext(t *testing.T) {
	if !CheckPassword("hunter2", "hunter2") {
		t.Error("matching plaintext rejected")
	}
	if CheckPassword("hunter2", "hunter3") {
		t.Error("wrong plaintext accepted")
	}
	if CheckPassword("", "") || CheckPassword("", "x") || CheckPassword("x", "") {
		t.Error("empty credentials accepted")
	}
}

func TestCheckPasswordBcrypt(t *testing.T) {
	h, err := Hash("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "s3cret") {
		t.Error("matching bcrypt rejected")
	}
	if CheckPassword(h, "wrong") {
		t.Error("wrong bcrypt accepted")
	}
}

func TestSessions(t *testing.T) {
	s, secret := NewSessions("")
	if secret == "" {
		t.Fatal("no secret generated")
	}
	tok := s.Token(time.Hour)
	if !s.Valid(tok) {
		t.Error("fresh token invalid")
	}
	if s.Valid(tok + "x") {
		t.Error("tampered token valid")
	}
	if s.Valid("") || s.Valid("junk") || s.Valid("123.456") {
		t.Error("junk token valid")
	}
	if s.Valid(s.Token(-time.Minute)) {
		t.Error("expired token valid")
	}

	// same secret verifies across restarts; different secret does not
	s2, _ := NewSessions(secret)
	if !s2.Valid(tok) {
		t.Error("token invalid after restart with same secret")
	}
	s3, _ := NewSessions("")
	if s3.Valid(tok) {
		t.Error("token valid under different secret")
	}
}
