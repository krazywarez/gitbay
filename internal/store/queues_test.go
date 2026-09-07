package store

import "testing"

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
	if _, ok, err := s.ClaimBuild(nil); err != nil || !ok {
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
