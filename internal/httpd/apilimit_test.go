package httpd

import (
	"testing"
	"time"
)

func TestAPILimiterBurstThenRefill(t *testing.T) {
	l := newAPILimiter(60) // 1/s sustained, burst 60

	// The burst is spendable immediately.
	for i := 0; i < 60; i++ {
		if ok, _ := l.allow("u1", false); !ok {
			t.Fatalf("read %d rejected inside the burst", i)
		}
	}
	ok, wait := l.allow("u1", false)
	if ok {
		t.Fatal("burst did not run out")
	}
	if wait < time.Second {
		t.Errorf("Retry-After %v, want at least a second", wait)
	}

	// Refill is by elapsed time, so a caller recovers without a restart.
	l.buckets["u1"].last = time.Now().Add(-5 * time.Second)
	if ok, _ := l.allow("u1", false); !ok {
		t.Error("bucket did not refill")
	}
}

func TestAPILimiterWritesHaveTheirOwnBudget(t *testing.T) {
	l := newAPILimiter(60) // write burst is 6

	for i := 0; i < 6; i++ {
		if ok, _ := l.allow("u1", true); !ok {
			t.Fatalf("write %d rejected inside the write burst", i)
		}
	}
	if ok, _ := l.allow("u1", true); ok {
		t.Fatal("writes are not separately limited")
	}
	// Reads still have budget: a client that hit the write ceiling can
	// still render a page.
	if ok, _ := l.allow("u1", false); !ok {
		t.Error("exhausting writes also blocked reads")
	}
}

func TestAPILimiterIsPerCaller(t *testing.T) {
	l := newAPILimiter(60)
	for i := 0; i < 60; i++ {
		l.allow("u1", false)
	}
	if ok, _ := l.allow("u1", false); ok {
		t.Fatal("u1 not limited")
	}
	if ok, _ := l.allow("u2", false); !ok {
		t.Error("one caller's flood limited another")
	}
}

// A caller with no budget must not be able to buy more by making the
// limiter forget them; the sweep only drops buckets that are idle.
func TestAPILimiterSweepKeepsActiveCallers(t *testing.T) {
	l := newAPILimiter(60)
	for i := 0; i < 60; i++ {
		l.allow("victim", false)
	}
	for i := 0; i < 4100; i++ {
		l.allow(string(rune(i))+"filler", false)
	}
	if ok, _ := l.allow("victim", false); ok {
		t.Error("an active caller's bucket was swept, resetting their budget")
	}
}
