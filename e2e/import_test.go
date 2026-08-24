package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoImport(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	// Source repo with a non-"main" default branch, a tag, and two commits.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/src"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/src"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "code.txt"), []byte("v1\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "trunk")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "first")
	mustGit(t, dir, env, "tag", "v1.0")
	mustGit(t, dir, env, "commit", "-q", "--allow-empty", "-m", "second")
	mustGit(t, dir, env, "push", "-q", "origin", "trunk", "v1.0")

	// Point the source repo's HEAD at trunk so the remote advertises it.
	srcBare := filepath.Join(inst.root, "repos", "alice", "src.git")
	mustGit(t, srcBare, env, "symbolic-ref", "HEAD", "refs/heads/trunk")

	// Import over HTTP from our own instance (public smart HTTP).
	httpURL := fmt.Sprintf("http://127.0.0.1:%d/alice/src.git", inst.httpPort)
	out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "import", "alice/mirror", "--from", httpURL, "--private")
	if code != 0 {
		t.Fatalf("import: exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "imported alice/mirror (private, default trunk)") {
		t.Fatalf("import output: %s", out)
	}
	if !strings.Contains(out, "git data only") {
		t.Fatalf("import note missing: %s", out)
	}

	// Everything came across: both commits, the tag, and the default branch.
	logOut, _, code := inst.ssh(t, aliceKey, "", "repo", "log", "alice/mirror")
	if code != 0 || !strings.Contains(logOut, "second") || !strings.Contains(logOut, "first") {
		t.Fatalf("imported log: %d\n%s", code, logOut)
	}
	mirrorBare := filepath.Join(inst.root, "repos", "alice", "mirror.git")
	tags := mustGit(t, mirrorBare, env, "tag", "--list")
	if !strings.Contains(tags, "v1.0") {
		t.Fatalf("tag not imported: %q", tags)
	}
	head := strings.TrimSpace(mustGit(t, mirrorBare, env, "symbolic-ref", "HEAD"))
	if head != "refs/heads/trunk" {
		t.Fatalf("imported HEAD = %s", head)
	}
	showOut, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/mirror")
	if !strings.Contains(showOut, "private") || !strings.Contains(showOut, "default: trunk") {
		t.Fatalf("repo show after import: %s", showOut)
	}

	// The imported repo has working hooks (core.hooksPath was set): a
	// protected-branch force-push is refused.
	if _, errOut, code = inst.ssh(t, aliceKey, "", "repo", "settings", "protect", "alice/mirror", "trunk"); code != 0 {
		t.Fatalf("protect: %s", errOut)
	}
	mWork := t.TempDir()
	mustGit(t, mWork, env, "clone", inst.sshURL("alice/mirror"), "m")
	mDir := filepath.Join(mWork, "m")
	mustGit(t, mDir, env, "commit", "-q", "--amend", "--allow-empty", "-m", "rewrite")
	pushOut, pushCode := gitRun(t, mDir, env, "push", "--force", "origin", "trunk")
	if pushCode == 0 || !strings.Contains(pushOut, "force-push refused") {
		t.Fatalf("hooks not wired on imported repo:\n%s", pushOut)
	}

	// Import over git:// too.
	if _, errOut, code = inst.ssh(t, aliceKey, "", "repo", "settings", "git-daemon", "alice/src", "on"); code != 0 {
		t.Fatalf("git-daemon on: %s", errOut)
	}
	gitURL := fmt.Sprintf("git://127.0.0.1:%d/alice/src.git", inst.gitPort)
	if _, errOut, code = inst.ssh(t, aliceKey, "", "repo", "import", "alice/mirror2", "--from", gitURL); code != 0 {
		t.Fatalf("git:// import: %s", errOut)
	}

	// Org-owned imports: allowed for org admins, refused for non-members.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "create", "imports"); code != 0 {
		t.Fatalf("org create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "import", "imports/mirror", "--from", httpURL); code != 0 {
		t.Fatalf("org import: %s", errOut)
	}
	if out, _, code := inst.ssh(t, aliceKey, "", "repo", "log", "imports/mirror"); code != 0 || !strings.Contains(out, "first") {
		t.Fatalf("org import log: %d\n%s", code, out)
	}

	// Refusals: bad scheme, credentials in URL, existing name, foreign owner.
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"repo", "import", "alice/x", "--from", "file:///etc"}, "https://, http://, and git://"},
		{[]string{"repo", "import", "alice/x", "--from", "https://token@github.com/a/b"}, "--token-stdin"},
		{[]string{"repo", "import", "alice/mirror", "--from", httpURL}, "already exists"},
		{[]string{"repo", "import", "bob/x", "--from", httpURL}, "not you and not an organization"},
	}
	for _, tc := range cases {
		_, errOut, code := inst.ssh(t, aliceKey, "", tc.args...)
		if code == 0 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: exit %d, stderr %q (want %q)", tc.args, code, errOut, tc.want)
		}
	}

	// A failed import leaves nothing behind.
	_, _, code = inst.ssh(t, aliceKey, "", "repo", "import", "alice/gone", "--from",
		fmt.Sprintf("http://127.0.0.1:%d/alice/nonexistent.git", inst.httpPort))
	if code == 0 {
		t.Fatal("import of nonexistent source succeeded")
	}
	if _, _, code = inst.ssh(t, aliceKey, "", "repo", "show", "alice/gone"); code != 3 {
		t.Fatalf("failed import left a repo behind: exit %d, want 3", code)
	}
	if _, err := os.Stat(filepath.Join(inst.root, "repos", "alice", "gone.git")); !os.IsNotExist(err) {
		t.Fatal("failed import left a directory behind")
	}

	// --token-stdin consumes a token from stdin without leaking it: the
	// fetch works (token unused by our anonymous endpoint, but the askpass
	// plumbing must not break it) and the token string appears nowhere in
	// the repo config.
	_, errOut, code = inst.ssh(t, aliceKey, "s3cr3t-token\n", "repo", "import", "alice/mirror3",
		"--from", httpURL, "--token-stdin")
	if code != 0 {
		t.Fatalf("token-stdin import: %s", errOut)
	}
	cfgRaw, _ := os.ReadFile(filepath.Join(inst.root, "repos", "alice", "mirror3.git", "config"))
	if strings.Contains(string(cfgRaw), "s3cr3t") {
		t.Fatal("token leaked into repo config")
	}
}
