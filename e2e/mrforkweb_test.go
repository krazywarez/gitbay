package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A contributor without write access proposes a change from the browser:
// the source picker offers the branches of a fork they can push to, and
// the merge request opens against the parent (#168).
func TestMRFromForkWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// bob forks and pushes a branch to the fork.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "fork", "alice/app"); code != 0 {
		t.Fatalf("fork: %s", errOut)
	}
	benv := inst.gitEnv(bobKey)
	bwork := t.TempDir()
	mustGit(t, bwork, benv, "clone", inst.sshURL("bob/app"), "w")
	bdir := filepath.Join(bwork, "w")
	mustGit(t, bdir, benv, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(bdir, "f.txt"), []byte("y\n"), 0o644)
	mustGit(t, bdir, benv, "add", ".")
	mustGit(t, bdir, benv, "commit", "-q", "-m", "change")
	mustGit(t, bdir, benv, "push", "-q", "origin", "feat")

	// bob's fork branch is offered on alice's repo; eve, who can push to
	// neither, sees only the target's own branches.
	bob := inst.login(t, bobKey)
	eve := inst.login(t, eveKey)
	base := inst.base() + "/alice/app"
	_, page := browserGet(t, bob, base+"/mrs/new")
	if !strings.Contains(page, `value="bob/app:feat"`) {
		t.Fatalf("fork branch not offered:\n%s", page)
	}
	if _, p := browserGet(t, eve, base+"/mrs/new"); strings.Contains(p, "bob/app:feat") {
		t.Fatal("a fork branch is offered to someone who cannot push to it")
	}

	// Opening it lands on the merge request, with the fork as its source.
	if status, _ := browserPost(t, bob, base+"/mrs/new", url.Values{
		"source": {"bob/app:feat"}, "target": {"main"}, "title": {"change"}}); status != 200 {
		t.Fatal("mr create from a fork failed")
	}
	out, _, _ := inst.ssh(t, bobKey, "", "mr", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, `"source":"bob/app:feat"`) {
		t.Fatalf("merge request source is not the fork:\n%s", out)
	}

	// A branch of a repository that is not a fork of the target is
	// refused by the command, whatever the form posts.
	if _, errOut, code := inst.ssh(t, eveKey, "", "repo", "create", "eve/other"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	_, body := browserPost(t, eve, base+"/mrs/new", url.Values{
		"source": {"eve/other:main"}, "target": {"main"}, "title": {"sneak"}})
	if !strings.Contains(body, `class="error"`) {
		t.Errorf("a non-fork source was accepted:\n%s", body)
	}
}
