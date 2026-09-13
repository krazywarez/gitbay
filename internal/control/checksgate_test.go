package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// gatesForHead builds a repository with require_checks on, a bare dir
// holding the given .gitbay/ci.yml (empty string for none), and one MR
// whose head carries no statuses at all.
func gatesForHead(t *testing.T, ciYML string) GatesOut {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.UpdateRepoSettings(repo.ID, func(set *store.RepoSettings) { set.RequireChecks = true }); err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(repo.ID)
	if err != nil {
		t.Fatal(err)
	}

	git := gitRunner(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	git(root, "init", "-q", "-b", "main", "src")
	os.WriteFile(filepath.Join(src, "README"), []byte("x\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	targetSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))
	git(src, "checkout", "-q", "-b", "feature")
	if ciYML != "" {
		os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
		os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(ciYML), 0o644)
	}
	os.WriteFile(filepath.Join(src, "README"), []byte("y\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "change")
	headSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "feature", "main", "t", "", headSHA, "md", false); err != nil {
		t.Fatal(err)
	}
	mr, err := st.MRByNumber(repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	g, err := MergeGates(st, repo, mr, dir, targetSHA, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func checksUnmet(g GatesOut) bool {
	for _, u := range g.Unmet {
		if strings.Contains(u, "green checks") {
			return true
		}
	}
	return false
}

// A repository with require_checks on and no CI configuration at its head
// can never report a status, so the gate has nothing to wait for and the
// merge must go through rather than be refused forever.
func TestRequireChecksPassesWithNoCIConfig(t *testing.T) {
	if g := gatesForHead(t, ""); checksUnmet(g) {
		t.Fatalf("refused a head with no CI configuration: %v", g.Unmet)
	}
}

// Jobs that only run on a schedule or on tags never report on a merge
// request head either.
func TestRequireChecksPassesWithNoPushJobs(t *testing.T) {
	cfg := "jobs:\n  nightly:\n    schedule: \"0 3 * * *\"\n    steps:\n      - echo hi\n" +
		"  release:\n    tags: \"v*\"\n    steps:\n      - echo hi\n"
	if g := gatesForHead(t, cfg); checksUnmet(g) {
		t.Fatalf("refused a head whose jobs never run on a push: %v", g.Unmet)
	}
}

// A push job at the head should have reported something. Silence there
// means CI did not run, which is what the gate is for.
func TestRequireChecksRefusesSilentPushJob(t *testing.T) {
	cfg := "jobs:\n  unit:\n    steps:\n      - echo hi\n"
	if g := gatesForHead(t, cfg); !checksUnmet(g) {
		t.Fatalf("allowed a head whose push job reported nothing: %v", g.Unmet)
	}
}
