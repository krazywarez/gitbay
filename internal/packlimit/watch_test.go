package packlimit

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestWatchStalls(t *testing.T) {
	old := StallDeadline
	StallDeadline = 100 * time.Millisecond
	t.Cleanup(func() { StallDeadline = old })
	l := New(1, 0, 0, time.Second)
	w, stalled, stop := l.Watch(io.Discard)
	defer stop()
	// Writes keep it alive past the deadline.
	for range 6 {
		time.Sleep(40 * time.Millisecond)
		w.Write([]byte("x"))
		select {
		case <-stalled:
			t.Fatal("stalled while writing")
		default:
		}
	}
	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("no stall after writes stopped")
	}
}

func TestWatchWithoutLimit(t *testing.T) {
	var l *Limiter
	w, stalled, stop := l.Watch(io.Discard)
	defer stop()
	if w != io.Discard || stalled != nil {
		t.Fatal("a nil limiter watched")
	}
}

// Bytes either way keep an armed Idle watch alive; silence both ways
// ends it. Before arm, silence ends nothing.
func TestIdleCountsBothDirections(t *testing.T) {
	src := strings.NewReader(strings.Repeat("x", 16))
	r, w, idle, arm, stop := Idle(src, io.Discard, 100*time.Millisecond)
	defer stop()
	time.Sleep(300 * time.Millisecond)
	select {
	case <-idle:
		t.Fatal("idle before arm")
	default:
	}
	arm()
	buf := make([]byte, 1)
	for i := range 8 {
		time.Sleep(40 * time.Millisecond)
		if i%2 == 0 {
			r.Read(buf)
		} else {
			w.Write(buf)
		}
		select {
		case <-idle:
			t.Fatalf("idle at step %d while bytes moved", i)
		default:
		}
	}
	select {
	case <-idle:
	case <-time.After(2 * time.Second):
		t.Fatal("not idle after both directions went quiet")
	}
}
