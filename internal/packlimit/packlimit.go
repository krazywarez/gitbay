// Package packlimit bounds concurrent git pack generation. upload-pack
// and upload-archive over SSH, smart HTTP and git:// draw on one
// budget: a global cap, a cap per principal (an account, or a client
// address on the anonymous transports), and a bounded queue whose
// waiters give up after a fixed wait or when the client goes away.
// Waiters are not served in order; a new arrival can take a freed slot
// ahead of them, and the wait bounds how long any one of them waits.
package packlimit

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrBusy = errors.New("the server is busy generating packs for other clients; try again in a minute")
	ErrGone = errors.New("client went away while queued")
)

type Limiter struct {
	max, per, queue int
	wait            time.Duration

	mu      sync.Mutex
	running int
	queued  int
	held    map[string]int // running, per principal
	waiting map[string]int // queued, per principal
	changed chan struct{}  // closed and replaced on every release
}

// New returns a limiter, or nil — no limit — when max is not positive.
func New(max, per, queue int, wait time.Duration) *Limiter {
	if max <= 0 {
		return nil
	}
	return &Limiter{max: max, per: per, queue: queue, wait: wait,
		held: map[string]int{}, waiting: map[string]int{}, changed: make(chan struct{})}
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
	if l.queued >= l.queue || (l.per > 0 && l.waiting[principal] >= l.per) {
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
	return l.running < l.max && (l.per <= 0 || l.held[principal] < l.per)
}

func (l *Limiter) take(principal string) {
	l.running++
	l.held[principal]++
}

func (l *Limiter) releaser(principal string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.running--
			if l.held[principal]--; l.held[principal] == 0 {
				delete(l.held, principal)
			}
			close(l.changed)
			l.changed = make(chan struct{})
		})
	}
}
