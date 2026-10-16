package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/policy"
)

// A push that puts a merge request's head on its target records the
// merge request as merged by the pusher (#330).
func TestPushLandingTheHeadMarksMRMerged(t *testing.T) {
	f := newQueueFixture(t, nil)
	f.branch("main", f.headSHA)
	var cfg config.Config
	cfg.Server.Root = f.root
	RefsUpdated(f.st, cfg, f.repo.ID, f.alice.ID, "full", []policy.RefUpdate{
		{Ref: "refs/heads/main", Old: f.targetSH, New: f.headSHA}})

	mr := f.mr()
	if mr.State != "merged" || mr.MergedBy != "alice" || mr.MergedSHA != f.headSHA || mr.MergedBase != f.targetSH {
		t.Fatalf("MR = state %s by %q sha %q base %q, want merged by alice at %s on %s",
			mr.State, mr.MergedBy, mr.MergedSHA, mr.MergedBase, f.headSHA, f.targetSH)
	}
}

// A source_gone merge request whose head lands inside another merge
// request's merge is recorded as merged with it (#330).
func TestMergeCarryingAnotherHeadMarksItMerged(t *testing.T) {
	f := newQueueFixture(t, nil)
	f.git(f.src, "checkout", "-q", "-b", "next", "feature")
	f.write("next.txt", "z\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "next")
	next := strings.TrimSpace(f.git(f.src, "rev-parse", "HEAD"))
	f.git(f.dir, "fetch", "-q", f.src, "refs/heads/next:refs/heads/next")
	if _, err := f.st.CreateMR(f.repo.ID, f.alice.ID, f.repo.ID, "next", "main", "next", "", next, "md", false); err != nil {
		t.Fatal(err)
	}
	f.git(f.dir, "update-ref", mrHeadRef(2), next)
	if err := f.st.SetMRState(f.mr().ID, "source_gone"); err != nil {
		t.Fatal(err)
	}

	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "2", "--strategy", "ff")
	if mr := f.mr(); mr.State != "merged" || mr.MergedSHA != f.headSHA {
		t.Fatalf("!1 = state %s sha %q, want merged at %s", mr.State, mr.MergedSHA, f.headSHA)
	}
}

// A head already on the target before the push is left alone: the push
// did not land it.
func TestPushLeavesHeadAlreadyOnTarget(t *testing.T) {
	f := newQueueFixture(t, nil)
	f.branch("main", f.headSHA)
	var cfg config.Config
	cfg.Server.Root = f.root
	f.git(f.src, "checkout", "-q", "feature")
	f.write("more.txt", "m\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "more")
	more := strings.TrimSpace(f.git(f.src, "rev-parse", "HEAD"))
	f.git(f.dir, "fetch", "-q", f.src, "refs/heads/feature:refs/heads/main")
	RefsUpdated(f.st, cfg, f.repo.ID, f.alice.ID, "full", []policy.RefUpdate{
		{Ref: "refs/heads/main", Old: f.headSHA, New: more}})

	if mr := f.mr(); mr.State != "open" {
		t.Fatalf("MR = state %s, want open", mr.State)
	}
}

// mr merge still records a head that reached the target without
// post-receive seeing it (#108).
func TestMergeRecordsHeadAlreadyOnTarget(t *testing.T) {
	f := newQueueFixture(t, nil)
	f.branch("main", f.headSHA)
	out := f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--json")
	if !strings.Contains(out, `"strategy":"recorded"`) {
		t.Fatalf("merge of an already-landed head:\n%s", out)
	}
	if mr := f.mr(); mr.State != "merged" || mr.MergedBy != "alice" {
		t.Fatalf("MR = state %s by %q, want merged by alice", mr.State, mr.MergedBy)
	}
}
