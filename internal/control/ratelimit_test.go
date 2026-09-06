package control

import (
	"testing"
	"time"
)

// The budget is per account: spending one account's does not touch
// another's, which is the property that makes the limit meaningful when
// registration is open (#148).
func TestWriteLimiterIsPerAccount(t *testing.T) {
	l := &writeLimiter{buckets: map[int64]*writeBucket{}}
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow(1, 3); !ok {
			t.Fatalf("write %d of the burst refused", i+1)
		}
	}
	if ok, wait := l.allow(1, 3); ok || wait <= 0 {
		t.Errorf("the fourth write was allowed, or gave no wait: %v", wait)
	}
	if ok, _ := l.allow(2, 3); !ok {
		t.Error("a second account was charged for the first's writes")
	}
}

// An idle account refills at the sustained rate, so the limit throttles
// rather than locks out.
func TestWriteLimiterRefills(t *testing.T) {
	l := &writeLimiter{buckets: map[int64]*writeBucket{}}
	if ok, _ := l.allow(1, 60); !ok {
		t.Fatal("first write refused")
	}
	// 60 a minute is one a second: rewind the clock a second and the
	// spent token is back.
	l.buckets[1].tokens = 0
	l.buckets[1].last = time.Now().Add(-time.Second)
	if ok, wait := l.allow(1, 60); !ok {
		t.Errorf("no refill after a second: wait %v", wait)
	}
}

// The wait a refusal reports is the time until a token exists, so a
// caller that honours it is not refused again immediately.
func TestWriteLimiterReportsAUsableWait(t *testing.T) {
	l := &writeLimiter{buckets: map[int64]*writeBucket{}}
	l.allow(1, 60)
	l.buckets[1].tokens = 0
	ok, wait := l.allow(1, 60)
	if ok {
		t.Fatal("allowed with an empty bucket")
	}
	if wait < time.Second || wait > 3*time.Second {
		t.Errorf("wait %v is not the ~1s a 60/min bucket needs", wait)
	}
}
