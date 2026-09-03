package store

import (
	"strings"
	"testing"
)

// repos.owner_id is polymorphic, so no foreign key can hold it. The
// triggers from migration 0033 refuse deleting an owner that still owns
// repositories even when the application guards are bypassed (#136).
func TestOwnerDeleteRefusedWhileReposRemain(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DELETE FROM users WHERE id = ?", uid); err == nil || !strings.Contains(err.Error(), "owns repositories") {
		t.Fatalf("direct user delete with repositories: err=%v", err)
	}
	oid, err := s.CreateOrg("theorg", uid)
	if err != nil {
		t.Fatal(err)
	}
	orgRepo, err := s.CreateRepo("org", oid, "site", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DELETE FROM orgs WHERE id = ?", oid); err == nil || !strings.Contains(err.Error(), "owns repositories") {
		t.Fatalf("direct org delete with repositories: err=%v", err)
	}
	// Without repositories the deletes go through.
	for _, id := range []int64{repoID, orgRepo} {
		if err := s.DeleteRepo(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec("DELETE FROM orgs WHERE id = ?", oid); err != nil {
		t.Fatalf("org delete without repositories: %v", err)
	}
	if _, err := s.DB.Exec("DELETE FROM users WHERE id = ?", uid); err != nil {
		t.Fatalf("user delete without repositories: %v", err)
	}
}
