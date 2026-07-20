package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// authThrottle is a small per-IP failed-auth limiter. It exists to blunt
// online password/bearer guessing against /login, /api and /mcp — the admin
// password is high-entropy, this just makes brute force impractical without
// relying on an external fail2ban watching these ports.
type authThrottle struct {
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

func newAuthThrottle() *authThrottle {
	return &authThrottle{
		fails:  map[string]*failState{},
		max:    10,
		window: 5 * time.Minute,
	}
}

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// blocked reports whether this IP is currently locked out. now is passed in
// for testability.
func (t *authThrottle) blocked(ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.fails[ip]
	return st != nil && !st.until.IsZero() && now.Before(st.until)
}

// fail records a failed auth for ip and returns true if it is now locked.
func (t *authThrottle) fail(ip string, now time.Time) bool {
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

// ok clears any failure record for ip after a successful auth.
func (t *authThrottle) succeed(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, ip)
}

// gc drops stale entries (caller holds the lock).
func (t *authThrottle) gc(now time.Time) {
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
