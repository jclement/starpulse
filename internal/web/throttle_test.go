package web

import (
	"testing"
	"time"
)

func TestAuthThrottle(t *testing.T) {
	tr := newAuthThrottle()
	tr.max = 3
	tr.window = time.Minute
	now := time.Now()
	ip := "1.2.3.4"

	// below the limit: not blocked
	for i := 0; i < 2; i++ {
		if locked := tr.fail(ip, now); locked {
			t.Fatalf("locked too early at %d", i)
		}
	}
	if tr.blocked(ip, now) {
		t.Fatal("blocked before reaching max")
	}
	// hitting max locks out
	if !tr.fail(ip, now) {
		t.Fatal("not locked at max")
	}
	if !tr.blocked(ip, now) {
		t.Fatal("not blocked after lockout")
	}
	// a different IP is unaffected
	if tr.blocked("9.9.9.9", now) {
		t.Fatal("unrelated IP blocked")
	}
	// lockout expires after the window
	if tr.blocked(ip, now.Add(2*time.Minute)) {
		t.Fatal("still blocked after window")
	}
	// success clears the record
	tr.fail(ip, now)
	tr.fail(ip, now)
	tr.succeed(ip)
	if tr.fail(ip, now) {
		t.Fatal("counter not reset after success")
	}
}
