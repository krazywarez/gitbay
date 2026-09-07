package store

import "testing"

// Removing an org member drops their team memberships in that org. Team
// add requires membership, so a row that survived removal would grant a
// non-member access through the team (#196).
func TestRemoveOrgMemberDropsTeamRows(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	alice, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	oid, err := s.CreateOrg("acme", alice)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateOrg("other", alice)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []int64{oid, other} {
		if err := s.SetOrgMember(o, bob, "member"); err != nil {
			t.Fatal(err)
		}
	}
	core, err := s.CreateTeam(oid, "core")
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := s.CreateTeam(other, "core")
	if err != nil {
		t.Fatal(err)
	}
	for _, tm := range []int64{core, elsewhere} {
		if err := s.AddTeamMember(tm, bob); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveOrgMember(oid, bob); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.TeamMembers(core); len(m) != 0 {
		t.Fatalf("team rows survived org removal: %v", m)
	}
	// The other org's team is untouched.
	if m, _ := s.TeamMembers(elsewhere); len(m) != 1 || m[0] != "bob" {
		t.Fatalf("removal reached another org's team: %v", m)
	}
}
