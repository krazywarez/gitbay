package httpd

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// apiLimiter is a token bucket per caller, with a separate, smaller budget
// for writes. Unlike the SSH limiter — which counts only auth failures,
// because a busy CLI opens many connections legitimately — this counts
// every request: one HTTP call is one command, and a client looping over a
// list can issue them far faster than a person can type.
//
// Keyed by token hash when authenticated, so one caller's budget follows
// them across networks, and by IP otherwise, so an unauthenticated flood
// cannot mint budget by rotating tokens.
type apiLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // requests per second, sustained
	burst   float64
	writes  float64 // sustained write rate, a fraction of rate
}

type bucket struct {
	read, write float64
	last        time.Time
}

func newAPILimiter(perMinute int) *apiLimiter {
	if perMinute <= 0 {
		perMinute = 120
	}
	rate := float64(perMinute) / 60
	return &apiLimiter{
		buckets: map[string]*bucket{},
		rate:    rate,
		burst:   float64(perMinute),
		// Writes are rarer and more expensive; a tenth of the read budget
		// is generous for a client and useless for a scraper.
		writes: rate / 10,
	}
}

// allow reports whether the caller may make this request, and how long to
// wait if not. write requests draw on both buckets: a write is also a
// request.
func (l *apiLimiter) allow(key string, write bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()

	if len(l.buckets) > 4096 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}

	b := l.buckets[key]
	if b == nil {
		b = &bucket{read: l.burst, write: l.burst / 10, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.read = minf(l.burst, b.read+elapsed*l.rate)
	b.write = minf(l.burst/10, b.write+elapsed*l.writes)

	if b.read < 1 {
		return false, retryAfter(1-b.read, l.rate)
	}
	if write && b.write < 1 {
		return false, retryAfter(1-b.write, l.writes)
	}
	b.read--
	if write {
		b.write--
	}
	return true, 0
}

func retryAfter(deficit, rate float64) time.Duration {
	if rate <= 0 {
		return time.Minute
	}
	d := time.Duration(deficit / rate * float64(time.Second))
	if d < time.Second {
		return time.Second
	}
	return d
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// clientIP is the peer address. No forwarded headers are trusted: nothing
// in front of this process is required to set them, and honouring a
// client-supplied header would let a caller pick their own bucket.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func tooManyRequests(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds()+0.5)))
	apiError(w, http.StatusTooManyRequests,
		"rate limited; retry in "+strconv.Itoa(int(wait.Seconds()+0.5))+"s")
}
