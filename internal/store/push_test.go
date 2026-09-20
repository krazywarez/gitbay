package store

import (
	"testing"
	"time"
)

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

	firstID, err := s.AddPushDevice(uid, "tok-a", "iphone")
	if err != nil {
		t.Fatalf("AddPushDevice: %v", err)
	}
	devices, err := s.PushDevices(uid)
	if err != nil {
		t.Fatalf("PushDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].Token != "tok-a" || devices[0].Label != "iphone" {
		t.Fatalf("got %+v", devices)
	}
	if firstID != devices[0].ID {
		t.Fatalf("AddPushDevice returned %d, row id is %d", firstID, devices[0].ID)
	}

	// Apple reuses tokens: re-registering updates the label and the owner
	// rather than erroring, so a reinstall under another account works. The
	// returned id must be the existing row's, not an unrelated rowid left
	// over from SQLite's last real INSERT (the DO UPDATE arm does not
	// advance last_insert_rowid()).
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	reregID, err := s.AddPushDevice(bob, "tok-a", "ipad")
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if d, _ := s.PushDevices(uid); len(d) != 0 {
		t.Fatalf("token still owned by alice: %+v", d)
	}
	d, _ := s.PushDevices(bob)
	if len(d) != 1 || d[0].Label != "ipad" {
		t.Fatalf("got %+v", d)
	}
	if reregID != d[0].ID {
		t.Fatalf("re-register returned %d, existing row id is %d", reregID, d[0].ID)
	}
	if reregID != firstID {
		t.Fatalf("re-register returned %d, want the reused row's original id %d", reregID, firstID)
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

func TestEnqueuePush(t *testing.T) {
	s := pushFixture(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	s.AddPushDevice(uid, "tok-a", "iphone")
	s.AddPushDevice(uid, "tok-b", "ipad")

	// One row per device, so a retry to the phone does not resend to the
	// iPad.
	if err := s.EnqueuePush(uid, "krz/gitbay", "cmc opened issue #12", "krz/gitbay/issues/12"); err != nil {
		t.Fatalf("EnqueuePush: %v", err)
	}
	due, err := s.DuePush(20)
	if err != nil {
		t.Fatalf("DuePush: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("want a row per device, got %d", len(due))
	}
	if due[0].Token == "" || due[0].Body != "cmc opened issue #12" {
		t.Fatalf("got %+v", due[0])
	}

	// Sent rows stop being due.
	if err := s.MarkPushSent(due[0].ID); err != nil {
		t.Fatalf("MarkPushSent: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 1 {
		t.Fatalf("sent row still due")
	}

	// A failure with a next attempt in the future is not due yet.
	next := time.Now().Add(time.Hour)
	if err := s.MarkPushFailed(due[1].ID, "503", &next); err != nil {
		t.Fatalf("MarkPushFailed: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("backed-off row is due too early")
	}
}

func TestEnqueuePushRespectsSettingAndDevices(t *testing.T) {
	s := pushFixture(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}

	// No devices: nothing queued, no error.
	if err := s.EnqueuePush(uid, "t", "b", "p"); err != nil {
		t.Fatalf("EnqueuePush with no devices: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("queued for an account with no devices")
	}

	// Setting off: nothing queued.
	s.AddPushDevice(uid, "tok-a", "iphone")
	s.SetPushEnabled(uid, false)
	if err := s.EnqueuePush(uid, "t", "b", "p"); err != nil {
		t.Fatalf("EnqueuePush with push off: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("queued with notify_push off")
	}
}

// A token changing hands takes its undelivered queue with it. The row id
// survives the upsert, so anything queued for the previous owner would
// otherwise be delivered to a phone that now belongs to someone else —
// and an alert carries the repository name and item number in full. The
// iOS app calls device add on every sign-in, which is exactly when
// ownership changes.
func TestAddPushDeviceDropsThePreviousOwnersQueue(t *testing.T) {
	s := pushFixture(t)
	alice, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddPushDevice(alice, "tok-a", "iphone")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueuePush(alice, "alice/secret", "alice opened issue #1", "alice/secret/issues/1"); err != nil {
		t.Fatal(err)
	}
	due, err := s.DuePush(20)
	if err != nil || len(due) != 1 {
		t.Fatalf("DuePush: %v %+v", err, due)
	}
	// A second row, already sent: history, not a pending delivery.
	if err := s.EnqueuePush(alice, "alice/secret", "alice closed issue #1", "alice/secret/issues/1"); err != nil {
		t.Fatal(err)
	}
	sent, _ := s.DuePush(20)
	if err := s.MarkPushSent(sent[len(sent)-1].ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.AddPushDevice(bob, "tok-a", "iphone"); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("alice's pending push survived the handover: %+v", due)
	}
	var kept int
	s.DB.QueryRow("SELECT COUNT(*) FROM push_queue WHERE device_id = ? AND sent_at IS NOT NULL", id).Scan(&kept)
	if kept != 1 {
		t.Fatalf("delivered rows deleted too: %d remain", kept)
	}

	// Re-registering to the same owner leaves the queue alone: the app
	// calls device add on every launch.
	if err := s.EnqueuePush(bob, "bob/app", "bob opened issue #2", "bob/app/issues/2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPushDevice(bob, "tok-a", "iphone"); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DuePush(20); len(due) != 1 {
		t.Fatalf("re-registering to the same owner dropped its own queue: %+v", due)
	}
}

func TestDeletePushDeviceByTokenTakesItsQueue(t *testing.T) {
	s := pushFixture(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	s.AddPushDevice(uid, "tok-a", "iphone")
	s.EnqueuePush(uid, "t", "b", "p")

	if err := s.DeletePushDeviceByToken("tok-a"); err != nil {
		t.Fatalf("DeletePushDeviceByToken: %v", err)
	}
	if d, _ := s.PushDevices(uid); len(d) != 0 {
		t.Fatalf("device survived")
	}
	// push_queue.device_id is ON DELETE CASCADE, so the queued rows go
	// with it rather than being retried at a dead token forever.
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("queued rows outlived their device")
	}
}

// A queued push carries the recipient's username, so the alert can name
// the account it belongs to. A device token is one install, and one
// install registers against every account signed in on it; without the
// username the client cannot tell which of them a push is for.
func TestDuePushCarriesTheUsername(t *testing.T) {
	s := pushFixture(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPushDevice(uid, "tok-a", "iphone"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueuePush(uid, "alice/app", "bob opened issue #1", "alice/app/issues/1"); err != nil {
		t.Fatal(err)
	}
	due, err := s.DuePush(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("want one queued push, got %d", len(due))
	}
	if due[0].Username != "alice" {
		t.Fatalf("Username = %q, want alice", due[0].Username)
	}
}

// The queue row carries the recipient's unread count, so the alert can
// badge the app icon. Counted at send rather than at enqueue: an inbox
// cleared in the seconds before delivery is reflected.
func TestDuePushCarriesTheUnreadCount(t *testing.T) {
	s := pushFixture(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPushDevice(uid, "tok-a", "iphone"); err != nil {
		t.Fatal(err)
	}
	// Two unread inbox rows, then a queued push.
	for i := 0; i < 2; i++ {
		if err := s.AddNotice(uid, repoID, "issue", "bob", "opened issue #1", "alice/app/issues/1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueuePush(uid, "alice/app", "bob opened issue #1", "alice/app/issues/1"); err != nil {
		t.Fatal(err)
	}
	due, err := s.DuePush(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("want one queued push, got %d", len(due))
	}
	if due[0].Badge != 2 {
		t.Fatalf("Badge = %d, want 2", due[0].Badge)
	}
}
