package sshd

import (
	"net"
	"sync"
	"time"
)

// rateLimiter throttles per-IP authentication FAILURES: successful auths
// never count (a busy CLI makes many connections per minute) and clear the
// IP's slate. limits.ssh_auth_rate failures per window lock the IP out
// until the window passes.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	seen   map[string]*ipWindow
}

type ipWindow struct {
	start   time.Time
	count   int
	audited bool
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, seen: map[string]*ipWindow{}}
}

// allow reports whether ip may attempt authentication at all.
func (r *rateLimiter) allow(ip string) bool {
	if r.limit <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if len(r.seen) > 4096 {
		for k, w := range r.seen {
			if now.Sub(w.start) > r.window {
				delete(r.seen, k)
			}
		}
	}
	w := r.seen[ip]
	if w == nil || now.Sub(w.start) > r.window {
		delete(r.seen, ip)
		return true
	}
	return w.count < r.limit
}

// fail records an authentication failure for ip.
func (r *rateLimiter) fail(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	w := r.seen[ip]
	if w == nil || now.Sub(w.start) > r.window {
		r.seen[ip] = &ipWindow{start: now, count: 1}
		return
	}
	w.count++
}

// success clears the IP's failure slate.
func (r *rateLimiter) success(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.seen, ip)
}

// firstThrottle reports true exactly once per throttled window, so the
// audit log records a burst rather than every rejected attempt.
func (r *rateLimiter) firstThrottle(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.seen[ip]
	if w == nil || w.audited {
		return false
	}
	w.audited = true
	return true
}

func remoteIP(addr net.Addr) string {
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		return host
	}
	return addr.String()
}
