package packlimit

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestGlobalCap(t *testing.T) {
	l := New(2, 0, 0, time.Second)
	r1, err1 := l.Acquire(nil, "a")
	r2, err2 := l.Acquire(nil, "b")
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if _, err := l.Acquire(nil, "c"); !errors.Is(err, ErrBusy) {
		t.Fatalf("third with no queue: %v", err)
	}
	r1()
	r1() // a second release is a no-op
	r3, err := l.Acquire(nil, "c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(nil, "d"); !errors.Is(err, ErrBusy) {
		t.Fatalf("double release freed two slots: %v", err)
	}
	r2()
	r3()
}

func TestPerPrincipalCap(t *testing.T) {
	l := New(4, 1, 4, 50*time.Millisecond)
	ra, err := l.Acquire(nil, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer ra()
	if _, err := l.Acquire(nil, "a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second for a: %v", err)
	}
	rb, err := l.Acquire(nil, "b")
	if err != nil {
		t.Fatalf("b blocked by a: %v", err)
	}
	rb()
}

func TestWaiterGetsReleasedSlot(t *testing.T) {
	l := New(1, 0, 1, 5*time.Second)
	r1, _ := l.Acquire(nil, "a")
	got := make(chan error, 1)
	go func() {
		r, err := l.Acquire(nil, "b")
		if err == nil {
			r()
		}
		got <- err
	}()
	waitQueued(t, l, 1)
	r1()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never got the slot")
	}
}

func TestQueueIsBounded(t *testing.T) {
	l := New(1, 0, 1, 5*time.Second)
	r1, _ := l.Acquire(nil, "a")
	defer r1()
	go l.Acquire(nil, "b")
	waitQueued(t, l, 1)
	if _, err := l.Acquire(nil, "c"); !errors.Is(err, ErrBusy) {
		t.Fatalf("queue over its bound: %v", err)
	}
}

// A principal cannot fill the queue on its own.
func TestPrincipalQueueIsBounded(t *testing.T) {
	l := New(1, 1, 8, 5*time.Second)
	r1, _ := l.Acquire(nil, "x")
	defer r1()
	go l.Acquire(nil, "a")
	waitQueued(t, l, 1)
	if _, err := l.Acquire(nil, "a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second waiter for a: %v", err)
	}
}

func TestClientGoneWhileQueued(t *testing.T) {
	l := New(1, 0, 1, 5*time.Second)
	r1, _ := l.Acquire(nil, "a")
	done := make(chan struct{})
	close(done)
	if _, err := l.Acquire(done, "b"); !errors.Is(err, ErrGone) {
		t.Fatalf("got %v, want ErrGone", err)
	}
	assertQueueEmpty(t, l)
	r1()
	assertHeldEmpty(t, l)
}

func TestWaitRunsOut(t *testing.T) {
	l := New(1, 0, 1, 20*time.Millisecond)
	r1, _ := l.Acquire(nil, "a")
	if _, err := l.Acquire(nil, "b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
	assertQueueEmpty(t, l)
	r1()
	assertHeldEmpty(t, l)
}

func assertQueueEmpty(t *testing.T, l *Limiter) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.queued != 0 || len(l.waiting) != 0 {
		t.Fatalf("queue not cleaned up: queued=%d waiting=%v", l.queued, l.waiting)
	}
}

func assertHeldEmpty(t *testing.T, l *Limiter) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.held) != 0 {
		t.Fatalf("held not cleaned up: %v", l.held)
	}
}

func TestNilLimiterNeverWaits(t *testing.T) {
	var l *Limiter
	if l = New(0, 1, 1, time.Second); l != nil {
		t.Fatal("max 0 should mean no limit")
	}
	r, err := l.Acquire(nil, "a")
	if err != nil {
		t.Fatal(err)
	}
	r()
}

// A waiter whose done channel closes just as a slot frees up must not be
// granted the slot: it has to see ErrGone, and the slot must go to
// someone else instead of leaking to an abandoned caller.
func TestGivenUpWaiterNeverGetsSlot(t *testing.T) {
	l := New(1, 0, 1, 10*time.Second)
	_, _ = l.Acquire(nil, "a") // holds the only slot

	done := make(chan struct{})
	got := make(chan error, 1)
	go func() {
		r, err := l.Acquire(done, "b")
		if err == nil {
			r()
		}
		got <- err
	}()
	waitQueued(t, l, 1)

	// Close done and free a's slot in the same critical section, so
	// changed and done become ready to b's select at the same instant
	// — the exact race the done-check-before-fits ordering in Acquire
	// has to win, whichever the select picks.
	l.mu.Lock()
	close(done)
	l.running--
	delete(l.held, "a")
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()

	select {
	case err := <-got:
		if !errors.Is(err, ErrGone) {
			t.Fatalf("got %v, want ErrGone", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b never returned")
	}

	// The slot must still be free for someone else: b must not hold it.
	r2, err := l.Acquire(nil, "c")
	if err != nil {
		t.Fatalf("slot leaked to the abandoned waiter: %v", err)
	}
	r2()
}

func waitQueued(t *testing.T, l *Limiter, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		q := l.queued
		l.mu.Unlock()
		if q == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue never reached %d", n)
}

// config maps pack_queue = -1 to math.MaxInt: waiters are not turned
// away for want of queue room.
func TestUnboundedQueue(t *testing.T) {
	l := New(1, 0, math.MaxInt, 5*time.Second)
	r1, _ := l.Acquire(nil, "a")
	done := make(chan struct{})
	defer close(done)
	for i := 0; i < 64; i++ {
		go l.Acquire(done, "b")
	}
	waitQueued(t, l, 64)
	r1()
}
