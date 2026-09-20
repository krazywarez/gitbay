package store

import (
	"testing"
	"time"
)

// Push is a worker queue like the others, and the one failure config
// validation cannot catch — a key_id Apple did not issue — dead-letters
// every row on its first attempt. Without a count and the rows here an
// admin has no way to see that happening.
func TestQueuesReportsPush(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddPushDevice(uid, "tok-a", "iphone")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.EnqueuePush(uid, "krz/gitbay", "cmc opened issue #1", "krz/gitbay/issues/1"); err != nil {
			t.Fatal(err)
		}
	}
	due, err := s.DuePush(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 3 {
		t.Fatalf("queued %d, want 3", len(due))
	}
	next := time.Now().Add(time.Minute)
	if err := s.MarkPushFailed(due[0].ID, "apns 503", &next); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPushFailed(due[1].ID, "apns 403 InvalidProviderToken", nil); err != nil {
		t.Fatal(err)
	}

	q, err := s.QueueStatus()
	if err != nil {
		t.Fatal(err)
	}
	if q.Push.Pending != 2 || q.Push.Retrying != 1 || q.Push.Failed != 1 {
		t.Fatalf("counts: %+v", q.Push)
	}
	if q.Push.OldestPending == "" {
		t.Fatalf("no oldest pending: %+v", q.Push)
	}
	// Retrying and dead-lettered rows, newest first, as the mail queue
	// lists them.
	if len(q.Push.Items) != 2 {
		t.Fatalf("items: %+v", q.Push.Items)
	}
	if q.Push.Items[0].DeviceID != id || q.Push.Items[0].FailedAt == "" ||
		q.Push.Items[0].LastError != "apns 403 InvalidProviderToken" {
		t.Fatalf("dead-lettered row: %+v", q.Push.Items[0])
	}
	if q.Push.Items[1].Attempts != 1 || q.Push.Items[1].FailedAt != "" {
		t.Fatalf("retrying row: %+v", q.Push.Items[1])
	}
	// A device token is never echoed, here included.
	for _, it := range q.Push.Items {
		if it.Title != "krz/gitbay" || it.CreatedAt == "" {
			t.Fatalf("row: %+v", it)
		}
	}
}

// The build queue lists pending builds as well as running ones, so an
// admin can see what no runner is claiming without walking every repo.
func TestQueuesListsPendingBuilds(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRepo("user", uid, "orgo", "public"); err != nil {
		t.Fatal(err)
	}
	running, err := s.CreateBuild(1, "test", "abc123", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.CreateBuild(1, "pages", "abc123", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimBuild(nil, false); err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}

	q, err := s.QueueStatus()
	if err != nil {
		t.Fatal(err)
	}
	if q.Builds.Pending != 1 || q.Builds.Running != 1 {
		t.Fatalf("counts: %+v", q.Builds)
	}
	items := q.Builds.Items
	if len(items) != 2 {
		t.Fatalf("items: %+v", items)
	}
	if items[0].Number != running || items[0].Status != "running" || items[0].StartedAt == "" {
		t.Fatalf("running row first: %+v", items[0])
	}
	if items[1].Number != pending || items[1].Status != "pending" || items[1].StartedAt != "" || items[1].CreatedAt == "" {
		t.Fatalf("pending row after: %+v", items[1])
	}
}
