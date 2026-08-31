package store

import (
	"strings"
	"testing"
)

// A runner that dies between claiming a build and reporting it leaves the row
// claimed. The next claim resolves it rather than leaving the build running and
// the commit pending forever.
func TestReapStaleBuilds(t *testing.T) {
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

	stuck, err := s.CreateBuild(1, "test", "abc123", "main", `["true"]`)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.CreateBuild(1, "pages", "abc123", "main", `["true"]`)
	if err != nil {
		t.Fatal(err)
	}

	// Claim both, then age only the first past the deadline.
	for range 2 {
		if _, ok, err := s.ClaimBuild(); err != nil || !ok {
			t.Fatalf("claim: %v ok=%v", err, ok)
		}
	}
	if _, err := s.DB.Exec(
		`UPDATE builds SET started_at = '2020-01-01T00:00:00Z' WHERE number = ?`, stuck); err != nil {
		t.Fatal(err)
	}

	reaped, err := s.ReapStaleBuilds()
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0].Number != stuck {
		t.Fatalf("reaped %+v, want only build %d", reaped, stuck)
	}

	b, err := s.BuildByNumber(1, stuck)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "failure" || b.FinishedAt == "" {
		t.Fatalf("stale build is %s finished %q, want failure with a timestamp", b.Status, b.FinishedAt)
	}
	log, err := s.BuildLog(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "abandoned") {
		t.Fatalf("log does not say why it failed: %q", log)
	}

	// A build still inside the deadline is left alone.
	if b, err := s.BuildByNumber(1, fresh); err != nil || b.Status != "running" {
		t.Fatalf("fresh build is %v (%v), want running", b.Status, err)
	}
}

// Checks on a merge request report how long their build ran, which means
// pairing ci/<job> statuses with builds on the same commit.
func TestBuildsForCommitTiming(t *testing.T) {
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
	// Two runs of the same job on one commit: the retry is what counts.
	for range 2 {
		if _, err := s.CreateBuild(repoID, "test", "abc123", "main", `["true"]`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateBuild(repoID, "lint", "def456", "main", `["true"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE builds SET started_at = '2026-08-28T04:42:54Z',
		finished_at = '2026-08-28T04:44:06Z', status = 'success' WHERE number = 2`); err != nil {
		t.Fatal(err)
	}

	byJob, err := s.BuildsForCommit(repoID, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(byJob) != 1 {
		t.Fatalf("builds for commit: %+v", byJob)
	}
	b := byJob["test"]
	if b.Number != 2 {
		t.Fatalf("older run won: %d", b.Number)
	}
	if got := b.Elapsed().String(); got != "1m12s" {
		t.Fatalf("elapsed: %s", got)
	}
	// A build that never finished has no duration to report.
	if d := byJob["lint"].Elapsed(); d != 0 {
		t.Fatalf("unfinished build reported %s", d)
	}
}
