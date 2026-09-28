package control

import (
	"sync"
	"time"

	"gitbay.org/gitbay/internal/store"
)

// refusalsPerMinute bounds audit rows for refused writes per actor. A
// probe is what these rows record, and a loop of probes must not grow
// the table without bound.
const refusalsPerMinute = 10

// refusalsPerMinuteGlobal bounds them across all actors. Registration is
// open and pending accounts reach Dispatch, so the per-actor bound alone
// scales with the number of accounts.
const refusalsPerMinuteGlobal = 600

type refusalLimiter struct {
	mu     sync.Mutex
	seen   map[int64]*refusalWindow
	global refusalWindow
}

type refusalWindow struct {
	start time.Time
	n     int
}

var refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}

// What to write for one refusal.
const (
	refusalDrop = iota
	refusalRecord
	refusalThrottleActor
	refusalThrottleGlobal
)

// allow decides what to write for this refusal. Every row written,
// throttle rows included, counts against the global bound.
func (l *refusalLimiter) allow(actor int64, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) > 4096 {
		for k, w := range l.seen {
			if now.Sub(w.start) >= time.Minute {
				delete(l.seen, k)
			}
		}
	}
	w := l.seen[actor]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &refusalWindow{start: now}
		l.seen[actor] = w
	}
	w.n++
	want := refusalRecord
	switch {
	case w.n == refusalsPerMinute+1:
		want = refusalThrottleActor
	case w.n > refusalsPerMinute:
		return refusalDrop
	}
	if now.Sub(l.global.start) >= time.Minute {
		l.global = refusalWindow{start: now}
	}
	l.global.n++
	switch {
	case l.global.n <= refusalsPerMinuteGlobal:
		return want
	case l.global.n == refusalsPerMinuteGlobal+1:
		return refusalThrottleGlobal
	}
	return refusalDrop
}

// AuditRefused records a refused attempt to change something. Past the
// per-actor or the global limit it records one refused.throttled row a
// minute for that scope and drops the rest. Dispatcher tests run without
// a store.
func AuditRefused(st *store.Store, actorID int64, action string, data map[string]any) {
	if st == nil {
		return
	}
	switch refusals.allow(actorID, time.Now()) {
	case refusalRecord:
		st.Audit(actorID, action, data)
	case refusalThrottleActor:
		st.Audit(actorID, "refused.throttled", map[string]any{"scope": "actor", "limit_per_minute": refusalsPerMinute})
	case refusalThrottleGlobal:
		st.Audit(actorID, "refused.throttled", map[string]any{"scope": "global", "limit_per_minute": refusalsPerMinuteGlobal})
	}
}
