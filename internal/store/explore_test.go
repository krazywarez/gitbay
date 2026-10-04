package store

import "testing"

func TestListPublicReposByActivity(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, name := range []string{"aa", "bb", "cc", "dd", "secret"} {
		vis := "public"
		if name == "secret" {
			vis = "private"
		}
		if ids[name], err = s.CreateRepo("user", uid, name, vis); err != nil {
			t.Fatal(err)
		}
	}
	for _, ev := range []struct{ repo, kind string }{
		{"aa", "push"}, {"cc", "issue.created"}, {"bb", "push"}, {"aa", "build.finished"}, {"secret", "push"},
	} {
		if err := s.RecordEvent(ids[ev.repo], uid, ev.kind, "{}"); err != nil {
			t.Fatal(err)
		}
	}

	repos, err := s.ListPublicReposByActivity()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range repos {
		got = append(got, r.Name)
	}
	// bb was touched last; a build on aa is not activity; dd has none
	// and comes after every repo that does.
	want := []string{"bb", "cc", "aa", "dd"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
