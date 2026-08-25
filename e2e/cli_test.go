package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildGitbayCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gitbay")
	cmd := exec.Command("go", "build", "-o", bin, "gitbay.org/gitbay/cmd/gitbay")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gitbay: %v\n%s", err, out)
	}
	return bin
}

// cli runs the forge binary with an isolated config home.
type cli struct {
	bin       string
	configDir string
	inst      *instance
	key       string
}

func (c *cli) run(t *testing.T, dir, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+c.configDir,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
		"EDITOR=", // no editor in tests: bodies come from flags
	)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("gitbay %v: %v", args, err)
	}
	return out.String(), errOut.String(), code
}

func (c *cli) must(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	out, errOut, code := c.run(t, dir, stdin, args...)
	if code != 0 {
		t.Fatalf("gitbay %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

func TestCLI(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	c := &cli{
		bin:       buildGitbayCLI(t),
		configDir: t.TempDir(),
		inst:      inst,
		key:       aliceKey,
	}

	// Configure the instance, with ssh options so the test's throwaway key
	// and known_hosts are used.
	c.must(t, "", "", "remote", "add", "test", "127.0.0.1",
		"--port", fmt.Sprint(inst.port),
		"--ssh-option", "-i", "--ssh-option", aliceKey,
		"--ssh-option", "-oIdentitiesOnly=yes",
		"--ssh-option", "-oStrictHostKeyChecking=no",
		"--ssh-option", "-oUserKnownHostsFile="+filepath.Join(inst.sshDir, "kh"),
		"--ssh-option", "-oBatchMode=yes",
		"--default")
	if out := c.must(t, "", "", "remote", "list"); !strings.Contains(out, "test\tgit@127.0.0.1") || !strings.Contains(out, "(default)") {
		t.Fatalf("remote list: %s", out)
	}

	// whoami through the CLI, JSON passthrough intact.
	out := c.must(t, "", "", "auth", "whoami", "--json")
	var env struct {
		ProtocolVersion int `json:"protocol_version"`
		Data            struct {
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Data.Username != "alice" || env.ProtocolVersion != 1 {
		t.Fatalf("whoami via CLI: %v %s", err, out)
	}

	// Repo create + clone through the CLI.
	c.must(t, "", "", "repo", "create", "alice/proj")

	// Secret values travel on stdin through the CLI (piped, no flag).
	c.must(t, "", "hunter2\n", "repo", "secret", "set", "alice/proj", "TOKEN")
	if out := c.must(t, "", "", "repo", "secret", "list", "alice/proj"); !strings.Contains(out, "TOKEN") || strings.Contains(out, "hunter2") {
		t.Fatalf("secret list via CLI: %s", out)
	}

	work := t.TempDir()
	c.must(t, work, "", "repo", "clone", "alice/proj")
	dir := filepath.Join(work, "proj")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal("clone did not produce a repo")
	}

	// Push some content (plain git, using the clone's remote).
	cliGitEnv := inst.gitEnv(aliceKey)
	os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644)
	mustGit(t, dir, cliGitEnv, "checkout", "-q", "-b", "main")
	mustGit(t, dir, cliGitEnv, "add", ".")
	mustGit(t, dir, cliGitEnv, "commit", "-q", "-m", "init")
	mustGit(t, dir, cliGitEnv, "push", "-q", "origin", "main")

	// Inside the clone, the repo argument is inferred from origin.
	c.must(t, dir, "", "issue", "create", "--title", "inferred repo works", "--body", "body")
	out = c.must(t, dir, "", "issue", "list")
	if !strings.Contains(out, "inferred repo works") {
		t.Fatalf("issue list in clone: %s", out)
	}
	// Explicit owner/name still works from anywhere.
	out = c.must(t, "", "", "issue", "show", "alice/proj", "1")
	if !strings.Contains(out, "inferred repo works") {
		t.Fatalf("issue show explicit: %s", out)
	}
	// Outside a clone with no explicit repo: usage error, not a hang.
	_, errOut, code := c.run(t, "", "", "issue", "list")
	if code != 2 || !strings.Contains(errOut, "none inferable") {
		t.Fatalf("bare issue list outside clone: exit %d, %s", code, errOut)
	}

	// Exit codes pass through: missing issue is 3.
	if _, _, code := c.run(t, dir, "", "issue", "show", "99"); code != 3 {
		t.Fatalf("missing issue via CLI: exit %d, want 3", code)
	}

	// MR flow: branch, push, create (inferred), checkout, merge.
	mustGit(t, dir, cliGitEnv, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("feature\n"), 0o644)
	mustGit(t, dir, cliGitEnv, "add", ".")
	mustGit(t, dir, cliGitEnv, "commit", "-q", "-m", "feature work")
	mustGit(t, dir, cliGitEnv, "push", "-q", "origin", "feature")
	c.must(t, dir, "", "mr", "create", "--source", "feature", "--target", "main", "--title", "via cli")

	// mr checkout uses the clone's own git; the MR ref comes from origin.
	mustGit(t, dir, cliGitEnv, "checkout", "-q", "main")
	cmd := exec.Command(c.bin, "mr", "checkout", "1")
	cmd.Dir = dir
	cmd.Env = append(cliGitEnv, "XDG_CONFIG_HOME="+c.configDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mr checkout: %v\n%s", err, out)
	}
	branch := strings.TrimSpace(mustGit(t, dir, cliGitEnv, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "mr/1" {
		t.Fatalf("mr checkout branch = %s", branch)
	}
	if _, err := os.Stat(filepath.Join(dir, "f.txt")); err != nil {
		t.Fatal("mr checkout content missing")
	}

	c.must(t, dir, "", "mr", "merge", "1")
	out = c.must(t, dir, "", "mr", "show", "1")
	if !strings.Contains(out, "merged") {
		t.Fatalf("mr not merged: %s", out)
	}

	// keys add reads the public key from CLI stdin.
	secondKey := inst.newKey(t, "alice2")
	pub, _ := os.ReadFile(secondKey + ".pub")
	c.must(t, "", string(pub), "auth", "keys", "add", "--scope", "git")
	if out = c.must(t, "", "", "auth", "keys", "list"); len(strings.Split(strings.TrimSpace(out), "\n")) != 2 {
		t.Fatalf("keys list: %s", out)
	}

	// forge init: new local project, repo created server-side, origin set.
	proj := filepath.Join(t.TempDir(), "newthing")
	os.MkdirAll(proj, 0o755)
	c.must(t, proj, "", "init", "--private")
	originOut := mustGit(t, proj, cliGitEnv, "remote", "get-url", "origin")
	if !strings.Contains(originOut, "/alice/newthing.git") {
		t.Fatalf("init origin: %s", originOut)
	}
	if out = c.must(t, "", "", "repo", "show", "alice/newthing"); !strings.Contains(out, "private") {
		t.Fatalf("init-created repo: %s", out)
	}

	// A clone whose origin points at a FOREIGN host must not hijack the
	// command: the configured default instance is used, and repo inference
	// is dropped rather than guessed across hosts.
	foreign := filepath.Join(t.TempDir(), "f")
	os.MkdirAll(foreign, 0o755)
	mustGit(t, foreign, cliGitEnv, "init", "-q")
	mustGit(t, foreign, cliGitEnv, "remote", "add", "origin", "git@github.example:someone/thing.git")
	out = c.must(t, foreign, "", "auth", "whoami")
	if strings.TrimSpace(out) != "alice" {
		t.Fatalf("foreign-origin whoami hit the wrong host: %q", out)
	}
	_, errOut2, code2 := c.run(t, foreign, "", "issue", "list")
	if code2 != 2 || !strings.Contains(errOut2, "none inferable") {
		t.Fatalf("foreign-origin repo inference: exit %d, %s", code2, errOut2)
	}

	// Man pages and completions generate.
	manDir := t.TempDir()
	c.must(t, "", "", "man", "--dir", manDir)
	if entries, _ := os.ReadDir(manDir); len(entries) < 10 {
		t.Fatalf("man pages: only %d generated", len(entries))
	}
	if out = c.must(t, "", "", "completion", "zsh"); !strings.Contains(out, "compdef") {
		t.Fatal("zsh completion missing")
	}
}
