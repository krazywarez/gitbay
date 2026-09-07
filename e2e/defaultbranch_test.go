package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A first push of a branch other than main moves the unborn HEAD to it,
// and repo settings default-branch moves it later (#189).
func TestDefaultBranch(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/proj"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	env := inst.gitEnv(aliceKey)
	dir := filepath.Join(t.TempDir(), "proj")
	mustGit(t, t.TempDir(), env, "init", "-q", "-b", "master", dir)
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, env, "add", "README")
	mustGit(t, dir, env, "commit", "-q", "-m", "one")
	mustGit(t, dir, env, "remote", "add", "origin", inst.sshURL("alice/proj"))
	mustGit(t, dir, env, "push", "-q", "origin", "master")

	// HEAD followed the push: a clone checks master out, and the record
	// agrees.
	clone := filepath.Join(t.TempDir(), "c1")
	mustGit(t, t.TempDir(), env, "clone", "-q", inst.sshURL("alice/proj"), clone)
	if head := strings.TrimSpace(mustGit(t, clone, env, "symbolic-ref", "HEAD")); head != "refs/heads/master" {
		t.Fatalf("clone HEAD %q", head)
	}
	if _, err := os.Stat(filepath.Join(clone, "README")); err != nil {
		t.Fatalf("clone checked out nothing: %v", err)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/proj", "--json")
	if !strings.Contains(out, `"default_branch":"master"`) {
		t.Fatalf("repo show after first push: %s", out)
	}

	// A second branch does not move HEAD; the setting does.
	mustGit(t, dir, env, "checkout", "-q", "-b", "dev")
	if err := os.WriteFile(filepath.Join(dir, "DEV"), []byte("dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, env, "add", "DEV")
	mustGit(t, dir, env, "commit", "-q", "-m", "two")
	mustGit(t, dir, env, "push", "-q", "origin", "dev")
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/proj", "--json")
	if !strings.Contains(out, `"default_branch":"master"`) {
		t.Fatalf("second branch moved the default: %s", out)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "settings", "default-branch", "alice/proj", "dev"); code != 4 {
		t.Fatalf("non-admin set the default branch: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "default-branch", "alice/proj", "nope"); code != 1 || !strings.Contains(errOut, "no branch") {
		t.Fatalf("missing branch accepted: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "default-branch", "alice/proj", "dev"); code != 0 {
		t.Fatalf("default-branch: %s", errOut)
	}
	clone = filepath.Join(t.TempDir(), "c2")
	mustGit(t, t.TempDir(), env, "clone", "-q", inst.sshURL("alice/proj"), clone)
	if head := strings.TrimSpace(mustGit(t, clone, env, "symbolic-ref", "HEAD")); head != "refs/heads/dev" {
		t.Fatalf("clone HEAD after setting %q", head)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/proj", "--json")
	if !strings.Contains(out, `"default_branch":"dev"`) {
		t.Fatalf("repo show after setting: %s", out)
	}

	// The settings page carries the same control.
	alice := inst.login(t, aliceKey)
	set := inst.base() + "/alice/proj/settings"
	if _, body := browserGet(t, alice, set); !strings.Contains(body, `value="dev" selected`) {
		t.Fatalf("settings page does not show the default branch:\n%s", body)
	}
	if status, _ := browserPost(t, alice, set, url.Values{"field": {"default-branch"}, "default-branch": {"master"}}); status != 200 {
		t.Fatalf("settings post: %d", status)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/proj", "--json")
	if !strings.Contains(out, `"default_branch":"master"`) {
		t.Fatalf("repo show after web: %s", out)
	}
}
