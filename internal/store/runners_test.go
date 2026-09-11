package store

import (
	"errors"
	"testing"
)

// runnerFixture is one user with a runner key and two repositories.
func runnerFixture(t *testing.T) (s *Store, uid, keyID, repoA, repoB int64) {
	t.Helper()
	s = open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddSSHKey(uid, "SHA256:runnerkey", "ssh-ed25519", []byte("blob"), "runner", ""); err != nil {
		t.Fatal(err)
	}
	k, err := s.SSHKeyByFingerprint("SHA256:runnerkey")
	if err != nil {
		t.Fatal(err)
	}
	repoA, err = s.CreateRepo("user", uid, "a", "public")
	if err != nil {
		t.Fatal(err)
	}
	repoB, err = s.CreateRepo("user", uid, "b", "public")
	if err != nil {
		t.Fatal(err)
	}
	return s, uid, k.ID, repoA, repoB
}

// Attaching twice is one row; detaching what is not attached is not found.
func TestAttachRunnerIdempotentAndDetach(t *testing.T) {
	s, _, keyID, repoA, repoB := runnerFixture(t)
	for range 2 {
		if err := s.AttachRunner(keyID, repoA); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.RunnerRepoIDs(keyID)
	if err != nil || len(ids) != 1 || ids[0] != repoA {
		t.Fatalf("attached repos %v err=%v, want [%d]", ids, err, repoA)
	}
	if ok, _ := s.RunnerAttached(keyID, repoB); ok {
		t.Fatal("attached to a repo it was never attached to")
	}
	if err := s.DetachRunner(repoB, "SHA256:runnerkey"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("detach of an unattached repo: %v, want ErrNotFound", err)
	}
	if err := s.DetachRunner(repoA, "SHA256:runnerkey"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.RunnerAttached(keyID, repoA); ok {
		t.Fatal("still attached after detach")
	}
}

// Removing the key or the repository removes the attachment with it.
func TestRunnerAttachmentCascades(t *testing.T) {
	s, uid, keyID, repoA, repoB := runnerFixture(t)
	if err := s.AttachRunner(keyID, repoA); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachRunner(keyID, repoB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DELETE FROM repos WHERE id = ?", repoB); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.RunnerRepoIDs(keyID); len(ids) != 1 {
		t.Fatalf("after repo delete: %v, want one attachment", ids)
	}
	if err := s.RemoveSSHKey(uid, "SHA256:runnerkey"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM runner_repos").Scan(&n); err != nil || n != 0 {
		t.Fatalf("after key delete: %d rows err=%v, want 0", n, err)
	}
}

// The heartbeat is per key: two keys on one account are two rows, and a
// repository's runner list shows each key's last poll and the build it holds.
func TestRunnerSeenPerKeyAndRepoList(t *testing.T) {
	s, uid, keyID, repoA, _ := runnerFixture(t)
	if err := s.AddSSHKey(uid, "SHA256:second", "ssh-ed25519", []byte("blob2"), "runner", ""); err != nil {
		t.Fatal(err)
	}
	k2, _ := s.SSHKeyByFingerprint("SHA256:second")
	for _, id := range []int64{keyID, k2.ID} {
		if err := s.AttachRunner(id, repoA); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateBuild(repoA, "unit", "abc123", "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	b, ok, err := s.ClaimBuild(nil, false)
	if err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}
	if err := s.TouchRunner(keyID, uid, "", b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchRunner(k2.ID, uid, "", 0); err != nil {
		t.Fatal(err)
	}
	runners, err := s.ListRunners()
	if err != nil || len(runners) != 2 {
		t.Fatalf("ListRunners: %v err=%v, want two rows", runners, err)
	}
	list, err := s.ListRepoRunners(repoA)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListRepoRunners: %v err=%v, want two rows", list, err)
	}
	var held, idle int
	for _, r := range list {
		if r.Username != "alice" || r.LastSeen == "" || r.AddedAt == "" {
			t.Fatalf("row %+v lacks username, last_seen or added_at", r)
		}
		if r.BuildNumber == b.Number && r.BuildJob == "unit" && r.BuildRepo == "alice/a" {
			held++
		} else if r.BuildNumber == 0 {
			idle++
		}
	}
	if held != 1 || idle != 1 {
		t.Fatalf("held=%d idle=%d, want 1 and 1: %+v", held, idle, list)
	}
	if err := s.RunnerDone(keyID); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListRepoRunners(repoA)
	for _, r := range list {
		if r.BuildNumber != 0 {
			t.Fatalf("build still held after RunnerDone: %+v", r)
		}
	}
	paths, err := s.RunnerRepoPaths(keyID)
	if err != nil || len(paths) != 1 || paths[0] != "alice/a" {
		t.Fatalf("RunnerRepoPaths: %v err=%v", paths, err)
	}
}

// A heartbeat row outlives its usefulness when a key polled once by
// mistake; forgetting it by fingerprint removes the row and nothing else.
func TestForgetRunner(t *testing.T) {
	s, uid, keyID, _, _ := runnerFixture(t)
	if err := s.TouchRunner(keyID, uid, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetRunner("SHA256:nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown fingerprint: %v, want ErrNotFound", err)
	}
	if err := s.ForgetRunner("SHA256:runnerkey"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.ListRunners(); len(rows) != 0 {
		t.Fatalf("row survived forget: %+v", rows)
	}
	if _, err := s.SSHKeyByFingerprint("SHA256:runnerkey"); err != nil {
		t.Fatalf("forget removed the key itself: %v", err)
	}
	if err := s.ForgetRunner("SHA256:runnerkey"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second forget: %v, want ErrNotFound", err)
	}
}
