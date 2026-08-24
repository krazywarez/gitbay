package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitBinaries returns every distinct git on this machine, so transport
// behavior is verified against more than one client version.
func gitBinaries() []string {
	bins := []string{"git"}
	if _, err := os.Stat("/usr/bin/git"); err == nil {
		bins = append(bins, "/usr/bin/git")
	}
	return bins
}

// setupPublicRepo creates alice with a public repo containing one commit and
// returns her key path.
func setupPublicRepo(t *testing.T, inst *instance, repo string) string {
	t.Helper()
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	_, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", repo)
	if code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL(repo), "w")
	dir := filepath.Join(work, "w")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("public\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", "README")
	mustGit(t, dir, env, "commit", "-q", "-m", "init")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	return aliceKey
}

// anonEnv is a git environment with no credentials and prompting hard-failed:
// if git ever tries to ask for a username or password, the command errors
// with a distinctive message instead of hanging.
func anonEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=false",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", // no ~/.gitconfig credential helpers or signing
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
	)
}

func (i *instance) httpURL(repo string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/%s.git", i.httpPort, repo)
}

func TestHTTPTransport(t *testing.T) {
	inst := startInstance(t)
	aliceKey := setupPublicRepo(t, inst, "alice/pub")

	// Anonymous clone of a public repo over HTTP.
	work := t.TempDir()
	mustGit(t, work, anonEnv(), "clone", inst.httpURL("alice/pub"), "c")
	dir := filepath.Join(work, "c")
	if data, err := os.ReadFile(filepath.Join(dir, "README")); err != nil || string(data) != "public\n" {
		t.Fatalf("cloned content wrong: %q, %v", data, err)
	}

	// Push over HTTP: fatal remote error with the SSH URL, no credential
	// prompting of any kind — checked against every git version on this
	// machine (the pkt-line ERR mechanism must be version-independent).
	mustGit(t, dir, anonEnv(), "commit", "-q", "--allow-empty", "-m", "x")
	for _, gitBin := range gitBinaries() {
		cmd := exec.Command(gitBin, "push", "origin", "main")
		cmd.Dir = dir
		cmd.Env = anonEnv()
		rawOut, err := cmd.CombinedOutput()
		out := string(rawOut)
		if err == nil {
			t.Fatalf("[%s] push over http succeeded", gitBin)
		}
		if !strings.Contains(out, "remote error:") ||
			!strings.Contains(out, "pushes to this forge go over SSH") ||
			!strings.Contains(out, "git@gitbay.test:alice/pub.git") {
			t.Fatalf("[%s] push refusal output:\n%s", gitBin, out)
		}
		for _, banned := range []string{"Username", "Password", "Authentication failed", "terminal prompts disabled", "401", "403"} {
			if strings.Contains(out, banned) {
				t.Fatalf("[%s] push refusal fell into credential path (%q):\n%s", gitBin, banned, out)
			}
		}
	}

	// Private repo: 404 on the wire for anonymous HTTP, for both services
	// and for a nonexistent repo — all indistinguishable.
	_, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private")
	if code != 0 {
		t.Fatalf("create private: %s", errOut)
	}
	for _, u := range []string{
		inst.httpURL("alice/secret") + "/info/refs?service=git-upload-pack",
		inst.httpURL("alice/secret") + "/info/refs?service=git-receive-pack",
		inst.httpURL("alice/nonexistent") + "/info/refs?service=git-upload-pack",
	} {
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404\n%s", u, resp.StatusCode, body)
		}
	}
	if out, code := gitRun(t, t.TempDir(), anonEnv(), "clone", inst.httpURL("alice/secret")); code == 0 {
		t.Fatalf("anonymous clone of private repo succeeded:\n%s", out)
	}
}

func TestGitDaemon(t *testing.T) {
	inst := startInstance(t)
	aliceKey := setupPublicRepo(t, inst, "alice/pub")
	gitURL := func(repo string) string {
		return fmt.Sprintf("git://127.0.0.1:%d/%s.git", inst.gitPort, repo)
	}

	// Not opted in yet: refused even though public.
	if out, code := gitRun(t, t.TempDir(), anonEnv(), "clone", gitURL("alice/pub")); code == 0 {
		t.Fatalf("git:// clone before opt-in succeeded:\n%s", out)
	} else if !strings.Contains(out, "repository not exported") {
		t.Fatalf("opt-out message:\n%s", out)
	}

	// Opt in, clone works.
	_, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "git-daemon", "alice/pub", "on")
	if code != 0 {
		t.Fatalf("git-daemon on: %s", errOut)
	}
	work := t.TempDir()
	mustGit(t, work, anonEnv(), "clone", gitURL("alice/pub"), "c")
	if data, _ := os.ReadFile(filepath.Join(work, "c", "README")); string(data) != "public\n" {
		t.Fatalf("git:// clone content wrong: %q", data)
	}

	// Private repos cannot be opted in.
	_, _, code = inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private")
	if code != 0 {
		t.Fatal("create private failed")
	}
	_, errOut, code = inst.ssh(t, aliceKey, "", "repo", "settings", "git-daemon", "alice/secret", "on")
	if code != 2 || !strings.Contains(errOut, "only public repositories") {
		t.Fatalf("private opt-in: exit %d, %s", code, errOut)
	}
}
