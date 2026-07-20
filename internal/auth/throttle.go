package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Throttle is a small per-source failed-auth limiter. It exists to blunt
// online password/bearer guessing against every door that takes a password —
// the web login, /api, /mcp and ssh. The admin password is high-entropy;
// this just makes brute force impractical without relying on an external
// fail2ban watching these ports.
//
// It lives here rather than in the web package because ssh needs it too, and
// a door that is throttled everywhere except one place is not throttled.
type Throttle struct {
	mu     sync.Mutex
	fails  map[string]*failState
	max    int           // failures before lockout
	window time.Duration // sliding window / lockout duration
	lastGC time.Time
}

type failState struct {
	count int
	until time.Time // lockout expiry (zero if not locked)
	seen  time.Time // last activity, for GC
}

// NewThrottle returns a limiter: max failures within window, then locked
// out for window.
func NewThrottle(max int, window time.Duration) *Throttle {
	return &Throttle{
		fails:  map[string]*failState{},
		max:    max,
		window: window,
	}
}

// ClientIP is the throttle key for an HTTP request.
func ClientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// SetMax changes the failure allowance. Tests use it to reach a lockout
// without performing the real number of attempts.
func (t *Throttle) SetMax(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.max = n
}

// Blocked reports whether this source is currently locked out. now is
// passed in for testability.
func (t *Throttle) Blocked(ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.fails[ip]
	return st != nil && !st.until.IsZero() && now.Before(st.until)
}

// Fail records a failed attempt and reports whether it triggered a lockout.
func (t *Throttle) Fail(ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gc(now)
	st := t.fails[ip]
	if st == nil {
		st = &failState{}
		t.fails[ip] = st
	}
	// reset the counter if the window since last activity has elapsed
	if !st.seen.IsZero() && now.Sub(st.seen) > t.window {
		st.count = 0
		st.until = time.Time{}
	}
	st.count++
	st.seen = now
	if st.count >= t.max {
		st.until = now.Add(t.window)
		return true
	}
	return false
}

// Succeed clears any failure record after a successful auth.
func (t *Throttle) Succeed(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, ip)
}

// gc drops stale entries (caller holds the lock).
func (t *Throttle) gc(now time.Time) {
	if now.Sub(t.lastGC) < t.window {
		return
	}
	t.lastGC = now
	for ip, st := range t.fails {
		if now.Sub(st.seen) > 2*t.window {
			delete(t.fails, ip)
		}
	}
}
