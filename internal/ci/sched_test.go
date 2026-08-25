package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/store"
)

// TestSchedulerRunDue drives one scheduler pass against a real store and
// bare repo: a due entry queues a build, sets a pending status, and
// advances next_run; entries whose job vanished are dropped.
func TestSchedulerRunDue(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = uid
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}

	// A bare repo whose main holds a ci.yml with one scheduled job.
	work := t.TempDir()
	env := append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	src := filepath.Join(work, "src")
	os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"),
		[]byte("jobs:\n  nightly:\n    schedule: \"0 6 * * *\"\n    steps: [echo hi]\n"), 0o644)
	git(work, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	bare := filepath.Join(work, "bare.git")
	git(work, "clone", "-q", "--bare", src, "bare.git")

	now := time.Now()
	past := now.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	if err := st.SyncSchedules(repoID, []store.Schedule{
		{RepoID: repoID, Job: "nightly", Cron: "0 6 * * *", NextRun: past},
		{RepoID: repoID, Job: "gone", Cron: "0 7 * * *", NextRun: past},
	}); err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{St: st, SiteURL: "https://x.test",
		RepoDir: func(owner, name string) string { return bare }}
	s.RunDue(now)

	builds, err := st.ListBuilds(repoID, 10)
	if err != nil || len(builds) != 1 {
		t.Fatalf("builds after run: %v %v", builds, err)
	}
	if builds[0].Job != "nightly" || builds[0].Status != "pending" || builds[0].Ref != "main" {
		t.Fatalf("queued build wrong: %+v", builds[0])
	}
	statuses, _ := st.ListCommitStatuses(repoID, builds[0].SHA)
	if len(statuses) != 1 || statuses[0].Context != "ci/nightly" || statuses[0].State != "pending" {
		t.Fatalf("status wrong: %+v", statuses)
	}
	// next_run advanced past now; the vanished job's entry is gone.
	due, _ := st.DueSchedules(now.UTC().Format("2006-01-02T15:04:05Z"))
	if len(due) != 0 {
		t.Fatalf("still due after run: %+v", due)
	}
	all, _ := st.DueSchedules("9999-01-01T00:00:00Z")
	if len(all) != 1 || all[0].Job != "nightly" {
		t.Fatalf("schedule set after run: %+v", all)
	}
	// Cron fires in server-local time, stored as UTC.
	cr, _ := ParseCron("0 6 * * *")
	if want := cr.Next(now).UTC().Format("2006-01-02T15:04:05Z"); all[0].NextRun != want {
		t.Fatalf("next_run = %s, want %s", all[0].NextRun, want)
	}

	// A second pass fires nothing: next_run is in the future.
	s.RunDue(now)
	if builds, _ = st.ListBuilds(repoID, 10); len(builds) != 1 {
		t.Fatalf("second pass queued extra builds: %+v", builds)
	}
}
