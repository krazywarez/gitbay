package store

import "testing"

// EffectiveAccess folds owner, org roles, team grants and direct grants
// into one row per account carrying the highest role and its source
// (#200).
func TestEffectiveAccess(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) int64 {
		id, err := s.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	alice, bob, carol, dave, eve := mk("alice"), mk("bob"), mk("carol"), mk("dave"), mk("eve")
	oid, err := s.CreateOrg("acme", alice)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []int64{bob, carol, dave} {
		if err := s.SetOrgMember(oid, u, "member"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetOrgMembersRole(oid, "read"); err != nil {
		t.Fatal(err)
	}
	repo, err := s.CreateRepo("org", oid, "core", "private")
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(oid, "core")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTeamMember(team, bob); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantTeamRepo(team, repo, "write"); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantAccess(repo, carol, "admin"); err != nil {
		t.Fatal(err)
	}
	_ = eve

	got, err := s.EffectiveAccess(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []EffectiveEntry{
		{"alice", "admin", "org admin"},
		{"bob", "write", "team core"},
		{"carol", "admin", "direct"},
		{"dave", "read", "org member"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %v, want %v", i, got[i], want[i])
		}
	}

	// A user-owned repository: the owner, and nobody else.
	own, err := s.CreateRepo("user", eve, "mine", "public")
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.EffectiveAccess(own)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (EffectiveEntry{"eve", "admin", "owner"}) {
		t.Fatalf("owner row: %v", got)
	}
}
