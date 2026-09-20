package store

import "testing"

func pushFixture(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPushDevices(t *testing.T) {
	s := pushFixture(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.AddPushDevice(uid, "tok-a", "iphone"); err != nil {
		t.Fatalf("AddPushDevice: %v", err)
	}
	devices, err := s.PushDevices(uid)
	if err != nil {
		t.Fatalf("PushDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].Token != "tok-a" || devices[0].Label != "iphone" {
		t.Fatalf("got %+v", devices)
	}

	// Apple reuses tokens: re-registering updates the label and the owner
	// rather than erroring, so a reinstall under another account works.
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPushDevice(bob, "tok-a", "ipad"); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if d, _ := s.PushDevices(uid); len(d) != 0 {
		t.Fatalf("token still owned by alice: %+v", d)
	}
	d, _ := s.PushDevices(bob)
	if len(d) != 1 || d[0].Label != "ipad" {
		t.Fatalf("got %+v", d)
	}

	// Removal is scoped to the owner: alice cannot remove bob's device.
	if err := s.RemovePushDevice(uid, d[0].ID); err != ErrNotFound {
		t.Fatalf("cross-account remove: got %v, want ErrNotFound", err)
	}
	if err := s.RemovePushDevice(bob, d[0].ID); err != nil {
		t.Fatalf("RemovePushDevice: %v", err)
	}
	if d, _ := s.PushDevices(bob); len(d) != 0 {
		t.Fatalf("device survived removal: %+v", d)
	}
}

func TestPushEnabledDefaultsOn(t *testing.T) {
	s := pushFixture(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	on, err := s.PushEnabled(uid)
	if err != nil {
		t.Fatalf("PushEnabled: %v", err)
	}
	if !on {
		t.Fatal("notify_push should default on")
	}
	if err := s.SetPushEnabled(uid, false); err != nil {
		t.Fatalf("SetPushEnabled: %v", err)
	}
	if on, _ := s.PushEnabled(uid); on {
		t.Fatal("SetPushEnabled(false) did not stick")
	}
}
