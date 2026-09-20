package push

import (
	"context"
	"net/http"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/store"
)

// testStoreWithQueuedPush opens an in-memory store, creates a user with
// push enabled, registers one device and enqueues one push for it.
func testStoreWithQueuedPush(t *testing.T, token string) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPushEnabled(uid, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPushDevice(uid, token, "iphone"); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueuePush(uid, "krz/gitbay", "cmc opened issue #12", "krz/gitbay/issues/12"); err != nil {
		t.Fatal(err)
	}
	return st
}

// countPushDevices counts the test user's own devices. testStoreWithQueuedPush
// always creates "alice", so looking her up here keeps the helper's signature
// matching the brief's test code, which passes no user id.
func countPushDevices(t *testing.T, st *store.Store) int {
	t.Helper()
	u, err := st.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	devices, err := st.PushDevices(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(devices)
}

func TestDrainSendsAndMarks(t *testing.T) {
	var hits int
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
	})
	st := testStoreWithQueuedPush(t, "tok-a")
	d := &Deliverer{St: st, Cl: c, RetryBase: time.Millisecond, MaxAttempts: 5}

	d.drain(context.Background())

	if hits != 1 {
		t.Fatalf("sent %d times, want 1", hits)
	}
	if due, _ := st.DuePush(20); len(due) != 0 {
		t.Fatalf("row still due after a 200")
	}
}

func TestDrainReapsADeadToken(t *testing.T) {
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
		w.Write([]byte(`{"reason":"Unregistered"}`))
	})
	st := testStoreWithQueuedPush(t, "tok-a")
	d := &Deliverer{St: st, Cl: c, RetryBase: time.Millisecond, MaxAttempts: 5}

	d.drain(context.Background())

	if due, _ := st.DuePush(20); len(due) != 0 {
		t.Fatalf("queue survived the reap")
	}
	// The device is gone, not merely its queue row.
	if n := countPushDevices(t, st); n != 0 {
		t.Fatalf("%d devices left after 410", n)
	}
}

func TestDrainBacksOffThenDeadLetters(t *testing.T) {
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	})
	st := testStoreWithQueuedPush(t, "tok-a")
	d := &Deliverer{St: st, Cl: c, RetryBase: time.Nanosecond, MaxAttempts: 3}

	// Three passes: two back off, the third gives up.
	for i := 0; i < 3; i++ {
		d.drain(context.Background())
	}
	if due, _ := st.DuePush(20); len(due) != 0 {
		t.Fatalf("row still due after MaxAttempts")
	}
	// A transient failure must not take the device with it.
	if n := countPushDevices(t, st); n != 1 {
		t.Fatalf("device reaped on a 503")
	}
}
