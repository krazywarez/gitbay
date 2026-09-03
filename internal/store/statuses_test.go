package store

import "testing"

// Checks carry the timing of the build behind them. Statuses posted from
// outside CI have no build, and must not borrow one.
func TestChecksForCommit(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBuild(repoID, "test", "abc123", "main", `["true"]`, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE builds SET started_at = '2026-08-28T04:42:54Z',
		finished_at = '2026-08-28T04:44:06Z', status = 'success' WHERE number = 1`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ context, state string }{
		{"ci/test", "success"},
		{"ci/absent", "pending"}, // a status with no build of its own
		{"external/lint", "success"},
	} {
		if err := s.SetCommitStatus(repoID, "abc123", c.context, c.state, "", "", uid); err != nil {
			t.Fatal(err)
		}
	}

	checks, combined, err := s.ChecksForCommit(repoID, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if combined != "pending" {
		t.Fatalf("combined: %s", combined)
	}
	byContext := map[string]Check{}
	for _, c := range checks {
		if c.UpdatedAt == "" {
			t.Errorf("%s has no timestamp", c.Context)
		}
		byContext[c.Context] = c
	}
	if got := byContext["ci/test"]; got.Build != 1 || got.Duration.String() != "1m12s" {
		t.Errorf("ci/test timing: build %d, %s", got.Build, got.Duration)
	}
	for _, ctx := range []string{"ci/absent", "external/lint"} {
		if got := byContext[ctx]; got.Build != 0 || got.Duration != 0 {
			t.Errorf("%s borrowed timing: build %d, %s", ctx, got.Build, got.Duration)
		}
	}
}
