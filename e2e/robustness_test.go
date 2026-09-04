package e2e

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Paths and content outside ASCII go through every read surface: the
// tree, cat, grep, blame and the web blob page. Nothing in the suite
// covered a non-ASCII path before (#129).
func TestNonASCIIPaths(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	const name = "dokumente/übersicht — ünïcode.txt"
	os.MkdirAll(filepath.Join(dir, "dokumente"), 0o755)
	os.WriteFile(filepath.Join(dir, name), []byte("Grüße aus Nürnberg\n日本語の行\n"), 0o644)
	mustGit(t, dir, env, "-c", "core.quotepath=off", "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ünïcode")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	if out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "tree", "alice/app", "dokumente", "--json"); code != 0 || !strings.Contains(out, "übersicht") {
		t.Fatalf("repo tree: exit %d\n%s%s", code, out, errOut)
	}
	// inst.ssh joins arguments into one command line; the name has spaces.
	quoted := "'" + name + "'"
	if out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "cat", "alice/app", quoted); code != 0 || !strings.Contains(out, "Nürnberg") {
		t.Fatalf("repo cat: exit %d\n%s%s", code, out, errOut)
	}
	if out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "grep", "alice/app", "日本語"); code != 0 || !strings.Contains(out, "übersicht") {
		t.Fatalf("repo grep: exit %d\n%s%s", code, out, errOut)
	}
	if out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "blame", "alice/app", quoted); code != 0 || !strings.Contains(out, "Nürnberg") {
		t.Fatalf("repo blame: exit %d\n%s%s", code, out, errOut)
	}
	page := inst.base() + "/alice/app/blob/main/" + url.PathEscape("dokumente") + "/" + url.PathEscape("übersicht — ünïcode.txt")
	resp, err := http.Get(page)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("web blob page: %d", resp.StatusCode)
	}
}

// Pushes to different branches of one repository at the same time all
// land: the receive hooks, the post-receive work and the store take
// them concurrently without losing one (#129).
func TestConcurrentPushes(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	seed := filepath.Join(t.TempDir(), "seed")
	mustGit(t, t.TempDir(), env, "clone", inst.sshURL("alice/app"), seed)
	os.WriteFile(filepath.Join(seed, "f.txt"), []byte("x\n"), 0o644)
	mustGit(t, seed, env, "checkout", "-q", "-b", "main")
	mustGit(t, seed, env, "add", ".")
	mustGit(t, seed, env, "commit", "-q", "-m", "base")
	mustGit(t, seed, env, "push", "-q", "origin", "main")

	const n = 6
	var wg sync.WaitGroup
	errs := make([]string, n)
	for i := 0; i < n; i++ {
		branch := "b" + string(rune('0'+i))
		clone := filepath.Join(t.TempDir(), branch)
		mustGit(t, t.TempDir(), env, "clone", "-q", inst.sshURL("alice/app"), clone)
		mustGit(t, clone, env, "checkout", "-q", "-b", branch)
		os.WriteFile(filepath.Join(clone, branch+".txt"), []byte(branch+"\n"), 0o644)
		mustGit(t, clone, env, "add", ".")
		mustGit(t, clone, env, "commit", "-q", "-m", branch)
		wg.Add(1)
		go func(i int, clone, branch string) {
			defer wg.Done()
			cmd := exec.Command("git", "push", "-q", "origin", branch)
			cmd.Dir, cmd.Env = clone, env
			if out, err := cmd.CombinedOutput(); err != nil {
				errs[i] = string(out)
			}
		}(i, clone, branch)
	}
	wg.Wait()
	for i, e := range errs {
		if e != "" {
			t.Errorf("push %d failed:\n%s", i, e)
		}
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "refs", "alice/app")
	for i := 0; i < n; i++ {
		if !strings.Contains(out, "b"+string(rune('0'+i))) {
			t.Errorf("branch b%d missing after concurrent pushes:\n%s", i, out)
		}
	}
}
