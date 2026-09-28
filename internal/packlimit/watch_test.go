package packlimit

import (
	"io"
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
