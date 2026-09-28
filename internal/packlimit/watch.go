package packlimit

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// StallDeadline is how long a limited transport may go without
// completing a write to its client before it is killed. upload-pack
// sends a keepalive every five seconds while it prepares a pack.
var StallDeadline = 2 * time.Minute

// Watch wraps w, a transport's writer to its client, so that a client
// that stops reading does not hold its slot for as long as its
// connection stays open. stalled closes once no write to w has
// completed for StallDeadline; stop ends the watch. With no limit in
// force (a nil Limiter) nothing is watched: w comes back as is and
// stalled never closes.
func (l *Limiter) Watch(w io.Writer) (out io.Writer, stalled <-chan struct{}, stop func()) {
	if l == nil {
		return w, nil, func() {}
	}
	deadline := StallDeadline
	pw := &progressWriter{w: w}
	pw.last.Store(time.Now().UnixNano())
	st := make(chan struct{})
	quit := make(chan struct{})
	go func() {
		t := time.NewTicker(deadline / 4)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				if time.Since(time.Unix(0, pw.last.Load())) >= deadline {
					close(st)
					return
				}
			}
		}
	}()
	var once sync.Once
	return pw, st, func() { once.Do(func() { close(quit) }) }
}

// progressWriter records when a write to the client last completed.
type progressWriter struct {
	w    io.Writer
	last atomic.Int64 // unix nanoseconds
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	if n > 0 {
		p.last.Store(time.Now().UnixNano())
	}
	return n, err
}
