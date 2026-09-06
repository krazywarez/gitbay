package control

import (
	"sync"
	"time"
)

// writeLimiter bounds mutating commands per account. It lives in the
// dispatcher rather than in a surface because every surface reaches the
// same registry: SSH, the JSON API and the web draw on one budget, so a
// caller cannot get a fresh allowance by changing how it connects (#148).
//
// The API's own limiter stays in front of it, keyed by token or IP: that
// one bounds a network source, this one bounds an account.
//
// A command is one token whatever it writes. `account import-bundle`
// replays a whole bundle in a single dispatch, so bulk work costs one
// write and only a loop of separate commands spends the budget.
type writeLimiter struct {
	mu      sync.Mutex
	buckets map[int64]*writeBucket
}

type writeBucket struct {
	tokens float64
	last   time.Time
}

var writes = &writeLimiter{buckets: map[int64]*writeBucket{}}

// allow reports whether this account may write now, and how long until it
// can if not. perMinute is both the sustained rate and the burst, so an
// idle account gets a full minute's worth at once.
func (l *writeLimiter) allow(user int64, perMinute int) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()

	// Bounded by the number of accounts that wrote in the last ten
	// minutes; a busy instance never grows this without also using it.
	if len(l.buckets) > 4096 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}

	burst := float64(perMinute)
	rate := burst / 60
	b := l.buckets[user]
	if b == nil {
		b = &writeBucket{tokens: burst, last: now}
		l.buckets[user] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	if b.tokens += elapsed * rate; b.tokens > burst {
		b.tokens = burst
	}
	if b.tokens < 1 {
		return false, time.Duration((1-b.tokens)/rate*float64(time.Second)) + time.Second
	}
	b.tokens--
	return true, 0
}
