package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// suggestionCLI is a CLI configured as key against inst.
func suggestionCLI(t *testing.T, inst *instance, key string) *cli {
	t.Helper()
	c := &cli{bin: buildGitbayCLI(t), configDir: t.TempDir(), inst: inst, key: key}
	c.must(t, "", "", "remote", "add", "test", "127.0.0.1",
		"--port", fmt.Sprint(inst.port),
		"--ssh-option", "-i", "--ssh-option", key,
		"--ssh-option", "-oIdentitiesOnly=yes",
		"--ssh-option", "-oStrictHostKeyChecking=no",
		"--ssh-option", "-oUserKnownHostsFile="+filepath.Join(inst.sshDir, "kh"),
		"--ssh-option", "-oBatchMode=yes",
		"--default")
	return c
}

// postSuggestion opens a thread on lib.txt line 2 of !1 in repo whose
// suggestion replaces it with two lines, and returns the thread id.
func postSuggestion(t *testing.T, inst *instance, key, repo string) string {
	t.Helper()
	body := "split this\n```suggestion\nTWO\nTWO AND A HALF\n```\n"
	out, errOut, code := inst.ssh(t, key, body, "mr", "diff-comment", repo, "1",
		"--path", "lib.txt", "--line", "2", "--file", "-", "--json")
	if code != 0 {
		t.Fatalf("diff-comment: exit %d %s", code, errOut)
	}
	var env struct {
		Data struct {
			Thread int64 `json:"thread"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	return fmt.Sprint(env.Data.Thread)
}

type e2eThread struct {
	ID         int64  `json:"id"`
	Resolved   string `json:"resolved_by"`
	Suggestion *struct {
		StartLine   int    `json:"start_line"`
		EndLine     int    `json:"end_line"`
		Original    string `json:"original"`
		Replacement string `json:"replacement"`
		Outdated    bool   `json:"outdated"`
		Apply       string `json:"apply"`
	} `json:"suggestion"`
}

func threadsOf(t *testing.T, inst *instance, key, repo string) []e2eThread {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", "mr", "threads", repo, "1", "--json")
	if code != 0 {
		t.Fatalf("mr threads: %s", errOut)
	}
	var env struct {
		Data []e2eThread `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("threads JSON: %v\n%s", err, out)
	}
	return env.Data
}

// A reviewer's suggestion, applied by the author through the CLI, which
// asks the server to commit it: the source branch gets one commit by the
// author with the suggested lines, and the thread is resolved.
func TestSuggestionAppliedByServer(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	c := suggestionCLI(t, inst, aliceKey)
	c.must(t, "", "", "repo", "create", "alice/lib")

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("one\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	mustGit(t, dir, env, "commit", "-q", "-am", "more")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	c.must(t, dir, "", "mr", "create", "alice/lib", "--source", "feat", "--target", "main", "--title", "more")

	thread := postSuggestion(t, inst, bobKey, "alice/lib")
	th := threadsOf(t, inst, aliceKey, "alice/lib")
	if len(th) != 1 || th[0].Suggestion == nil || th[0].Suggestion.Original != "two\n" ||
		th[0].Suggestion.Replacement != "TWO\nTWO AND A HALF\n" || th[0].Suggestion.Apply != "server" {
		t.Fatalf("threads = %+v", th)
	}

	// bob reads the repository and cannot push to it, so he cannot apply.
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "apply-suggestion", "alice/lib", "1", thread); code != 4 {
		t.Fatalf("reader applied a suggestion: exit %d %s", code, errOut)
	}

	out, errOut, code := c.run(t, dir, "", "mr", "apply-suggestion", "1", thread)
	if code != 0 {
		t.Fatalf("apply-suggestion: exit %d\n%s\n%s", code, out, errOut)
	}
	mustGit(t, dir, env, "fetch", "-q", "origin", "feat")
	if got := mustGit(t, dir, env, "show", "FETCH_HEAD:lib.txt"); got != "one\nTWO\nTWO AND A HALF\nthree\n" {
		t.Fatalf("lib.txt = %q", got)
	}
	if who := mustGit(t, dir, env, "log", "-1", "--format=%an <%ae>", "FETCH_HEAD"); strings.TrimSpace(who) != "alice <alice@example.test>" {
		t.Errorf("commit by %q", who)
	}
	if th := threadsOf(t, inst, aliceKey, "alice/lib"); th[0].Resolved != "alice" || !th[0].Suggestion.Outdated {
		t.Errorf("after apply: thread = %+v", th[0])
	}
}

// On a repository requiring signed commits the server refuses, and the
// CLI applies the suggestion in the clone with the user's own signing
// key: the push passes the signed-commit check, and the working tree is
// left as it was.
func TestSuggestionAppliedLocallyWhenSigned(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	c := suggestionCLI(t, inst, aliceKey)
	c.must(t, "", "", "repo", "create", "alice/sec")
	c.must(t, "", "", "repo", "settings", "require-signed", "alice/sec", "on")

	// alice signs with her SSH key, as git's own config says to.
	ident := []string{"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@example.test",
		"GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@example.test"}
	env := append(inst.gitEnv(aliceKey), ident...)
	c.env = ident
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/sec"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "config", "gpg.format", "ssh")
	mustGit(t, dir, env, "config", "user.signingkey", aliceKey)
	mustGit(t, dir, env, "config", "commit.gpgsign", "true")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("one\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	mustGit(t, dir, env, "commit", "-q", "-am", "more")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	c.must(t, dir, "", "mr", "create", "alice/sec", "--source", "feat", "--target", "main", "--title", "more")
	mustGit(t, dir, env, "checkout", "-q", "main")

	thread := postSuggestion(t, inst, aliceKey, "alice/sec")
	if th := threadsOf(t, inst, aliceKey, "alice/sec"); th[0].Suggestion == nil || th[0].Suggestion.Apply != "local" {
		t.Fatalf("threads = %+v, want a suggestion applied locally", th)
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "mr", "apply-suggestion", "alice/sec", "1", thread)
	if code != 4 || !strings.Contains(errOut, "gitbay mr apply-suggestion alice/sec 1 "+thread) {
		t.Fatalf("server-side apply on a require-signed repository: exit %d %s", code, errOut)
	}

	out, errOut, code := c.run(t, dir, "", "mr", "apply-suggestion", "1", thread)
	if code != 0 {
		t.Fatalf("local apply-suggestion: exit %d\n%s\n%s", code, out, errOut)
	}
	mustGit(t, dir, env, "fetch", "-q", "origin", "feat")
	if got := mustGit(t, dir, env, "show", "FETCH_HEAD:lib.txt"); got != "one\nTWO\nTWO AND A HALF\nthree\n" {
		t.Fatalf("lib.txt = %q", got)
	}
	if msg := mustGit(t, dir, env, "log", "-1", "--format=%B", "FETCH_HEAD"); !strings.Contains(msg, "Thread "+thread+" on alice/sec!1") {
		t.Errorf("commit message = %q", msg)
	}
	if branch := strings.TrimSpace(mustGit(t, dir, env, "branch", "--show-current")); branch != "main" {
		t.Errorf("checked-out branch moved to %q", branch)
	}
	if st := mustGit(t, dir, env, "status", "--porcelain"); st != "" {
		t.Errorf("working tree touched:\n%s", st)
	}
	if th := threadsOf(t, inst, aliceKey, "alice/sec"); th[0].Resolved != "alice" {
		t.Errorf("thread not resolved: %+v", th[0])
	}
}
