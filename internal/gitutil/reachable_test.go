package gitutil

import (
	"path/filepath"
	"strings"
	"testing"
)

func mustResolve(t *testing.T, dir, ref string) string {
	t.Helper()
	sha, err := ResolveRef(dir, ref)
	if err != nil {
		t.Fatalf("resolving %s: %v", ref, err)
	}
	return sha
}

// A commit still an ancestor of a branch, or a branch tip itself, is
// reachable — the ordinary case a claimed build's sha is in.
func TestReachableAncestorAndTip(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "one")
	first := mustResolve(t, dir, "HEAD")
	write(t, dir, "f.txt", "two\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "two")
	tip := mustResolve(t, dir, "HEAD")

	if ok, err := Reachable(dir, first); err != nil || !ok {
		t.Fatalf("ancestor: ok=%v err=%v", ok, err)
	}
	if ok, err := Reachable(dir, tip); err != nil || !ok {
		t.Fatalf("tip: ok=%v err=%v", ok, err)
	}
}

// A force-push moves a branch off a commit, and the old commit's object
// sticks around until the next gc: exactly what a rebase-and-force-push
// leaves a queued build pointed at. Reachable must say false here, not
// error — a git error would let the caller read it as "cannot tell" and
// hand the build to a runner that fails at clone.
func TestReachableOrphanedByForcePushNotYetPruned(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "one")
	base := mustResolve(t, dir, "HEAD")
	write(t, dir, "f.txt", "two\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "orphaned")
	orphaned := mustResolve(t, dir, "HEAD")

	git(t, dir, "update-ref", "refs/heads/main", base)

	if ok, err := Reachable(dir, orphaned); err != nil || ok {
		t.Fatalf("orphaned, object still present: ok=%v err=%v", ok, err)
	}
	// The rewound branch itself is untouched.
	if ok, err := Reachable(dir, base); err != nil || !ok {
		t.Fatalf("base after rewind: ok=%v err=%v", ok, err)
	}
}

// Once gc has actually removed the object, the object no longer exists at
// all. Still false, still no error: pruned is a stronger form of orphaned,
// not a different outcome.
func TestReachablePrunedObject(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "one")
	base := mustResolve(t, dir, "HEAD")
	write(t, dir, "f.txt", "two\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "orphaned")
	orphaned := mustResolve(t, dir, "HEAD")

	git(t, dir, "update-ref", "refs/heads/main", base)
	git(t, dir, "reflog", "expire", "--expire=now", "--all")
	git(t, dir, "gc", "--prune=now", "-q")

	if ok, err := Reachable(dir, orphaned); err != nil || ok {
		t.Fatalf("pruned: ok=%v err=%v", ok, err)
	}
}

// A check that cannot run at all — no repository at the path — must not
// come back as "unreachable": that would read as a real answer and cancel
// a build that was never actually checked.
func TestReachableErrorsRatherThanFalseWhenItCannotCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-such-repo")
	if ok, err := Reachable(dir, strings.Repeat("a", 40)); err == nil {
		t.Fatalf("expected an error for a missing repository, got ok=%v", ok)
	}
}

// A directory that exists but holds no git repository at all — corrupted,
// mid-restore from a backup, or simply never initialized — must error the
// same way: cat-file -e on a well-formed sha exits 1 only when the object
// is genuinely absent from a real repository. Outside a repository it
// exits 128, the same code a peeled ^{commit} lookup uses for a missing
// object, so collapsing "any exit" to false would read a broken
// repository as an orphaned queue and cancel every build in it.
func TestReachableErrorsWhenDirectoryIsNotAGitRepository(t *testing.T) {
	dir := t.TempDir() // exists, but no `git init` ever ran here
	if ok, err := Reachable(dir, strings.Repeat("a", 40)); err == nil {
		t.Fatalf("expected an error for a non-repository directory, got ok=%v", ok)
	}
}

// A merge request head fetched from a fork lives at
// refs/merge-requests/<n>/head — no branch and no tag ever points at it.
// It must still read as reachable: it is fetched into the target
// repository and a runner's clone reaches it exactly like a branch tip
// does. Restricting the ancestry check to refs/heads and refs/tags (a
// first pass of Reachable did this) would read every such build as
// unreachable and cancel it, which is worse than the bug this whole
// change fixes — CI silently stops running on every fork merge request.
func TestReachableFromMergeRequestHeadRef(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	git(t, dir, "checkout", "-q", "-b", "fork-head")
	write(t, dir, "f.txt", "two\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "mr head")
	mrSHA := mustResolve(t, dir, "HEAD")
	git(t, dir, "update-ref", "refs/merge-requests/1/head", mrSHA)
	git(t, dir, "checkout", "-q", "main")
	git(t, dir, "branch", "-D", "fork-head")

	if ok, err := Reachable(dir, mrSHA); err != nil || !ok {
		t.Fatalf("mr head reachable only via refs/merge-requests: ok=%v err=%v", ok, err)
	}
}
