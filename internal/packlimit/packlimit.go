// Package packlimit bounds concurrent git processes. upload-pack and
// upload-archive over SSH, smart HTTP and git:// draw on one budget,
// receive-pack on a second: each a global cap, a cap per principal (an
// account, a deploy key, or a client address on the anonymous
// transports), and a bounded queue whose waiters give up after a fixed
// wait or when the client goes away.
// Waiters are not served in order; a new arrival can take a freed slot
// ahead of them, and the wait bounds how long any one of them waits.
package packlimit

import (
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"
)

var (
	ErrBusy = errors.New("the server is busy generating packs for other clients; try again in a minute")
	ErrGone = errors.New("client went away while queued")
)

type Limiter struct {
	max, per, queue int
	perQueue        int // waiting, per principal; per unless set
	wait            time.Duration
	name            string // what is limited, for the refusal log

	// Principals starting with class may hold at most classCap slots
	// between them; classCap 0 is no class cap.
	class    string
	classCap int

	mu        sync.Mutex
	running   int
	classHeld int
	queued    int
	held      map[string]int       // running, per principal
	waiting   map[string]int       // queued, per principal
	changed   chan struct{}        // closed and replaced on every release
	warned    map[string]time.Time // last refusal logged, per transport
}

// New returns a limiter, or nil — no limit — when max is not positive.
func New(max, per, queue int, wait time.Duration) *Limiter {
	if max <= 0 {
		return nil
	}
	return &Limiter{max: max, per: per, perQueue: per, queue: queue, wait: wait, name: "pack",
		held: map[string]int{}, waiting: map[string]int{}, changed: make(chan struct{}),
		warned: map[string]time.Time{}}
}

// Refused logs that a request on transport was turned away with err, at
// most once a minute per transport. It names the principal's class
// (user or ip), never the principal: an address is personal data.
func (l *Limiter) Refused(transport, principal string, err error) {
	if l == nil {
		return
	}
	now := time.Now()
	l.mu.Lock()
	last, seen := l.warned[transport]
	if seen && now.Sub(last) < time.Minute {
		l.mu.Unlock()
		return
	}
	l.warned[transport] = now
	l.mu.Unlock()
	class, _, _ := strings.Cut(principal, ":")
	reason := "busy"
	if errors.Is(err, ErrGone) {
		reason = "gone"
	}
	slog.Warn(l.name+" limit: request turned away (logged at most once a minute per transport)",
		"transport", transport, "class", class, "reason", reason)
}

// Name sets what the refusal log calls this limit ("pack" unless set).
// Call it before the limiter is in use.
func (l *Limiter) Name(name string) {
	if l == nil {
		return
	}
	l.name = name
}

// CapQueue lets one principal have up to n requests waiting, where by
// default it may have as many as it may run. It applies only while a
// per-principal cap is set. Call it before the limiter is in use.
func (l *Limiter) CapQueue(n int) {
	if l == nil {
		return
	}
	l.perQueue = n
}

// CapClass caps the slots that principals starting with prefix may hold
// between them. Call it before the limiter is in use.
func (l *Limiter) CapClass(prefix string, n int) {
	if l == nil {
		return
	}
	l.class, l.classCap = prefix, n
}

// AddrPrincipal is the principal for an unauthenticated client at addr:
// an IPv4 address as is, an IPv6 address by its /64, since one host
// commonly holds a whole /64. An address that does not parse is used
// as given.
func AddrPrincipal(addr string) string {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return "ip:" + addr
	}
	a = a.WithZone("").Unmap()
	if a.Is4() {
		return "ip:" + a.String()
	}
	return "ip:" + netip.PrefixFrom(a, 64).Masked().String()
}

// Acquire takes a slot for principal, queueing when none is free.
// done, when it closes, ends the wait. Once Acquire returns a nil
// error, the caller holds the slot and must call release — once git
// has exited — regardless of what its own context has done since:
// done closing after that point does not release the slot on the
// caller's behalf.
func (l *Limiter) Acquire(done <-chan struct{}, principal string) (release func(), err error) {
	if l == nil {
		return func() {}, nil
	}
	l.mu.Lock()
	if l.fits(principal) {
		l.take(principal)
		l.mu.Unlock()
		return l.releaser(principal), nil
	}
	if l.queued >= l.queue || (l.per > 0 && l.waiting[principal] >= l.perQueue) {
		l.mu.Unlock()
		return nil, ErrBusy
	}
	l.queued++
	l.waiting[principal]++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.queued--
		if l.waiting[principal]--; l.waiting[principal] == 0 {
			delete(l.waiting, principal)
		}
		l.mu.Unlock()
	}()

	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	for {
		l.mu.Lock()
		// changed and done can both be ready at once — a slot can
		// free up at the same moment the caller gives up. select
		// among the wake sources would then pick between them at
		// random, so re-check done first, under the lock, on every
		// pass: this makes the limiter prefer ErrGone whenever both
		// are ready, instead of leaving it to chance which one a
		// given pass observes.
		select {
		case <-done:
			l.mu.Unlock()
			return nil, ErrGone
		default:
		}
		if l.fits(principal) {
			l.take(principal)
			l.mu.Unlock()
			return l.releaser(principal), nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return nil, ErrBusy
		case <-done:
			return nil, ErrGone
		}
	}
}

func (l *Limiter) fits(principal string) bool {
	if l.inClass(principal) && l.classHeld >= l.classCap {
		return false
	}
	return l.running < l.max && (l.per <= 0 || l.held[principal] < l.per)
}

func (l *Limiter) inClass(principal string) bool {
	return l.classCap > 0 && strings.HasPrefix(principal, l.class)
}

func (l *Limiter) take(principal string) {
	l.running++
	l.held[principal]++
	if l.inClass(principal) {
		l.classHeld++
	}
}

func (l *Limiter) releaser(principal string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.running--
			if l.inClass(principal) {
				l.classHeld--
			}
			if l.held[principal]--; l.held[principal] == 0 {
				delete(l.held, principal)
			}
			close(l.changed)
			l.changed = make(chan struct{})
		})
	}
}
