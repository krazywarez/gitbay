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

// Idle wraps both directions of a transport, r from the client and w to
// it, so that once arm has been called idle closes when no byte has
// moved either way for d. The window starts at arm; before it idle
// never closes. stop ends the watch.
func Idle(r io.Reader, w io.Writer, d time.Duration) (in io.Reader, out io.Writer, idle <-chan struct{}, arm, stop func()) {
	var last atomic.Int64
	var armed atomic.Bool
	st := make(chan struct{})
	quit := make(chan struct{})
	go func() {
		t := time.NewTicker(d / 4)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				if armed.Load() && time.Since(time.Unix(0, last.Load())) >= d {
					close(st)
					return
				}
			}
		}
	}()
	var armOnce, stopOnce sync.Once
	return &progressReader{r: r, last: &last}, &stampWriter{w: w, last: &last}, st,
		func() {
			armOnce.Do(func() {
				last.Store(time.Now().UnixNano())
				armed.Store(true)
			})
		},
		func() { stopOnce.Do(func() { close(quit) }) }
}

type progressReader struct {
	r    io.Reader
	last *atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.last.Store(time.Now().UnixNano())
	}
	return n, err
}

type stampWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (s *stampWriter) Write(b []byte) (int, error) {
	n, err := s.w.Write(b)
	if n > 0 {
		s.last.Store(time.Now().UnixNano())
	}
	return n, err
}
